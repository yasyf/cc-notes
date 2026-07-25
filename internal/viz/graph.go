package viz

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/yasyf/cc-notes/model"
)

// Lane statuses and merge kinds, the wire strings the swimlane renderer keys on.
const (
	statusActive = "active"
	statusMerged = "merged"

	kindMerge       = "merge"
	kindFastForward = "fast-forward"
	kindInferred    = "inferred"
)

// mergeInfo is a branch's resolved rejoin into the trunk.
type mergeInfo struct {
	sha  model.SHA
	time int64
	into string
	kind string
}

// branchState is one lane under construction: the ref it came from, its tip and
// tip time, its fork point off the trunk, its inferred parent lane, and its
// merge classification. It becomes a Lane via toLane once the walk completes.
type branchState struct {
	name    string
	ref     string
	tip     model.SHA
	tipTime int64
	remote  bool
	isTrunk bool

	hasFork  bool
	forkBase model.SHA
	forkTime int64

	parent   string
	inferred bool
	merge    *mergeInfo
	status   string

	start   int64
	end     int64
	commits int
}

func (s *branchState) toLane() Lane {
	l := Lane{
		Name:     s.name,
		Parent:   s.parent,
		Status:   s.status,
		Inferred: s.inferred,
		Tip:      &Point{SHA: s.tip, Time: s.tipTime},
		Start:    s.start,
		End:      s.end,
		Commits:  s.commits,
	}
	if s.hasFork {
		l.Fork = &Point{SHA: s.forkBase, Time: s.forkTime}
	}
	if s.merge != nil {
		l.Merge = &MergePoint{SHA: s.merge.sha, Time: s.merge.time, Into: s.merge.into, Kind: s.merge.kind}
	}
	return l
}

// topology is the branch-topology half of the graph: the trunk lane, every
// non-trunk lane sorted by short name, whether the commit walk truncated, and
// two kinds of synthesized lane with no live ref.
type topology struct {
	trunk    *branchState
	branches []*branchState
	// hidden holds the enumerated branches the window filter and the lane cap
	// dropped, sorted by short name. They render no lane, but their refs are
	// live, so every reconstruction of a deleted branch must still exclude them
	// by name and tip or it invents a lane for a branch that plainly exists.
	hidden    []*branchState
	truncated bool
	// mined holds the deleted-branch lanes reconstructed from the git DAG — a
	// merged branch whose ref was later deleted — each DAG-proven with a real
	// fork, tip, and merge point.
	mined []Lane
	// extra holds the deleted-branch lanes the entity-events pass reconstructs
	// from task trails alone, for branches with no surviving merge commit (a
	// squash then delete). lanes() merges both kinds among the branch lanes in
	// name order.
	extra []Lane
}

// lanes renders the trunk lane first, then every non-trunk lane — the live
// branch lanes and the mined and task-inferred deleted lanes — in name order.
func (t *topology) lanes() []Lane {
	rest := make([]Lane, 0, len(t.branches)+len(t.mined)+len(t.extra))
	for _, b := range t.branches {
		rest = append(rest, b.toLane())
	}
	rest = append(rest, t.mined...)
	rest = append(rest, t.extra...)
	sort.Slice(rest, func(i, j int) bool { return rest[i].Name < rest[j].Name })
	out := make([]Lane, 0, len(rest)+1)
	out = append(out, t.trunk.toLane())
	return append(out, rest...)
}

// topology builds the branch topology over the history window starting at since
// (unix seconds; 0 selects the default window). It resolves the trunk,
// enumerates every branch, drops the ones outside the window, finds each
// surviving fork point, infers nested parentage, classifies each branch's merge
// into the trunk, attributes walked commits to lanes, then mines the lanes of
// branches that were merged and deleted from the git DAG.
//
// The window cutoff is fixed before any per-branch work, so every scan below it
// is bounded; the attribution window since is resolved only after merge
// classification, which rewrites merged branches' fork times and so the floor
// they imply. cutoff is never later than since, so a scan bounded by it covers
// everything attribution reads.
func (b *Builder) topology(ctx context.Context, since int64) (*topology, error) {
	trunkName, err := b.trunkName(ctx)
	if err != nil {
		return nil, err
	}
	states, err := b.enumerate(ctx, trunkName)
	if err != nil {
		return nil, err
	}
	trunk := states[trunkName]
	trunk.isTrunk = true
	trunk.status = statusActive

	cutoff := since
	if cutoff == 0 {
		cutoff = time.Now().Unix() - int64(defaultWindow.Seconds())
	}
	r := newTopoRun(ctx, b, cutoff)

	others := make([]*branchState, 0, len(states)-1)
	for name, s := range states {
		r.times[s.tip] = s.tipTime
		if name != trunkName {
			others = append(others, s)
		}
	}
	sort.Slice(others, func(i, j int) bool { return others[i].name < others[j].name })

	tasks, err := b.store.ListTasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	others, hidden, err := b.filterLanes(trunk, others, tasks, r)
	if err != nil {
		return nil, err
	}
	if err := b.resolveForks(ctx, trunk, others, r); err != nil {
		return nil, err
	}
	if err := b.assignParents(ctx, trunk, others, r); err != nil {
		return nil, err
	}
	if err := b.classifyMerges(ctx, trunk, others, tasks, r); err != nil {
		return nil, err
	}

	if since == 0 {
		since = windowSince(time.Now().Unix(), oldestRefBackedFork(others))
	}
	r.since = since

	truncated, err := b.walkTruncated(ctx, trunk, others, since)
	if err != nil {
		return nil, err
	}
	if err := r.attribute(trunk, others); err != nil {
		return nil, err
	}
	mined, err := b.mineDeletedBranches(ctx, trunk, others, hidden, r)
	if err != nil {
		return nil, err
	}
	return &topology{trunk: trunk, branches: others, hidden: hidden, truncated: truncated, mined: mined}, nil
}

// resolveForks sets every branch's fork point off the trunk — its merge base
// with the trunk tip — with the whole batch of merge bases prefetched in one
// bounded fan-out.
func (b *Builder) resolveForks(ctx context.Context, trunk *branchState, others []*branchState, r *topoRun) error {
	pairs := make([][2]model.SHA, 0, len(others))
	for _, s := range others {
		pairs = append(pairs, [2]model.SHA{s.tip, trunk.tip})
	}
	if err := b.prefetchMergeBases(ctx, pairs); err != nil {
		return err
	}
	for _, s := range others {
		base, found, err := b.mergeBaseOf(ctx, s.tip, trunk.tip)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		s.hasFork = true
		s.forkBase = base
		if s.forkTime, err = r.commitTime(base); err != nil {
			return err
		}
	}
	return nil
}

// walkTruncated runs the bounded window walk over every lane tip solely to
// report whether it hit the commit cap, which becomes RepoInfo.Truncated.
func (b *Builder) walkTruncated(ctx context.Context, trunk *branchState, others []*branchState, since int64) (bool, error) {
	tips := make([]model.SHA, 0, len(others)+1)
	tips = append(tips, trunk.tip)
	for _, s := range others {
		tips = append(tips, s.tip)
	}
	_, truncated, err := b.store.Repo.WalkCommits(ctx, tips, walkLimit, since)
	if err != nil {
		return false, fmt.Errorf("walk commits: %w", err)
	}
	return truncated, nil
}

// oldestRefBackedFork is the earliest fork time among the ref-backed lanes —
// every rendered branch, merged included — or 0 when none has a fork. The
// synthesized inferred and deleted lanes carry no ref and never reach here, and
// neither do the branches the window filter hid, so a stale branch cannot drag
// the attribution window back open. It floors the default history window so a merged
// branch that forked before every open branch still starts the trunk rail early
// enough for its fork and merge connectors to land on it.
func oldestRefBackedFork(others []*branchState) int64 {
	oldest := int64(0)
	for _, s := range others {
		if !s.hasFork {
			continue
		}
		if oldest == 0 || s.forkTime < oldest {
			oldest = s.forkTime
		}
	}
	return oldest
}

// windowSince is the default window lower bound: no earlier than defaultWindow
// before now, and no earlier than the oldest ref-backed lane's fork.
func windowSince(now, oldestFork int64) int64 {
	floor := now - int64(defaultWindow.Seconds())
	return max(floor, oldestFork)
}
