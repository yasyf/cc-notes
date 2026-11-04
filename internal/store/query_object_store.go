package store

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/model"
)

type queryObjectStore struct {
	objects *bolt.Bucket
	ids     *bolt.Bucket
}

func createQueryObjectStore(root *bolt.Bucket) (queryObjectStore, error) {
	objects, err := root.CreateBucketIfNotExists(queryObjectsBucket)
	if err != nil {
		return queryObjectStore{}, err
	}
	ids, err := root.CreateBucketIfNotExists([]byte(managed.QueryObjectIDs))
	return queryObjectStore{objects: objects, ids: ids}, err
}

func queryObjectStoreAt(root *bolt.Bucket) queryObjectStore {
	return queryObjectStore{objects: root.Bucket(queryObjectsBucket), ids: root.Bucket([]byte(managed.QueryObjectIDs))}
}

func queryObjectKey(hash model.SHA) ([]byte, error) {
	if len(hash) != 40 {
		return nil, fmt.Errorf("invalid immutable query object hash %s: %w", hash, managed.ErrReindex)
	}
	key, err := hex.DecodeString(string(hash))
	return key, err
}

func queryObjectID(ids *bolt.Bucket, hash model.SHA) (uint64, error) {
	key, err := queryObjectKey(hash)
	if err != nil {
		return 0, err
	}
	data := ids.Get(key)
	if data == nil {
		return 0, nil
	}
	if len(data) != 8 || binary.BigEndian.Uint64(data) == 0 {
		return 0, fmt.Errorf("invalid immutable query object ID %s: %w", hash, managed.ErrReindex)
	}
	return binary.BigEndian.Uint64(data), nil
}

func (store queryObjectStore) get(hash model.SHA) ([]byte, error) {
	id, err := queryObjectID(store.ids, hash)
	if err != nil || id == 0 {
		return nil, err
	}
	data := store.objects.Get(binary.BigEndian.AppendUint64(nil, id))
	if data == nil {
		return nil, fmt.Errorf("missing immutable query object data %s: %w", hash, managed.ErrReindex)
	}
	return data, nil
}

func (store queryObjectStore) put(hash model.SHA, data []byte) error {
	hashKey, err := queryObjectKey(hash)
	if err != nil {
		return err
	}
	id, err := store.objects.NextSequence()
	if err != nil {
		return err
	}
	key := binary.BigEndian.AppendUint64(nil, id)
	if err := store.objects.Put(key, data); err != nil {
		return err
	}
	return store.ids.Put(hashKey, key)
}

func (store queryObjectStore) putBatch(ctx context.Context, order []model.SHA, values map[model.SHA][]byte) error {
	ids := make(map[model.SHA]uint64, len(order))
	for _, hash := range order {
		if err := ctx.Err(); err != nil {
			return err
		}
		id, err := store.objects.NextSequence()
		if err != nil {
			return err
		}
		if err := store.objects.Put(binary.BigEndian.AppendUint64(nil, id), values[hash]); err != nil {
			return err
		}
		ids[hash] = id
	}
	keys := slices.Clone(order)
	slices.Sort(keys)
	for _, hash := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		key, err := queryObjectKey(hash)
		if err != nil {
			return err
		}
		if err := store.ids.Put(key, binary.BigEndian.AppendUint64(nil, ids[hash])); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (store queryObjectStore) delete(hash model.SHA) error {
	key, err := queryObjectKey(hash)
	if err != nil {
		return err
	}
	id := store.ids.Get(key)
	if len(id) != 8 {
		return fmt.Errorf("missing immutable query object ID %s: %w", hash, managed.ErrReindex)
	}
	return errors.Join(store.objects.Delete(id), store.ids.Delete(key))
}
