package viz

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

const (
	// defaultCommitLimit is the page size when ?limit is absent.
	defaultCommitLimit = 500
	// maxCommitLimit caps ?limit and the underlying commit walk; the DAG horizon
	// is bounded, so a page beyond it reports Truncated.
	maxCommitLimit = 1000
)

// commitPage is one commit in the DAG page: its identity and parents, the
// attributed lane (null when unclaimed), the cc-task ids its trailer names
// (resolved to full ids where a task matches), and the lifecycle events landing
// on it.
type commitPage struct {
	SHA     model.SHA   `json:"sha"`
	Parents []model.SHA `json:"parents"`
	Author  model.Actor `json:"author"`
	Time    int64       `json:"time"`
	Summary string      `json:"summary"`
	Branch  *string     `json:"branch"`
	Tasks   []string    `json:"tasks"`
	Events  []Event     `json:"events"`
}

// commitsResponse is the /api/commits payload: the page, the cursor to pass as
// ?before for the next page (null at the end), and whether the walk hit the DAG
// horizon.
type commitsResponse struct {
	Commits    []commitPage `json:"commits"`
	NextBefore *string      `json:"next_before"`
	Truncated  bool         `json:"truncated"`
}

func (s *Server) handleCommits(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q := r.URL.Query()
	limit := defaultCommitLimit
	if raw := q.Get("limit"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid limit %q: want an integer", raw))
			return
		}
		limit = v
	}
	if limit < 1 {
		limit = defaultCommitLimit
	}
	if limit > maxCommitLimit {
		limit = maxCommitLimit
	}
	before := q.Get("before")

	g, err := s.builder.Graph(ctx, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	all, truncated, err := s.store.Repo.WalkCommits(ctx, liveTips(g), maxCommitLimit, 0)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	start := 0
	if before != "" {
		idx := indexOfSHA(all, model.SHA(before))
		if idx < 0 {
			writeError(w, http.StatusBadRequest, "unknown before cursor "+before)
			return
		}
		start = idx + 1
	}
	end := min(start+limit, len(all))
	page := all[start:end]

	cache := make(map[model.SHA]reachSet)
	order, err := attributeCommits(ctx, s.store, g, cache)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	trailers, err := commitTrailers(ctx, s.store, g, cache)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resolve := taskResolver(g)
	events := eventsIndex(g)

	dtos := make([]commitPage, len(page))
	for i, c := range page {
		dtos[i] = commitPage{
			SHA:     c.SHA,
			Parents: c.Parents,
			Author:  c.Author,
			Time:    c.CommitTime,
			Summary: c.Summary,
			Branch:  claimLane(order, c.SHA),
			Tasks:   resolveTasks(resolve, trailers[c.SHA]),
			Events:  events[c.SHA],
		}
	}
	var next *string
	if end < len(all) {
		last := string(all[end-1].SHA)
		next = &last
	}
	writeJSON(w, http.StatusOK, commitsResponse{Commits: dtos, NextBefore: next, Truncated: truncated})
}

// liveTips is the deduped tip sha of every lane backed by a live ref, in lane
// order (trunk first). Deleted and inferred lanes carry no tip and drop out.
func liveTips(g *Graph) []model.SHA {
	seen := make(map[model.SHA]bool)
	var tips []model.SHA
	for _, lane := range g.Lanes {
		if lane.Tip == nil || seen[lane.Tip.SHA] {
			continue
		}
		seen[lane.Tip.SHA] = true
		tips = append(tips, lane.Tip.SHA)
	}
	return tips
}

// indexOfSHA returns the position of sha in commits, or -1.
func indexOfSHA(commits []gitobj.CodeCommit, sha model.SHA) int {
	for i, c := range commits {
		if c.SHA == sha {
			return i
		}
	}
	return -1
}

// reachSet is a tip's reachable-commit set within the walk window plus one
// parentless commit reachable from it, the root that bounds a trailer range.
type reachSet struct {
	set  map[model.SHA]bool
	root model.SHA
}

// reachFrom walks the commits reachable from sha within the window, memoized by
// sha across one request, recording the reachable set and a reachable root.
func reachFrom(ctx context.Context, st *store.Store, cache map[model.SHA]reachSet, sha model.SHA) (reachSet, error) {
	if rs, ok := cache[sha]; ok {
		return rs, nil
	}
	commits, _, err := st.Repo.WalkCommits(ctx, []model.SHA{sha}, maxCommitLimit, 0)
	if err != nil {
		return reachSet{}, fmt.Errorf("walk reachable from %s: %w", sha, err)
	}
	rs := reachSet{set: make(map[model.SHA]bool, len(commits))}
	for _, c := range commits {
		rs.set[c.SHA] = true
		if len(c.Parents) == 0 {
			rs.root = c.SHA
		}
	}
	cache[sha] = rs
	return rs, nil
}

// laneClaim is a lane's commit-attribution rule: it claims a commit reachable
// from its tip but not its fork (its post-fork commits); the trunk, with no
// fork, claims everything reachable from its tip.
type laneClaim struct {
	name      string
	forkTime  int64
	tipReach  map[model.SHA]bool
	forkReach map[model.SHA]bool
}

// attributeCommits builds the lane-attribution priority order from the Graph's
// lanes: non-trunk lanes first, most recently forked (most specific) first with
// name as tie-break, then the trunk. claimLane assigns each commit to the first
// lane in this order that claims it.
func attributeCommits(ctx context.Context, st *store.Store, g *Graph, cache map[model.SHA]reachSet) ([]laneClaim, error) {
	var nonTrunk []laneClaim
	var trunk *laneClaim
	for _, lane := range g.Lanes {
		if lane.Tip == nil {
			continue
		}
		tipRS, err := reachFrom(ctx, st, cache, lane.Tip.SHA)
		if err != nil {
			return nil, err
		}
		lc := laneClaim{name: lane.Name, tipReach: tipRS.set}
		if lane.Fork != nil {
			forkRS, err := reachFrom(ctx, st, cache, lane.Fork.SHA)
			if err != nil {
				return nil, err
			}
			lc.forkReach = forkRS.set
			lc.forkTime = lane.Fork.Time
		}
		if lane.Name == g.Repo.Trunk {
			claim := lc
			trunk = &claim
		} else {
			nonTrunk = append(nonTrunk, lc)
		}
	}
	sort.Slice(nonTrunk, func(i, j int) bool {
		if nonTrunk[i].forkTime != nonTrunk[j].forkTime {
			return nonTrunk[i].forkTime > nonTrunk[j].forkTime
		}
		return nonTrunk[i].name < nonTrunk[j].name
	})
	if trunk != nil {
		return append(nonTrunk, *trunk), nil
	}
	return nonTrunk, nil
}

// claimLane returns the name of the first lane in order that claims sha, or nil
// when no lane does.
func claimLane(order []laneClaim, sha model.SHA) *string {
	for i := range order {
		lc := &order[i]
		if lc.tipReach[sha] && (lc.forkReach == nil || !lc.forkReach[sha]) {
			name := lc.name
			return &name
		}
	}
	return nil
}

// commitTrailers gathers the cc-task trailers of every commit reachable from
// the lane tips, keyed by commit sha. Each lane contributes one range —
// fork..tip for a forked branch (its exclusive commits) and root..tip for the
// trunk (its whole reachable history) — so a squash-merge trailer on a trunk
// commit is covered. The root commit's own trailers are merged in separately,
// since root..tip by range semantics excludes the root itself.
func commitTrailers(ctx context.Context, st *store.Store, g *Graph, cache map[model.SHA]reachSet) (map[model.SHA][]string, error) {
	merged := make(map[model.SHA][]string)
	for _, lane := range g.Lanes {
		if lane.Tip == nil {
			continue
		}
		var base string
		if lane.Fork != nil {
			base = string(lane.Fork.SHA)
		} else {
			rs, err := reachFrom(ctx, st, cache, lane.Tip.SHA)
			if err != nil {
				return nil, err
			}
			base = string(rs.root)
			// root..tip excludes root, so fold in the root's own trailers.
			rootTrailers, err := st.Git.TaskTrailers(ctx, string(rs.root))
			if err != nil {
				return nil, err
			}
			if len(rootTrailers) > 0 {
				if _, ok := merged[rs.root]; !ok {
					merged[rs.root] = rootTrailers
				}
			}
		}
		if base == "" || base == string(lane.Tip.SHA) {
			continue
		}
		trailers, err := st.Git.TaskTrailersRange(ctx, base, string(lane.Tip.SHA))
		if err != nil {
			return nil, err
		}
		for sha, vals := range trailers {
			if _, ok := merged[sha]; !ok {
				merged[sha] = vals
			}
		}
	}
	return merged, nil
}

// taskResolver resolves a cc-task trailer value to a full task id from the
// Graph's entity legend, accepting the full id or a short (>= 7-char) prefix and
// falling back to the raw value when no task matches.
func taskResolver(g *Graph) func(string) string {
	full := make(map[string]string)
	var ids []string
	for _, e := range g.Entities {
		if e.Kind == entityTask {
			id := string(e.ID)
			full[id] = id
			ids = append(ids, id)
		}
	}
	return func(v string) string {
		if id, ok := full[v]; ok {
			return id
		}
		if len(v) >= 7 {
			for _, id := range ids {
				if strings.HasPrefix(id, v) {
					return id
				}
			}
		}
		return v
	}
}

// resolveTasks resolves and dedups a commit's trailer values, preserving order.
func resolveTasks(resolve func(string) string, values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	var out []string
	for _, v := range values {
		id := resolve(v)
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// eventsIndex maps each commit sha to the Graph events landing on it: an event's
// own carrying sha, plus a detail sha (the linked code commit of a commit_linked
// event) so the DAG shows the link on the code commit, not the entity's op
// commit.
func eventsIndex(g *Graph) map[model.SHA][]Event {
	idx := make(map[model.SHA][]Event)
	for _, ev := range g.Events {
		idx[ev.SHA] = append(idx[ev.SHA], ev)
		if d := model.SHA(ev.Detail["sha"]); d != "" && d != ev.SHA {
			idx[d] = append(idx[d], ev)
		}
	}
	return idx
}
