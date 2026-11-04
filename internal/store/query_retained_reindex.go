package store

import (
	"bytes"
	"context"
	"errors"
	"os"

	"github.com/go-git/go-git/v5/plumbing"
	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/model"
)

func activateQueryGeneration(ctx context.Context, tx *bolt.Tx, current, active queryGeneration) error {
	if current.Root == active.Root {
		return nil
	}
	root := tx.Bucket(queryGenerationsBucket)
	objects, counts := root.Bucket(queryObjectsBucket), root.Bucket([]byte(managed.QueryObjectRefs))
	if current.Root != "" {
		if err := retainQueryObject(ctx, objects, counts, current.Root); err != nil {
			return err
		}
	}
	if active.Root != "" {
		if err := releaseQueryObject(ctx, objects, counts, active.Root); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func copyQueryObject(ctx context.Context, source, objects, counts *bolt.Bucket, hash model.SHA) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if objects.Get([]byte(hash)) != nil {
		return nil
	}
	data := bytes.Clone(source.Get([]byte(hash)))
	if len(data) == 0 {
		return managed.ErrReindex
	}
	if data[0] == byte(plumbing.TreeObject) {
		original := queryGraphObjects{ctx: ctx, bucket: source}
		entries, err := original.ReadTree(hash)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if err := copyQueryObject(ctx, source, objects, counts, model.SHA(entry.Hash.String())); err != nil {
				return err
			}
		}
		if err := objects.Put([]byte(hash), data); err != nil {
			return err
		}
		for _, entry := range entries {
			if err := retainQueryObject(ctx, objects, counts, model.SHA(entry.Hash.String())); err != nil {
				return err
			}
		}
		return nil
	}
	if data[0] != byte(plumbing.BlobObject) || plumbing.ComputeHash(plumbing.BlobObject, data[1:]).String() != string(hash) {
		return managed.ErrReindex
	}
	return objects.Put([]byte(hash), data)
}

func (s *Store) preserveActiveQueryGenerations(ctx context.Context, destination *bolt.Tx) (err error) {
	if _, err := os.Stat(managed.Path(s.recordsCommonDir)); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	db, err := s.openManaged(true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, db.Close()) }()
	return db.View(func(source *bolt.Tx) error {
		old := source.Bucket(queryGenerationsBucket)
		if old == nil {
			return nil
		}
		next := destination.Bucket(queryGenerationsBucket)
		objects, counts := next.Bucket(queryObjectsBucket), next.Bucket([]byte(managed.QueryObjectRefs))
		return old.ForEach(func(key, value []byte) error {
			if value != nil || string(key) == managed.QueryObjects || string(key) == managed.QueryObjectRefs {
				return nil
			}
			binding := old.Bucket(key)
			data := bytes.Clone(binding.Get(queryActiveRoot))
			if data == nil {
				return nil
			}
			generation, err := readQueryGeneration(binding, queryActiveRoot)
			if err != nil {
				return err
			}
			if generation.Root != "" {
				if err := copyQueryObject(ctx, old.Bucket(queryObjectsBucket), objects, counts, generation.Root); err != nil {
					return err
				}
				if err := retainQueryObject(ctx, objects, counts, generation.Root); err != nil {
					return err
				}
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
			return target.Put(queryActiveRoot, data)
		})
	})
}
