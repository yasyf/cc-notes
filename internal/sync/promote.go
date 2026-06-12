package sync

import (
	"context"
	"errors"
	"fmt"

	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
)

// ErrNotLive reports a promote from a dead ref: the task's folded branch no
// longer matches the ref's branch, so recording the promote there would lie
// about where the task came from.
var ErrNotLive = errors.New("task ref not live")

// Promote moves tasks from one branch namespace to another. Each task must
// be live on from — its folded branch equals from, else the promote fails
// wrapping ErrNotLive — and gets the promote op appended to its old chain;
// the op is the tombstone, since the old ref is never deleted. The
// destination ref refs.Task(to, id) is created at the new tip, or
// union-merged with it when it already exists. The next Sync publishes both
// refs; everywhere else, a task ref is live only while its folded branch
// equals the ref's branch.
func Promote(ctx context.Context, s *store.Store, from, to model.Branch, ids []model.EntityID) error {
	if from == "" || to == "" {
		return errors.New("promote: empty branch")
	}
	if from == to {
		return fmt.Errorf("promote: from and to are both %q", from)
	}
	for _, id := range ids {
		fromRef := refs.Task(from, id)
		loaded, err := s.Load(ctx, fromRef)
		if err != nil {
			return fmt.Errorf("promote task %s: %w", id.Short(), err)
		}
		if branch := loaded.(model.Task).Branch; branch != from {
			return fmt.Errorf("promote task %s: %w: %s folds to branch %q", id.Short(), ErrNotLive, fromRef, branch)
		}
		snapshot, err := s.Append(ctx, fromRef, []model.Op{model.Promote{From: from, To: to}})
		if err != nil {
			return fmt.Errorf("promote task %s: %w", id.Short(), err)
		}
		task := snapshot.(model.Task)
		if _, err := ensureContains(ctx, s, refs.Task(to, id), task.Head); err != nil {
			return fmt.Errorf("promote task %s: %w", id.Short(), err)
		}
	}
	return nil
}
