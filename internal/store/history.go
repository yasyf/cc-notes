package store

import (
	"context"
	"fmt"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

// History resolves ref and folds its chain step by step: one fold.Step per
// commit in linearization order (oldest first), each carrying the entity
// snapshot through that commit. A caller diffs adjacent steps to see what each
// commit changed. A missing ref fails with gitobj.ErrRefNotFound. Unlike Load
// it does not consult the fold cache — the trail is a one-off read.
func (s *Store) History(ctx context.Context, ref string) ([]fold.Step, error) {
	chain, err := s.historyChain(ctx, ref)
	if err != nil {
		return nil, err
	}
	steps, err := fold.History(chain)
	if err != nil {
		return nil, fmt.Errorf("history %s: %w", ref, err)
	}
	return steps, nil
}

// Walk resolves ref and calls fn with each fold.Step History would return, in
// the same order, through fold.Walk: each step's Snapshot is valid only until fn
// returns, so the walk holds one fold's state rather than every step's. Each
// step is folded first and then checked against ctx before fn sees it, so once
// ctx is done the walk stops at the next folded step with ctx's error; it stops
// with fn's error when fn fails. A missing ref fails with gitobj.ErrRefNotFound.
func (s *Store) Walk(ctx context.Context, ref string, fn func(fold.Step) error) error {
	chain, err := s.historyChain(ctx, ref)
	if err != nil {
		return err
	}
	err = fold.Walk(chain, func(st fold.Step) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return fn(st)
	})
	if err != nil {
		return fmt.Errorf("history %s: %w", ref, err)
	}
	return nil
}

func (s *Store) historyChain(ctx context.Context, ref string) ([]model.PackCommit, error) {
	if err := s.CheckRecords(); err != nil {
		return nil, fmt.Errorf("history %s: %w", ref, err)
	}
	tip, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("history %s: %w", ref, err)
	}
	chain, err := s.Repo.ReadChain(ctx, tip)
	if err != nil {
		return nil, fmt.Errorf("history %s: %w", ref, err)
	}
	return chain, nil
}
