package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"

	bolt "go.etcd.io/bbolt"
	"golang.org/x/sync/errgroup"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitobj"
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
	if err := s.CheckRecords(); err != nil {
		return nil, fmt.Errorf("load %s: %w", ref, err)
	}
	tip, err := s.managedTip(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", ref, err)
	}
	if snap, ok := s.cache.get(tip); ok {
		return snap, nil
	}
	snapshot, publication, err := s.foldAt(ctx, ref, tip)
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", ref, err)
	}
	if publication != nil {
		s.cache.writeBatch([]cachePublication{*publication})
	}
	return snapshot, nil
}

// LoadAt folds the chain ending at head into a snapshot, using the same
// tip-keyed fold cache as Load. Where Load resolves a ref to its current tip,
// LoadAt takes the chain tip directly, so a caller holding a past head sha —
// the base a file-edit buffer was rendered from — can reconstruct that exact
// snapshot to diff an edit against. A head whose chain is unreadable fails.
func (s *Store) LoadAt(ctx context.Context, head model.SHA) (model.Snapshot, error) {
	snapshot, publication, err := s.loadAt(ctx, "", head)
	if err != nil {
		return nil, err
	}
	if publication != nil {
		s.cache.writeBatch([]cachePublication{*publication})
	}
	return snapshot, nil
}

// loadAt folds head through the fold cache. ref names the entity head belongs
// to, or is empty when the caller holds only a sha; an entity ref lets a fold
// that misses the cache start from the entity's checkpoint.
func (s *Store) loadAt(ctx context.Context, ref string, head model.SHA) (model.Snapshot, *cachePublication, error) {
	if err := s.CheckRecords(); err != nil {
		return nil, nil, fmt.Errorf("load at %s: %w", head, err)
	}
	if snap, ok := s.cache.get(head); ok {
		return snap, nil, nil
	}
	snapshot, publication, err := s.foldAt(ctx, ref, head)
	if err != nil {
		return nil, nil, fmt.Errorf("load at %s: %w", head, err)
	}
	return snapshot, publication, nil
}

func (s *Store) foldAt(ctx context.Context, ref string, head model.SHA) (model.Snapshot, *cachePublication, error) {
	commit, err := s.Repo.ReadCommit(ctx, head)
	if err == nil && len(commit.Parents) == 1 && !slices.ContainsFunc(commit.Pack.Ops, isCheckpoint) {
		if base, top, floor, ok := s.cache.getTop(commit.Parents[0]); ok && commit.Pack.Lamport > floor {
			if snapshot, err := fold.Extend(base, commit); err == nil {
				return snapshot, foldPublication(head, snapshot, max(top, commit.Pack.Lamport), floor, commit.Parents[0]), nil
			}
		}
	}
	if err == nil && !slices.ContainsFunc(commit.Pack.Ops, isCheckpoint) {
		if snapshot, publication, ok, err := s.foldLinear(ctx, ref, commit); err != nil || ok {
			return snapshot, publication, err
		}
	}
	snapshot, top, ok, cpErr := s.foldFromCheckpoint(ctx, ref, head)
	if cpErr != nil {
		return nil, nil, cpErr
	}
	if ok {
		return snapshot, foldPublication(head, snapshot, top, 0), nil
	}
	var chain []model.PackCommit
	if err == nil {
		chain, err = s.Repo.ReadChainFromCommit(ctx, commit)
	} else {
		chain, err = s.Repo.ReadChain(ctx, head)
	}
	if err != nil {
		return nil, nil, err
	}
	snapshot, err = fold.Fold(chain)
	if err != nil {
		return nil, nil, err
	}
	if err := s.checkpointFromChain(ctx, ref, head, chain); err != nil {
		return nil, nil, err
	}
	return snapshot, foldChainPublication(head, snapshot, chain), nil
}

// foldLinear folds a chain that is linear below head without holding it: it
// walks first parents back to the nearest commit the fold cache holds, or to
// the root, then streams the commits above that base through the fold oldest
// first, so memory stays at one snapshot however long the chain. A merge, a
// checkpoint, or a commit the fold refuses leaves the chain to the full fold,
// and foldLinear reports false. So does an entity with a checkpoint whose
// cached base lies more than checkpointInterval commits down, which folds from
// its checkpoint instead. A fold from the root that a checkpoint is due on
// streams in legs, recording the snapshot at each of checkpointLags below head
// on the way.
func (s *Store) foldLinear(ctx context.Context, ref string, head model.PackCommit) (model.Snapshot, *cachePublication, bool, error) {
	ctx, release := gitobj.ShareReaders(ctx)
	defer func() { _ = release() }()
	bounded, err := s.hasCheckpoint(ctx, ref)
	if err != nil {
		return nil, nil, false, err
	}
	var base model.Snapshot
	var top, floor model.Lamport
	var cached []model.SHA
	var steps model.Lamport
	tooDeep := false
	walked, linear, err := s.Repo.LinearHistory(ctx, head.SHA, func(sha model.SHA) bool {
		snap, snapTop, snapFloor, ok := s.cache.getTop(sha)
		if ok {
			base, top, floor, cached = snap, snapTop, snapFloor, []model.SHA{sha}
			return true
		}
		steps++
		tooDeep = bounded && steps > checkpointInterval
		return tooDeep
	})
	if err != nil || !linear || tooDeep {
		return nil, nil, false, nil
	}
	var legs []int
	if base == nil {
		due, err := s.checkpointDue(ctx, ref, head.Pack.Lamport)
		if err != nil {
			return nil, nil, false, err
		}
		depth := model.Lamport(len(walked)) //nolint:gosec // G115: a slice length is nonnegative.
		for _, lag := range checkpointLags() {
			if due && lag < depth {
				legs = append(legs, int(lag)) //nolint:gosec // G115: lag is below a slice length.
			}
		}
	}
	next := len(walked) - 1
	stream := func(stop int) func() (model.PackCommit, bool, error) {
		return func() (model.PackCommit, bool, error) {
			if next < stop {
				return model.PackCommit{}, false, nil
			}
			sha := walked[next]
			next--
			c := head
			if sha != head.SHA {
				var err error
				if c, err = s.Repo.ReadCommit(ctx, sha); err != nil {
					return model.PackCommit{}, false, err
				}
			}
			if base != nil && c.Pack.Lamport <= floor {
				return model.PackCommit{}, false, fmt.Errorf("%s at lamport %d is not above the cached floor %d", c.SHA, c.Pack.Lamport, floor)
			}
			top = max(top, c.Pack.Lamport)
			return c, true, nil
		}
	}
	var entries []checkpointCandidate
	for _, leg := range slices.Backward(legs) {
		if base, err = fold.Stream(base, stream(leg)); err != nil {
			return nil, nil, false, nil
		}
		entries = append([]checkpointCandidate{{frontier: walked[leg], covers: top, state: base}}, entries...)
	}
	snapshot, err := fold.Stream(base, stream(0))
	if err != nil {
		return nil, nil, false, nil
	}
	if err := s.recordCheckpoint(ctx, ref, entries); err != nil {
		return nil, nil, false, err
	}
	return snapshot, foldPublication(head.SHA, snapshot, top, floor, cached...), true, nil
}

// LoadRootedAt folds the immutable chain ending at head and returns both its
// snapshot and unique create commit. It never resolves a mutable ref.
func (s *Store) LoadRootedAt(ctx context.Context, head model.SHA) (RootedSnapshot, error) {
	rooted, publication, err := s.loadRootedAt(ctx, "", head)
	if err != nil {
		return RootedSnapshot{}, err
	}
	if publication != nil {
		s.cache.writeBatch([]cachePublication{*publication})
	}
	return rooted, nil
}

// loadRootedAt is LoadRootedAt for the entity ref names, which may be empty as
// loadAt's may. A fold from ref's checkpoint reads the create commit alone,
// which the checkpoint's root fold proved the chain's only one.
func (s *Store) loadRootedAt(ctx context.Context, ref string, head model.SHA) (RootedSnapshot, *cachePublication, error) {
	if err := s.CheckRecords(); err != nil {
		return RootedSnapshot{}, nil, fmt.Errorf("load rooted at %s: %w", head, err)
	}
	if snapshot, top, ok, err := s.foldFromCheckpoint(ctx, ref, head); err != nil || ok {
		if err != nil {
			return RootedSnapshot{}, nil, fmt.Errorf("load rooted at %s: %w", head, err)
		}
		root, err := s.Repo.ReadCommit(ctx, model.SHA(snapshot.EntityID()))
		if err != nil {
			return RootedSnapshot{}, nil, fmt.Errorf("root at %s: %w", head, err)
		}
		return RootedSnapshot{Snapshot: snapshot, Root: root}, foldPublication(head, snapshot, top, 0), nil
	}
	chain, err := s.Repo.ReadChain(ctx, head)
	if err != nil {
		return RootedSnapshot{}, nil, fmt.Errorf("load rooted at %s: %w", head, err)
	}
	snapshot, err := fold.Fold(chain)
	if err != nil {
		return RootedSnapshot{}, nil, fmt.Errorf("load rooted at %s: %w", head, err)
	}
	root, err := uniqueRootCommit(chain)
	if err != nil {
		return RootedSnapshot{}, nil, fmt.Errorf("root at %s: %w", head, err)
	}
	if err := s.checkpointFromChain(ctx, ref, head, chain); err != nil {
		return RootedSnapshot{}, nil, fmt.Errorf("checkpoint at %s: %w", head, err)
	}
	return RootedSnapshot{Snapshot: snapshot, Root: root}, foldChainPublication(head, snapshot, chain), nil
}

// HasNotes reports whether the records repository holds any cc-notes entity:
// any ref under refs/cc-notes/, found by one bounded existence query rather
// than an enumeration.
func (s *Store) HasNotes(ctx context.Context) (bool, error) {
	if err := s.CheckRecords(); err != nil {
		return false, err
	}
	if fromRefs, err := s.tipsFromRefs(); err != nil || fromRefs {
		if err != nil {
			return false, err
		}
		tips, err := s.Repo.ListPrefix(ctx, refs.Namespace)
		return len(tips) > 0, err
	}
	var present bool
	err := s.managedView(ctx, func(tx *bolt.Tx) error {
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		key, _ := root.Bucket([]byte("refs")).Cursor().First()
		present = key != nil
		return nil
	})
	return present, err
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
			s.cache.putChain(entry.tip, snapshot, chain)
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
	if err := s.CheckRecords(); err != nil {
		return false, err
	}
	tip, err := s.managedTip(ctx, ref)
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

// ListLedgers folds every ledger in the repository, ordered by creation time
// then id. Tombstoned ledgers are skipped.
func (s *Store) ListLedgers(ctx context.Context) ([]model.Ledger, error) {
	return listOf(ctx, s, model.KindLedger, fold.Ledger, ListOpts{})
}

// ListInvestigations folds every investigation in the repository, ordered by
// creation time then id. The default filter hides tombstoned and superseded
// records, matching the other durable kinds.
func (s *Store) ListInvestigations(ctx context.Context) ([]model.Investigation, error) {
	return listOf(ctx, s, model.KindInvestigation, fold.Investigation, ListOpts{})
}

// ListPlans folds every plan in the repository, ordered by creation time then
// id. The default filter hides tombstoned and superseded records, matching the
// other durable kinds.
func (s *Store) ListPlans(ctx context.Context) ([]model.Plan, error) {
	return listOf(ctx, s, model.KindPlan, fold.Plan, ListOpts{})
}

// ListAnswers folds every answer in the repository, ordered by creation time
// then id. Same tombstone and supersede filters as ListNotes.
func (s *Store) ListAnswers(ctx context.Context, includeDeleted, includeSuperseded bool) ([]model.Answer, error) {
	return listOf(ctx, s, model.KindAnswer, fold.Answer, ListOpts{IncludeDeleted: includeDeleted, IncludeSuperseded: includeSuperseded})
}

// PinnedKinds reads the refs of exactly kinds from the records repository and
// returns the Pinned view of them, so listing several kinds folds one
// consistent set of tips without a ref query per listing.
func (s *Store) PinnedKinds(ctx context.Context, kinds ...model.Kind) (*Store, error) {
	if err := s.CheckRecords(); err != nil {
		return nil, err
	}
	roots := make([]string, len(kinds))
	for i, kind := range kinds {
		roots[i] = refs.Root(kind)
	}
	tips, err := s.ManagedTips(ctx, roots...)
	if err != nil {
		return nil, err
	}
	if tips == nil {
		tips = map[string]model.SHA{}
	}
	return s.Pinned(tips), nil
}

func (s *Store) children(ctx context.Context, prefix string) ([]tipEntry, error) {
	return s.childrenInRange(ctx, prefix, prefix)
}

func (s *Store) childrenInRange(ctx context.Context, namespace, selection string) ([]tipEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.CheckRecords(); err != nil {
		return nil, err
	}
	tips := s.pins
	if tips == nil {
		var err error
		if tips, err = s.ManagedTips(ctx, selection); err != nil {
			return nil, err
		}
	}
	return directChildren(namespace, tips), nil
}

func directChildren(prefix string, tips map[string]model.SHA) []tipEntry {
	entries := make([]tipEntry, 0, len(tips))
	for ref, tip := range tips {
		if refs.DirectChild(prefix, ref) {
			entries = append(entries, tipEntry{ref: ref, tip: tip})
		}
	}
	return entries
}

// foldAll loads and folds every entry's chain, fanning out through an
// errgroup bounded at listConcurrency.
func foldAll[T model.Snapshot](ctx context.Context, s *Store, entries []tipEntry, foldFn func([]model.PackCommit) (T, error)) ([]T, error) {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = string(entry.tip)
	}
	cached := s.cache.entries(ctx, names)
	batch := newFoldPublicationBatch(ctx, s.cache)
	defer batch.discard()
	out := make([]T, len(entries))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(listConcurrency)
	for i, e := range entries {
		g.Go(func() error {
			if entry, ok := cached[string(e.tip)]; ok {
				if snap, ok := s.cache.getEntry(e.tip, entry); ok {
					if t, ok := snap.(T); ok {
						out[i] = t
						return nil
					}
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
			batch.putChain(e.tip, snapshot, chain)
			out[i] = snapshot
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	batch.flush()
	return out, nil
}
