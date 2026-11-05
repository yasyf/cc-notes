package store

import (
	"bytes"
	"context"
	"errors"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
)

// preservedQueryGeneration numbers an active generation a reindex carries over:
// it precedes the first generation the reindex builds, so its versions die there.
const preservedQueryGeneration = 0

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
		return old.ForEach(func(key, _ []byte) error {
			binding := old.Bucket(key)
			if binding.Get(queryActiveRoot) == nil {
				return nil
			}
			active, err := readQueryGeneration(binding, queryActiveRoot)
			if err != nil {
				return err
			}
			target, err := next.CreateBucketIfNotExists(bytes.Clone(key))
			if err != nil {
				return err
			}
			versions, err := target.CreateBucketIfNotExists(queryVersionsBucket)
			if err != nil {
				return err
			}
			deaths, err := target.CreateBucketIfNotExists(queryDeathsBucket)
			if err != nil {
				return err
			}
			retained := &retainedQuery{versions: binding.Bucket(queryVersionsBucket), generation: active}
			if err := retained.each(func(key, value []byte) error {
				if err := ctx.Err(); err != nil {
					return err
				}
				return preserveQueryVersion(versions, deaths, key, value)
			}); err != nil {
				return err
			}
			if target.Get(queryCurrentRoot) == nil {
				if err := putQueryGeneration(target, queryCurrentRoot, queryGeneration{Number: preservedQueryGeneration + 1}); err != nil {
					return err
				}
			}
			return putQueryGeneration(target, queryActiveRoot, queryGeneration{Number: preservedQueryGeneration, Rows: active.Rows})
		})
	})
}

func preserveQueryVersion(versions, deaths *bolt.Bucket, key, value []byte) error {
	list, err := decodeQueryVersions(versions.Get(key))
	if err != nil {
		return err
	}
	list = append([]queryVersion{{born: preservedQueryGeneration, died: preservedQueryGeneration + 1, value: value}}, list...)
	if err := deaths.Put(queryDeathKey(preservedQueryGeneration+1, key), nil); err != nil {
		return err
	}
	return versions.Put(bytes.Clone(key), encodeQueryVersions(list))
}
