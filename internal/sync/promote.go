package sync

import (
	"context"
	"errors"
	"fmt"

	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
)

// Promote moves tasks from one branch namespace to another. Each task gets
// the promote op appended to its old chain — the op is the tombstone, since
// the old ref is never deleted — and the destination ref refs.Task(to, id)
// is created at the new tip, or union-merged with it when it already
// exists. The next Sync publishes both refs; everywhere else, a task ref is
// live only while its folded branch equals the ref's branch.
func Promote(ctx context.Context, s *store.Store, from, to model.Branch, ids []model.EntityID) error {
	if from == "" || to == "" {
		return errors.New("promote: empty branch")
	}
	if from == to {
		return fmt.Errorf("promote: from and to are both %q", from)
	}
	for _, id := range ids {
		snapshot, err := s.Append(ctx, refs.Task(from, id), []model.Op{model.Promote{From: from, To: to}})
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
