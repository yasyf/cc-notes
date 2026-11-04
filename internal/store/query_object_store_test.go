package store

import (
	"bytes"
	"errors"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

func TestQueryObjectStorageKeepsIdentityAcrossOrderedAllocationAndRollback(t *testing.T) {
	db := newQueryMetadataDB(t)
	firstHash := model.SHA(plumbing.ComputeHash(plumbing.BlobObject, []byte("first payload")).String())
	secondHash := model.SHA(plumbing.ComputeHash(plumbing.BlobObject, []byte("second payload")).String())
	rolledHash := model.SHA(plumbing.ComputeHash(plumbing.BlobObject, []byte("uncommitted")).String())
	var first uint64
	if err := db.Update(func(tx *bolt.Tx) error {
		root, err := tx.CreateBucket(queryGenerationsBucket)
		if err != nil {
			return err
		}
		store, err := createQueryObjectStore(root)
		if err != nil {
			return err
		}
		if err := store.put(firstHash, []byte("first payload")); err != nil {
			return err
		}
		first, err = queryObjectID(store.ids, firstHash)
		if err != nil {
			return err
		}
		if err := store.delete(firstHash); err != nil {
			return err
		}
		return store.put(secondHash, []byte("second payload"))
	}); err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback")
	if err := db.Update(func(tx *bolt.Tx) error {
		if err := queryObjectStoreAt(tx.Bucket(queryGenerationsBucket)).put(rolledHash, []byte("uncommitted")); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("transaction = %v", err)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		store := queryObjectStoreAt(tx.Bucket(queryGenerationsBucket))
		id, err := queryObjectID(store.ids, secondHash)
		if err != nil {
			return err
		}
		if id <= first {
			t.Fatalf("deleted ID reused: first=%d second=%d", first, id)
		}
		for hash, expected := range map[model.SHA][]byte{firstHash: nil, secondHash: []byte("second payload"), rolledHash: nil} {
			got, err := store.get(hash)
			if err != nil || !bytes.Equal(got, expected) {
				t.Fatalf("get(%s)=%q, %v; want %q", hash, got, err, expected)
			}
		}
		if store.objects.Stats().KeyN != 1 || store.ids.Stats().KeyN != 1 {
			t.Fatal("deleted or rolled-back object leaked")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
