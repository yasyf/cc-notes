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
//
// The first-parent merge scan is bounded by walkLimit (1000): a branch whose
// merge lies more than that many first-parent merges behind the trunk tip
// reports "fast-forward" at its tip instead of the true merge commit.
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
			// An octopus merge carries B under any non-first parent, so scan every
			// one; the first that contains B's tip is the merge that landed it.
			matched := false
			for _, parent := range m.Parents[1:] {
				in, err := b.store.Repo.IsAncestor(ctx, s.tip, parent)
				if err != nil {
					return fmt.Errorf("ancestry %s %s: %w", s.tip, parent, err)
				}
				if in {
					matched = true
					break
				}
			}
			if matched {
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

// inferSquash looks for a squash merge of B: a commit on the trunk's
// first-parent line in the fork..trunk window carrying a cc-task trailer that
// names a task folded onto B and done. The newest such commit (ties broken by
// sha) becomes an "inferred" merge. Restricting to the first-parent line keeps a
// trailer on a merged side branch — which names a task folded onto a different,
// still-active branch — from falsely marking B squash-merged.
func (b *Builder) inferSquash(ctx context.Context, s, trunk *branchState, tasks []model.Task, r *topoRun) error {
	trailers, err := b.store.Git.TaskTrailersFirstParent(ctx, string(s.forkBase), string(trunk.tip))
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
