package store

import (
	"context"
	"encoding/binary"
	"errors"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func TestQueryReferenceCountsCancelWithoutDirtyingPages(t *testing.T) {
	db := newQueryMetadataDB(t)
	if err := db.Update(func(tx *bolt.Tx) error {
		bucket, err := tx.CreateBucket([]byte("counts"))
		if err != nil {
			return err
		}
		return bucket.Put([]byte("shared"), binary.BigEndian.AppendUint64(nil, 3))
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
		value, err := counts.get("shared")
		if err != nil {
			return err
		}
		counts.set("shared", value+1)
		counts.set("shared", value)
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
		bucket, err := tx.CreateBucket([]byte("counts"))
		if err != nil {
			return err
		}
		counts := newQueryReferenceCounts(bucket)
		if _, err := counts.get("shared"); err != nil {
			return err
		}
		counts.set("shared", 3)
		if err := counts.flush(t.Context()); err != nil {
			return err
		}
		counts.set("shared", 0)
		return counts.flush(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	err := db.Update(func(tx *bolt.Tx) error {
		counts := newQueryReferenceCounts(tx.Bucket([]byte("counts")))
		value, err := counts.get("shared")
		if err != nil {
			return err
		}
		if value != 0 {
			t.Fatalf("released count = %d", value)
		}
		counts.set("shared", 2)
		cancel()
		return counts.flush(ctx)
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("flush = %v", err)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		if value := tx.Bucket([]byte("counts")).Get([]byte("shared")); value != nil {
			t.Fatalf("canceled count committed: %x", value)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
