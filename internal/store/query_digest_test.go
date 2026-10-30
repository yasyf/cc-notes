package store

import (
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

func TestQueryMetadataLongPostingTermsReplaceAndDelete(t *testing.T) {
	db := newQueryMetadataDB(t)
	oldLabel := "label\x00" + strings.Repeat("l", 40<<10)
	newLabel := oldLabel + "new"
	oldAnchor := model.Anchor{Kind: model.AnchorPath, Value: "path\x00" + strings.Repeat("p", 40<<10)}
	newAnchor := model.Anchor{Kind: model.AnchorDir, Value: oldAnchor.Value + "new"}
	row := projectQueryMetadata("binding", "old-tip", model.Doc{ID: "source", Tags: []string{oldLabel, oldLabel}, Anchors: []model.Anchor{oldAnchor}, SkippedOps: 7})
	for _, prefix := range [][]byte{queryMetadataLabelPrefix(row.Kind, oldLabel), queryMetadataAnchorPrefix(row.Kind, oldAnchor)} {
		if len(prefix) != 32 || len(queryMetadataPostingKey(prefix, row)) != 33+len(row.ID) {
			t.Fatalf("posting key depends on term length: prefix=%d", len(prefix))
		}
	}
	if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(tx, row) }); err != nil {
		t.Fatal(err)
	}
	if got := labelMetadata(t, db, "binding", model.KindDoc, oldLabel); len(got) != 1 || got[0].SkippedOps != 7 {
		t.Fatalf("long duplicate label rows = %#v", got)
	}
	if got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func(queryMetadata) error) error {
		return walkQueryMetadataAnchor(tx, "binding", model.KindDoc, oldAnchor, false, visit)
	}); !slices.Equal(got, []model.EntityID{"source"}) {
		t.Fatalf("long anchor rows = %v", got)
	}
	row.Tip, row.Labels, row.Anchors = "new-tip", []string{newLabel}, []model.Anchor{newAnchor}
	if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(tx, row) }); err != nil {
		t.Fatal(err)
	}
	if got := labelMetadata(t, db, "binding", model.KindDoc, oldLabel); len(got) != 0 {
		t.Fatalf("replacement retained old long label: %#v", got)
	}
	if got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func(queryMetadata) error) error {
		return walkQueryMetadataAnchor(tx, "binding", model.KindDoc, oldAnchor, true, visit)
	}); len(got) != 0 {
		t.Fatalf("replacement retained old long anchor: %v", got)
	}
	if got := labelMetadata(t, db, "binding", model.KindDoc, newLabel); len(got) != 1 || got[0].Tip != "new-tip" {
		t.Fatalf("replacement lost new long label: %#v", got)
	}
	if got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func(queryMetadata) error) error {
		return walkQueryMetadataAnchor(tx, "binding", model.KindDoc, newAnchor, false, visit)
	}); !slices.Equal(got, []model.EntityID{"source"}) {
		t.Fatalf("replacement lost new long anchor: %v", got)
	}
	if err := db.Update(func(tx *bolt.Tx) error { return deleteQueryMetadata(tx, "binding", "source") }); err != nil {
		t.Fatal(err)
	}
	if got := labelMetadata(t, db, "binding", model.KindDoc, newLabel); len(got) != 0 {
		t.Fatalf("delete retained long label: %#v", got)
	}
	if got := metadataPostingIDs(t, db, func(tx *bolt.Tx, visit func(queryMetadata) error) error {
		return walkQueryMetadataAnchor(tx, "binding", model.KindDoc, newAnchor, true, visit)
	}); len(got) != 0 {
		t.Fatalf("delete retained long anchor: %v", got)
	}
}

func TestQueryMetadataPostingDigestCollisionUsesExactStoredTerms(t *testing.T) {
	db := newQueryMetadataDB(t)
	anchor := model.Anchor{Kind: model.AnchorPath, Value: "selected\x00value"}
	rows := []queryMetadata{
		projectQueryMetadata("binding", "tip", model.Doc{ID: "exact", Tags: []string{"selected"}, Anchors: []model.Anchor{anchor}, SupersededBy: []model.EntityID{"target"}}),
		projectQueryMetadata("binding", "tip", model.Doc{ID: "both", Tags: []string{"other", "selected", "selected"}, Anchors: []model.Anchor{{Kind: model.AnchorDir, Value: anchor.Value}, anchor}, SupersededBy: []model.EntityID{"other", "target"}}),
		projectQueryMetadata("binding", "tip", model.Doc{ID: "different", Tags: []string{"other"}, Anchors: []model.Anchor{{Kind: model.AnchorDir, Value: anchor.Value}}, SupersededBy: []model.EntityID{"other"}}),
		projectQueryMetadata("binding", "tip", model.Doc{ID: "nul", Tags: []string{"selected\x00"}, Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: anchor.Value + "\x00"}}, SupersededBy: []model.EntityID{"target\x00"}}),
		projectQueryMetadata("binding", "tip", model.Answer{ID: "other-kind", Tags: []string{"selected"}, Anchors: []model.Anchor{anchor}, SupersededBy: []model.EntityID{"target"}}),
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, row := range rows {
			if err := replaceQueryMetadata(tx, row); err != nil {
				return err
			}
			binding := queryMetadataBinding(tx, "binding")
			for _, posting := range []struct {
				bucket []byte
				prefix []byte
			}{
				{queryMetadataLabelsBucket, queryMetadataLabelPrefix(model.KindDoc, "selected")},
				{queryMetadataAnchorsBucket, queryMetadataAnchorPrefix(model.KindDoc, anchor)},
				{queryMetadataSupersedersBucket, queryMetadataSupersedersPrefix(model.KindDoc, "target")},
			} {
				for range 2 {
					if err := binding.Bucket(posting.bucket).Put(queryMetadataPostingKey(slices.Clone(posting.prefix), row), nil); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for name, walk := range map[string]func(*bolt.Tx, func(queryMetadata) error) error{
		"label": func(tx *bolt.Tx, visit func(queryMetadata) error) error {
			return walkQueryMetadataLabel(tx, "binding", model.KindDoc, "selected", false, visit)
		},
		"anchor": func(tx *bolt.Tx, visit func(queryMetadata) error) error {
			return walkQueryMetadataAnchor(tx, "binding", model.KindDoc, anchor, false, visit)
		},
		"inbound": func(tx *bolt.Tx, visit func(queryMetadata) error) error {
			return walkQueryMetadataSuperseders(tx, "binding", model.KindDoc, "target", false, visit)
		},
	} {
		if got := metadataPostingIDs(t, db, walk); !slices.Equal(got, []model.EntityID{"both", "exact"}) {
			t.Fatalf("%s digest collision changed exact results: %v", name, got)
		}
	}
}

func TestQueryMetadataLongOpaqueBindingIsolation(t *testing.T) {
	db := newQueryMetadataDB(t)
	bindings := []string{"binding\x00" + strings.Repeat("b", 40<<10), "binding\x00" + strings.Repeat("b", 40<<10) + "other"}
	if err := db.Update(func(tx *bolt.Tx) error {
		for i, binding := range bindings {
			tip := model.SHA("first")
			if i != 0 {
				tip = "second"
			}
			if err := replaceQueryMetadata(tx, projectQueryMetadata(binding, tip, model.Doc{ID: "same-id", Tags: []string{"label"}})); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i, binding := range bindings {
		want := model.SHA("first")
		if i != 0 {
			want = "second"
		}
		if got := labelMetadata(t, db, binding, model.KindDoc, "label"); len(got) != 1 || got[0].Tip != want || got[0].Binding != binding {
			t.Fatalf("long binding %d was not isolated", i)
		}
	}
}
