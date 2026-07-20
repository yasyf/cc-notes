package store

import (
	"cmp"
	"context"
	"errors"
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

// RootedSnapshot pairs one folded entity with the create commit that owns its
// durable source identity.
type RootedSnapshot struct {
	Snapshot model.Snapshot
	Root     model.PackCommit
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

// LoadRootedAt folds the immutable chain ending at head and returns both its
// snapshot and unique create commit. It never resolves a mutable ref.
func (s *Store) LoadRootedAt(ctx context.Context, head model.SHA) (RootedSnapshot, error) {
	chain, err := s.Repo.ReadChain(ctx, head)
	if err != nil {
		return RootedSnapshot{}, fmt.Errorf("load rooted at %s: %w", head, err)
	}
	snapshot, err := fold.Fold(chain)
	if err != nil {
		return RootedSnapshot{}, fmt.Errorf("load rooted at %s: %w", head, err)
	}
	root, err := uniqueRootCommit(chain)
	if err != nil {
		return RootedSnapshot{}, fmt.Errorf("root at %s: %w", head, err)
	}
	s.cache.put(head, snapshot)
	return RootedSnapshot{Snapshot: snapshot, Root: root}, nil
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

// ListOpts are the inclusion knobs the List methods honor. A kind that does not
// model a lifecycle leaves the matching Meta field zero, so one set of options
// serves every kind: IncludeSuperseded is inert for logs and the coarse kinds,
// IncludeDeleted inert for the kinds with no delete tombstone.
type ListOpts struct {
	IncludeDeleted    bool
	IncludeSuperseded bool
}

// keepInList reports whether a snapshot with meta survives the list filter under
// opts: deleted tombstones and superseded entities are hidden unless explicitly
// included. Meta reports Deleted and Superseded false for kinds that model
// neither, so this one predicate reproduces every kind's filter.
func keepInList(meta model.Meta, opts ListOpts) bool {
	if meta.Deleted && !opts.IncludeDeleted {
		return false
	}
	if meta.Superseded && !opts.IncludeSuperseded {
		return false
	}
	return true
}

// listOf folds every entity of kind, drops the ones opts filters out, and sorts
// the survivors by creation time then id. foldFn is the kind's typed folder; the
// typed ListX methods and ListSnapshots are thin wrappers over it.
func listOf[T model.Snapshot](ctx context.Context, s *Store, kind model.Kind, foldFn func([]model.PackCommit) (T, error), opts ListOpts) ([]T, error) {
	entries, err := s.children(ctx, refs.Root(kind))
	if err != nil {
		return nil, err
	}
	all, err := foldAll(ctx, s, entries, foldFn)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(all))
	for _, v := range all {
		if keepInList(v.Meta(), opts) {
			out = append(out, v)
		}
	}
	slices.SortFunc(out, func(a, b T) int {
		am, bm := a.Meta(), b.Meta()
		if c := am.CreatedAt.Compare(bm.CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.EntityID(), b.EntityID())
	})
	return out, nil
}

// ListSnapshots folds every entity of kind, applying the same inclusion filter
// and (created, id) ordering as the typed ListX methods, for consumers that
// dispatch on a runtime kind rather than a static type.
func (s *Store) ListSnapshots(ctx context.Context, kind model.Kind, opts ListOpts) ([]model.Snapshot, error) {
	return listOf(ctx, s, kind, fold.Fold, opts)
}

// ListRootedSnapshots folds every entity of kind once and retains its unique
// root create commit for authority identity projection.
func (s *Store) ListRootedSnapshots(ctx context.Context, kind model.Kind, opts ListOpts) ([]RootedSnapshot, error) {
	entries, err := s.children(ctx, refs.Root(kind))
	if err != nil {
		return nil, err
	}
	all := make([]RootedSnapshot, len(entries))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(listConcurrency)
	for i, entry := range entries {
		g.Go(func() error {
			chain, err := s.Repo.ReadChain(gctx, entry.tip)
			if err != nil {
				return fmt.Errorf("load %s: %w", entry.ref, err)
			}
			snapshot, err := fold.Fold(chain)
			if err != nil {
				return fmt.Errorf("fold %s: %w", entry.ref, err)
			}
			root, err := uniqueRootCommit(chain)
			if err != nil {
				return fmt.Errorf("root %s: %w", entry.ref, err)
			}
			s.cache.put(entry.tip, snapshot)
			all[i] = RootedSnapshot{Snapshot: snapshot, Root: root}
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	result := make([]RootedSnapshot, 0, len(all))
	for _, value := range all {
		if keepInList(value.Snapshot.Meta(), opts) {
			result = append(result, value)
		}
	}
	slices.SortFunc(result, func(a, b RootedSnapshot) int {
		am, bm := a.Snapshot.Meta(), b.Snapshot.Meta()
		if order := am.CreatedAt.Compare(bm.CreatedAt); order != 0 {
			return order
		}
		return cmp.Compare(a.Snapshot.EntityID(), b.Snapshot.EntityID())
	})
	return result, nil
}

func uniqueRootCommit(chain []model.PackCommit) (model.PackCommit, error) {
	var root model.PackCommit
	found := false
	for _, commit := range chain {
		for _, operation := range commit.Pack.Ops {
			if _, ok := operation.(model.CreateOp); !ok {
				continue
			}
			if found {
				return model.PackCommit{}, errors.New("entity chain has multiple create operations")
			}
			root, found = commit, true
		}
	}
	if !found {
		return model.PackCommit{}, errors.New("entity chain has no create operation")
	}
	return root, nil
}

// HasSession reports whether ref already contains an operation pack from the
// exact idempotency session.
func (s *Store) HasSession(ctx context.Context, ref, sessionID string) (bool, error) {
	tip, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		return false, err
	}
	chain, err := s.Repo.ReadChain(ctx, tip)
	if err != nil {
		return false, err
	}
	for _, commit := range chain {
		if commit.Pack.Session == sessionID {
			return true, nil
		}
	}
	return false, nil
}

// ListNotes folds every note in the repository, ordered by creation time then
// id. Tombstoned notes are skipped unless includeDeleted is set, and superseded
// notes (those with any SupersededBy edge) unless includeSuperseded is set.
func (s *Store) ListNotes(ctx context.Context, includeDeleted, includeSuperseded bool) ([]model.Note, error) {
	return listOf(ctx, s, model.KindNote, fold.Note, ListOpts{IncludeDeleted: includeDeleted, IncludeSuperseded: includeSuperseded})
}

// ListDocs folds every doc in the repository, ordered by creation time then id.
// Same tombstone and supersede filters as ListNotes.
func (s *Store) ListDocs(ctx context.Context, includeDeleted, includeSuperseded bool) ([]model.Doc, error) {
	return listOf(ctx, s, model.KindDoc, fold.Doc, ListOpts{IncludeDeleted: includeDeleted, IncludeSuperseded: includeSuperseded})
}

// ListLogs folds every log in the repository, ordered by creation time then id.
// Tombstoned logs are skipped unless includeDeleted is set; logs carry no
// supersede lifecycle.
func (s *Store) ListLogs(ctx context.Context, includeDeleted bool) ([]model.Log, error) {
	return listOf(ctx, s, model.KindLog, fold.Log, ListOpts{IncludeDeleted: includeDeleted})
}

// ListTasks folds every task in the repository, ordered by creation time then
// id. Branch is a folded attribute; callers filter by it.
func (s *Store) ListTasks(ctx context.Context) ([]model.Task, error) {
	return listOf(ctx, s, model.KindTask, fold.Task, ListOpts{})
}

// ListSprints folds every sprint in the repository, ordered by creation time
// then id.
func (s *Store) ListSprints(ctx context.Context) ([]model.Sprint, error) {
	return listOf(ctx, s, model.KindSprint, fold.Sprint, ListOpts{})
}

// ListProjects folds every project in the repository, ordered by creation time
// then id.
func (s *Store) ListProjects(ctx context.Context) ([]model.Project, error) {
	return listOf(ctx, s, model.KindProject, fold.Project, ListOpts{})
}

// ListRunbooks folds every runbook in the repository, ordered by creation time
// then id. Tombstoned runbooks are skipped.
func (s *Store) ListRunbooks(ctx context.Context) ([]model.Runbook, error) {
	return listOf(ctx, s, model.KindRunbook, fold.Runbook, ListOpts{})
}

// ListInvestigations folds every investigation in the repository, ordered by
// creation time then id. The default filter hides tombstoned and superseded
// records, matching the other durable kinds.
func (s *Store) ListInvestigations(ctx context.Context) ([]model.Investigation, error) {
	return listOf(ctx, s, model.KindInvestigation, fold.Investigation, ListOpts{})
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
