package store

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/model"
)

func TestQueryAllocationBreadthFirst(t *testing.T) {
	writes := &queryObjectWrites{
		objects:  map[model.SHA][]byte{"root": {1}, "other": {1}, "left": {1}, "right": {1}, "shared": {1}, "transient": {1}},
		order:    []model.SHA{"shared", "left", "right", "root", "other", "transient"},
		children: map[model.SHA][]model.SHA{"root": {"left", "right", "stored"}, "other": {"right"}, "left": {"shared"}, "right": {"shared"}},
	}
	got, err := writes.breadthFirst(t.Context(), []model.SHA{"root", "other", "root"})
	want := []model.SHA{"root", "other", "left", "right", "shared", "transient"}
	if err != nil || !slices.Equal(got, want) {
		t.Fatalf("allocation = %v, %v; want %v", got, err, want)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := writes.breadthFirst(ctx, []model.SHA{"root"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled allocation = %v", err)
	}
}

func TestQueryAllocationGenerationRootPrecedesChildren(t *testing.T) {
	db := newQueryMetadataDB(t)
	if err := db.Update(func(tx *bolt.Tx) error {
		batch := make(queryGenerationBatch)
		for i := range 256 {
			row := projectQueryMetadata("binding", "tip", model.Doc{ID: model.EntityID(fmt.Sprintf("doc-%03d", i)), Title: "allocation", Tags: []string{"selected"}})
			if err := batch.replaceMetadata(tx, row); err != nil {
				return err
			}
		}
		return batch.flush(t.Context(), tx)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		generation, err := readQueryGeneration(queryGenerationBinding(tx, "binding"), queryCurrentRoot)
		if err != nil {
			return err
		}
		store := queryObjectStoreAt(tx.Bucket(queryGenerationsBucket))
		id, err := queryObjectID(store.ids, generation.Root)
		if err != nil {
			return err
		}
		if id != 1 {
			t.Fatalf("generation root ID = %d; want1", id)
		}
		entries, err := (queryGraphObjects{ctx: t.Context(), store: store}).ReadTree(generation.Root)
		if err != nil {
			return err
		}
		for i, entry := range entries {
			id, err := queryObjectID(store.ids, model.SHA(entry.Hash.String()))
			if err != nil {
				return err
			}
			if want := uint64(i + 2); id != want {
				t.Fatalf("child %s ID=%d; want%d", entry.Name, id, want)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueryAllocationReducesSiblingCounterPages(t *testing.T) {
	type result struct {
		allocated int64
		root      model.SHA
		counts    map[model.SHA]uint64
		blocks    int
	}
	measure := func(breadthFirst bool) result {
		db := newQueryMetadataDB(t)
		var rootHash model.SHA
		var siblings []model.SHA
		if err := db.Update(func(tx *bolt.Tx) error {
			root, err := tx.CreateBucket(queryGenerationsBucket)
			if err != nil {
				return err
			}
			objects, err := createQueryObjectStore(root)
			if err != nil {
				return err
			}
			if _, err := root.CreateBucket([]byte(managed.QueryObjectRefs)); err != nil {
				return err
			}
			writes := &queryObjectWrites{objects: make(map[model.SHA][]byte), refs: make(map[model.SHA]uint64), children: make(map[model.SHA][]model.SHA)}
			graph := queryGraphObjects{ctx: t.Context(), store: objects, writes: writes}
			var branches []object.TreeEntry
			for i := range 16 {
				var leaves []object.TreeEntry
				for j := range 160 {
					hash, err := graph.WriteBlob(fmt.Appendf(nil, "branch-%02d-leaf-%03d", i, j))
					if err != nil {
						return err
					}
					leaves = append(leaves, object.TreeEntry{Name: fmt.Sprintf("%03d", j), Mode: filemode.Regular, Hash: plumbing.NewHash(string(hash))})
				}
				hash, err := graph.WriteTree(leaves)
				if err != nil {
					return err
				}
				siblings = append(siblings, hash)
				branches = append(branches, object.TreeEntry{Name: fmt.Sprintf("%02d", i), Mode: filemode.Dir, Hash: plumbing.NewHash(string(hash))})
			}
			rootHash, err = graph.WriteTree(branches)
			if err != nil {
				return err
			}
			writes.refs[rootHash]++
			order := writes.order
			if breadthFirst {
				order, err = writes.breadthFirst(t.Context(), []model.SHA{rootHash})
				if err != nil {
					return err
				}
			}
			if err := objects.putBatch(t.Context(), order, writes.objects); err != nil {
				return err
			}
			counts := newQueryReferenceCounts(root)
			for hash, value := range writes.refs {
				if _, err := counts.get(hash); err != nil {
					return err
				}
				counts.set(hash, value)
			}
			return counts.flush(t.Context())
		}); err != nil {
			t.Fatal(err)
		}
		before := db.Stats().TxStats.GetPageAlloc()
		for _, delta := range []int{1, -1} {
			if err := db.Update(func(tx *bolt.Tx) error {
				counts := newQueryReferenceCounts(tx.Bucket(queryGenerationsBucket))
				for _, hash := range siblings {
					value, err := counts.get(hash)
					if err != nil {
						return err
					}
					counts.set(hash, uint64(int64(value)+int64(delta)))
				}
				return counts.flush(t.Context())
			}); err != nil {
				t.Fatal(err)
			}
		}
		got := result{allocated: db.Stats().TxStats.GetPageAlloc() - before, root: rootHash, counts: make(map[model.SHA]uint64)}
		if err := db.View(func(tx *bolt.Tx) error {
			root := tx.Bucket(queryGenerationsBucket)
			store := queryObjectStoreAt(root)
			blocks := make(map[uint64]bool)
			for _, hash := range siblings {
				id, err := queryObjectID(store.ids, hash)
				if err != nil {
					return err
				}
				blocks[(id-1)/queryReferenceBlockEntries] = true
			}
			got.blocks = len(blocks)
			return store.ids.ForEach(func(key, value []byte) error {
				hash := model.SHA(plumbing.NewHash(fmt.Sprintf("%x", key)).String())
				count, err := readQueryObjectCount(root.Bucket([]byte(managed.QueryObjectRefs)), binary.BigEndian.Uint64(value), hash)
				got.counts[hash] = count
				return err
			})
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	postorder, breadthFirst := measure(false), measure(true)
	if postorder.root != breadthFirst.root || !maps.Equal(postorder.counts, breadthFirst.counts) {
		t.Fatal("allocation changed graph identity or final reference counts")
	}
	if breadthFirst.blocks != 1 || postorder.blocks != 16 {
		t.Fatalf("sibling blocks postorder=%d breadth-first=%d", postorder.blocks, breadthFirst.blocks)
	}
	if breadthFirst.allocated >= postorder.allocated {
		t.Fatalf("counter page allocation postorder=%d breadth-first=%d", postorder.allocated, breadthFirst.allocated)
	}
	t.Logf("counter page allocation postorder=%d breadth-first=%d", postorder.allocated, breadthFirst.allocated)
}
