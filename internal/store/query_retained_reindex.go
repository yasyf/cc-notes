package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/model"
)

// copyQueryObject copies a graph into a new authority under fresh IDs, recording
// each one, and returns the handle of its root.
func copyQueryObject(ctx context.Context, source, objects queryObjectStore, ids *[]byte, handle model.SHA) (model.SHA, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	stored, err := source.get(handle)
	if err != nil {
		return "", err
	}
	if len(stored) == 0 {
		return "", managed.ErrReindex
	}
	kind, err := queryObjectKind(stored[0])
	if err != nil {
		return "", err
	}
	data := bytes.Clone(stored)
	if kind == plumbing.TreeObject {
		entries, err := (queryGraphObjects{ctx: ctx, store: source}).ReadTree(handle)
		if err != nil {
			return "", err
		}
		for i, entry := range entries {
			child, err := copyQueryObject(ctx, source, objects, ids, model.SHA(entry.Hash.String()))
			if err != nil {
				return "", err
			}
			entries[i].Hash = plumbing.NewHash(string(child))
		}
		value := &plumbing.MemoryObject{}
		if err := (&object.Tree{Entries: entries}).Encode(value); err != nil {
			return "", err
		}
		if data, err = queryObjectBytes(value); err != nil {
			return "", err
		}
	}
	copied, err := objects.allocate()
	if err != nil {
		return "", err
	}
	if err := objects.put(copied, data); err != nil {
		return "", err
	}
	key, err := queryObjectKey(copied)
	if err != nil {
		return "", err
	}
	*ids = append(*ids, key...)
	return copied, nil
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
				generation.Root, err = copyQueryObject(ctx, queryObjectStoreAt(old), objects, &ids, generation.Root)
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
