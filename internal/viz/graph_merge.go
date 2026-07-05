package viz

import (
	"context"
	"fmt"
	"strings"

	"github.com/yasyf/cc-notes/model"
)

// detectMerge classifies branch B's rejoin into the trunk. When B's tip is an
// ancestor of the trunk tip it is merged: the oldest first-parent merge commit
// on the trunk whose second parent contains B's tip gives a "merge" — every
// later merge contains B transitively, so only the oldest is B's own — and B's
// fork is recomputed as its merge base with that commit's first parent, the
// pre-merge trunk, recovering the true divergence point the post-merge trunk
// tip hides; absent such a commit it was a "fast-forward" at B's tip. Otherwise
// B's real commits are off-trunk, so a squash is inferred from a cc-task
// trailer on a trunk commit naming a task folded onto B and done; failing that,
// B stays active.
func (b *Builder) detectMerge(ctx context.Context, s, trunk *branchState, tasks []model.Task, r *topoRun) error {
	s.status = statusActive
	anc, err := b.store.Repo.IsAncestor(ctx, s.tip, trunk.tip)
	if err != nil {
		return fmt.Errorf("ancestry %s %s: %w", s.tip, trunk.tip, err)
	}
	if anc {
		merges, err := b.store.Repo.FirstParentMerges(ctx, trunk.tip, walkLimit, 0)
		if err != nil {
			return fmt.Errorf("first-parent merges: %w", err)
		}
		// merges is newest-first; scan oldest-first so B lands on its own
		// merge, not a later one whose branch contains B's tip transitively.
		for i := len(merges) - 1; i >= 0; i-- {
			m := merges[i]
			if len(m.Parents) < 2 {
				continue
			}
			in, err := b.store.Repo.IsAncestor(ctx, s.tip, m.Parents[1])
			if err != nil {
				return fmt.Errorf("ancestry %s %s: %w", s.tip, m.Parents[1], err)
			}
			if in {
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
				s.status = statusMerged
				return nil
			}
		}
		s.merge = &mergeInfo{sha: s.tip, time: s.tipTime, into: trunk.name, kind: kindFastForward}
		s.status = statusMerged
		return nil
	}
	if !s.hasFork {
		return nil
	}
	return b.inferSquash(ctx, s, trunk, tasks, r)
}

// inferSquash looks for a squash merge of B: a trunk commit in the fork..trunk
// window carrying a cc-task trailer that names a task folded onto B and done.
// The newest such commit (ties broken by sha) becomes an "inferred" merge.
func (b *Builder) inferSquash(ctx context.Context, s, trunk *branchState, tasks []model.Task, r *topoRun) error {
	trailers, err := b.store.Git.TaskTrailersRange(ctx, string(s.forkBase), string(trunk.tip))
	if err != nil {
		return fmt.Errorf("task trailers %s..%s: %w", s.forkBase, trunk.tip, err)
	}
	var bestSHA model.SHA
	var bestTime int64
	found := false
	for sha, values := range trailers {
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
