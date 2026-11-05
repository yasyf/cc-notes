package store

import (
	"encoding/hex"
	"fmt"
	"maps"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

func TestQueryObjectBulkFillReducesCompleteMutationAllocation(t *testing.T) {
	type result struct {
		allocated   int64
		leafPages   int
		branchPages int
		objects     map[model.SHA]string
	}
	measure := func(dense bool) result {
		db := newQueryMetadataDB(t)
		seed := make([]model.SHA, 8192)
		values := make(map[model.SHA][]byte, len(seed))
		for i := range seed {
			data := fmt.Appendf(nil, "bulk-index-object-%05d", i)
			hash := model.SHA(plumbing.ComputeHash(plumbing.BlobObject, data).String())
			seed[i] = hash
			values[hash] = append([]byte{byte(plumbing.BlobObject)}, data...)
		}
		if err := db.Update(func(tx *bolt.Tx) error {
			root, err := tx.CreateBucket(queryGenerationsBucket)
			if err != nil {
				return err
			}
			store, err := createQueryObjectStore(root)
			if err != nil {
				return err
			}
			if err := store.putBatch(t.Context(), seed, values); err != nil {
				return err
			}
			if dense {
				if store.ids.FillPercent != 0.9 {
					t.Fatalf("initial bulk fill = %f", store.ids.FillPercent)
				}
			} else {
				store.ids.FillPercent = bolt.DefaultFillPercent
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		got := result{objects: make(map[model.SHA]string)}
		if err := db.View(func(tx *bolt.Tx) error {
			stats := queryObjectStoreAt(tx.Bucket(queryGenerationsBucket)).ids.Stats()
			got.leafPages, got.branchPages = stats.LeafPageN, stats.BranchPageN
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		before := db.Stats()
		for round := range 4 {
			if err := db.Update(func(tx *bolt.Tx) error {
				store := queryObjectStoreAt(tx.Bucket(queryGenerationsBucket))
				if store.ids.FillPercent != bolt.DefaultFillPercent {
					t.Fatalf("incremental initial fill = %f", store.ids.FillPercent)
				}
				order := make([]model.SHA, 64)
				values := make(map[model.SHA][]byte, len(order))
				for i := range order {
					data := fmt.Appendf(nil, "incremental-object-%d-%05d", round, i)
					hash := model.SHA(plumbing.ComputeHash(plumbing.BlobObject, data).String())
					order[i] = hash
					values[hash] = append([]byte{byte(plumbing.BlobObject)}, data...)
				}
				if err := store.putBatch(t.Context(), order, values); err != nil {
					return err
				}
				if store.ids.FillPercent != bolt.DefaultFillPercent {
					t.Fatalf("incremental changed fill = %f", store.ids.FillPercent)
				}
				for i := range order {
					if err := store.delete(seed[(round*len(order)+i)*127%len(seed)]); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		after := db.Stats()
		got.allocated = after.TxStats.GetPageAlloc() - before.TxStats.GetPageAlloc()
		if err := db.View(func(tx *bolt.Tx) error {
			store := queryObjectStoreAt(tx.Bucket(queryGenerationsBucket))
			return store.ids.ForEach(func(hash, id []byte) error {
				data := store.objects.Get(id)
				if data == nil {
					t.Fatalf("missing object data for %x", hash)
				}
				got.objects[model.SHA(hex.EncodeToString(hash))] = string(id) + string(data)
				return nil
			})
		}); err != nil {
			t.Fatal(err)
		}
		if len(got.objects) != len(seed) {
			t.Fatalf("objects=%d; want%d", len(got.objects), len(seed))
		}
		return got
	}
	ordinary, dense := measure(false), measure(true)
	if !maps.Equal(ordinary.objects, dense.objects) {
		t.Fatal("bulk fill changed object identities, allocation IDs or data")
	}
	if dense.leafPages >= ordinary.leafPages || dense.branchPages >= ordinary.branchPages {
		t.Fatalf("index pages ordinary=%d/%d dense=%d/%d", ordinary.leafPages, ordinary.branchPages, dense.leafPages, dense.branchPages)
	}
	if dense.allocated >= ordinary.allocated {
		t.Fatalf("complete mutation allocation ordinary=%d dense=%d", ordinary.allocated, dense.allocated)
	}
	t.Logf("bulk leaf/branch pages ordinary=%d/%d dense=%d/%d; complete mutation allocated bytes ordinary=%d dense=%d", ordinary.leafPages, ordinary.branchPages, dense.leafPages, dense.branchPages, ordinary.allocated, dense.allocated)
}
