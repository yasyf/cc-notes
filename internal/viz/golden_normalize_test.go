package viz

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

// goldenRoot, goldenGeneratedAt, and goldenOpTime replace the values that vary
// per run with fixed sentinels: the temp-dir worktree root, the wall-clock
// generation instant, and every op-commit-derived timestamp. Op timestamps
// collapse to a single sentinel rather than ranked "t-N" because the whole
// fixture writes within one or two wall-clock seconds and which second-boundary a
// given op lands on is timing-dependent — so the count of distinct op seconds, and
// any rank derived from it, is not stable across runs.
const (
	goldenRoot        = "<root>"
	goldenGeneratedAt = "<generated-at>"
	goldenOpTime      = "<op-time>"
)

// hexSHA matches a full 40-character lowercase-hex commit sha or entity id.
var hexSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)

// hexShort matches a 7-character lowercase-hex short id — a run's short id and a
// cited task's short id ride event detail in this form, random per run.
var hexShort = regexp.MustCompile(`^[0-9a-f]{7}$`)

// normalizeGraph renders g as deterministic JSON for golden comparison, without
// mutating g. The store stamps op-commit signatures from an unexported wall
// clock — no external package can pin it — so op-commit shas, entity ids, and
// op-commit timestamps are all random per run and must be renamed: every distinct
// sha or id becomes "sha-N" in first-appearance order, and every op-commit
// timestamp becomes the goldenOpTime sentinel. Git fixture commit times and
// literal sprint dates stay raw, since fixed GIT_AUTHOR_DATE/GIT_COMMITTER_DATE
// make them reproducible. Events and entities are re-sorted onto a deterministic
// key first: the builder orders near-simultaneous events by entity ref — a random
// id — and by op-commit second, which is timing-dependent, so their order varies
// per run.
func normalizeGraph(t *testing.T, g *Graph) string {
	t.Helper()
	c := copyGraph(g)
	sortEvents(c.Events)
	sortEntities(c.Entities)

	c.Repo.Root = goldenRoot
	c.Repo.GeneratedAt = goldenGeneratedAt
	renameSHAs(c, newSHARegistry())

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatalf("marshal graph: %v", err)
	}
	// Exactly one trailing newline: end-of-file-fixer rewrites any other EOF
	// shape in testdata (see the cc-notes note on golden EOF bytes).
	return renameTimes(string(data), opTimes(c)) + "\n"
}

// copyGraph returns a deep copy of g, so normalization never mutates a cached
// graph shared across Graph calls.
func copyGraph(g *Graph) *Graph {
	c := &Graph{Repo: g.Repo, Lanes: make([]Lane, len(g.Lanes)), Events: make([]Event, len(g.Events)), Entities: make([]EntitySummary, len(g.Entities))}
	for i, l := range g.Lanes {
		if l.Fork != nil {
			f := *l.Fork
			l.Fork = &f
		}
		if l.Tip != nil {
			tp := *l.Tip
			l.Tip = &tp
		}
		if l.Merge != nil {
			m := *l.Merge
			l.Merge = &m
		}
		c.Lanes[i] = l
	}
	for i, e := range g.Events {
		if e.Detail != nil {
			d := make(map[string]string, len(e.Detail))
			for k, v := range e.Detail {
				d[k] = v
			}
			e.Detail = d
		}
		c.Events[i] = e
	}
	copy(c.Entities, g.Entities)
	return c
}

// sortEvents re-sorts events onto (entity kind, entity title, per-entity trail
// index). The builder orders same-second events by entity ref — a random id — and
// events that straddle a wall-clock-second boundary by op-commit time, both
// unstable across runs, so op time is left out of the key entirely: (kind, title)
// imposes a deterministic entity order, and the trail index preserves each
// entity's lifecycle order. Titles are unique per entity, so (kind, title)
// identifies the entity.
func sortEvents(evs []Event) {
	seq := make([]int, len(evs))
	counter := make(map[model.EntityID]int, len(evs))
	for i := range evs {
		id := evs[i].Entity.ID
		seq[i] = counter[id]
		counter[id]++
	}
	idx := make([]int, len(evs))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		ea, eb := evs[idx[a]], evs[idx[b]]
		switch {
		case ea.Entity.Kind != eb.Entity.Kind:
			return ea.Entity.Kind < eb.Entity.Kind
		case ea.Entity.Title != eb.Entity.Title:
			return ea.Entity.Title < eb.Entity.Title
		default:
			return seq[idx[a]] < seq[idx[b]]
		}
	})
	out := make([]Event, len(evs))
	for i, j := range idx {
		out[i] = evs[j]
	}
	copy(evs, out)
}

// sortEntities re-sorts entity summaries onto (kind, title), replacing the
// builder's (created-at, id) order whose ties break on a random id.
func sortEntities(es []EntitySummary) {
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].Kind != es[j].Kind {
			return es[i].Kind < es[j].Kind
		}
		return es[i].Title < es[j].Title
	})
}

// shaRegistry renames distinct 40-hex shas and entity ids to "sha-N" in
// first-appearance order.
type shaRegistry struct {
	m map[string]string
}

func newSHARegistry() *shaRegistry { return &shaRegistry{m: make(map[string]string)} }

// name returns the stable "sha-N" for sha, minting the next one on first sight;
// the empty string maps to itself.
func (r *shaRegistry) name(sha string) string {
	if sha == "" {
		return ""
	}
	if n, ok := r.m[sha]; ok {
		return n
	}
	n := fmt.Sprintf("sha-%d", len(r.m)+1)
	r.m[sha] = n
	return n
}

// renameSHAs walks g in a fixed order — lanes, then events, then entities —
// renaming every sha and id in place. An entity's short id is renamed to its full
// id's "sha-N", since it is a prefix of the same id.
func renameSHAs(g *Graph, r *shaRegistry) {
	for i := range g.Lanes {
		l := &g.Lanes[i]
		if l.Fork != nil {
			l.Fork.SHA = model.SHA(r.name(string(l.Fork.SHA)))
		}
		if l.Tip != nil {
			l.Tip.SHA = model.SHA(r.name(string(l.Tip.SHA)))
		}
		if l.Merge != nil {
			l.Merge.SHA = model.SHA(r.name(string(l.Merge.SHA)))
		}
	}
	for i := range g.Events {
		e := &g.Events[i]
		id := r.name(string(e.Entity.ID))
		e.Entity.ID, e.Entity.Short = model.EntityID(id), id
		e.SHA = model.SHA(r.name(string(e.SHA)))
		for _, k := range sortedKeys(e.Detail) {
			if hexSHA.MatchString(e.Detail[k]) || hexShort.MatchString(e.Detail[k]) {
				e.Detail[k] = r.name(e.Detail[k])
			}
		}
	}
	for i := range g.Entities {
		s := &g.Entities[i]
		id := r.name(string(s.ID))
		s.ID, s.Short = model.EntityID(id), id
		s.Sprint = r.name(s.Sprint)
		s.Project = r.name(s.Project)
	}
}

// opTimes collects the distinct op-commit-derived timestamps — random wall-clock
// seconds — from g. These are event times, the extents of the task-inferred
// deleted lanes, and the task/note lifecycle stamps; a live lane's and a
// DAG-mined deleted lane's times are git fixture commit times, and sprint
// start/end dates are literal, so both stay raw.
func opTimes(g *Graph) []int64 {
	set := make(map[int64]bool)
	add := func(v int64) {
		if v != 0 {
			set[v] = true
		}
	}
	for _, e := range g.Events {
		add(e.Time)
	}
	for _, l := range g.Lanes {
		if l.Status == statusDeleted && l.Inferred {
			add(l.Start)
			add(l.End)
			if l.Merge != nil {
				add(l.Merge.Time)
			}
		}
	}
	for _, s := range g.Entities {
		add(s.StartedAt)
		add(s.ClosedAt)
		add(s.VerifiedAt)
	}
	out := make([]int64, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// renameTimes replaces every op-commit timestamp in the marshaled JSON with the
// goldenOpTime sentinel. Op-commit seconds are ~July-2026 wall-clock values,
// disjoint from the ~January-2026 git fixture times and the 2023 literal sprint
// dates, so a whole-word numeric replacement is unambiguous.
func renameTimes(s string, times []int64) string {
	for _, v := range times {
		re := regexp.MustCompile(`\b` + strconv.FormatInt(v, 10) + `\b`)
		s = re.ReplaceAllString(s, strconv.Quote(goldenOpTime))
	}
	return s
}

// sortedKeys returns m's keys in ascending order.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
