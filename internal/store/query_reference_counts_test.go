package store

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/model"
)

func TestQueryReferenceCountsCancelWithoutDirtyingPages(t *testing.T) {
	db := newQueryMetadataDB(t)
	if err := db.Update(func(tx *bolt.Tx) error {
		bucket, err := createQueryReferenceTestRoot(tx)
		if err != nil {
			return err
		}
		id, err := queryObjectID(bucket.Bucket([]byte(managed.QueryObjectIDs)), queryReferenceTestHash)
		if err != nil {
			return err
		}
		return bucket.Bucket([]byte(managed.QueryObjectRefs)).Put(binary.BigEndian.AppendUint64(nil, id), binary.BigEndian.AppendUint64(nil, 3))
	}); err != nil {
		t.Fatal(err)
	}
	writes := func() int64 {
		stats := db.Stats()
		return stats.TxStats.GetWrite()
	}
	before := writes()
	if err := db.Update(func(*bolt.Tx) error { return nil }); err != nil {
		t.Fatal(err)
	}
	emptyWrites := writes() - before
	before = writes()
	if err := db.Update(func(tx *bolt.Tx) error {
		counts := newQueryReferenceCounts(tx.Bucket([]byte("counts")))
		value, err := counts.get(queryReferenceTestHash)
		if err != nil {
			return err
		}
		counts.set(queryReferenceTestHash, value+1)
		counts.set(queryReferenceTestHash, value)
		return counts.flush(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
	if changed := writes() - before; changed != emptyWrites {
		t.Fatalf("neutral reference changes wrote %d pages; empty transaction wrote %d", changed, emptyWrites)
	}
}

func TestQueryReferenceCountsFlushAndCancellation(t *testing.T) {
	db := newQueryMetadataDB(t)
	if err := db.Update(func(tx *bolt.Tx) error {
		bucket, err := createQueryReferenceTestRoot(tx)
		if err != nil {
			return err
		}
		counts := newQueryReferenceCounts(bucket)
		if _, err := counts.get(queryReferenceTestHash); err != nil {
			return err
		}
		counts.set(queryReferenceTestHash, 3)
		if err := counts.flush(t.Context()); err != nil {
			return err
		}
		counts.set(queryReferenceTestHash, 0)
		return counts.flush(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	err := db.Update(func(tx *bolt.Tx) error {
		counts := newQueryReferenceCounts(tx.Bucket([]byte("counts")))
		value, err := counts.get(queryReferenceTestHash)
		if err != nil {
			return err
		}
		if value != 0 {
			t.Fatalf("released count = %d", value)
		}
		counts.set(queryReferenceTestHash, 2)
		cancel()
		return counts.flush(ctx)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("flush = %v", err)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		root := tx.Bucket([]byte("counts"))
		id, err := queryObjectID(root.Bucket([]byte(managed.QueryObjectIDs)), queryReferenceTestHash)
		if err != nil {
			return err
		}
		if value := root.Bucket([]byte(managed.QueryObjectRefs)).Get(binary.BigEndian.AppendUint64(nil, id)); value != nil {
			t.Fatalf("canceled count committed: %x", value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

var queryReferenceTestHash = model.SHA(plumbing.ComputeHash(plumbing.BlobObject, []byte("shared")).String())

func createQueryReferenceTestRoot(tx *bolt.Tx) (*bolt.Bucket, error) {
	root, err := tx.CreateBucket([]byte("counts"))
	if err != nil {
		return nil, err
	}
	store, err := createQueryObjectStore(root)
	if err != nil {
		return nil, err
	}
	if _, err := root.CreateBucket([]byte(managed.QueryObjectRefs)); err != nil {
		return nil, err
	}
	return root, store.put(queryReferenceTestHash, []byte("shared"))
}
