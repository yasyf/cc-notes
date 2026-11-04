package store

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/model"
)

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

func retainQueryObject(ctx context.Context, objects, counts *bolt.Bucket, hash model.SHA) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := queryObjectChildren(ctx, objects, hash); err != nil {
		return err
	}
	count, err := queryObjectRefCount(counts, hash)
	if err != nil {
		return err
	}
	if count == math.MaxUint64 {
		return fmt.Errorf("immutable query object reference count overflow %s: %w", hash, managed.ErrReindex)
	}
	return counts.Put([]byte(hash), binary.BigEndian.AppendUint64(nil, count+1))
}

func releaseQueryObject(ctx context.Context, objects, counts *bolt.Bucket, hash model.SHA) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	count, err := queryObjectRefCount(counts, hash)
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
		return counts.Put([]byte(hash), binary.BigEndian.AppendUint64(nil, count-1))
	}
	return deleteQueryObject(ctx, objects, counts, hash)
}

func deleteQueryObject(ctx context.Context, objects, counts *bolt.Bucket, hash model.SHA) error {
	children, err := queryObjectChildren(ctx, objects, hash)
	if err != nil {
		return err
	}
	if err := errors.Join(objects.Delete([]byte(hash)), counts.Delete([]byte(hash))); err != nil {
		return err
	}
	for _, child := range children {
		if err := releaseQueryObject(ctx, objects, counts, child); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func pruneQueryObjects(ctx context.Context, objects, counts *bolt.Bucket, created []model.SHA) error {
	for _, hash := range created {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := queryObjectRefCount(counts, hash)
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
