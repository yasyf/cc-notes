package store

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

func metadataPostingIDs(t *testing.T, db *bolt.DB, walk func(*bolt.Tx, func(queryMetadata) error) error) []model.EntityID {
	t.Helper()
	var ids []model.EntityID
	if err := db.View(func(tx *bolt.Tx) error {
		return walk(tx, func(row queryMetadata) error {
			ids = append(ids, row.ID)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	slices.Sort(ids)
	return ids
}

func TestQueryMetadataSupersedersMatchSnapshotOracle(t *testing.T) {
	db := newQueryMetadataDB(t)
	docs := []model.Doc{
		{ID: "first", SupersededBy: []model.EntityID{"target", "missing"}},
		{ID: "second", SupersededBy: []model.EntityID{"target"}},
		{ID: "deleted", SupersededBy: []model.EntityID{"target"}, Deleted: true},
		{ID: "cycle-a", SupersededBy: []model.EntityID{"cycle-b"}},
		{ID: "cycle-b", SupersededBy: []model.EntityID{"cycle-a"}},
		{ID: "terminal"},
		{ID: "nul-target", SupersededBy: []model.EntityID{"target\x00suffix"}},
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, doc := range docs {
			if err := replaceQueryMetadata(tx, projectQueryMetadata("binding", "tip", doc)); err != nil {
				return err
			}
		}
		if err := replaceQueryMetadata(tx, projectQueryMetadata("binding", "tip", model.Answer{ID: "other-kind", SupersededBy: []model.EntityID{"target"}})); err != nil {
			return err
		}
		return replaceQueryMetadata(tx, projectQueryMetadata("binding\x00other", "tip", model.Doc{ID: "other-binding", SupersededBy: []model.EntityID{"target"}}))
	}); err != nil {
		t.Fatal(err)
	}
	for _, target := range []model.EntityID{"target", "missing", "cycle-a", "cycle-b", "terminal", "target\x00suffix", "absent"} {
		for _, includeDeleted := range []bool{false, true} {
			var want []model.EntityID
			for _, doc := range docs {
				if (includeDeleted || !doc.Deleted) && slices.Contains(doc.SupersededBy, target) {
					want = append(want, doc.ID)
				}
			}
			slices.Sort(want)
			got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func(queryMetadata) error) error {
				return walkQueryMetadataSuperseders(tx, "binding", model.KindDoc, target, includeDeleted, visit)
			})
			if !slices.Equal(got, want) {
				t.Fatalf("target %q includeDeleted=%t got %v want %v", target, includeDeleted, got, want)
			}
		}
	}
}

func TestQueryMetadataExactAnchorsAndKindIsolation(t *testing.T) {
	db := newQueryMetadataDB(t)
	anchors := []model.Anchor{
		{Kind: model.AnchorPath, Value: "a"},
		{Kind: model.AnchorPath, Value: "a\x00b"},
		{Kind: model.AnchorDir, Value: "a\x00b"},
		{Kind: model.AnchorBranch, Value: "a:b"},
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for i, anchor := range anchors {
			id := model.EntityID(fmt.Sprintf("doc-%d", i))
			if err := replaceQueryMetadata(tx, projectQueryMetadata("binding", "tip", model.Doc{ID: id, Anchors: []model.Anchor{anchor}})); err != nil {
				return err
			}
		}
		if err := replaceQueryMetadata(tx, projectQueryMetadata("binding", "tip", model.Answer{ID: "answer", Anchors: anchors})); err != nil {
			return err
		}
		return replaceQueryMetadata(tx, projectQueryMetadata("binding\x00other", "tip", model.Doc{ID: "other-binding", Anchors: anchors}))
	}); err != nil {
		t.Fatal(err)
	}
	for i, anchor := range anchors {
		got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func(queryMetadata) error) error {
			return walkQueryMetadataAnchor(tx, "binding", model.KindDoc, anchor, false, visit)
		})
		if want := []model.EntityID{model.EntityID(fmt.Sprintf("doc-%d", i))}; !slices.Equal(got, want) {
			t.Fatalf("anchor %#v = %v, want %v", anchor, got, want)
		}
	}
	got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func(queryMetadata) error) error {
		return walkQueryMetadataAnchor(tx, "binding", model.KindDoc, model.Anchor{Kind: model.AnchorPath, Value: "a/"}, false, visit)
	})
	if len(got) != 0 {
		t.Fatalf("exact anchor query normalized the value: %v", got)
	}
}

func TestQueryMetadataRelationsReplaceDeleteAndRollback(t *testing.T) {
	db := newQueryMetadataDB(t)
	oldAnchor := model.Anchor{Kind: model.AnchorPath, Value: "old"}
	newAnchor := model.Anchor{Kind: model.AnchorPath, Value: "new"}
	original := projectQueryMetadata("binding", "old-tip", model.Doc{ID: "source", Tags: []string{"label"}, Anchors: []model.Anchor{oldAnchor}, SupersededBy: []model.EntityID{"old-target"}})
	replacement := projectQueryMetadata("binding", "new-tip", model.Doc{ID: "source", Tags: []string{"label"}, Anchors: []model.Anchor{newAnchor}, SupersededBy: []model.EntityID{"new-target"}, Deleted: true})
	if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(tx, original) }); err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("rollback")
	if err := db.Update(func(tx *bolt.Tx) error {
		if err := replaceQueryMetadata(tx, replacement); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func(queryMetadata) error) error {
		return walkQueryMetadataAnchor(tx, "binding", model.KindDoc, oldAnchor, false, visit)
	}); !slices.Equal(got, []model.EntityID{"source"}) {
		t.Fatalf("rollback lost old anchor: %v", got)
	}
	if got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func(queryMetadata) error) error {
		return walkQueryMetadataSuperseders(tx, "binding", model.KindDoc, "new-target", true, visit)
	}); len(got) != 0 {
		t.Fatalf("rollback retained new edge: %v", got)
	}
	if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(tx, replacement) }); err != nil {
		t.Fatal(err)
	}
	for _, includeDeleted := range []bool{false, true} {
		var want []model.EntityID
		if includeDeleted {
			want = []model.EntityID{"source"}
		}
		for name, walk := range map[string]func(*bolt.Tx, func(queryMetadata) error) error{
			"label": func(tx *bolt.Tx, visit func(queryMetadata) error) error {
				return walkQueryMetadataLabel(tx, "binding", model.KindDoc, "label", includeDeleted, visit)
			},
			"anchor": func(tx *bolt.Tx, visit func(queryMetadata) error) error {
				return walkQueryMetadataAnchor(tx, "binding", model.KindDoc, newAnchor, includeDeleted, visit)
			},
			"edge": func(tx *bolt.Tx, visit func(queryMetadata) error) error {
				return walkQueryMetadataSuperseders(tx, "binding", model.KindDoc, "new-target", includeDeleted, visit)
			},
		} {
			if got := metadataPostingIDs(t, db, walk); !slices.Equal(got, want) {
				t.Fatalf("%s includeDeleted=%t got %v want %v", name, includeDeleted, got, want)
			}
		}
	}
	if got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func(queryMetadata) error) error {
		return walkQueryMetadataAnchor(tx, "binding", model.KindDoc, oldAnchor, true, visit)
	}); len(got) != 0 {
		t.Fatalf("replace retained old anchor: %v", got)
	}
	if got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func(queryMetadata) error) error {
		return walkQueryMetadataSuperseders(tx, "binding", model.KindDoc, "old-target", true, visit)
	}); len(got) != 0 {
		t.Fatalf("replace retained old edge: %v", got)
	}
	if err := db.Update(func(tx *bolt.Tx) error { return deleteQueryMetadata(tx, "binding", "source") }); err != nil {
		t.Fatal(err)
	}
	if got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func(queryMetadata) error) error {
		return walkQueryMetadataAnchor(tx, "binding", model.KindDoc, newAnchor, true, visit)
	}); len(got) != 0 {
		t.Fatalf("delete retained new anchor: %v", got)
	}
	if got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func(queryMetadata) error) error {
		return walkQueryMetadataSuperseders(tx, "binding", model.KindDoc, "new-target", true, visit)
	}); len(got) != 0 {
		t.Fatalf("delete retained new edge: %v", got)
	}
}

func TestQueryMetadataLivePostingsDoNotHydrateDeletedRows(t *testing.T) {
	anchor := model.Anchor{Kind: model.AnchorPath, Value: "selected"}
	for _, size := range []int{64, 640} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			db := newQueryMetadataDB(t)
			if err := db.Update(func(tx *bolt.Tx) error {
				for i := range size {
					row := projectQueryMetadata("binding", "tip", model.Doc{ID: model.EntityID(fmt.Sprintf("deleted-%04d", i)), Tags: []string{"selected"}, Anchors: []model.Anchor{anchor}, SupersededBy: []model.EntityID{"target"}, Deleted: true})
					if err := replaceQueryMetadata(tx, row); err != nil {
						return err
					}
					if err := queryMetadataBinding(tx, "binding").Bucket(queryMetadataRowsBucket).Put([]byte(row.ID), []byte("invalid deleted JSON")); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			walks := []func(*bolt.Tx, func(queryMetadata) error) error{
				func(tx *bolt.Tx, visit func(queryMetadata) error) error {
					return walkQueryMetadataLabel(tx, "binding", model.KindDoc, "selected", false, visit)
				},
				func(tx *bolt.Tx, visit func(queryMetadata) error) error {
					return walkQueryMetadataAnchor(tx, "binding", model.KindDoc, anchor, false, visit)
				},
				func(tx *bolt.Tx, visit func(queryMetadata) error) error {
					return walkQueryMetadataSuperseders(tx, "binding", model.KindDoc, "target", false, visit)
				},
			}
			for _, walk := range walks {
				if got := metadataPostingIDs(t, db, walk); len(got) != 0 {
					t.Fatalf("deleted corpus produced live rows: %v", got)
				}
			}
			if err := db.Update(func(tx *bolt.Tx) error {
				for _, id := range []model.EntityID{"live-a", "live-b"} {
					row := projectQueryMetadata("binding", "tip", model.Doc{ID: id, Tags: []string{"selected"}, Anchors: []model.Anchor{anchor}, SupersededBy: []model.EntityID{"target"}})
					if err := replaceQueryMetadata(tx, row); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			for _, walk := range walks {
				if got := metadataPostingIDs(t, db, walk); !reflect.DeepEqual(got, []model.EntityID{"live-a", "live-b"}) {
					t.Fatalf("size %d selected rows = %v", size, got)
				}
			}
		})
	}
}
