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

var queryMetadataGenerationKey = []byte("generation")

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
	var authorityErr error
	lazy := !q.pinned && q.tips == nil
	if lazy {
		q.generation, err = q.currentGeneration(ctx)
		if err != nil {
			return value, false, err
		}
	}
	refresh := false
	capture := func() error {
		if q.tips != nil {
			return nil
		}
		var err error
		q, err = q.capture(ctx)
		if err != nil {
			authorityErr = err
		}
		return err
	}
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
		generationHit := !q.pinned && q.tips == nil && q.generation != "" && binding != nil && string(binding.Get(queryMetadataGenerationKey)) == q.generation
		if !tx.Writable() {
			if binding == nil {
				refresh = !q.pinned
				return nil
			}
			if !generationHit {
				if err := capture(); err != nil {
					return err
				}
				diff, err := diffQueryMetadata(ctx, tx, q.binding, q.kind, q.tips)
				if err != nil {
					return err
				}
				if len(diff.Changed) != 0 || len(diff.Removed) != 0 {
					refresh = !q.pinned
					return nil
				}
				refresh = lazy && q.generation != "" && string(binding.Get(queryMetadataGenerationKey)) != q.generation
			}
		} else {
			if err := capture(); err != nil {
				return err
			}
			binding, err := ensureQueryMetadataBinding(tx, q.binding)
			if err != nil {
				return err
			}
			if err := binding.Delete(queryMetadataGenerationKey); err != nil {
				return err
			}
			complete := true
			if err := applyQueryMetadataDelta(ctx, tx, q.binding, q.kind, q.tips, func(ctx context.Context, id model.EntityID, tip model.SHA) (queryMetadata, error) {
				row, err := q.load(ctx, id, tip)
				complete = complete && row.SkippedOps == 0
				if err != nil {
					authorityErr = err
				}
				return row, err
			}); err != nil {
				return err
			}
			if q.generation != "" && complete {
				generation, err := q.currentGeneration(ctx)
				if err != nil {
					authorityErr = err
					return err
				}
				if generation == q.generation {
					if err := binding.Put(queryMetadataGenerationKey, []byte(generation)); err != nil {
						return err
					}
				}
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
		if generationHit {
			generation, err := q.currentGeneration(ctx)
			if err != nil {
				authorityErr = err
				return err
			}
			if generation != q.generation {
				q.generation = ""
				refresh = true
				return nil
			}
		}
		hit = true
		return nil
	}
	var cacheErr error
	var cacheOpenErr error
	access := func(readOnly bool) bool {
		if err := ctx.Err(); err != nil {
			cacheOpenErr = err
			return false
		}
		timeout := queryMetadataOpenTimeout
		if deadline, ok := ctx.Deadline(); ok {
			timeout = min(timeout, time.Until(deadline))
			if timeout <= 0 {
				cacheOpenErr = context.DeadlineExceeded
				return false
			}
		}
		db, openErr := bolt.Open(q.indexPath(), 0o600, &bolt.Options{ReadOnly: readOnly, Timeout: timeout})
		if openErr != nil {
			cacheOpenErr = openErr
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
	if hit && refresh {
		validated := value
		defer func() {
			if err == nil && !hit {
				value, hit = validated, true
			}
		}()
	}
	if !q.pinned && ((!available && errors.Is(cacheOpenErr, os.ErrNotExist)) || refresh) {
		if err := os.MkdirAll(filepath.Dir(q.indexPath()), 0o750); err != nil {
			return value, hit, nil
		}
		if !access(false) {
			return value, hit, nil
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
