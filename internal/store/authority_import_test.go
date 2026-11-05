package store

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestLegacyAuthorityImportKeepsRecordsAndDropsItsGraph(t *testing.T) {
	s := initStore(t)
	a := create(t, s, docOps("a")).EntityID()
	b := create(t, s, docOps("b")).EntityID()
	appendOps(t, s, refs.For(model.KindDoc, a), model.AddTag{Tag: "kept"}, model.AddSupersededBy{ID: b})
	legacy := managed.LegacyPath(s.recordsCommonDir)
	if err := os.Rename(managed.Path(s.recordsCommonDir), legacy); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(legacy, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		graph, err := tx.CreateBucket([]byte(managed.LegacyQueryGenerations))
		if err != nil {
			return err
		}
		for _, name := range []string{"objects", "object-ids", "object-refs"} {
			if _, err := graph.CreateBucket([]byte(name)); err != nil {
				return err
			}
		}
		if err := tx.Bucket(queryGenerationsBucket).ForEach(func(atom, _ []byte) error {
			current, err := readQueryGeneration(tx.Bucket(queryGenerationsBucket).Bucket(atom), queryCurrentRoot)
			if err != nil {
				return err
			}
			binding, err := graph.CreateBucket(atom)
			if err != nil {
				return err
			}
			return binding.Put(queryCurrentRoot, fmt.Appendf(nil, `{"Root":"%040d","Rows":%d}`, 1, current.Rows))
		}); err != nil {
			return err
		}
		return tx.DeleteBucket(queryGenerationsBucket)
	})
	if err = errors.Join(err, db.Close()); err != nil {
		t.Fatal(err)
	}
	if s.AuthorityExists() {
		t.Fatal("fixture left the current authority in place")
	}
	if err := s.ReindexIfMissing(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, heads, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || !slices.Equal(heads, []model.EntityID{b}) {
		t.Fatalf("imported supersession = %v %v %v", heads, hit, err)
	}
	err = s.managedView(t.Context(), func(tx *bolt.Tx) error {
		if tx.Bucket([]byte(managed.LegacyQueryGenerations)) != nil {
			t.Fatal("import carried the legacy query graph")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	appendOps(t, s, refs.For(model.KindDoc, a), model.AddTag{Tag: "after import"})
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("import removed the legacy authority an older binary still reads: %v", err)
	}
}
