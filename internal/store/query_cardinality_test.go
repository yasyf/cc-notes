package store

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

func assertQueryTipCount(t *testing.T, db *bolt.DB, binding string, kind model.Kind, want uint64) {
	t.Helper()
	if err := db.View(func(tx *bolt.Tx) error {
		tips := queryMetadataKindTips(tx, binding, kind)
		if tips == nil {
			t.Fatalf("missing %s tips", kind)
		}
		var keys uint64
		if err := tips.ForEach(func(_, _ []byte) error { keys++; return nil }); err != nil {
			return err
		}
		if got := tips.Sequence(); got != want || keys != want {
			t.Fatalf("%s/%s count=%d actual=%d want=%d", binding, kind, got, keys, want)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueryMetadataCardinalityLifecycle(t *testing.T) {
	db := newQueryMetadataDB(t)
	for _, binding := range []string{"binding", "other"} {
		if err := db.Update(func(tx *bolt.Tx) error {
			return replaceQueryMetadata(tx, projectQueryMetadata(binding, "first", model.Doc{ID: "shared"}))
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, skipped := range []int{2, 0} {
		if err := db.Update(func(tx *bolt.Tx) error {
			row := projectQueryMetadata("binding", "updated", model.Doc{ID: "shared", SkippedOps: skipped})
			if err := replaceQueryMetadata(tx, row); err != nil {
				return err
			}
			return deleteQueryMetadataTip(queryMetadataBinding(tx, "binding"), queryMetadata{ID: "absent", Kind: model.KindDoc})
		}); err != nil {
			t.Fatal(err)
		}
		assertQueryTipCount(t, db, "binding", model.KindDoc, 1)
		assertQueryTipCount(t, db, "other", model.KindDoc, 1)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		return replaceQueryMetadata(tx, projectQueryMetadata("binding", "task", model.Task{ID: "shared"}))
	}); err != nil {
		t.Fatal(err)
	}
	assertQueryTipCount(t, db, "binding", model.KindDoc, 0)
	assertQueryTipCount(t, db, "binding", model.KindTask, 1)
	for range 2 {
		if err := db.Update(func(tx *bolt.Tx) error { return deleteQueryMetadata(tx, "binding", "shared") }); err != nil {
			t.Fatal(err)
		}
		assertQueryTipCount(t, db, "binding", model.KindTask, 0)
	}
	assertQueryTipCount(t, db, "other", model.KindDoc, 1)
}

func TestQueryMetadataCardinalityRollback(t *testing.T) {
	db := newQueryMetadataDB(t)
	if err := db.Update(func(tx *bolt.Tx) error {
		return replaceQueryMetadata(tx, projectQueryMetadata("binding", "original", model.Doc{ID: "kept"}))
	}); err != nil {
		t.Fatal(err)
	}
	abort := errors.New("abort metadata changes")
	err := db.Update(func(tx *bolt.Tx) error {
		if err := replaceQueryMetadata(tx, projectQueryMetadata("binding", "new", model.Doc{ID: "new"})); err != nil {
			return err
		}
		if err := replaceQueryMetadata(tx, projectQueryMetadata("binding", "changed-kind", model.Task{ID: "kept"})); err != nil {
			return err
		}
		if err := deleteQueryMetadata(tx, "binding", "new"); err != nil {
			return err
		}
		return abort
	})
	if !errors.Is(err, abort) {
		t.Fatalf("rollback error = %v", err)
	}
	assertQueryTipCount(t, db, "binding", model.KindDoc, 1)
	if err := db.View(func(tx *bolt.Tx) error {
		if readQueryMetadata(tx, "binding", "new") != nil || queryMetadataKindTips(tx, "binding", model.KindTask) != nil {
			t.Fatal("aborted rows or kind bucket survived")
		}
		diff, err := diffQueryMetadata(t.Context(), tx, "binding", model.KindDoc, map[model.EntityID]model.SHA{"kept": "original"})
		if err == nil && (len(diff.Changed) != 0 || len(diff.Removed) != 0) {
			t.Fatalf("aborted tip changes survived: %+v", diff)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

type queryCardinalityWorkContext struct {
	context.Context
	checks int
}

func (c *queryCardinalityWorkContext) Err() error {
	c.checks++
	return c.Context.Err()
}

func TestQueryMetadataCardinalityDiffWork(t *testing.T) {
	for _, count := range []int{32, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			db := newQueryMetadataDB(t)
			original := make(map[model.EntityID]model.SHA, count)
			if err := db.Update(func(tx *bolt.Tx) error {
				for i := range count {
					id := model.EntityID(fmt.Sprintf("id-%05d", i))
					original[id] = "tip"
					if err := replaceQueryMetadata(tx, projectQueryMetadata("binding", "tip", model.Doc{ID: id})); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			for _, mode := range []string{"unchanged", "add", "update", "delete", "delete-and-add"} {
				t.Run(mode, func(t *testing.T) {
					captured := maps.Clone(original)
					var changed []queryMetadataTip
					var removed []model.EntityID
					if mode == "add" || mode == "delete-and-add" {
						captured["new"] = "new-tip"
						changed = []queryMetadataTip{{ID: "new", Tip: "new-tip"}}
					}
					if mode == "update" {
						captured["id-00000"] = "new-tip"
						changed = []queryMetadataTip{{ID: "id-00000", Tip: "new-tip"}}
					}
					if mode == "delete" || mode == "delete-and-add" {
						delete(captured, "id-00000")
						removed = []model.EntityID{"id-00000"}
					}
					ctx := &queryCardinalityWorkContext{Context: t.Context()}
					if err := db.View(func(tx *bolt.Tx) error {
						diff, err := diffQueryMetadata(ctx, tx, "binding", model.KindDoc, captured)
						if err == nil && (!slices.Equal(diff.Changed, changed) || !slices.Equal(diff.Removed, removed)) {
							t.Fatalf("diff = %+v want changed=%v removed=%v", diff, changed, removed)
						}
						return err
					}); err != nil {
						t.Fatal(err)
					}
					t.Logf("stored=%d captured=%d mode=%s cancellation_checks=%d", count, len(captured), mode, ctx.checks)
					if len(removed) == 0 && ctx.checks > len(captured)+8 {
						t.Fatalf("enumerated stored tips without removals: %d checks for %d captured", ctx.checks, len(captured))
					}
				})
			}
		})
	}
}

func TestQueryMetadataCardinalityRejectsImpossibleCounts(t *testing.T) {
	for _, mode := range []string{"captured undercount", "stored overcount", "delete underflow", "replace zero", "insert overflow"} {
		t.Run(mode, func(t *testing.T) {
			db := newQueryMetadataDB(t)
			row := projectQueryMetadata("binding", "tip", model.Doc{ID: "id"})
			if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(tx, row) }); err != nil {
				t.Fatal(err)
			}
			err := db.Update(func(tx *bolt.Tx) error {
				tips := queryMetadataKindTips(tx, "binding", model.KindDoc)
				var count uint64
				switch mode {
				case "stored overcount":
					count = 2
				case "insert overflow":
					count = math.MaxUint64
				}
				if err := tips.SetSequence(count); err != nil {
					return err
				}
				switch mode {
				case "delete underflow":
					return deleteQueryMetadataTip(queryMetadataBinding(tx, "binding"), row)
				case "replace zero":
					return putQueryMetadataTip(queryMetadataBinding(tx, "binding"), row)
				case "insert overflow":
					row.ID = "new"
					return putQueryMetadataTip(queryMetadataBinding(tx, "binding"), row)
				default:
					_, err := diffQueryMetadata(t.Context(), tx, "binding", model.KindDoc, map[model.EntityID]model.SHA{"id": "tip"})
					return err
				}
			})
			if err == nil || !strings.Contains(err.Error(), "metadata tip count") {
				t.Fatalf("impossible count accepted: %v", err)
			}
			assertQueryTipCount(t, db, "binding", model.KindDoc, 1)
		})
	}
}
