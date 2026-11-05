package store

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/model"
)

var (
	queryDeadBucket = []byte("dead")
	queryDeadEpoch  = []byte("dead-epoch")
)

type queryObjectWrites struct {
	objects  map[model.SHA][]byte
	children map[model.SHA][]model.SHA
}

func newQueryObjectWrites() *queryObjectWrites {
	return &queryObjectWrites{objects: make(map[model.SHA][]byte), children: make(map[model.SHA][]model.SHA)}
}

// flush stores what the roots reach and drops the nodes a bulk apply built and
// replaced within this write. It returns the older nodes the new graphs still
// use, which is where a walk of the previous graph stops.
func (w *queryObjectWrites) flush(ctx context.Context, objects queryObjectStore, roots []model.SHA) (map[model.SHA]struct{}, error) {
	reachable := make([]model.SHA, 0, len(w.objects))
	seen := make(map[model.SHA]struct{}, len(w.objects))
	kept := make(map[model.SHA]struct{})
	stack := slices.Clone(roots)
	for len(stack) > 0 {
		handle := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, staged := w.objects[handle]; !staged {
			kept[handle] = struct{}{}
			continue
		}
		if _, visited := seen[handle]; visited {
			continue
		}
		seen[handle] = struct{}{}
		reachable = append(reachable, handle)
		stack = append(stack, w.children[handle]...)
	}
	slices.Sort(reachable)
	for _, handle := range reachable {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := objects.put(handle, w.objects[handle]); err != nil {
			return nil, err
		}
	}
	return kept, ctx.Err()
}

func queryObjectChildren(ctx context.Context, objects queryObjectStore, handle model.SHA) ([]model.SHA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := objects.get(handle)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("missing immutable query object %s: %w", handle, managed.ErrReindex)
	}
	kind, err := queryObjectKind(data[0])
	if err != nil || kind == plumbing.BlobObject {
		return nil, err
	}
	value, err := (queryGraphObjects{ctx: ctx, store: objects}).read(handle, kind)
	if err != nil {
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

// retireReplacedQueryObjects frees the nodes of the previous graph that the new
// one no longer uses. IDs only grow, so a node at or past the active generation's
// NextID was born after it and no reader can reach it. Older nodes stay on the
// dead list until the active generation that can reach them has retired.
func retireReplacedQueryObjects(ctx context.Context, objects queryObjectStore, binding *bolt.Bucket, previous model.SHA, kept map[model.SHA]struct{}) error {
	active, err := readQueryGeneration(binding, queryActiveRoot)
	if err != nil {
		return err
	}
	var dead []byte
	stack := []model.SHA{previous}
	for len(stack) > 0 {
		handle := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, stays := kept[handle]; stays {
			continue
		}
		children, err := queryObjectChildren(ctx, objects, handle)
		if err != nil {
			return err
		}
		stack = append(stack, children...)
		key, err := queryObjectKey(handle)
		if err != nil {
			return err
		}
		if active.Root != "" && queryObjectKeyID(key) < active.NextID {
			dead = append(dead, key...)
		} else if err := objects.objects.Delete(key); err != nil {
			return err
		}
	}
	return appendQueryDeadObjects(binding, dead)
}

func queryDeadEpochOf(binding *bolt.Bucket) uint64 {
	data := binding.Get(queryDeadEpoch)
	if data == nil {
		return 0
	}
	return binary.BigEndian.Uint64(data)
}

func appendQueryDeadObjects(binding *bolt.Bucket, dead []byte) error {
	if len(dead) == 0 {
		return nil
	}
	bucket, err := binding.CreateBucketIfNotExists(queryDeadBucket)
	if err != nil {
		return err
	}
	seq, err := bucket.NextSequence()
	if err != nil {
		return err
	}
	key := binary.BigEndian.AppendUint64(binary.BigEndian.AppendUint64(nil, queryDeadEpochOf(binding)), seq)
	return bucket.Put(key, dead)
}

// reclaimRetiredQueryGeneration drops the retired generation and frees the nodes
// only it could reach.
func reclaimRetiredQueryGeneration(ctx context.Context, objects queryObjectStore, binding *bolt.Bucket) error {
	if binding.Get(queryRetiredRoot) == nil {
		return ctx.Err()
	}
	if err := binding.Delete(queryRetiredRoot); err != nil {
		return err
	}
	bucket := binding.Bucket(queryDeadBucket)
	if bucket == nil {
		return ctx.Err()
	}
	epoch := queryDeadEpochOf(binding)
	var expired [][]byte
	cursor := bucket.Cursor()
	for key, ids := cursor.First(); key != nil && binary.BigEndian.Uint64(key) < epoch; key, ids = cursor.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		expired = append(expired, key)
		for start := 0; start < len(ids); start += 8 {
			if err := objects.objects.Delete(ids[start : start+8]); err != nil {
				return err
			}
		}
	}
	for _, key := range expired {
		if err := bucket.Delete(key); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func activateQueryGeneration(ctx context.Context, tx *bolt.Tx, binding *bolt.Bucket, current, active queryGeneration) error {
	if current.Root == active.Root {
		return nil
	}
	if err := reclaimRetiredQueryGeneration(ctx, queryObjectStoreAt(tx.Bucket(queryGenerationsBucket)), binding); err != nil {
		return err
	}
	if active.Root == "" {
		return nil
	}
	data, err := json.Marshal(active)
	if err != nil {
		return err
	}
	if err := binding.Put(queryRetiredRoot, data); err != nil {
		return err
	}
	return binding.Put(queryDeadEpoch, binary.BigEndian.AppendUint64(nil, queryDeadEpochOf(binding)+1))
}
