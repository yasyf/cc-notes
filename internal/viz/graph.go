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
// any synthesized lanes with no live ref.
type topology struct {
	trunk     *branchState
	branches  []*branchState
	truncated bool
	// extra holds synthesized lanes that back no live ref — the deleted-branch
	// lanes the entity-events pass reconstructs from task trails. lanes() merges
	// them among the branch lanes in name order.
	extra []Lane
}

// lanes renders the trunk lane first, then every non-trunk lane — the live
// branch lanes and any synthesized extra lanes — in name order.
func (t *topology) lanes() []Lane {
	rest := make([]Lane, 0, len(t.branches)+len(t.extra))
	for _, b := range t.branches {
		rest = append(rest, b.toLane())
	}
	rest = append(rest, t.extra...)
	sort.Slice(rest, func(i, j int) bool { return rest[i].Name < rest[j].Name })
	out := make([]Lane, 0, len(rest)+1)
	out = append(out, t.trunk.toLane())
	return append(out, rest...)
}

// topology builds the branch topology over the history window starting at since
// (unix seconds; 0 selects the default window). It resolves the trunk,
// enumerates every branch, finds each fork point, infers nested parentage,
// classifies each branch's merge into the trunk, then attributes walked commits
// to lanes.
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

	others := make([]*branchState, 0, len(states)-1)
	for name, s := range states {
		if name != trunkName {
			others = append(others, s)
		}
	}
	sort.Slice(others, func(i, j int) bool { return others[i].name < others[j].name })

	r := &topoRun{
		b:       b,
		ctx:     ctx,
		times:   make(map[model.SHA]int64),
		windows: make(map[model.SHA]windowSet),
	}
	if trunk.tipTime, err = r.commitTime(trunk.tip); err != nil {
		return nil, err
	}
	for _, s := range others {
		if s.tipTime, err = r.commitTime(s.tip); err != nil {
			return nil, err
		}
		base, found, err := b.mergeBaseOf(ctx, s.tip, trunk.tip)
		if err != nil {
			return nil, err
		}
		if found {
			s.hasFork = true
			s.forkBase = base
			if s.forkTime, err = r.commitTime(base); err != nil {
				return nil, err
			}
		}
	}
	if err := b.assignParents(ctx, trunk, others, r); err != nil {
		return nil, err
	}
	tasks, err := b.store.ListTasks(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	for _, s := range others {
		if err := b.detectMerge(ctx, s, trunk, tasks, r); err != nil {
			return nil, err
		}
	}

	if since == 0 {
		since = windowSince(time.Now().Unix(), oldestActiveFork(others))
	}
	r.since = since

	truncated, err := b.walkTruncated(ctx, trunk, others, since)
	if err != nil {
		return nil, err
	}
	if err := r.attribute(trunk, others); err != nil {
		return nil, err
	}
	return &topology{trunk: trunk, branches: others, truncated: truncated}, nil
}

// assignParents infers each branch's parent lane. Every branch defaults to the
// trunk. When the branch count is within maxParentageBranches, parent(B) is the
// lane P whose merge-base with B has the greatest commit time among lanes whose
// merge-base with B strictly descends from B's fork off the trunk; a tie for
// that maximum, or no candidate, falls back to the trunk. A candidate that
// descends from B is skipped so a branch never adopts its own descendant, with
// an identical-tip pair broken by name so exactly one direction parents. Above
// the cap the pairwise scan is quadratic, so parentage stays flat.
func (b *Builder) assignParents(ctx context.Context, trunk *branchState, others []*branchState, r *topoRun) error {
	for _, s := range others {
		s.parent = trunk.name
	}
	if len(others) > maxParentageBranches {
		return nil
	}
	for _, s := range others {
		if !s.hasFork {
			continue
		}
		bestName := ""
		var bestTime int64
		tie := false
		for _, p := range others {
			if p == s {
				continue
			}
			descFromS, err := b.store.Repo.IsAncestor(ctx, s.tip, p.tip)
			if err != nil {
				return fmt.Errorf("ancestry %s %s: %w", s.tip, p.tip, err)
			}
			if descFromS && (s.tip != p.tip || s.name < p.name) {
				continue
			}
			mb, found, err := b.mergeBaseOf(ctx, s.tip, p.tip)
			if err != nil {
				return err
			}
			if !found || mb == s.forkBase {
				continue
			}
			desc, err := b.store.Repo.IsAncestor(ctx, s.forkBase, mb)
			if err != nil {
				return fmt.Errorf("ancestry %s %s: %w", s.forkBase, mb, err)
			}
			if !desc {
				continue
			}
			mbTime, err := r.commitTime(mb)
			if err != nil {
				return err
			}
			switch {
			case bestName == "" || mbTime > bestTime:
				bestName, bestTime, tie = p.name, mbTime, false
			case mbTime == bestTime:
				tie = true
			}
		}
		if bestName != "" && !tie {
			s.parent = bestName
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

// oldestActiveFork is the earliest fork time among the still-active lanes, or 0
// when none is active. It floors the default history window.
func oldestActiveFork(others []*branchState) int64 {
	oldest := int64(0)
	for _, s := range others {
		if s.status != statusActive || !s.hasFork {
			continue
		}
		if oldest == 0 || s.forkTime < oldest {
			oldest = s.forkTime
		}
	}
	return oldest
}

// windowSince is the default window lower bound: no earlier than defaultWindow
// before now, and no earlier than the oldest active lane's fork.
func windowSince(now, oldestActiveFork int64) int64 {
	floor := now - int64(defaultWindow.Seconds())
	return max(floor, oldestActiveFork)
}
