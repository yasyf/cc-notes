package store

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/model"
)

type queryReferenceCount struct {
	before uint64
	after  uint64
}

type queryReferenceCounts struct {
	bucket *bolt.Bucket
	values map[model.SHA]queryReferenceCount
}

func newQueryReferenceCounts(bucket *bolt.Bucket) *queryReferenceCounts {
	return &queryReferenceCounts{bucket: bucket, values: make(map[model.SHA]queryReferenceCount)}
}

func (counts *queryReferenceCounts) get(hash model.SHA) (uint64, error) {
	if value, found := counts.values[hash]; found {
		return value.after, nil
	}
	value, err := queryObjectRefCount(counts.bucket, hash)
	if err != nil {
		return 0, err
	}
	counts.values[hash] = queryReferenceCount{before: value, after: value}
	return value, nil
}

func (counts *queryReferenceCounts) set(hash model.SHA, value uint64) {
	entry := counts.values[hash]
	entry.after = value
	counts.values[hash] = entry
}

func (counts *queryReferenceCounts) flush(ctx context.Context) error {
	keys := make([]model.SHA, 0, len(counts.values))
	for hash, value := range counts.values {
		if value.before != value.after {
			keys = append(keys, hash)
		}
	}
	slices.Sort(keys)
	for _, hash := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		value := counts.values[hash].after
		if value == 0 {
			if err := counts.bucket.Delete([]byte(hash)); err != nil {
				return err
			}
		} else if err := counts.bucket.Put([]byte(hash), binary.BigEndian.AppendUint64(nil, value)); err != nil {
			return err
		}
		counts.values[hash] = queryReferenceCount{before: value, after: value}
	}
	return ctx.Err()
}

func queryObjectRefCount(counts *bolt.Bucket, hash model.SHA) (uint64, error) {
	data := counts.Get([]byte(hash))
	if data == nil {
		return 0, nil
	}
	if len(data) != 8 {
		return 0, fmt.Errorf("invalid immutable query object reference count %s: %w", hash, managed.ErrReindex)
	}
	return binary.BigEndian.Uint64(data), nil
}

func queryObjectChildren(ctx context.Context, objects *bolt.Bucket, hash model.SHA) ([]model.SHA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data := objects.Get([]byte(hash))
	if len(data) == 0 {
		return nil, fmt.Errorf("missing immutable query object %s: %w", hash, managed.ErrReindex)
	}
	kind, err := queryObjectKind(data[0])
	if err != nil {
		return nil, err
	}
	value, err := (queryGraphObjects{ctx: ctx, bucket: objects}).read(hash, kind)
	if err != nil || kind == plumbing.BlobObject {
		return nil, err
	}
	tree := &object.Tree{}
	if err := tree.Decode(value); err != nil {
		return nil, err
	}
	children := make([]model.SHA, len(tree.Entries))
	for i, entry := range tree.Entries {
		children[i] = model.SHA(entry.Hash.String())
	}
	return children, nil
}

func retainQueryObject(ctx context.Context, objects *bolt.Bucket, counts *queryReferenceCounts, hash model.SHA) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := queryObjectChildren(ctx, objects, hash); err != nil {
		return err
	}
	count, err := counts.get(hash)
	if err != nil {
		return err
	}
	if count == math.MaxUint64 {
		return fmt.Errorf("immutable query object reference count overflow %s: %w", hash, managed.ErrReindex)
	}
	counts.set(hash, count+1)
	return nil
}

func releaseQueryObject(ctx context.Context, objects *bolt.Bucket, counts *queryReferenceCounts, hash model.SHA) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	count, err := counts.get(hash)
	if err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("unreferenced immutable query object release %s: %w", hash, managed.ErrReindex)
	}
	if count > 1 {
		if objects.Get([]byte(hash)) == nil {
			return fmt.Errorf("missing immutable query object %s: %w", hash, managed.ErrReindex)
		}
		counts.set(hash, count-1)
		return nil
	}
	return deleteQueryObject(ctx, objects, counts, hash)
}

func deleteQueryObject(ctx context.Context, objects *bolt.Bucket, counts *queryReferenceCounts, hash model.SHA) error {
	children, err := queryObjectChildren(ctx, objects, hash)
	if err != nil {
		return err
	}
	if err := objects.Delete([]byte(hash)); err != nil {
		return err
	}
	counts.set(hash, 0)
	for _, child := range children {
		if err := releaseQueryObject(ctx, objects, counts, child); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func pruneQueryObjects(ctx context.Context, objects *bolt.Bucket, counts *queryReferenceCounts, created []model.SHA) error {
	for _, hash := range created {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := counts.get(hash)
		if err != nil {
			return err
		}
		if objects.Get([]byte(hash)) == nil {
			if count != 0 {
				return fmt.Errorf("missing referenced immutable query object %s: %w", hash, managed.ErrReindex)
			}
			continue
		}
		if count == 0 {
			if err := deleteQueryObject(ctx, objects, counts, hash); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}
