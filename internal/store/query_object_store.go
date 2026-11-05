package store

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/model"
)

const queryObjectHandleBytes = 20

// queryObjectStore holds the retained query graph under creation-ordered IDs. A
// node's handle is its ID in the shape of a Git hash, so the graph code addresses
// children by number: a write touches no hash index and no shared counters.
type queryObjectStore struct {
	objects *bolt.Bucket
}

func createQueryObjectStore(root *bolt.Bucket) (queryObjectStore, error) {
	objects, err := root.CreateBucketIfNotExists(queryObjectsBucket)
	return queryObjectStore{objects: objects}, err
}

func queryObjectStoreAt(root *bolt.Bucket) queryObjectStore {
	return queryObjectStore{objects: root.Bucket(queryObjectsBucket)}
}

func queryObjectHandle(id uint64) model.SHA {
	return model.SHA(fmt.Sprintf("%0*x", queryObjectHandleBytes*2, id))
}

func queryObjectKey(handle model.SHA) ([]byte, error) {
	raw, err := hex.DecodeString(string(handle))
	if err != nil || len(raw) != queryObjectHandleBytes {
		return nil, fmt.Errorf("invalid immutable query object handle %s: %w", handle, managed.ErrReindex)
	}
	return raw[queryObjectHandleBytes-8:], nil
}

func queryObjectKeyID(key []byte) uint64 { return binary.BigEndian.Uint64(key) }

func (store queryObjectStore) allocate() (model.SHA, error) {
	id, err := store.objects.NextSequence()
	if err != nil {
		return "", err
	}
	return queryObjectHandle(id), nil
}

func (store queryObjectStore) get(handle model.SHA) ([]byte, error) {
	key, err := queryObjectKey(handle)
	if err != nil {
		return nil, err
	}
	return store.objects.Get(key), nil
}

func (store queryObjectStore) put(handle model.SHA, data []byte) error {
	key, err := queryObjectKey(handle)
	if err != nil {
		return err
	}
	return store.objects.Put(key, data)
}
