package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	Updates []gitcmd.RefUpdate `json:"updates"`
	Before  []model.SHA        `json:"before"`
	Rows    []queryMetadata    `json:"rows"`
}

func managedRoot(tx *bolt.Tx) (*bolt.Bucket, error) {
	root := tx.Bucket([]byte(managed.Format))
	if root == nil || root.Bucket([]byte("refs")) == nil {
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

func (s *Store) publishManaged(ctx context.Context, updates []gitcmd.RefUpdate, snapshots map[model.SHA]model.Snapshot) (err error) {
	published := false
	defer func() {
		if published && err != nil {
			err = publicationFailure(updates, err)
		}
	}()
	if err := s.CheckRecords(); err != nil {
		return err
	}
	if len(updates) == 0 {
		return errors.New("update refs: no updates")
	}
	rows, err := s.managedRows(ctx, updates, snapshots, nil)
	if err != nil {
		return err
	}
	lock, err := managed.Lock(ctx, s.recordsCommonDir, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	db, err := s.openManaged(false)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	if err := s.recoverManaged(ctx, db); err != nil {
		return err
	}
	intent := managedIntent{Updates: updates, Rows: rows, Before: make([]model.SHA, len(updates))}
	for i, update := range updates {
		tip, err := s.Repo.Tip(ctx, update.Ref)
		if err != nil && !errors.Is(err, gitobj.ErrRefNotFound) {
			return err
		}
		if _, parseErr := refs.Parse(update.Ref); parseErr == nil {
			if err := db.View(func(tx *bolt.Tx) error {
				root, err := managedRoot(tx)
				if err != nil {
					return err
				}
				if model.SHA(root.Bucket([]byte("refs")).Get([]byte(update.Ref))) != tip {
					return fmt.Errorf("git ref changed outside managed publication: %s: %w", update.Ref, managed.ErrReindex)
				}
				return nil
			}); err != nil {
				return err
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
	publishErr := s.RecordsGit.Holding(lock).UpdateRefs(ctx, updates)
	published = publishErr == nil
	recoverErr := s.recoverManaged(context.WithoutCancel(ctx), db)
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
			if err := s.applyManaged(tx, intent); err != nil {
				return err
			}
		}
		return root.Delete(managedPendingKey)
	})
}

func (s *Store) applyManaged(tx *bolt.Tx, intent managedIntent) error {
	root, err := managedRoot(tx)
	if err != nil {
		return err
	}
	tips := root.Bucket([]byte("refs"))
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
			if err := deleteQueryMetadata(tx, binding, parsed.ID); err != nil {
				return err
			}
		} else if err := tips.Put([]byte(update.Ref), []byte(update.New)); err != nil {
			return err
		}
	}
	for _, row := range intent.Rows {
		if err := replaceQueryMetadata(tx, row); err != nil {
			return err
		}
	}
	return root.SetSequence(root.Sequence() + 1)
}

// Reindex explicitly imports current Git entity refs into the managed authority.
func (s *Store) Reindex(ctx context.Context) (err error) {
	if err := s.CheckRecords(); err != nil {
		return err
	}
	lock, err := managed.Lock(ctx, s.recordsCommonDir, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	tips, err := s.Repo.ListPrefix(ctx, refs.Namespace)
	if err != nil {
		return err
	}
	updates := make([]gitcmd.RefUpdate, 0, len(tips))
	for ref, tip := range tips {
		updates = append(updates, gitcmd.RefUpdate{Ref: ref, New: tip})
	}
	publications := newFoldPublicationBatch(ctx, s.cache)
	defer publications.discard()
	rows, err := s.managedRows(ctx, updates, nil, publications)
	if err != nil {
		return err
	}
	publications.flush()
	path := managed.Path(s.recordsCommonDir)
	staging := path + ".reindex"
	if err := os.Remove(staging); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	db, err := bolt.Open(staging, 0o600, nil)
	if err != nil {
		return err
	}
	err = db.Update(func(tx *bolt.Tx) error {
		root, err := tx.CreateBucket([]byte(managed.Format))
		if err != nil {
			return err
		}
		if _, err := root.CreateBucket([]byte("refs")); err != nil {
			return err
		}
		return s.applyManaged(tx, managedIntent{Updates: updates, Rows: rows})
	})
	err = errors.Join(err, db.Close())
	if err != nil {
		return err
	}
	if err := s.CheckRecords(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(staging, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

func (s *Store) publishSnapshot(ctx context.Context, ref string, sha, old model.SHA, snapshot model.Snapshot) error {
	return s.publishRefs(ctx, []gitcmd.RefUpdate{{Ref: ref, New: sha, Old: old}}, map[model.SHA]model.Snapshot{sha: snapshot})
}
