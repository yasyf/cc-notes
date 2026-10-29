package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

func newQueryMetadataDB(t *testing.T) *bolt.DB {
	t.Helper()
	db, err := bolt.Open(filepath.Join(t.TempDir(), "query.db"), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	})
	return db
}

func labelMetadata(t *testing.T, db *bolt.DB, binding string, kind model.Kind, label string) []queryMetadata {
	t.Helper()
	var rows []queryMetadata
	if err := db.View(func(tx *bolt.Tx) error {
		return walkQueryMetadataLabel(tx, binding, kind, label, false, func(data []byte) error {
			var row queryMetadata
			if err := json.Unmarshal(data, &row); err != nil {
				return err
			}
			rows = append(rows, row)
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestQueryMetadataLabelReplaceDeleteAndRollback(t *testing.T) {
	db := newQueryMetadataDB(t)
	original := projectQueryMetadata("binding", "old-tip", model.Doc{ID: "doc", Tags: []string{"old", "shared"}, SkippedOps: 3})
	other := projectQueryMetadata("binding", "other-tip", model.Doc{ID: "other", Tags: []string{"shared"}})
	if err := db.Update(func(tx *bolt.Tx) error {
		if err := replaceQueryMetadata(tx, original); err != nil {
			return err
		}
		return replaceQueryMetadata(tx, other)
	}); err != nil {
		t.Fatal(err)
	}
	replacement := projectQueryMetadata("binding", "new-tip", model.Doc{ID: "doc", Tags: []string{"new", "shared", "new"}})
	rollback := errors.New("rollback")
	if err := db.Update(func(tx *bolt.Tx) error {
		if err := replaceQueryMetadata(tx, replacement); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("rollback = %v", err)
	}
	if got := labelMetadata(t, db, "binding", model.KindDoc, "old"); !reflect.DeepEqual(got, []queryMetadata{original}) {
		t.Fatalf("rollback old rows = %#v", got)
	}
	if got := labelMetadata(t, db, "binding", model.KindDoc, "new"); len(got) != 0 {
		t.Fatalf("rollback left new postings: %#v", got)
	}
	if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(tx, replacement) }); err != nil {
		t.Fatal(err)
	}
	if got := labelMetadata(t, db, "binding", model.KindDoc, "old"); len(got) != 0 {
		t.Fatalf("replace retained old posting: %#v", got)
	}
	if got := labelMetadata(t, db, "binding", model.KindDoc, "new"); !reflect.DeepEqual(got, []queryMetadata{replacement}) {
		t.Fatalf("replacement rows = %#v", got)
	}
	if got := labelMetadata(t, db, "binding", model.KindDoc, "shared"); !reflect.DeepEqual(got, []queryMetadata{replacement, other}) {
		t.Fatalf("shared rows = %#v", got)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		if err := deleteQueryMetadata(tx, "binding", "doc"); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("delete rollback = %v", err)
	}
	if got := labelMetadata(t, db, "binding", model.KindDoc, "new"); len(got) != 1 {
		t.Fatalf("delete rollback lost row: %#v", got)
	}
	if err := db.Update(func(tx *bolt.Tx) error { return deleteQueryMetadata(tx, "binding", "doc") }); err != nil {
		t.Fatal(err)
	}
	if got := labelMetadata(t, db, "binding", model.KindDoc, "new"); len(got) != 0 {
		t.Fatalf("delete retained posting: %#v", got)
	}
	if got := labelMetadata(t, db, "binding", model.KindDoc, "shared"); !reflect.DeepEqual(got, []queryMetadata{other}) {
		t.Fatalf("delete changed another row: %#v", got)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		if got := readQueryMetadata(tx, "binding", "doc"); got != nil {
			t.Fatalf("deleted row remains: %s", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueryMetadataLabelBindingIsolationAndNUL(t *testing.T) {
	db := newQueryMetadataDB(t)
	bindings := []string{"", "a", "a\x00b", "a:b"}
	labels := []string{"", "a", "a\x00", "a\x00b", "a:b", "b"}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, binding := range bindings {
			for i, label := range labels {
				row := projectQueryMetadata(binding, model.SHA(fmt.Sprintf("tip-%d", i)), model.Note{ID: model.EntityID(fmt.Sprintf("id-%d", i)), Tags: []string{label}})
				if err := replaceQueryMetadata(tx, row); err != nil {
					return err
				}
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, binding := range bindings {
		for i, label := range labels {
			got := labelMetadata(t, db, binding, model.KindNote, label)
			if len(got) != 1 || got[0].Binding != binding || got[0].ID != model.EntityID(fmt.Sprintf("id-%d", i)) || !slices.Equal(got[0].Labels, []string{label}) {
				t.Fatalf("binding %q label %q = %#v", binding, label, got)
			}
		}
	}
	if got := labelMetadata(t, db, "missing", model.KindNote, "a"); len(got) != 0 {
		t.Fatalf("unknown binding saw rows: %#v", got)
	}
	if err := db.Update(func(tx *bolt.Tx) error { return deleteQueryMetadata(tx, "a", "id-0") }); err != nil {
		t.Fatal(err)
	}
	if got := labelMetadata(t, db, "a\x00b", model.KindNote, ""); len(got) != 1 {
		t.Fatalf("deletion crossed binding: %#v", got)
	}
}

func TestQueryMetadataLabelCopiesTransactionBytes(t *testing.T) {
	db := newQueryMetadataDB(t)
	row := projectQueryMetadata("binding", "before", model.Answer{ID: "answer", Tags: []string{"label"}, SkippedOps: 7})
	if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(tx, row) }); err != nil {
		t.Fatal(err)
	}
	var captured []byte
	if err := db.View(func(tx *bolt.Tx) error {
		point := readQueryMetadata(tx, "binding", "answer")
		point[0] = '!'
		return walkQueryMetadataLabel(tx, "binding", model.KindAnswer, "label", false, func(data []byte) error {
			captured = data
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	row.Tip = "after"
	if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(tx, row) }); err != nil {
		t.Fatal(err)
	}
	var previous queryMetadata
	if err := json.Unmarshal(captured, &previous); err != nil {
		t.Fatal(err)
	}
	if previous.Tip != "before" || previous.SkippedOps != 7 {
		t.Fatalf("captured row changed after transaction: %#v", previous)
	}
	captured[0] = '!'
	if got := labelMetadata(t, db, "binding", model.KindAnswer, "label"); len(got) != 1 || got[0].Tip != "after" || got[0].SkippedOps != 7 {
		t.Fatalf("cursor bytes alias stored row: %#v", got)
	}
}

func TestQueryMetadataLabelOnlyVisitsSelectedRows(t *testing.T) {
	for _, size := range []int{64, 640} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			db := newQueryMetadataDB(t)
			if err := db.Update(func(tx *bolt.Tx) error {
				for i := range size {
					label := "unrelated"
					if i < 2 {
						label = "selected"
					}
					row := projectQueryMetadata("binding", "tip", model.Doc{ID: model.EntityID(fmt.Sprintf("id-%04d", i)), Tags: []string{label}})
					if err := replaceQueryMetadata(tx, row); err != nil {
						return err
					}
					if i >= 2 {
						if err := queryMetadataBinding(tx, "binding").Bucket(queryMetadataRowsBucket).Put([]byte(row.ID), []byte("invalid unselected JSON")); err != nil {
							return err
						}
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			visits := 0
			if err := db.View(func(tx *bolt.Tx) error {
				return walkQueryMetadataLabel(tx, "binding", model.KindDoc, "selected", false, func(data []byte) error {
					visits++
					var row queryMetadata
					return json.Unmarshal(data, &row)
				})
			}); err != nil {
				t.Fatal(err)
			}
			if visits != 2 {
				t.Fatalf("size %d visited %d rows, want 2", size, visits)
			}
			if got := labelMetadata(t, db, "binding", model.KindDoc, "absent"); len(got) != 0 {
				t.Fatalf("absent label saw rows: %#v", got)
			}
		})
	}
}

func TestQueryMetadataLabelStopsAtVisitorError(t *testing.T) {
	db := newQueryMetadataDB(t)
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, id := range []model.EntityID{"a", "b"} {
			if err := replaceQueryMetadata(tx, projectQueryMetadata("binding", "tip", model.Doc{ID: id, Tags: []string{"label"}})); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop")
	visits := 0
	err := db.View(func(tx *bolt.Tx) error {
		return walkQueryMetadataLabel(tx, "binding", model.KindDoc, "label", false, func([]byte) error {
			visits++
			return stop
		})
	})
	if !errors.Is(err, stop) || visits != 1 {
		t.Fatalf("walk error=%v visits=%d", err, visits)
	}
}
