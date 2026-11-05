package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
	"github.com/yasyf/cc-notes/model"
)

// copyQueryGraph rebuilds a graph in a new authority and returns its root with the
// packed IDs of its nodes. Nodes name their children by ID, so a graph moves
// between authorities by its entries rather than by its bytes.
func copyQueryGraph(ctx context.Context, source, objects queryObjectStore, root model.SHA) (model.SHA, []byte, error) {
	index := sourcegraph.New(queryGraphObjects{ctx: ctx, store: source})
	count, err := index.Count(root)
	if err != nil {
		return "", nil, err
	}
	entries, err := index.Range(root, 0, int(count))
	if err != nil {
		return "", nil, err
	}
	mutations := make([]sourcegraph.Mutation, len(entries))
	for i, entry := range entries {
		mutations[i] = sourcegraph.Mutation{Key: entry.Key, Value: entry.Value}
	}
	writes := newQueryObjectWrites()
	copied, err := sourcegraph.New(queryGraphObjects{ctx: ctx, store: objects, writes: writes}).Apply("", mutations)
	if err != nil {
		return "", nil, err
	}
	if _, err := writes.flush(ctx, objects, []model.SHA{copied}); err != nil {
		return "", nil, err
	}
	var ids []byte
	stack := []model.SHA{copied}
	for len(stack) > 0 {
		handle := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		children, err := queryObjectChildren(ctx, objects, handle)
		if err != nil {
			return "", nil, err
		}
		stack = append(stack, children...)
		key, err := queryObjectKey(handle)
		if err != nil {
			return "", nil, err
		}
		ids = append(ids, key...)
	}
	return copied, ids, nil
}

// preserveActiveQueryGenerations carries each pinned generation across a reindex.
// Its nodes go straight on the dead list, since no later graph shares them, and
// are freed when the generation retires.
func (s *Store) preserveActiveQueryGenerations(ctx context.Context, destination *bolt.Tx) (err error) {
	db, err := s.openManaged(true)
	if errors.Is(err, managed.ErrReindex) {
		return nil
	} else if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	return db.View(func(source *bolt.Tx) error {
		old := source.Bucket(queryGenerationsBucket)
		if old == nil {
			return nil
		}
		next := destination.Bucket(queryGenerationsBucket)
		objects := queryObjectStoreAt(next)
		return old.ForEach(func(key, value []byte) error {
			if value != nil || string(key) == managed.QueryObjects {
				return nil
			}
			binding := old.Bucket(key)
			if binding.Get(queryActiveRoot) == nil {
				return nil
			}
			generation, err := readQueryGeneration(binding, queryActiveRoot)
			if err != nil {
				return err
			}
			var ids []byte
			if generation.Root != "" {
				generation.Root, ids, err = copyQueryGraph(ctx, queryObjectStoreAt(old), objects, generation.Root)
				if err != nil {
					return err
				}
				generation.NextID = objects.objects.Sequence() + 1
			}
			target, err := next.CreateBucketIfNotExists(bytes.Clone(key))
			if err != nil {
				return err
			}
			if target.Get(queryCurrentRoot) == nil {
				empty := []byte(`{"Root":"","Rows":0}`)
				if err := target.Put(queryCurrentRoot, empty); err != nil {
					return err
				}
			}
			data, err := json.Marshal(generation)
			if err != nil {
				return err
			}
			if err := target.Put(queryActiveRoot, data); err != nil {
				return err
			}
			return appendQueryDeadObjects(target, ids)
		})
	})
}
