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
	generations := tx.Bucket([]byte(managed.QueryGenerations))
	if root == nil || root.Bucket([]byte("refs")) == nil || generations == nil || generations.Bucket([]byte(managed.QueryObjects)) == nil || generations.Bucket([]byte(managed.QueryObjectRefs)) == nil || generations.Bucket([]byte(managed.QueryObjectIDs)) == nil {
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

func (s *Store) managedView(ctx context.Context, read func(*bolt.Tx) error) (err error) {
	if err := s.CheckRecords(); err != nil {
		return err
	}
	lock, err := managed.Lock(ctx, s.recordsCommonDir, false)
	if err != nil {
		return err
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
			snapshot, publication, err = s.loadAt(ctx, update.New)
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
	return s.managedWrite(ctx, func(db *bolt.DB) (*managedIntent, error) {
		intent, err := s.buildManagedIntent(ctx, db, updates, preparation)
		if err != nil {
			return nil, err
		}
		intent.Imported = ref
		return intent, nil
	})
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

// AdoptExtendedRefs imports every entity ref the authority lacks or trails,
// returning how many it adopted and joining the failures of those it could not.
// A ref that does not descend from its authority tip stays out: only an explicit
// reindex may discard managed history.
func (s *Store) AdoptExtendedRefs(ctx context.Context) (int, error) {
	tips, err := s.Repo.ListPrefix(ctx, refs.Namespace)
	if err != nil {
		return 0, err
	}
	authoritative, err := s.ManagedTips(ctx, refs.Namespace)
	if err != nil {
		return 0, err
	}
	adopted := 0
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
		case !errors.Is(err, gitcmd.ErrCASMismatch) && !errors.Is(err, managed.ErrReindex):
			failures = append(failures, fmt.Errorf("adopt %s: %w", ref, err))
		}
	}
	return adopted, errors.Join(failures...)
}

func (s *Store) extendsAuthority(ctx context.Context, authoritative, tip model.SHA) (bool, error) {
	if authoritative == "" {
		return true, nil
	}
	return s.Repo.IsAncestor(ctx, authoritative, tip)
}

func (s *Store) publishManagedIntent(ctx context.Context, build func(*bolt.DB) (*managedIntent, error)) (err error) {
	var published bool
	var publishedUpdates []gitcmd.RefUpdate
	defer func() {
		if published && err != nil {
			err = publicationFailure(publishedUpdates, err)
		}
	}()

	if err := s.CheckRecords(); err != nil {
		return err
	}
	lock, err := managed.Lock(ctx, s.recordsCommonDir, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
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
	if err := s.CheckRecords(); err != nil {
		return err
	}
	intent, err := build(db)
	if err != nil || intent == nil {
		return err
	}
	if len(intent.Updates) == 0 {
		return errors.New("managed publication has no exact ref updates")
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
					return fmt.Errorf("git ref changed outside managed publication: %s: %w", update.Ref, managed.ErrReindex)
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
			binding := s.listCacheName(parsed.Kind, fmt.Appendf(nil, "metadata %d", queryMetadataVersion), foldCacheGeneration, "")
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
	return root.SetSequence(root.Sequence() + 1)
}

func (s *Store) publishSnapshot(ctx context.Context, ref string, sha, old model.SHA, snapshot model.Snapshot) error {
	return s.publishRefs(ctx, []gitcmd.RefUpdate{{Ref: ref, New: sha, Old: old}}, map[model.SHA]model.Snapshot{sha: snapshot})
}
