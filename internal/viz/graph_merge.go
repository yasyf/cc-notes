package viz

import (
	"context"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

// classifyMerges resolves every branch's rejoin into the trunk. Reachability is
// decided in bulk: one for-each-ref names the branches the trunk tip already
// contains, and one more per in-window merge parent names the branches that
// merge landed, so no branch pays for a DAG walk of its own.
//
// A branch the trunk contains is merged. The oldest in-window first-parent merge
// commit on the trunk whose parents past the first contain the branch tip gives
// a "merge" — every later merge contains the branch transitively, so only the
// oldest is its own — and its fork is recomputed as the merge base with that
// commit's first parent, the pre-merge trunk, recovering the true divergence
// point the post-merge trunk tip hides; absent such a commit it was a
// "fast-forward" at the branch tip. Otherwise the branch's real commits are
// off-trunk, so a squash is inferred from a cc-task trailer on a trunk commit
// naming a task folded onto it and done; failing that, the branch stays active.
//
// The first-parent merge scan is bounded by walkLimit (1000) and by the window
// cutoff: a branch whose merge lies further behind the trunk tip than either
// reports "fast-forward" at its tip instead of the true merge commit.
func (b *Builder) classifyMerges(ctx context.Context, trunk *branchState, others []*branchState, tasks []model.Task, r *topoRun) error {
	merged, err := r.mergedIntoTrunk(trunk.tip)
	if err != nil {
		return err
	}
	var landing []*branchState
	var refs []string
	for _, s := range others {
		s.status = statusActive
		if merged[s.ref] {
			landing = append(landing, s)
			refs = append(refs, s.ref)
		}
	}
	if len(landing) > 0 {
		merges, err := r.firstParentMerges(trunk.tip)
		if err != nil {
			return err
		}
		if err := r.prefetchContained(ctx, refs, mergeParents(merges)); err != nil {
			return err
		}
		if err := b.placeMerges(ctx, trunk, landing, merges, r); err != nil {
			return err
		}
	}
	return b.inferSquashes(ctx, trunk, others, tasks, r)
}

// placeMerges pins each merged branch to the oldest in-window trunk merge that
// contains it, then recomputes its fork against that merge's pre-merge trunk,
// with every merge base prefetched in one fan-out. A branch no merge commit
// claims was absorbed linearly: a fast-forward at its own tip.
func (b *Builder) placeMerges(ctx context.Context, trunk *branchState, landing []*branchState, merges []gitobj.CodeCommit, r *topoRun) error {
	landed := make(map[*branchState]gitobj.CodeCommit, len(landing))
	pairs := make([][2]model.SHA, 0, len(landing))
	for _, s := range landing {
		// merges is newest-first; scan oldest-first so the branch lands on its
		// own merge, not a later one whose side contains it transitively.
		for i := len(merges) - 1; i >= 0; i-- {
			if !r.contains(merges[i], s.ref) {
				continue
			}
			landed[s] = merges[i]
			pairs = append(pairs, [2]model.SHA{s.tip, merges[i].Parents[0]})
			break
		}
	}
	if err := b.prefetchMergeBases(ctx, pairs); err != nil {
		return err
	}
	for _, s := range landing {
		s.status = statusMerged
		m, ok := landed[s]
		if !ok {
			s.merge = &mergeInfo{sha: s.tip, time: s.tipTime, into: trunk.name, kind: kindFastForward}
			continue
		}
		base, found, err := b.mergeBaseOf(ctx, s.tip, m.Parents[0])
		if err != nil {
			return err
		}
		if found {
			s.forkBase = base
			if s.forkTime, err = r.commitTime(base); err != nil {
				return err
			}
		}
		s.merge = &mergeInfo{sha: m.SHA, time: m.CommitTime, into: trunk.name, kind: kindMerge}
	}
	return nil
}

// inferSquashes runs squash inference over the branches that could possibly
// match: still open, forked off the trunk, and named by a done task, since a
// trailer only counts when it names a done task folded onto the branch. That
// pre-filter is what keeps a repository full of stale branches from running a
// git log each; the survivors' logs are read in one fan-out.
func (b *Builder) inferSquashes(ctx context.Context, trunk *branchState, others []*branchState, tasks []model.Task, r *topoRun) error {
	done := doneTaskBranches(tasks)
	var candidates []*branchState
	for _, s := range others {
		if s.status == statusMerged || !s.hasFork || !done[s.name] {
			continue
		}
		candidates = append(candidates, s)
	}
	if len(candidates) == 0 {
		return nil
	}
	bases := make([]model.SHA, 0, len(candidates))
	for _, s := range candidates {
		bases = append(bases, s.forkBase)
	}
	if err := r.prefetchTrailers(ctx, bases, trunk.tip); err != nil {
		return err
	}
	for _, s := range candidates {
		if err := b.inferSquash(s, trunk, tasks, r); err != nil {
			return err
		}
	}
	return nil
}

// inferSquash looks for a squash merge of B: a commit on the trunk's
// first-parent line in the fork..trunk window carrying a cc-task trailer that
// names a task folded onto B and done. The newest such commit (ties broken by
// sha) becomes an "inferred" merge. Restricting to the first-parent line keeps a
// trailer on a merged side branch — which names a task folded onto a different,
// still-active branch — from falsely marking B squash-merged.
func (b *Builder) inferSquash(s, trunk *branchState, tasks []model.Task, r *topoRun) error {
	var bestSHA model.SHA
	var bestTime int64
	found := false
	for sha, values := range r.trailers[s.forkBase] {
		for _, v := range values {
			task, ok := matchTask(tasks, v)
			if !ok || string(task.Branch) != s.name || task.Status != model.StatusDone {
				continue
			}
			ct, err := r.commitTime(sha)
			if err != nil {
				return err
			}
			if !found || ct > bestTime || (ct == bestTime && sha > bestSHA) {
				bestSHA, bestTime, found = sha, ct, true
			}
			break
		}
	}
	if found {
		s.merge = &mergeInfo{sha: bestSHA, time: bestTime, into: trunk.name, kind: kindInferred}
		s.status = statusMerged
	}
	return nil
}

// doneTaskBranches is the set of branch names carrying a done task — the only
// branches a cc-task trailer can ever mark squash-merged.
func doneTaskBranches(tasks []model.Task) map[string]bool {
	done := make(map[string]bool)
	for _, t := range tasks {
		if t.Branch != "" && t.Status == model.StatusDone {
			done[string(t.Branch)] = true
		}
	}
	return done
}

// matchTask resolves a cc-task trailer value to a task, accepting either the
// full entity id or a short (>= 7-char) id prefix.
func matchTask(tasks []model.Task, value string) (model.Task, bool) {
	for _, t := range tasks {
		id := string(t.ID)
		if id == value || (len(value) >= 7 && strings.HasPrefix(id, value)) {
			return t, true
		}
	}
	return model.Task{}, false
}
