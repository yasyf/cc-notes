package store

import (
	"context"
	"errors"
	"fmt"
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
		counts := newQueryReferenceCounts(bucket)
		if _, err := counts.get(queryReferenceTestHash); err != nil {
			return err
		}
		counts.set(queryReferenceTestHash, 3)
		return counts.flush(t.Context())
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
		if tx.Bucket([]byte("counts")).Bucket([]byte(managed.QueryObjectRefs)).Stats().KeyN != 0 {
			t.Fatal("canceled count block committed")
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

func TestQueryReferenceBlocksPreserveBoundaryNeighbors(t *testing.T) {
	db := newQueryMetadataDB(t)
	hashes := make([]model.SHA, queryReferenceBlockEntries+2)
	if err := db.Update(func(tx *bolt.Tx) error {
		root, err := createQueryReferenceTestRoot(tx)
		if err != nil {
			return err
		}
		store, counts := queryObjectStoreAt(root), newQueryReferenceCounts(root)
		for i := range hashes {
			data := []byte(fmt.Sprintf("boundary-%d", i))
			hash := model.SHA(plumbing.ComputeHash(plumbing.BlobObject, data).String())
			hashes[i] = hash
			if err := store.put(hash, data); err != nil {
				return err
			}
			if _, err := counts.get(hash); err != nil {
				return err
			}
			counts.set(hash, uint64(i+1))
		}
		return counts.flush(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		counts := newQueryReferenceCounts(tx.Bucket([]byte("counts")))
		for _, i := range []int{queryReferenceBlockEntries - 2, queryReferenceBlockEntries - 1} {
			if _, err := counts.get(hashes[i]); err != nil {
				return err
			}
			counts.set(hashes[i], 0)
		}
		return counts.flush(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		root := tx.Bucket([]byte("counts"))
		counts := newQueryReferenceCounts(root)
		for i, hash := range hashes {
			want := uint64(i + 1)
			if i == queryReferenceBlockEntries-2 || i == queryReferenceBlockEntries-1 {
				want = 0
			}
			got, err := counts.get(hash)
			if err != nil || got != want {
				t.Fatalf("count %d = %d, %v; want %d", i, got, err, want)
			}
			counts.set(hash, 0)
		}
		if err := counts.flush(t.Context()); err != nil {
			return err
		}
		if n := root.Bucket([]byte(managed.QueryObjectRefs)).Stats().KeyN; n != 0 {
			t.Fatalf("retired reference blocks = %d", n)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueryOrderedStorageKeepsDensePages(t *testing.T) {
	db := newQueryMetadataDB(t)
	hashes := make([]model.SHA, 8192)
	values := make(map[model.SHA][]byte, len(hashes))
	for i := range hashes {
		data := []byte(fmt.Sprintf("%016d", i))
		hash := model.SHA(plumbing.ComputeHash(plumbing.BlobObject, data).String())
		hashes[i], values[hash] = hash, data
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		root, err := createQueryReferenceTestRoot(tx)
		if err != nil {
			return err
		}
		store := queryObjectStoreAt(root)
		if err := store.putBatch(t.Context(), hashes, values); err != nil {
			return err
		}
		counts := newQueryReferenceCounts(root)
		for i, hash := range hashes {
			if _, err := counts.get(hash); err != nil {
				return err
			}
			counts.set(hash, uint64(i%7+1))
		}
		return counts.flush(t.Context())
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		root := tx.Bucket([]byte("counts"))
		store, counts := queryObjectStoreAt(root), newQueryReferenceCounts(root)
		for i, hash := range hashes {
			count, err := counts.get(hash)
			if want := uint64(i%7 + 1); err != nil || count != want {
				t.Fatalf("count %d = %d, %v; want %d", i, count, err, want)
			}
		}
		blocks := (len(hashes) + queryReferenceBlockEntries) / queryReferenceBlockEntries
		for _, item := range []struct {
			name    string
			bucket  *bolt.Bucket
			entries int
			bytes   int
		}{
			{"objects", store.objects, len(hashes) + 1, len(hashes)*(16+8+16) + len("shared") + 8 + 16},
			{"reference blocks", counts.bucket, blocks, blocks * (queryReferenceBlockBytes + 8 + 16)},
		} {
			stats := item.bucket.Stats()
			maxPages := item.bytes/(db.Info().PageSize*3/4) + 2
			if stats.KeyN != item.entries || stats.LeafPageN > maxPages {
				t.Fatalf("%s entries=%d pages=%d; want entries=%d at most %d pages", item.name, stats.KeyN, stats.LeafPageN, item.entries, maxPages)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
