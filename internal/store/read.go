package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	"golang.org/x/sync/errgroup"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
)

// tipEntry pairs a ref name with the commit it points at.
type tipEntry struct {
	ref string
	tip model.SHA
}

// Load resolves ref and folds its chain into a snapshot. A missing ref
// fails with gitobj.ErrRefNotFound.
func (s *Store) Load(ctx context.Context, ref string) (model.Snapshot, error) {
	tip, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", ref, err)
	}
	chain, err := s.Repo.ReadChain(ctx, tip)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", ref, err)
	}
	snapshot, err := fold.Fold(chain)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", ref, err)
	}
	return snapshot, nil
}

// ListNotes folds every note in the repository, ordered by creation time
// then id. Tombstoned notes are skipped unless includeDeleted is set.
func (s *Store) ListNotes(ctx context.Context, includeDeleted bool) ([]model.Note, error) {
	entries, err := s.children(ctx, refs.NotesPrefix)
	if err != nil {
		return nil, err
	}
	all, err := foldAll(ctx, s, entries, fold.Note)
	if err != nil {
		return nil, err
	}
	notes := make([]model.Note, 0, len(all))
	for _, n := range all {
		if includeDeleted || !n.Deleted {
			notes = append(notes, n)
		}
	}
	slices.SortFunc(notes, func(a, b model.Note) int {
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return notes, nil
}

// ListTasks folds every live task in branch's namespace, ordered by creation
// time then id. A task ref is live only while its folded branch still equals
// the ref's branch: a promote op moves the folded branch and tombstones the
// old ref in place, so a promoted-away chain no longer lists here.
func (s *Store) ListTasks(ctx context.Context, branch model.Branch) ([]model.Task, error) {
	if branch == "" {
		return nil, errors.New("list tasks: empty branch")
	}
	entries, err := s.children(ctx, refs.TasksPrefix(branch))
	if err != nil {
		return nil, err
	}
	all, err := foldAll(ctx, s, entries, fold.Task)
	if err != nil {
		return nil, err
	}
	tasks := make([]model.Task, 0, len(all))
	for _, t := range all {
		if t.Branch == branch {
			tasks = append(tasks, t)
		}
	}
	slices.SortFunc(tasks, func(a, b model.Task) int {
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return tasks, nil
}

// children lists the refs that are immediate children of prefix, excluding
// nested namespaces such as sub-branch task dirs.
func (s *Store) children(ctx context.Context, prefix string) ([]tipEntry, error) {
	tips, err := s.Repo.ListPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	entries := make([]tipEntry, 0, len(tips))
	for ref, tip := range tips {
		if refs.DirectChild(prefix, ref) {
			entries = append(entries, tipEntry{ref: ref, tip: tip})
		}
	}
	return entries, nil
}

// foldAll loads and folds every entry's chain, fanning out through an
// errgroup bounded at listConcurrency.
func foldAll[T model.Snapshot](ctx context.Context, s *Store, entries []tipEntry, foldFn func([]model.PackCommit) (T, error)) ([]T, error) {
	out := make([]T, len(entries))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(listConcurrency)
	for i, e := range entries {
		g.Go(func() error {
			chain, err := s.Repo.ReadChain(gctx, e.tip)
			if err != nil {
				return fmt.Errorf("load %s: %w", e.ref, err)
			}
			snapshot, err := foldFn(chain)
			if err != nil {
				return fmt.Errorf("fold %s: %w", e.ref, err)
			}
			out[i] = snapshot
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}
