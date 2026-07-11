package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"golang.org/x/sync/errgroup"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
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
	if snap, ok := s.cache.get(tip); ok {
		return snap, nil
	}
	chain, err := s.Repo.ReadChain(ctx, tip)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", ref, err)
	}
	snapshot, err := fold.Fold(chain)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", ref, err)
	}
	s.cache.put(tip, snapshot)
	return snapshot, nil
}

// LoadAt folds the chain ending at head into a snapshot, using the same
// tip-keyed fold cache as Load. Where Load resolves a ref to its current tip,
// LoadAt takes the chain tip directly, so a caller holding a past head sha —
// the base a file-edit buffer was rendered from — can reconstruct that exact
// snapshot to diff an edit against. A head whose chain is unreadable fails.
func (s *Store) LoadAt(ctx context.Context, head model.SHA) (model.Snapshot, error) {
	if snap, ok := s.cache.get(head); ok {
		return snap, nil
	}
	chain, err := s.Repo.ReadChain(ctx, head)
	if err != nil {
		return nil, fmt.Errorf("load at %s: %w", head, err)
	}
	snapshot, err := fold.Fold(chain)
	if err != nil {
		return nil, fmt.Errorf("load at %s: %w", head, err)
	}
	s.cache.put(head, snapshot)
	return snapshot, nil
}

// HasNotes reports whether the repository holds any cc-notes entity: any ref
// under refs/cc-notes/. It is the in-process equivalent of
// `git for-each-ref --count=1 refs/cc-notes/`, with no binary lookup.
func (s *Store) HasNotes(ctx context.Context) (bool, error) {
	tips, err := s.Repo.ListPrefix(ctx, refs.Namespace)
	if err != nil {
		return false, err
	}
	return len(tips) > 0, nil
}

// ListNotes folds every note in the repository, ordered by creation time
// then id. Tombstoned notes are skipped unless includeDeleted is set, and
// superseded notes (those with any SupersededBy edge) are skipped unless
// includeSuperseded is set.
func (s *Store) ListNotes(ctx context.Context, includeDeleted, includeSuperseded bool) ([]model.Note, error) {
	entries, err := s.children(ctx, refs.Root(model.KindNote))
	if err != nil {
		return nil, err
	}
	all, err := foldAll(ctx, s, entries, fold.Note)
	if err != nil {
		return nil, err
	}
	notes := make([]model.Note, 0, len(all))
	for _, n := range all {
		if !includeDeleted && n.Deleted {
			continue
		}
		if !includeSuperseded && len(n.SupersededBy) > 0 {
			continue
		}
		notes = append(notes, n)
	}
	slices.SortFunc(notes, func(a, b model.Note) int {
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return notes, nil
}

// ListDocs folds every doc in the repository, ordered by creation time
// then id. Tombstoned docs are skipped unless includeDeleted is set, and
// superseded docs (those with any SupersededBy edge) are skipped unless
// includeSuperseded is set.
func (s *Store) ListDocs(ctx context.Context, includeDeleted, includeSuperseded bool) ([]model.Doc, error) {
	entries, err := s.children(ctx, refs.Root(model.KindDoc))
	if err != nil {
		return nil, err
	}
	all, err := foldAll(ctx, s, entries, fold.Doc)
	if err != nil {
		return nil, err
	}
	docs := make([]model.Doc, 0, len(all))
	for _, d := range all {
		if !includeDeleted && d.Deleted {
			continue
		}
		if !includeSuperseded && len(d.SupersededBy) > 0 {
			continue
		}
		docs = append(docs, d)
	}
	slices.SortFunc(docs, func(a, b model.Doc) int {
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return docs, nil
}

// ListLogs folds every log in the repository, ordered by creation time then
// id. Tombstoned logs are skipped unless includeDeleted is set. Logs carry no
// supersede lifecycle, so there is no superseded filter.
func (s *Store) ListLogs(ctx context.Context, includeDeleted bool) ([]model.Log, error) {
	entries, err := s.children(ctx, refs.Root(model.KindLog))
	if err != nil {
		return nil, err
	}
	all, err := foldAll(ctx, s, entries, fold.Log)
	if err != nil {
		return nil, err
	}
	logs := make([]model.Log, 0, len(all))
	for _, l := range all {
		if !includeDeleted && l.Deleted {
			continue
		}
		logs = append(logs, l)
	}
	slices.SortFunc(logs, func(a, b model.Log) int {
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return logs, nil
}

// ListTasks folds every task in the repository, ordered by creation time
// then id. Branch is a folded attribute; callers filter by it.
func (s *Store) ListTasks(ctx context.Context) ([]model.Task, error) {
	entries, err := s.children(ctx, refs.Root(model.KindTask))
	if err != nil {
		return nil, err
	}
	all, err := foldAll(ctx, s, entries, fold.Task)
	if err != nil {
		return nil, err
	}
	tasks := make([]model.Task, 0, len(all))
	tasks = append(tasks, all...)
	slices.SortFunc(tasks, func(a, b model.Task) int {
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return tasks, nil
}

// ListSprints folds every sprint in the repository, ordered by creation time
// then id.
func (s *Store) ListSprints(ctx context.Context) ([]model.Sprint, error) {
	entries, err := s.children(ctx, refs.Root(model.KindSprint))
	if err != nil {
		return nil, err
	}
	all, err := foldAll(ctx, s, entries, fold.Sprint)
	if err != nil {
		return nil, err
	}
	sprints := make([]model.Sprint, 0, len(all))
	sprints = append(sprints, all...)
	slices.SortFunc(sprints, func(a, b model.Sprint) int {
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return sprints, nil
}

// ListProjects folds every project in the repository, ordered by creation time
// then id.
func (s *Store) ListProjects(ctx context.Context) ([]model.Project, error) {
	entries, err := s.children(ctx, refs.Root(model.KindProject))
	if err != nil {
		return nil, err
	}
	all, err := foldAll(ctx, s, entries, fold.Project)
	if err != nil {
		return nil, err
	}
	projects := make([]model.Project, 0, len(all))
	projects = append(projects, all...)
	slices.SortFunc(projects, func(a, b model.Project) int {
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return projects, nil
}

// ListRunbooks folds every runbook in the repository, ordered by creation time
// then id.
func (s *Store) ListRunbooks(ctx context.Context) ([]model.Runbook, error) {
	entries, err := s.children(ctx, refs.Root(model.KindRunbook))
	if err != nil {
		return nil, err
	}
	all, err := foldAll(ctx, s, entries, fold.Runbook)
	if err != nil {
		return nil, err
	}
	runbooks := make([]model.Runbook, 0, len(all))
	runbooks = append(runbooks, all...)
	slices.SortFunc(runbooks, func(a, b model.Runbook) int {
		if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	return runbooks, nil
}

// children lists the refs that are immediate children of prefix, excluding
// nested namespaces.
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
			if snap, ok := s.cache.get(e.tip); ok {
				if t, ok := snap.(T); ok {
					out[i] = t
					return nil
				}
			}
			chain, err := s.Repo.ReadChain(gctx, e.tip)
			if err != nil {
				return fmt.Errorf("load %s: %w", e.ref, err)
			}
			snapshot, err := foldFn(chain)
			if err != nil {
				return fmt.Errorf("fold %s: %w", e.ref, err)
			}
			s.cache.put(e.tip, snapshot)
			out[i] = snapshot
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	return out, nil
}
