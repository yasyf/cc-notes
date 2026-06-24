package store

import (
	"context"
	"fmt"

	"github.com/yasyf/cc-notes/internal/fold"
)

// History resolves ref and folds its chain step by step: one fold.Step per
// commit in linearization order (oldest first), each carrying the entity
// snapshot through that commit. A caller diffs adjacent steps to see what each
// commit changed. A missing ref fails with gitobj.ErrRefNotFound. Unlike Load
// it does not consult the fold cache — the trail is a one-off read.
func (s *Store) History(ctx context.Context, ref string) ([]fold.Step, error) {
	tip, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("history %s: %w", ref, err)
	}
	chain, err := s.Repo.ReadChain(ctx, tip)
	if err != nil {
		return nil, fmt.Errorf("history %s: %w", ref, err)
	}
	steps, err := fold.History(chain)
	if err != nil {
		return nil, fmt.Errorf("history %s: %w", ref, err)
	}
	return steps, nil
}
