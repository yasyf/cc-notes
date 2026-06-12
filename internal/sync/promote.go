package sync

import (
	"context"
	"errors"
	"fmt"

	"github.com/yasyf/cc-notes/internal/gitcmd"
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
// wrapping ErrNotLive. The destination ref refs.Task(to, id) is created at
// the task's current tip first, so a destination git refuses fails before
// any chain mutation; only then is the promote op appended to the old chain
// — the op is the tombstone, since the old ref is never deleted — and the
// destination advanced to the new tip. A crash between those steps leaves a
// destination ref folding to the old branch: dead in its namespace, and
// folded forward by the next Sync's consolidate. The next Sync publishes
// both refs; everywhere else, a task ref is live only while its folded
// branch equals the ref's branch.
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
		task := loaded.(model.Task)
		if task.Branch != from {
			return fmt.Errorf("promote task %s: %w: %s folds to branch %q", id.Short(), ErrNotLive, fromRef, task.Branch)
		}
		toRef := refs.Task(to, id)
		switch err := s.Git.UpdateRef(ctx, toRef, task.Head, ""); {
		case err == nil, errors.Is(err, gitcmd.ErrCASMismatch):
		default:
			return fmt.Errorf("promote task %s: %w", id.Short(), err)
		}
		snapshot, err := s.Append(ctx, fromRef, []model.Op{model.Promote{From: from, To: to}})
		if err != nil {
			return fmt.Errorf("promote task %s: %w", id.Short(), err)
		}
		if _, err := ensureContains(ctx, s, toRef, snapshot.(model.Task).Head); err != nil {
			return fmt.Errorf("promote task %s: %w", id.Short(), err)
		}
	}
	return nil
}
