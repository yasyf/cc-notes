package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/internal/sourceview"
	"github.com/yasyf/cc-notes/model"
)

var (
	managedReindexReady  = []byte("replace-ready")
	managedReindexIntent = []byte("replace-intent")
	managedReindexBase   = []byte("replace-base")
)

type reindexSnapshot struct {
	entities map[string]model.SHA
	source   map[string]model.SHA
}

// Reindex explicitly imports current entities and known historical source receipts.
func (s *Store) Reindex(ctx context.Context) error { return s.ReindexWithSourcePolicy(ctx, nil) }

// ReindexWithSourcePolicy authenticates legacy Applied projection bytes before publication.
func (s *Store) ReindexWithSourcePolicy(ctx context.Context, policy sourceindex.ImportPolicy) (err error) {
	if err := s.CheckRecords(); err != nil {
		return err
	}
	rebuild, err := managed.LockRebuild(ctx, s.recordsCommonDir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rebuild.Close()) }()
	return s.rebuildAuthority(ctx, policy)
}

// AuthorityExists reports whether this store's managed authority is present.
func (s *Store) AuthorityExists() bool { return managed.Exists(s.recordsCommonDir) }

// AuthorityImportLog names the file a background import writes its output to.
func (s *Store) AuthorityImportLog() (string, error) {
	path := managed.Path(s.recordsCommonDir) + ".import.log"
	return path, os.MkdirAll(filepath.Dir(path), 0o750)
}

// ReindexIfMissing imports current entities when no authority exists, copying
// the previous format's authority when one is present, first waiting out any
// rebuild already running, so it returns only once the authority exists or the
// import failed.
func (s *Store) ReindexIfMissing(ctx context.Context) (err error) {
	if err := s.CheckRecords(); err != nil {
		return err
	}
	rebuild, err := managed.AwaitRebuild(ctx, s.recordsCommonDir)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, rebuild.Close()) }()
	if s.AuthorityExists() {
		return nil
	}
	if _, err := os.Stat(managed.LegacyPath(s.recordsCommonDir)); err == nil {
		return s.importLegacyAuthority(ctx)
	}
	return s.rebuildAuthority(ctx, nil)
}

func (s *Store) rebuildAuthority(ctx context.Context, policy sourceindex.ImportPolicy) (err error) {
	snapshot, finished, err := s.snapshotReindex(ctx)
	if err != nil || finished {
		return err
	}
	if err := s.buildReindex(ctx, snapshot, policy); err != nil {
		return err
	}
	lock, err := managed.Lock(ctx, s.recordsCommonDir, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	return s.swapReindex(ctx, lock)
}

func (s *Store) snapshotReindex(ctx context.Context) (snapshot reindexSnapshot, finished bool, err error) {
	lock, err := managed.Lock(ctx, s.recordsCommonDir, true)
	if err != nil {
		return snapshot, false, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	if ready, err := s.reindexReady(); err != nil {
		return snapshot, false, err
	} else if ready {
		return snapshot, true, s.finishReindex(ctx, lock)
	}
	snapshot, err = s.readReindexSnapshot(ctx)
	return snapshot, false, err
}

func (s *Store) readReindexSnapshot(ctx context.Context) (reindexSnapshot, error) {
	entities, err := s.Repo.ListPrefix(ctx, refs.Namespace)
	if err != nil {
		return reindexSnapshot{}, err
	}
	source := make(map[string]model.SHA)
	for _, prefix := range []string{sourceindex.OperationPrefix, sourceindex.OperationPinPrefix} {
		listed, err := s.Repo.ListPrefix(ctx, prefix)
		if err != nil {
			return reindexSnapshot{}, err
		}
		maps.Copy(source, listed)
	}
	for _, ref := range []string{sourceindex.Ref, sourceindex.CatalogRef, sourceindex.LiveRef, sourceindex.LegacyRef} {
		tip, err := s.sourceRefTip(ctx, ref)
		if err != nil {
			return reindexSnapshot{}, err
		}
		if tip != "" {
			source[ref] = tip
		}
	}
	return reindexSnapshot{entities: entities, source: source}, nil
}

func (s *Store) swapReindex(ctx context.Context, lock *os.File) error {
	building := s.reindexBuildingPath()
	staging := managed.Path(s.recordsCommonDir) + ".reindex"
	if err := os.Rename(building, staging); err != nil {
		return err
	}
	if err := syncManagedDirectory(staging); err != nil {
		return err
	}
	return s.finishReindex(ctx, lock)
}

func (s *Store) buildReindex(ctx context.Context, snapshot reindexSnapshot, policy sourceindex.ImportPolicy) (err error) {
	tips := snapshot.entities
	updates := make([]gitcmd.RefUpdate, 0, len(tips))
	for ref, tip := range tips {
		updates = append(updates, gitcmd.RefUpdate{Ref: ref, New: tip})
	}
	slices.SortFunc(updates, func(a, b gitcmd.RefUpdate) int { return bytes.Compare([]byte(a.Ref), []byte(b.Ref)) })
	publications := newFoldPublicationBatch(ctx, s.cache)
	defer publications.discard()
	imported, err := s.importSourceMetadata(ctx, publications, snapshot)
	if err != nil {
		return err
	}
	building := s.reindexBuildingPath()
	if err := os.Remove(building); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	db, err := bolt.Open(building, 0o600, &bolt.Options{PageSize: managed.PageSize, NoSync: true})
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, db.Close())
		}
	}()
	if err := db.Update(func(tx *bolt.Tx) error {
		root, err := tx.CreateBucket([]byte(managed.Format))
		if err != nil {
			return err
		}
		if _, err := root.CreateBucket([]byte("refs")); err != nil {
			return err
		}
		if _, err := tx.CreateBucket([]byte(managed.QueryGenerations)); err != nil {
			return err
		}
		return applySource(tx, &sourcePublication{})
	}); err != nil {
		return err
	}
	if err := s.normalizeSourceImport(ctx, db, imported, policy); err != nil {
		return err
	}
	current := imported.revisions[imported.head]
	same := false
	if current.Token != "" {
		same, err = sourceImportsSame(ctx, s, current, tips)
		if err != nil {
			return err
		}
	}
	if !same || !current.ProjectionAvailable || current.RenderPolicy != sourceview.RenderPolicy {
		manifest, err := gitobj.NewSourceManifest(tips)
		if err != nil {
			return err
		}
		next := sourceindex.Revision{Predecessor: current.Token, Height: current.Height + 1, EntityGeneration: current.EntityGeneration, ProjectionAvailable: true}
		if !same && (current.Token != "" || len(tips) > 0) {
			next.EntityGeneration++
		}
		if current.Token != "" {
			if err := db.View(func(tx *bolt.Tx) error { var err error; next.AncestorJumps, err = sourceJumps(tx, current); return err }); err != nil {
				return err
			}
		}
		imported.manifests[""] = manifest
		imported.revisions[""] = next
		imported.legacy[""] = true
		next, err = s.materializeSourceImport(ctx, imported, "", nil)
		if err != nil {
			return err
		}
		err = s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
			var err error
			next.Token, err = writeSourceRevision(objects, next)
			return err
		})
		if err != nil {
			return err
		}
		delete(imported.manifests, "")
		delete(imported.revisions, "")
		delete(imported.legacy, "")
		current = next
	}
	rows := make([]queryMetadata, 0, len(updates))
	missing := make([]gitcmd.RefUpdate, 0)
	for _, update := range updates {
		if row, found := imported.rows[update.Ref]; found {
			rows = append(rows, row)
		} else {
			missing = append(missing, update)
		}
	}
	publications.flush()
	loadedRows, err := s.managedRows(ctx, missing, nil, publications)
	if err != nil {
		return err
	}
	rows = append(rows, loadedRows...)
	publications.flush()
	catalog, err := s.writeSourceCatalog(ctx, imported)
	if err != nil {
		return err
	}
	sourceUpdates := slices.Clone(imported.proofUpdates)
	oldHead := snapshot.source[sourceindex.Ref]
	base, err := json.Marshal(oldHead)
	if err != nil {
		return err
	}
	if oldHead != current.Token {
		sourceUpdates = append(sourceUpdates, gitcmd.RefUpdate{Ref: sourceindex.Ref, Old: oldHead, New: current.Token})
	}
	if imported.live != current.RetentionRoot {
		sourceUpdates = append(sourceUpdates, gitcmd.RefUpdate{Ref: sourceindex.LiveRef, Old: imported.live, New: current.RetentionRoot})
	}
	if imported.catalog != catalog {
		sourceUpdates = append(sourceUpdates, gitcmd.RefUpdate{Ref: sourceindex.CatalogRef, Old: imported.catalog, New: catalog})
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		if err := s.applyManaged(ctx, tx, managedIntent{Updates: updates, Rows: rows, Source: &sourcePublication{Revision: &current}}); err != nil {
			return err
		}
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		if err := root.Put(querySourceKey(s.queryMetadataBindingName(model.KindAnswer)), []byte(current.Token)); err != nil {
			return err
		}
		if err := root.Put(managedReindexBase, base); err != nil {
			return err
		}
		if len(sourceUpdates) > 0 {
			before := make([]model.SHA, len(sourceUpdates))
			for i, update := range sourceUpdates {
				before[i] = update.Old
			}
			data, err := json.Marshal(managedIntent{Updates: sourceUpdates, Before: before})
			if err != nil {
				return err
			}
			if err := root.Put(managedPendingKey, data); err != nil {
				return err
			}
			if err := root.Put(managedReindexIntent, data); err != nil {
				return err
			}
		}
		return root.Put(managedReindexReady, []byte("1"))
	}); err != nil {
		return err
	}
	if err := db.Sync(); err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	closed = true
	return syncManagedDirectory(building)
}

func (s *Store) reindexBuildingPath() string {
	return managed.Path(s.recordsCommonDir) + ".reindex-building"
}

func (s *Store) sourceRefTip(ctx context.Context, ref string) (model.SHA, error) {
	tip, err := s.Repo.Tip(ctx, ref)
	if errors.Is(err, gitobj.ErrRefNotFound) {
		return "", nil
	}
	return tip, err
}

func (s *Store) reindexReady() (ready bool, err error) {
	path := managed.Path(s.recordsCommonDir) + ".reindex"
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		return false, err
	}
	err = db.View(func(tx *bolt.Tx) error {
		root := tx.Bucket([]byte(managed.Format))
		if root != nil {
			ready = bytes.Equal(root.Get(managedReindexReady), []byte("1"))
		}
		return nil
	})
	err = errors.Join(err, db.Close())
	if err != nil {
		return false, fmt.Errorf("prepared source import %s: %w", path, errors.Join(managed.ErrReindex, err))
	}
	if !ready {
		return false, fmt.Errorf("prepared source import has no durable ready marker: %s: %w", path, managed.ErrReindex)
	}
	return true, nil
}

func (s *Store) finishReindex(ctx context.Context, lock *os.File) (err error) {
	var published bool
	var publishedUpdates []gitcmd.RefUpdate
	defer func() {
		if published && err != nil {
			err = publicationFailure(publishedUpdates, err)
		}
	}()
	defer func() { err = errors.Join(err, s.Repo.ReleaseHeldPacks()) }()

	path := managed.Path(s.recordsCommonDir)
	staging := path + ".reindex"
	db, err := bolt.Open(staging, 0o600, nil)
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			err = errors.Join(err, db.Close())
		}
	}()
	if err := s.catchUpReindex(ctx, db); err != nil {
		return err
	}
	var intent managedIntent
	if err := db.View(func(tx *bolt.Tx) error {
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		if !bytes.Equal(root.Get(managedReindexReady), []byte("1")) {
			return managed.ErrReindex
		}
		data := root.Get(managedReindexIntent)
		if data == nil {
			return nil
		}
		return json.Unmarshal(data, &intent)
	}); err != nil {
		return err
	}
	if len(intent.Updates) > 0 {
		if len(intent.Before) != len(intent.Updates) {
			return managed.ErrReindex
		}
		unchanged, applied := true, true
		for i, update := range intent.Updates {
			tip, err := s.sourceRefTip(ctx, update.Ref)
			if err != nil {
				return err
			}
			unchanged = unchanged && tip == intent.Before[i]
			applied = applied && tip == update.New
		}
		if !applied && !unchanged {
			return fmt.Errorf("staged source import refs differ: %w", managed.ErrReindex)
		}
		published = applied
		publishedUpdates = intent.Updates
		if unchanged && !applied {
			if err := s.CheckRecords(); err != nil {
				return err
			}
			if err := s.RecordsGit.Holding(lock).UpdateRefs(ctx, intent.Updates); err != nil {
				return err
			}
			published = true
		}
		if err := s.recoverManaged(context.WithoutCancel(ctx), db); err != nil {
			return err
		}
	}
	if err := s.CheckRecords(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return err
	}
	closed = true
	if err := os.Rename(staging, path); err != nil {
		return err
	}
	return syncManagedDirectory(path)
}

func (s *Store) catchUpReindex(ctx context.Context, db *bolt.DB) error {
	head, err := s.sourceRefTip(ctx, sourceindex.Ref)
	if err != nil {
		return err
	}
	var base, built sourceindex.Revision
	var intent managedIntent
	staged := false
	if err := db.View(func(tx *bolt.Tx) error {
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		data := root.Get(managedReindexBase)
		if staged = data != nil; !staged {
			return nil
		}
		if err := json.Unmarshal(data, &base.Token); err != nil {
			return errors.Join(managed.ErrReindex, err)
		}
		if data := root.Get(managedReindexIntent); data != nil {
			if err := json.Unmarshal(data, &intent); err != nil {
				return err
			}
		}
		if head == base.Token {
			return nil
		}
		if built, err = sourceRevision(tx, ""); err != nil {
			return err
		}
		if base.Token == built.Token {
			base = built
		} else if base.Token != "" {
			base, err = sourceRevision(tx, base.Token)
		}
		return err
	}); err != nil || !staged {
		return err
	}
	var adoption *sourceAdoption
	if head != base.Token {
		if adoption, err = s.prepareSourceAdoption(ctx, db, head, base); err != nil {
			return err
		}
		var kept managedIntent
		for i, update := range intent.Updates {
			if update.Ref != sourceindex.Ref && update.Ref != sourceindex.LiveRef {
				kept.Updates = append(kept.Updates, update)
				kept.Before = append(kept.Before, intent.Before[i])
			}
		}
		intent = kept
	}
	return db.Update(func(tx *bolt.Tx) error {
		if adoption != nil {
			derived, err := s.applySourceAdoption(ctx, tx, adoption, built)
			if err != nil {
				return err
			}
			for _, update := range derived {
				intent.Updates = append(intent.Updates, update)
				intent.Before = append(intent.Before, update.Old)
			}
		}
		if err := s.preserveActiveQueryGenerations(ctx, tx); err != nil {
			return err
		}
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		if err := root.Delete(managedReindexBase); err != nil || head == base.Token {
			return err
		}
		if len(intent.Updates) == 0 {
			return errors.Join(root.Delete(managedPendingKey), root.Delete(managedReindexIntent))
		}
		data, err := json.Marshal(intent)
		if err != nil {
			return err
		}
		return errors.Join(root.Put(managedPendingKey, data), root.Put(managedReindexIntent, data))
	})
}

func syncManagedDirectory(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}
