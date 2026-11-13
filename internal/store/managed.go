package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sync/atomic"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

var managedPendingKey = []byte("pending")

type managedIntent struct {
	Updates  []gitcmd.RefUpdate `json:"updates"`
	Before   []model.SHA        `json:"before"`
	Rows     []queryMetadata    `json:"rows"`
	Source   *sourcePublication `json:"source,omitempty"`
	Imported string             `json:"imported,omitempty"`
}

func managedRoot(tx *bolt.Tx) (*bolt.Bucket, error) {
	root := tx.Bucket([]byte(managed.Format))
	if root == nil || root.Bucket([]byte("refs")) == nil || tx.Bucket([]byte(managed.QueryGenerations)) == nil {
		return nil, managed.ErrReindex
	}
	return root, nil
}

func (s *Store) openManaged(readOnly bool) (*bolt.DB, error) {
	if _, err := os.Stat(managed.Path(s.recordsCommonDir)); err != nil {
		return nil, errors.Join(managed.ErrReindex, err)
	}
	db, err := bolt.Open(managed.Path(s.recordsCommonDir), 0o600, &bolt.Options{ReadOnly: readOnly})
	if err != nil {
		return nil, errors.Join(managed.ErrReindex, err)
	}
	return db, nil
}

// ReadWhileImporting lets s read while its managed authority is missing and
// being imported from the records refs: entity tips then come from those refs,
// which the import copies unchanged, and anything that needs the authority
// itself first calls await, which returns once the import finishes. A rebuild
// of an existing authority serves every read from that authority until it
// swaps in, so a list and a show of one entity always agree.
func (s *Store) ReadWhileImporting(await func(context.Context) error) { s.awaitImport = await }

func (s *Store) tipsFromRefs() (bool, error) {
	if s.awaitImport == nil || s.AuthorityExists() {
		return false, nil
	}
	_, err := os.Stat(managed.LegacyPath(s.recordsCommonDir))
	if errors.Is(err, os.ErrNotExist) {
		return true, nil
	}
	return false, err
}

// EntityTips selects entity refs by prefix for a read: from the authority, or
// from the records refs while ReadWhileImporting allows it.
func (s *Store) EntityTips(ctx context.Context, prefixes ...string) (map[string]model.SHA, error) {
	if fromRefs, err := s.tipsFromRefs(); err != nil || !fromRefs {
		if err != nil {
			return nil, err
		}
		return s.ManagedTips(ctx, prefixes...)
	}
	if err := s.CheckRecords(); err != nil {
		return nil, err
	}
	return s.Repo.ListPrefixes(ctx, prefixes...)
}

func (s *Store) entityTip(ctx context.Context, ref string) (model.SHA, error) {
	if fromRefs, err := s.tipsFromRefs(); err != nil || !fromRefs {
		if err != nil {
			return "", err
		}
		return s.managedTip(ctx, ref)
	}
	if err := s.CheckRecords(); err != nil {
		return "", err
	}
	tips, err := s.Repo.ListPrefix(ctx, ref)
	if err != nil {
		return "", err
	}
	tip, found := tips[ref]
	if !found {
		return "", gitobj.ErrRefNotFound
	}
	return tip, nil
}

func (s *Store) awaitAuthority(ctx context.Context) error {
	if s.awaitImport == nil || s.AuthorityExists() {
		return nil
	}
	return s.awaitImport(ctx)
}

func (s *Store) managedView(ctx context.Context, read func(*bolt.Tx) error) (err error) {
	if err := s.CheckRecords(); err != nil {
		return err
	}
	if err := s.awaitAuthority(ctx); err != nil {
		return err
	}
	lock, err := managed.TryLock(s.recordsCommonDir, false)
	if err != nil {
		return err
	}
	if lock == nil {
		if lock, err = managed.Lock(ctx, s.recordsCommonDir, false); err != nil {
			return err
		}
	}
	ready, err := s.reindexReady()
	if err != nil {
		return errors.Join(err, lock.Close())
	}
	if ready {
		if err := lock.Close(); err != nil {
			return err
		}
		lock, err = managed.Lock(ctx, s.recordsCommonDir, true)
		if err != nil {
			return err
		}
		if ready, err := s.reindexReady(); err != nil {
			return errors.Join(err, lock.Close())
		} else if ready {
			if err := s.finishReindex(ctx, lock); err != nil {
				return errors.Join(err, lock.Close())
			}
		}
	}
	db, err := s.openManaged(true)
	if err != nil {
		return errors.Join(err, lock.Close())
	}
	pending := false
	err = db.View(func(tx *bolt.Tx) error {
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		pending = root.Get(managedPendingKey) != nil
		if pending {
			return nil
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return read(tx)
	})
	err = errors.Join(err, db.Close(), lock.Close())
	if err != nil || !pending {
		return errors.Join(err, s.CheckRecords(), ctx.Err())
	}
	lock, err = managed.Lock(ctx, s.recordsCommonDir, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	db, err = s.openManaged(false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	if err := s.recoverManaged(ctx, db); err != nil {
		return err
	}
	err = db.View(read)
	return errors.Join(err, s.CheckRecords(), ctx.Err())
}

// ManagedTips selects managed entity refs by prefix from one authority snapshot.
func (s *Store) ManagedTips(ctx context.Context, prefixes ...string) (map[string]model.SHA, error) {
	tips := make(map[string]model.SHA)
	err := s.managedView(ctx, func(tx *bolt.Tx) error {
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		for _, prefix := range prefixes {
			cursor := root.Bucket([]byte("refs")).Cursor()
			p := []byte(prefix)
			for key, value := cursor.Seek(p); key != nil && bytes.HasPrefix(key, p); key, value = cursor.Next() {
				if err := ctx.Err(); err != nil {
					return err
				}
				tips[string(key)] = model.SHA(value)
			}
		}
		return nil
	})
	return tips, err
}

func (s *Store) managedTip(ctx context.Context, ref string) (model.SHA, error) {
	var tip model.SHA
	err := s.managedView(ctx, func(tx *bolt.Tx) error {
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		tip = model.SHA(root.Bucket([]byte("refs")).Get([]byte(ref)))
		if tip == "" {
			return gitobj.ErrRefNotFound
		}
		return nil
	})
	return tip, err
}

func (s *Store) managedRows(ctx context.Context, updates []gitcmd.RefUpdate, snapshots map[model.SHA]model.Snapshot, publications *foldPublicationBatch) ([]queryMetadata, error) {
	var rows []queryMetadata
	for _, update := range updates {
		parsed, err := refs.Parse(update.Ref)
		if errors.Is(err, refs.ErrNotCCNotes) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if update.New == "" {
			continue
		}
		var publication *cachePublication
		snapshot := snapshots[update.New]
		if snapshot == nil {
			snapshot, publication, err = s.loadAt(ctx, update.Ref, update.New)
			if err != nil {
				return nil, err
			}
		}
		if snapshot.EntityID() != parsed.ID || snapshot.Meta().Kind != parsed.Kind {
			return nil, fmt.Errorf("managed projection differs from %s", update.Ref)
		}
		source, err := queryMetadataIdentity(ctx, s, parsed.Kind)
		if err != nil {
			return nil, err
		}
		if work, ok := ctx.Value(answerCorpusMetadataWorkKey{}).(*atomic.Int64); ok {
			work.Add(1)
		}
		rows = append(rows, projectQueryMetadata(source.binding, update.New, snapshot))
		if publications != nil {
			publications.add(publication)
		}
	}
	return rows, nil
}

// ImportFetchedRef adopts the supplied canonical entity tip into managed authority
// without moving it, publishing its query metadata and retained source projection.
func (s *Store) ImportFetchedRef(ctx context.Context, ref string, expectedTip model.SHA) error {
	if _, err := refs.Parse(ref); err != nil {
		return err
	}
	if expectedTip == "" {
		return errors.New("import fetched ref: empty expected tip")
	}
	updates := []gitcmd.RefUpdate{{Ref: ref, Old: expectedTip, New: expectedTip}}
	preparation, err := s.prepareManaged(ctx, updates, nil)
	if err != nil {
		return err
	}
	err = s.managedWrite(ctx, func(db *bolt.DB) (*managedIntent, error) {
		intent, err := s.buildManagedIntent(ctx, db, updates, preparation)
		if err != nil {
			return nil, err
		}
		intent.Imported = ref
		return intent, nil
	})
	if err == nil || Published(err) {
		s.cacheFolds(ctx, preparation.Folds)
	}
	return err
}

// PublishFetchedRefs publishes refs a sync fetched as PublishRefs does, and
// once the transaction stands caches every snapshot its preparation folded, so
// the next fetch of those refs extends them instead of refolding their history.
func (s *Store) PublishFetchedRefs(ctx context.Context, updates []gitcmd.RefUpdate) error {
	preparation, err := s.prepareManaged(ctx, updates, nil)
	if err != nil {
		return err
	}
	err = s.managedWrite(ctx, func(db *bolt.DB) (*managedIntent, error) { return s.buildManagedIntent(ctx, db, updates, preparation) })
	if err == nil || Published(err) {
		s.cacheFolds(ctx, preparation.Folds)
	}
	return err
}

func (s *Store) cacheFolds(ctx context.Context, folds []*cachePublication) {
	batch := newFoldPublicationBatch(ctx, s.cache)
	for _, publication := range folds {
		batch.add(publication)
	}
	batch.flush()
}

func (s *Store) publishManaged(ctx context.Context, updates []gitcmd.RefUpdate, snapshots map[model.SHA]model.Snapshot) error {
	if len(updates) == 0 {
		return errors.New("update refs: no updates")
	}
	preparation, err := s.prepareManaged(ctx, updates, snapshots)
	if err != nil {
		return err
	}
	return s.managedWrite(ctx, func(db *bolt.DB) (*managedIntent, error) { return s.buildManagedIntent(ctx, db, updates, preparation) })
}

func (s *Store) buildManagedIntent(ctx context.Context, db *bolt.DB, updates []gitcmd.RefUpdate, preparation *managedPreparation) (*managedIntent, error) {
	var publication *sourcePublication
	var derived []gitcmd.RefUpdate
	err := db.View(func(tx *bolt.Tx) error {
		if err := s.validateManagedQueryGenerations(tx, updates); err != nil {
			return err
		}
		var err error
		publication, derived, err = s.buildSource(ctx, tx, updates, preparation.Entities)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &managedIntent{Updates: slices.Concat(updates, derived), Rows: preparation.Rows, Source: publication}, nil
}

type managedDrift struct {
	ref string
	tip model.SHA
}

func (d *managedDrift) Error() string {
	return fmt.Sprintf("git ref extended outside managed publication: %s", d.ref)
}

func (s *Store) managedWrite(ctx context.Context, build func(*bolt.DB) (*managedIntent, error)) error {
	err := s.publishManagedIntent(ctx, build)
	var drift *managedDrift
	if !errors.As(err, &drift) {
		return err
	}
	if err := s.ImportFetchedRef(ctx, drift.ref, drift.tip); err != nil {
		return fmt.Errorf("adopt %s: %w", drift.ref, err)
	}
	err = s.publishManagedIntent(ctx, build)
	if errors.As(err, &drift) {
		return fmt.Errorf("%w: %w", gitcmd.ErrCASMismatch, drift)
	}
	return err
}

// AlignRefs brings every entity ref back in line with its managed authority
// tip: it imports each ref a git command or an older binary extended outside
// cc-notes, and re-points each ref one deleted or rewound at its authority tip,
// returning how many it adopted and restored and joining the failures of those
// it could not. A ref that diverged from its authority tip stays as it is.
func (s *Store) AlignRefs(ctx context.Context) (adopted, restored int, err error) {
	tips, err := s.Repo.ListPrefix(ctx, refs.Namespace)
	if err != nil {
		return 0, 0, err
	}
	authoritative, err := s.ManagedTips(ctx, refs.Namespace)
	if err != nil {
		return 0, 0, err
	}
	var failures []error
	for ref, tip := range tips {
		if _, err := refs.Parse(ref); err != nil || authoritative[ref] == tip {
			continue
		}
		extended, err := s.extendsAuthority(ctx, authoritative[ref], tip)
		if err != nil {
			failures = append(failures, fmt.Errorf("adopt %s: %w", ref, err))
			continue
		}
		if !extended {
			continue
		}
		switch err := s.ImportFetchedRef(ctx, ref, tip); {
		case err == nil:
			adopted++
		case !errors.Is(err, gitcmd.ErrCASMismatch) && !errors.Is(err, ErrDiverged):
			failures = append(failures, fmt.Errorf("adopt %s: %w", ref, err))
		}
	}
	var moved []string
	for ref, tip := range authoritative {
		if tips[ref] != tip {
			moved = append(moved, ref)
		}
	}
	if len(moved) == 0 {
		return adopted, 0, errors.Join(failures...)
	}
	err = s.underAuthority(ctx, func(lock *os.File, db *bolt.DB) error {
		var err error
		restored, err = s.restoreTrailing(ctx, lock, db, moved)
		return err
	})
	return adopted, restored, errors.Join(append(failures, err)...)
}

func (s *Store) extendsAuthority(ctx context.Context, authoritative, tip model.SHA) (bool, error) {
	if authoritative == "" {
		return true, nil
	}
	return s.Repo.IsAncestor(ctx, authoritative, tip)
}

func (s *Store) trailsAuthority(ctx context.Context, authoritative, tip model.SHA) (bool, error) {
	if authoritative == "" || authoritative == tip {
		return false, nil
	}
	if tip == "" {
		return true, nil
	}
	return s.Repo.IsAncestor(ctx, tip, authoritative)
}

// restoreTrailing re-points each of names a git command deleted, or rewound to
// an ancestor of its authority tip, back at that tip under lock, so the
// records refs again hold every entry the authority published; it returns how
// many it restored.
func (s *Store) restoreTrailing(ctx context.Context, lock *os.File, db *bolt.DB, names []string) (int, error) {
	authoritative := make(map[string]model.SHA, len(names))
	if err := db.View(func(tx *bolt.Tx) error {
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		tips := root.Bucket([]byte("refs"))
		for _, name := range names {
			authoritative[name] = model.SHA(tips.Get([]byte(name)))
		}
		return nil
	}); err != nil {
		return 0, err
	}
	var restores []gitcmd.RefUpdate
	for _, name := range names {
		if _, err := refs.Parse(name); err != nil {
			continue
		}
		tip, err := s.Repo.Tip(ctx, name)
		if err != nil && !errors.Is(err, gitobj.ErrRefNotFound) {
			return 0, err
		}
		trailing, err := s.trailsAuthority(ctx, authoritative[name], tip)
		if err != nil {
			return 0, err
		}
		if trailing {
			restores = append(restores, gitcmd.RefUpdate{Ref: name, New: authoritative[name], Old: tip})
		}
	}
	if len(restores) == 0 {
		return 0, nil
	}
	if err := s.RecordsGit.Holding(lock).UpdateRefs(ctx, restores); err != nil {
		return 0, fmt.Errorf("restore refs moved outside cc-notes: %w", err)
	}
	return len(restores), nil
}

// underAuthority runs do holding the exclusive authority lock over the open
// authority, once any finished reindex is swapped in and any pending
// publication is settled.
func (s *Store) underAuthority(ctx context.Context, do func(*os.File, *bolt.DB) error) (err error) {
	if err := s.CheckRecords(); err != nil {
		return err
	}
	if err := s.awaitAuthority(ctx); err != nil {
		return err
	}
	lock, err := managed.Lock(ctx, s.recordsCommonDir, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	defer func() { err = errors.Join(err, s.Repo.ReleaseHeldPacks()) }()
	if ready, err := s.reindexReady(); err != nil {
		return err
	} else if ready {
		if err := s.finishReindex(ctx, lock); err != nil {
			return err
		}
	}
	db, err := s.openManaged(false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	if err := s.recoverManaged(ctx, db); err != nil {
		return err
	}
	if err := s.adoptSourceHead(ctx, db); err != nil {
		return err
	}
	if err := s.CheckRecords(); err != nil {
		return err
	}
	return do(lock, db)
}

func (s *Store) publishManagedIntent(ctx context.Context, build func(*bolt.DB) (*managedIntent, error)) (err error) {
	var published bool
	var publishedUpdates []gitcmd.RefUpdate
	defer func() {
		if published && err != nil {
			err = publicationFailure(publishedUpdates, err)
		}
	}()
	return s.underAuthority(ctx, func(lock *os.File, db *bolt.DB) error {
		intent, err := build(db)
		if err != nil || intent == nil {
			return err
		}
		if len(intent.Updates) == 0 {
			return errors.New("managed publication has no exact ref updates")
		}
		names := make([]string, len(intent.Updates))
		for i, update := range intent.Updates {
			names[i] = update.Ref
		}
		if _, err := s.restoreTrailing(ctx, lock, db, names); err != nil {
			return err
		}
		intent.Before = make([]model.SHA, len(intent.Updates))
		seen := make(map[string]struct{}, len(intent.Updates))
		for i, update := range intent.Updates {
			if _, found := seen[update.Ref]; found {
				return fmt.Errorf("duplicate managed ref %s", update.Ref)
			}
			seen[update.Ref] = struct{}{}
			tip, err := s.Repo.Tip(ctx, update.Ref)
			if err != nil && !errors.Is(err, gitobj.ErrRefNotFound) {
				return err
			}
			if _, parseErr := refs.Parse(update.Ref); parseErr == nil {
				if intent.Imported == update.Ref {
					if update.Old != update.New || update.New != tip {
						return fmt.Errorf("import fetched ref %s: %w", update.Ref, gitcmd.ErrCASMismatch)
					}
				}
				var authoritative model.SHA
				if err := db.View(func(tx *bolt.Tx) error {
					root, err := managedRoot(tx)
					if err != nil {
						return err
					}
					authoritative = model.SHA(root.Bucket([]byte("refs")).Get([]byte(update.Ref)))
					return nil
				}); err != nil {
					return err
				}
				if authoritative != tip {
					extended := false
					if tip != "" {
						if extended, err = s.extendsAuthority(ctx, authoritative, tip); err != nil {
							return err
						}
					}
					if !extended {
						return fmt.Errorf("%s moved outside cc-notes to %s, off its managed tip %s; `git update-ref %s %s` restores cc-notes' copy, `cc-notes reindex` keeps git's: %w", update.Ref, tip, authoritative, update.Ref, authoritative, ErrDiverged)
					}
					if intent.Imported != update.Ref {
						return &managedDrift{ref: update.Ref, tip: tip}
					}
				}
			}
			intent.Before[i] = tip
		}
		data, err := json.Marshal(intent)
		if err != nil {
			return err
		}
		if err := db.Update(func(tx *bolt.Tx) error {
			root, err := managedRoot(tx)
			if err != nil {
				return err
			}
			return root.Put(managedPendingKey, data)
		}); err != nil {
			return err
		}
		publishedUpdates = intent.Updates
		publishErr := s.RecordsGit.Holding(lock).UpdateRefs(ctx, intent.Updates)
		published = publishErr == nil
		recoverErr := s.recoverManaged(context.WithoutCancel(ctx), db)
		if published {
			return errors.Join(recoverErr, s.CheckRecords())
		}
		return errors.Join(publishErr, recoverErr)
	})
}

func (s *Store) recoverManaged(ctx context.Context, db *bolt.DB) error {
	var data []byte
	if err := db.View(func(tx *bolt.Tx) error {
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		data = bytes.Clone(root.Get(managedPendingKey))
		return nil
	}); err != nil {
		return err
	}
	if data == nil {
		return nil
	}
	var intent managedIntent
	if err := json.Unmarshal(data, &intent); err != nil {
		return errors.Join(managed.ErrReindex, err)
	}
	if len(intent.Updates) == 0 || len(intent.Before) != len(intent.Updates) {
		return managed.ErrReindex
	}
	applied, unchanged := true, true
	for i, update := range intent.Updates {
		tip, err := s.Repo.Tip(ctx, update.Ref)
		if err != nil && !errors.Is(err, gitobj.ErrRefNotFound) {
			return err
		}
		applied = applied && tip == update.New
		unchanged = unchanged && tip == intent.Before[i]
	}
	if !applied && !unchanged {
		return fmt.Errorf("pending refs differ from managed transaction: %w", managed.ErrReindex)
	}
	return db.Update(func(tx *bolt.Tx) error {
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		if applied {
			if err := s.applyManaged(ctx, tx, intent); err != nil {
				return err
			}
		}
		return root.Delete(managedPendingKey)
	})
}

func (s *Store) applyManaged(ctx context.Context, tx *bolt.Tx, intent managedIntent) error {
	root, err := managedRoot(tx)
	if err != nil {
		return err
	}
	tips := root.Bucket([]byte("refs"))
	key := querySourceKey(s.queryMetadataBindingName(model.KindAnswer))
	cursor := model.SHA(root.Get(key))
	current := model.SHA(root.Get(sourceHeadKey))
	prefix := []byte(refs.Root(model.KindAnswer))
	first, _ := tips.Cursor().Seek(prefix)
	initialize := cursor == "" && !bytes.HasPrefix(first, prefix)
	batch := make(queryGenerationBatch)
	for _, update := range intent.Updates {
		parsed, err := refs.Parse(update.Ref)
		if errors.Is(err, refs.ErrNotCCNotes) {
			continue
		}
		if err != nil {
			return err
		}
		if update.New == "" {
			if err := tips.Delete([]byte(update.Ref)); err != nil {
				return err
			}
			binding := s.queryMetadataBindingName(parsed.Kind)
			if err := batch.deleteMetadata(tx, binding, parsed.ID); err != nil {
				return err
			}
		} else if err := tips.Put([]byte(update.Ref), []byte(update.New)); err != nil {
			return err
		}
	}
	for _, row := range intent.Rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := batch.replaceMetadata(tx, row); err != nil {
			return err
		}
	}
	if err := batch.flush(ctx, tx); err != nil {
		return err
	}
	if err := applySource(tx, intent.Source); err != nil {
		return err
	}
	if initialize || cursor != "" && cursor == current {
		if err := root.Put(key, root.Get(sourceHeadKey)); err != nil {
			return err
		}
	}
	return root.SetSequence(root.Sequence() + 1)
}

func (s *Store) publishSnapshot(ctx context.Context, ref string, sha, old model.SHA, snapshot model.Snapshot) error {
	return s.publishRefs(ctx, []gitcmd.RefUpdate{{Ref: ref, New: sha, Old: old}}, map[model.SHA]model.Snapshot{sha: snapshot})
}
