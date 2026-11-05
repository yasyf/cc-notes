package store

import (
	"bytes"
	"cmp"
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
	id     uint64
	before uint64
	after  uint64
}

type queryReferenceCounts struct {
	root   *bolt.Bucket
	bucket *bolt.Bucket
	values map[model.SHA]queryReferenceCount
}

func newQueryReferenceCounts(root *bolt.Bucket) *queryReferenceCounts {
	return &queryReferenceCounts{root: root, bucket: root.Bucket([]byte(managed.QueryObjectRefs)), values: make(map[model.SHA]queryReferenceCount)}
}

func (counts *queryReferenceCounts) get(hash model.SHA) (uint64, error) {
	if value, found := counts.values[hash]; found {
		return value.after, nil
	}
	id, err := queryObjectID(counts.root.Bucket([]byte(managed.QueryObjectIDs)), hash)
	if err != nil {
		return 0, err
	}
	value, err := readQueryObjectCount(counts.bucket, id, hash)
	if err != nil {
		return 0, err
	}
	counts.values[hash] = queryReferenceCount{id: id, before: value, after: value}
	return value, nil
}

func (counts *queryReferenceCounts) set(hash model.SHA, value uint64) {
	entry := counts.values[hash]
	entry.after = value
	counts.values[hash] = entry
}

const (
	queryReferenceBlockEntries = 128
	queryReferenceBlockBytes   = queryReferenceBlockEntries * 8
)

func (counts *queryReferenceCounts) flush(ctx context.Context) error {
	keys := make([]model.SHA, 0, len(counts.values))
	for hash, value := range counts.values {
		if value.before != value.after {
			keys = append(keys, hash)
		}
	}
	slices.SortFunc(keys, func(a, b model.SHA) int { return cmp.Compare(counts.values[a].id, counts.values[b].id) })
	blocks := make(map[uint64][]byte)
	blockOrder := make([]uint64, 0, len(keys))
	for _, hash := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry := counts.values[hash]
		block, offset := (entry.id-1)/queryReferenceBlockEntries, (entry.id-1)%queryReferenceBlockEntries*8
		data := blocks[block]
		if data == nil {
			stored := counts.bucket.Get(binary.BigEndian.AppendUint64(nil, block))
			if stored == nil {
				data = make([]byte, queryReferenceBlockBytes)
			} else if len(stored) != queryReferenceBlockBytes {
				return fmt.Errorf("invalid immutable query reference block %d: %w", block, managed.ErrReindex)
			} else {
				data = bytes.Clone(stored)
			}
			blocks[block] = data
			blockOrder = append(blockOrder, block)
		}
		binary.BigEndian.PutUint64(data[offset:offset+8], entry.after)
	}
	var zero [queryReferenceBlockBytes]byte
	for _, block := range blockOrder {
		if err := ctx.Err(); err != nil {
			return err
		}
		key, data := binary.BigEndian.AppendUint64(nil, block), blocks[block]
		if bytes.Equal(data, zero[:]) {
			if err := counts.bucket.Delete(key); err != nil {
				return err
			}
		} else if err := counts.bucket.Put(key, data); err != nil {
			return err
		}
	}
	for _, hash := range keys {
		entry := counts.values[hash]
		entry.before = entry.after
		counts.values[hash] = entry
	}
	return ctx.Err()
}

type queryObjectWrites struct {
	objects  map[model.SHA][]byte
	refs     map[model.SHA]uint64
	order    []model.SHA
	children map[model.SHA][]model.SHA
}

func (w *queryObjectWrites) breadthFirst(ctx context.Context, roots []model.SHA) ([]model.SHA, error) {
	order := make([]model.SHA, 0, len(w.order))
	seen := make(map[model.SHA]bool, len(w.order))
	add := func(hash model.SHA) {
		if !seen[hash] && w.objects[hash] != nil {
			seen[hash] = true
			order = append(order, hash)
		}
	}
	for _, hash := range roots {
		add(hash)
	}
	for i := 0; i < len(order); i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, child := range w.children[order[i]] {
			add(child)
		}
	}
	for _, hash := range w.order {
		add(hash)
	}
	return order, ctx.Err()
}

func (w *queryObjectWrites) flush(ctx context.Context, objects queryObjectStore, counts *queryReferenceCounts, roots []model.SHA) ([]model.SHA, error) {
	order, err := w.breadthFirst(ctx, roots)
	if err != nil {
		return nil, err
	}
	if err := objects.putBatch(ctx, order, w.objects); err != nil {
		return nil, err
	}
	for hash, refs := range w.refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		data, err := objects.get(hash)
		if err != nil {
			return nil, err
		}
		if data == nil {
			return nil, fmt.Errorf("missing immutable query object %s: %w", hash, managed.ErrReindex)
		}
		count, err := counts.get(hash)
		if err != nil {
			return nil, err
		}
		if math.MaxUint64-count < refs {
			return nil, fmt.Errorf("immutable query object reference count overflow %s: %w", hash, managed.ErrReindex)
		}
		counts.set(hash, count+refs)
	}
	return w.order, nil
}

func queryObjectRefCount(root *bolt.Bucket, hash model.SHA) (uint64, error) {
	id, err := queryObjectID(root.Bucket([]byte(managed.QueryObjectIDs)), hash)
	if err != nil || id == 0 {
		return 0, err
	}
	return readQueryObjectCount(root.Bucket([]byte(managed.QueryObjectRefs)), id, hash)
}

func readQueryObjectCount(bucket *bolt.Bucket, id uint64, hash model.SHA) (uint64, error) {
	if id == 0 {
		return 0, nil
	}
	block, offset := (id-1)/queryReferenceBlockEntries, (id-1)%queryReferenceBlockEntries*8
	data := bucket.Get(binary.BigEndian.AppendUint64(nil, block))
	if data == nil {
		return 0, nil
	}
	if len(data) != queryReferenceBlockBytes {
		return 0, fmt.Errorf("invalid immutable query object reference count %s: %w", hash, managed.ErrReindex)
	}
	return binary.BigEndian.Uint64(data[offset : offset+8]), nil
}

func queryObjectChildren(ctx context.Context, objects queryObjectStore, hash model.SHA) ([]model.SHA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := objects.get(hash)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("missing immutable query object %s: %w", hash, managed.ErrReindex)
	}
	kind, err := queryObjectKind(data[0])
	if err != nil {
		return nil, err
	}
	value, err := (queryGraphObjects{ctx: ctx, store: objects}).read(hash, kind)
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

func retainQueryObject(ctx context.Context, objects queryObjectStore, counts *queryReferenceCounts, hash model.SHA) error {
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

func releaseQueryObject(ctx context.Context, objects queryObjectStore, counts *queryReferenceCounts, hash model.SHA) error {
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
		data, err := objects.get(hash)
		if err != nil {
			return err
		}
		if data == nil {
			return fmt.Errorf("missing immutable query object %s: %w", hash, managed.ErrReindex)
		}
		counts.set(hash, count-1)
		return nil
	}
	return deleteQueryObject(ctx, objects, counts, hash)
}

func deleteQueryObject(ctx context.Context, objects queryObjectStore, counts *queryReferenceCounts, hash model.SHA) error {
	children, err := queryObjectChildren(ctx, objects, hash)
	if err != nil {
		return err
	}
	if err := objects.delete(hash); err != nil {
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

func pruneQueryObjects(ctx context.Context, objects queryObjectStore, counts *queryReferenceCounts, created []model.SHA) error {
	for _, hash := range created {
		if err := ctx.Err(); err != nil {
			return err
		}
		count, err := counts.get(hash)
		if err != nil {
			return err
		}
		data, err := objects.get(hash)
		if err != nil {
			return err
		}
		if data == nil {
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
