package store

import (
	"bytes"
	"errors"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/model"
)

func TestQueryObjectHandlesNeverReuseAnIDAcrossDeleteAndRollback(t *testing.T) {
	db := newQueryMetadataDB(t)
	var first, second, rolled model.SHA
	if err := db.Update(func(tx *bolt.Tx) error {
		root, err := tx.CreateBucket(queryGenerationsBucket)
		if err != nil {
			return err
		}
		store, err := createQueryObjectStore(root)
		if err != nil {
			return err
		}
		if first, err = store.allocate(); err != nil {
			return err
		}
		if err := store.put(first, []byte("first payload")); err != nil {
			return err
		}
		key, err := queryObjectKey(first)
		if err != nil {
			return err
		}
		if err := store.objects.Delete(key); err != nil {
			return err
		}
		if second, err = store.allocate(); err != nil {
			return err
		}
		return store.put(second, []byte("second payload"))
	}); err != nil {
		t.Fatal(err)
	}
	if second <= first {
		t.Fatalf("deleted ID reused: first=%s second=%s", first, second)
	}
	rollback := errors.New("rollback")
	if err := db.Update(func(tx *bolt.Tx) error {
		store := queryObjectStoreAt(tx.Bucket(queryGenerationsBucket))
		var err error
		if rolled, err = store.allocate(); err != nil {
			return err
		}
		if err := store.put(rolled, []byte("uncommitted")); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("transaction = %v", err)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		store := queryObjectStoreAt(tx.Bucket(queryGenerationsBucket))
		for handle, expected := range map[model.SHA][]byte{first: nil, second: []byte("second payload"), rolled: nil} {
			got, err := store.get(handle)
			if err != nil || !bytes.Equal(got, expected) {
				t.Fatalf("get(%s)=%q, %v; want %q", handle, got, err, expected)
			}
		}
		if store.objects.Stats().KeyN != 1 {
			t.Fatal("deleted or rolled-back object leaked")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueryObjectKeyRejectsAMalformedHandle(t *testing.T) {
	for _, handle := range []model.SHA{"", "abc", "zz34567890123456789012345678901234567890"} {
		if _, err := queryObjectKey(handle); !errors.Is(err, managed.ErrReindex) {
			t.Errorf("queryObjectKey(%q) = %v, want ErrReindex", handle, err)
		}
	}
}
