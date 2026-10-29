package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

const queryMetadataOpenTimeout = 5 * time.Second

func (q queryMetadataSource) indexPath() string {
	return filepath.Join(q.store.recordsCommonDir, "cc-notes-query", q.binding+".db")
}

func readQueryMetadataIndex[T any](ctx context.Context, q queryMetadataSource, read func(*bolt.Tx) (T, error)) (value T, hit bool, err error) {
	if err := ctx.Err(); err != nil {
		return value, false, err
	}
	if err := q.store.CheckRecords(); err != nil {
		return value, false, err
	}
	defer func() {
		if finalErr := q.store.CheckRecords(); finalErr != nil {
			err = finalErr
			hit = false
		}
		if finalErr := ctx.Err(); finalErr != nil {
			err = finalErr
			hit = false
		}
	}()
	timeout := queryMetadataOpenTimeout
	if deadline, ok := ctx.Deadline(); ok {
		timeout = min(timeout, time.Until(deadline))
		if timeout <= 0 {
			return value, false, context.DeadlineExceeded
		}
	}
	var authorityErr error
	refresh := false
	operation := func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := q.store.CheckRecords(); err != nil {
			authorityErr = err
			return err
		}
		binding := queryMetadataBinding(tx, q.binding)
		if binding != nil && !queryMetadataBucketsComplete(binding) {
			return errors.New("incomplete query metadata buckets")
		}
		if !tx.Writable() {
			if binding == nil {
				refresh = !q.pinned
				return nil
			}
			diff, err := diffQueryMetadata(ctx, tx, q.binding, q.kind, q.tips)
			if err != nil {
				return err
			}
			if len(diff.Changed) != 0 || len(diff.Removed) != 0 {
				refresh = !q.pinned
				return nil
			}
		} else {
			if _, err := ensureQueryMetadataBinding(tx, q.binding); err != nil {
				return err
			}
			if err := applyQueryMetadataDelta(ctx, tx, q.binding, q.kind, q.tips, func(ctx context.Context, id model.EntityID, tip model.SHA) (queryMetadata, error) {
				row, err := q.load(ctx, id, tip)
				if err != nil {
					authorityErr = err
				}
				return row, err
			}); err != nil {
				return err
			}
		}
		var err error
		value, err = read(tx)
		if err != nil {
			return err
		}
		if err := q.store.CheckRecords(); err != nil {
			authorityErr = err
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		hit = true
		return nil
	}
	var cacheErr error
	access := func(readOnly bool) bool {
		db, openErr := bolt.Open(q.indexPath(), 0600, &bolt.Options{ReadOnly: readOnly, Timeout: timeout})
		if openErr != nil {
			return false
		}
		if readOnly {
			cacheErr = db.View(operation)
		} else {
			cacheErr = db.Update(operation)
		}
		if closeErr := db.Close(); cacheErr == nil {
			cacheErr = closeErr
		}
		return true
	}
	available := access(true)
	if authorityErr != nil {
		return value, false, authorityErr
	}
	if available && cacheErr != nil {
		return value, false, nil
	}
	if !q.pinned && (!available || refresh) {
		if err := os.MkdirAll(filepath.Dir(q.indexPath()), 0750); err != nil {
			return value, false, nil
		}
		if !access(false) {
			return value, false, nil
		}
	}

	if authorityErr != nil {
		return value, false, authorityErr
	}
	if cacheErr != nil {
		return value, false, nil
	}
	return value, hit, nil
}

func queryMetadataBucketsComplete(binding *bolt.Bucket) bool {
	for _, name := range [][]byte{queryMetadataRowsBucket, queryMetadataLabelsBucket, queryMetadataAnchorsBucket, queryMetadataSupersedersBucket, queryMetadataTipsBucket} {
		if binding.Bucket(name) == nil {
			return false
		}
	}
	return true
}
