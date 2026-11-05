package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
)

// importLegacyAuthority copies the authority managed.LegacyPath holds into
// managed.Path without its hash-addressed query graph, deriving each binding's
// first generation from the query rows it carries. It holds the authority lock
// so no writer of either file is mid-publication while it copies.
// TODO: remove with managed.LegacyPath.
func (s *Store) importLegacyAuthority(ctx context.Context) (err error) {
	lock, err := managed.Lock(ctx, s.recordsCommonDir, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	legacy, err := bolt.Open(managed.LegacyPath(s.recordsCommonDir), 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, legacy.Close()) }()
	building := s.reindexBuildingPath()
	if err := os.Remove(building); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	db, err := bolt.Open(building, 0o600, &bolt.Options{PageSize: managed.PageSize})
	if err != nil {
		return err
	}
	err = legacy.View(func(source *bolt.Tx) error {
		return db.Update(func(tx *bolt.Tx) error {
			if err := source.ForEach(func(name []byte, bucket *bolt.Bucket) error {
				if string(name) == managed.LegacyQueryGenerations {
					return nil
				}
				target, err := tx.CreateBucket(name)
				if err != nil {
					return err
				}
				return copyAuthorityBucket(ctx, target, bucket)
			}); err != nil {
				return err
			}
			return importLegacyQueryGenerations(ctx, source, tx)
		})
	})
	if err = errors.Join(err, db.Close()); err != nil {
		return errors.Join(err, os.Remove(building))
	}
	if err := os.Rename(building, managed.Path(s.recordsCommonDir)); err != nil {
		return err
	}
	return syncManagedDirectory(managed.Path(s.recordsCommonDir))
}

func copyAuthorityBucket(ctx context.Context, target, source *bolt.Bucket) error {
	if err := target.SetSequence(source.Sequence()); err != nil {
		return err
	}
	return source.ForEach(func(key, value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if value == nil {
			if nested := source.Bucket(key); nested != nil {
				child, err := target.CreateBucket(key)
				if err != nil {
					return err
				}
				return copyAuthorityBucket(ctx, child, nested)
			}
		}
		return target.Put(key, value)
	})
}

func importLegacyQueryGenerations(ctx context.Context, source, tx *bolt.Tx) error {
	root, err := tx.CreateBucket(queryGenerationsBucket)
	if err != nil {
		return err
	}
	legacy := source.Bucket([]byte(managed.LegacyQueryGenerations))
	metadata := source.Bucket(queryMetadataRootBucket)
	if legacy == nil || metadata == nil {
		return nil
	}
	return legacy.ForEach(func(atom, _ []byte) error {
		binding, rows := legacy.Bucket(atom), metadata.Bucket(atom)
		if binding == nil || binding.Get(queryCurrentRoot) == nil || rows == nil {
			return nil
		}
		current, err := readQueryGeneration(binding, queryCurrentRoot)
		if err != nil {
			return err
		}
		var mutations []sourcegraph.Mutation
		if err := rows.Bucket(queryMetadataRowsBucket).ForEach(func(_, data []byte) error {
			var row queryMetadata
			if err := json.Unmarshal(data, &row); err != nil {
				return err
			}
			mutations = append(mutations, sourcegraph.Mutation{Key: queryRowKey(row.ID), Value: data})
			mutations = append(mutations, queryRowPostings(row)...)
			return nil
		}); err != nil {
			return err
		}
		target, err := root.CreateBucket(atom)
		if err != nil {
			return err
		}
		return advanceQueryGeneration(ctx, target, mutations, current.Rows, 0)
	})
}
