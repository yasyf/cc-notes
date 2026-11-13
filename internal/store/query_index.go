package store

import (
	"bytes"
	"context"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

const queryIndexSubdir = "cc-notes-query"

func (q queryMetadataSource) indexPath() string {
	return managed.Path(q.store.recordsCommonDir)
}

func readQueryMetadataIndex[T any](ctx context.Context, q queryMetadataSource, read func(*bolt.Tx) (T, error)) (value T, hit bool, err error) {
	err = q.store.managedView(ctx, func(tx *bolt.Tx) error {
		if err := validateQueryMetadataBinding(tx, q); err != nil {
			return err
		}
		if q.tips != nil {
			diff, err := diffQueryMetadata(ctx, tx, q.binding, q.kind, q.tips)
			if err != nil {
				return err
			}
			if len(diff.Changed) != 0 || len(diff.Removed) != 0 {
				return nil
			}
		}
		var err error
		value, err = read(tx)
		if err != nil {
			return err
		}
		hit = true
		return nil
	})
	if err != nil {
		var empty T
		return empty, false, err
	}
	return value, hit, nil
}

func validateQueryMetadataBinding(tx *bolt.Tx, q queryMetadataSource) error {
	binding := queryMetadataBinding(tx, q.binding)
	if binding != nil && queryMetadataBucketsComplete(binding, q.kind) {
		return nil
	}
	if binding == nil {
		prefix := []byte(refs.Root(q.kind))
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		key, _ := root.Bucket([]byte("refs")).Cursor().Seek(prefix)
		if !bytes.HasPrefix(key, prefix) {
			return nil
		}
	}
	return fmt.Errorf("%w: incomplete query metadata for %s", managed.ErrReindex, q.kind)
}

func queryMetadataBucketsComplete(binding *bolt.Bucket, kind model.Kind) bool {
	names := [][]byte{queryMetadataRowsBucket, queryMetadataTipsBucket, queryMetadataLabelsBucket, queryMetadataAnchorsBucket, queryMetadataSupersedersBucket, queryMetadataBlockedByBucket, queryMetadataParentsBucket, queryMetadataRunTasksBucket, queryMetadataDuplicatesBucket}
	if kind == model.KindAnswer {
		names = append(names, queryMetadataQuestionTermsBucket, queryMetadataBodyTermsBucket, queryMetadataAnswerRankingBucket)
	}
	for _, name := range names {
		if binding.Bucket(name) == nil {
			return false
		}
	}
	return true
}
