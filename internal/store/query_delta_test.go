package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

func TestQueryMetadataDeltaOnlyFoldsChangedTips(t *testing.T) {
	for _, size := range []int{64, 640} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			db := newQueryMetadataDB(t)
			captured := make(map[model.EntityID]model.SHA, size)
			if err := db.Update(func(tx *bolt.Tx) error {
				for i := range size {
					id := model.EntityID(fmt.Sprintf("id-%04d", i))
					captured[id] = "original-tip"
					row := projectQueryMetadata("binding", "original-tip", model.Doc{ID: id, Tags: []string{"original"}})
					if err := replaceQueryMetadata(tx, row); err != nil {
						return err
					}
					if err := queryMetadataBinding(tx, "binding").Bucket(queryMetadataRowsBucket).Put([]byte(id), []byte("invalid unchanged JSON")); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			folds := 0
			fold := func(_ context.Context, id model.EntityID, tip model.SHA) (queryMetadata, error) {
				folds++
				return projectQueryMetadata("binding", tip, model.Doc{ID: id, Tags: []string{"changed"}}), nil
			}
			if err := db.Update(func(tx *bolt.Tx) error {
				return applyQueryMetadataDelta(t.Context(), tx, "binding", model.KindDoc, captured, fold)
			}); err != nil {
				t.Fatal(err)
			}
			if folds != 0 {
				t.Fatalf("unchanged size %d folded %d rows", size, folds)
			}
			if err := db.Update(func(tx *bolt.Tx) error {
				data, err := json.Marshal(projectQueryMetadata("binding", "original-tip", model.Doc{ID: "id-0000", Tags: []string{"original"}}))
				if err != nil {
					return err
				}
				return queryMetadataBinding(tx, "binding").Bucket(queryMetadataRowsBucket).Put([]byte("id-0000"), data)
			}); err != nil {
				t.Fatal(err)
			}
			captured["id-0000"] = "changed-tip"
			if err := db.Update(func(tx *bolt.Tx) error {
				return applyQueryMetadataDelta(t.Context(), tx, "binding", model.KindDoc, captured, fold)
			}); err != nil {
				t.Fatal(err)
			}
			if folds != 1 {
				t.Fatalf("one change in size %d folded %d rows", size, folds)
			}
			if got := labelMetadata(t, db, "binding", model.KindDoc, "changed"); len(got) != 1 || got[0].Tip != "changed-tip" || got[0].ID != "id-0000" {
				t.Fatalf("changed output = %#v", got)
			}
			if err := db.Update(func(tx *bolt.Tx) error {
				return applyQueryMetadataDelta(t.Context(), tx, "binding", model.KindDoc, captured, fold)
			}); err != nil {
				t.Fatal(err)
			}
			if folds != 1 {
				t.Fatalf("unchanged second pass folded another row: %d", folds)
			}
		})
	}
}

func TestQueryMetadataDeltaRefoldsPartialAtSameTip(t *testing.T) {
	db := newQueryMetadataDB(t)
	row := projectQueryMetadata("binding", "tip", model.Doc{ID: "doc", SkippedOps: 5})
	if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(tx, row) }); err != nil {
		t.Fatal(err)
	}
	captured := map[model.EntityID]model.SHA{"doc": "tip"}
	folds := 0
	remaining := 3
	fold := func(_ context.Context, id model.EntityID, tip model.SHA) (queryMetadata, error) {
		folds++
		return projectQueryMetadata("binding", tip, model.Doc{ID: id, SkippedOps: remaining}), nil
	}
	for _, want := range []int{1, 2} {
		if err := db.Update(func(tx *bolt.Tx) error {
			return applyQueryMetadataDelta(t.Context(), tx, "binding", model.KindDoc, captured, fold)
		}); err != nil {
			t.Fatal(err)
		}
		if folds != want {
			t.Fatalf("partial fold count = %d want %d", folds, want)
		}
	}
	remaining = 0
	for range 2 {
		if err := db.Update(func(tx *bolt.Tx) error {
			return applyQueryMetadataDelta(t.Context(), tx, "binding", model.KindDoc, captured, fold)
		}); err != nil {
			t.Fatal(err)
		}
	}
	if folds != 3 {
		t.Fatalf("complete metadata kept refolding: %d", folds)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		summary := queryMetadataKindTips(tx, "binding", model.KindDoc).Get([]byte("doc"))
		if !slices.Equal(summary, append([]byte{0}, []byte("tip")...)) {
			t.Fatalf("compact completed summary = %v", summary)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueryMetadataDeltaRemovalCleansAllPostings(t *testing.T) {
	db := newQueryMetadataDB(t)
	anchor := model.Anchor{Kind: model.AnchorPath, Value: "removed-path"}
	if err := db.Update(func(tx *bolt.Tx) error {
		remove := projectQueryMetadata("binding", "remove-tip", model.Doc{ID: "remove", Tags: []string{"removed-label"}, Anchors: []model.Anchor{anchor}, SupersededBy: []model.EntityID{"removed-target"}})
		if err := replaceQueryMetadata(tx, remove); err != nil {
			return err
		}
		if err := replaceQueryMetadata(tx, projectQueryMetadata("binding", "keep-tip", model.Doc{ID: "keep"})); err != nil {
			return err
		}
		return queryMetadataBinding(tx, "binding").Bucket(queryMetadataRowsBucket).Put([]byte("keep"), []byte("invalid unchanged JSON"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		return applyQueryMetadataDelta(t.Context(), tx, "binding", model.KindDoc, map[model.EntityID]model.SHA{"keep": "keep-tip"}, func(context.Context, model.EntityID, model.SHA) (queryMetadata, error) {
			t.Fatal("removal folded an unchanged row")
			return queryMetadata{}, nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	for _, walk := range []func(*bolt.Tx, func([]byte) error) error{
		func(tx *bolt.Tx, visit func([]byte) error) error {
			return walkQueryMetadataLabel(tx, "binding", model.KindDoc, "removed-label", true, visit)
		},
		func(tx *bolt.Tx, visit func([]byte) error) error {
			return walkQueryMetadataAnchor(tx, "binding", model.KindDoc, anchor, true, visit)
		},
		func(tx *bolt.Tx, visit func([]byte) error) error {
			return walkQueryMetadataSuperseders(tx, "binding", model.KindDoc, "removed-target", true, visit)
		},
	} {
		if got := metadataPostingIDs(t, db, walk); len(got) != 0 {
			t.Fatalf("removed postings remain: %v", got)
		}
	}
	if err := db.View(func(tx *bolt.Tx) error {
		if readQueryMetadata(tx, "binding", "remove") != nil || queryMetadataKindTips(tx, "binding", model.KindDoc).Get([]byte("remove")) != nil {
			t.Fatal("removal left row or compact tip")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueryMetadataDeltaErrorAndCancellationRollBack(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(fmt.Sprint(cancel), func(t *testing.T) {
			db := newQueryMetadataDB(t)
			if err := db.Update(func(tx *bolt.Tx) error {
				for _, id := range []model.EntityID{"a", "b"} {
					if err := replaceQueryMetadata(tx, projectQueryMetadata("binding", "old-tip", model.Doc{ID: id, Tags: []string{"old"}})); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			ctx, stop := context.WithCancel(t.Context())
			defer stop()
			failure := errors.New("fold failed")
			want := failure
			if cancel {
				want = context.Canceled
			}
			var folded []model.EntityID
			err := db.Update(func(tx *bolt.Tx) error {
				return applyQueryMetadataDelta(ctx, tx, "binding", model.KindDoc, map[model.EntityID]model.SHA{"a": "new-tip", "b": "new-tip"}, func(_ context.Context, id model.EntityID, tip model.SHA) (queryMetadata, error) {
					folded = append(folded, id)
					if id == "b" {
						if !cancel {
							return queryMetadata{}, failure
						}
						stop()
					}
					return projectQueryMetadata("binding", tip, model.Doc{ID: id, Tags: []string{"new"}}), nil
				})
			})
			if !errors.Is(err, want) || !slices.Equal(folded, []model.EntityID{"a", "b"}) {
				t.Fatalf("apply error=%v folds=%v", err, folded)
			}
			if got := labelMetadata(t, db, "binding", model.KindDoc, "new"); len(got) != 0 {
				t.Fatalf("failed delta retained new postings: %#v", got)
			}
			if got := labelMetadata(t, db, "binding", model.KindDoc, "old"); len(got) != 2 || got[0].Tip != "old-tip" || got[1].Tip != "old-tip" {
				t.Fatalf("failed delta changed rows: %#v", got)
			}
			if err := db.View(func(tx *bolt.Tx) error {
				diff, err := diffQueryMetadata(t.Context(), tx, "binding", model.KindDoc, map[model.EntityID]model.SHA{"a": "old-tip", "b": "old-tip"})
				if err == nil && (len(diff.Changed) != 0 || len(diff.Removed) != 0) {
					t.Fatalf("failed delta changed summaries: %#v", diff)
				}
				return err
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQueryMetadataDeltaRejectsDifferentFoldIdentity(t *testing.T) {
	for name, mutate := range map[string]func(*queryMetadata){
		"version": func(row *queryMetadata) { row.Version++ },
		"binding": func(row *queryMetadata) { row.Binding = "other" },
		"kind":    func(row *queryMetadata) { row.Kind = model.KindAnswer },
		"id":      func(row *queryMetadata) { row.ID = "other" },
		"tip":     func(row *queryMetadata) { row.Tip = "other" },
	} {
		t.Run(name, func(t *testing.T) {
			db := newQueryMetadataDB(t)
			err := db.Update(func(tx *bolt.Tx) error {
				return applyQueryMetadataDelta(t.Context(), tx, "binding", model.KindDoc, map[model.EntityID]model.SHA{"id": "tip"}, func(_ context.Context, id model.EntityID, tip model.SHA) (queryMetadata, error) {
					row := projectQueryMetadata("binding", tip, model.Doc{ID: id})
					mutate(&row)
					return row, nil
				})
			})
			if err == nil || !strings.Contains(err.Error(), "metadata identity") {
				t.Fatalf("invalid identity = %v", err)
			}
			if err := db.View(func(tx *bolt.Tx) error {
				if queryMetadataBinding(tx, "binding") != nil {
					t.Fatal("invalid identity wrote metadata")
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQueryMetadataDeltaScopeIsolation(t *testing.T) {
	db := newQueryMetadataDB(t)
	rows := []queryMetadata{
		projectQueryMetadata("binding", "doc-tip", model.Doc{ID: "doc"}),
		projectQueryMetadata("binding", "answer-tip", model.Answer{ID: "answer"}),
		projectQueryMetadata("binding\x00other", "other-tip", model.Doc{ID: "doc"}),
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, row := range rows {
			if err := replaceQueryMetadata(tx, row); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		return applyQueryMetadataDelta(t.Context(), tx, "binding", model.KindDoc, nil, nil)
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		if readQueryMetadata(tx, "binding", "doc") != nil {
			t.Fatal("selected scope was not cleared")
		}
		for _, want := range rows[1:] {
			var got queryMetadata
			if err := json.Unmarshal(readQueryMetadata(tx, want.Binding, want.ID), &got); err != nil {
				return err
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("another scope changed: %#v", got)
			}
			diff, err := diffQueryMetadata(t.Context(), tx, want.Binding, want.Kind, map[model.EntityID]model.SHA{want.ID: want.Tip})
			if err != nil {
				return err
			}
			if len(diff.Changed) != 0 || len(diff.Removed) != 0 {
				t.Fatalf("another compact scope changed: %#v", diff)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueryMetadataDeltaAlreadyCancelledDoesNoWork(t *testing.T) {
	db := newQueryMetadataDB(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := db.Update(func(tx *bolt.Tx) error {
		return applyQueryMetadataDelta(ctx, tx, "binding", model.KindDoc, map[model.EntityID]model.SHA{"id": "tip"}, func(context.Context, model.EntityID, model.SHA) (queryMetadata, error) {
			t.Fatal("cancelled delta folded a row")
			return queryMetadata{}, nil
		})
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled apply = %v", err)
	}
}

func TestQueryMetadataDeltaAddsNewCapturedRows(t *testing.T) {
	db := newQueryMetadataDB(t)
	captured := map[model.EntityID]model.SHA{"b": "tip-b", "a": "tip-a"}
	var folded []model.EntityID
	if err := db.Update(func(tx *bolt.Tx) error {
		return applyQueryMetadataDelta(t.Context(), tx, "binding", model.KindDoc, captured, func(_ context.Context, id model.EntityID, tip model.SHA) (queryMetadata, error) {
			folded = append(folded, id)
			return projectQueryMetadata("binding", tip, model.Doc{ID: id, Tags: []string{"new"}}), nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(folded, []model.EntityID{"a", "b"}) {
		t.Fatalf("new fold order = %v", folded)
	}
	if got := labelMetadata(t, db, "binding", model.KindDoc, "new"); len(got) != 2 || got[0].Tip != "tip-a" || got[1].Tip != "tip-b" {
		t.Fatalf("new captured rows = %#v", got)
	}
	if err := db.View(func(tx *bolt.Tx) error {
		diff, err := diffQueryMetadata(t.Context(), tx, "binding", model.KindDoc, captured)
		if err == nil && (len(diff.Changed) != 0 || len(diff.Removed) != 0) {
			t.Fatalf("new compact summaries differ: %#v", diff)
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func TestQueryMetadataDeltaRejectsMalformedCompactSummary(t *testing.T) {
	for _, malformed := range [][]byte{{}, {2, 't'}} {
		t.Run(string(malformed), func(t *testing.T) {
			db := newQueryMetadataDB(t)
			if err := db.Update(func(tx *bolt.Tx) error {
				if err := replaceQueryMetadata(tx, projectQueryMetadata("binding", "tip", model.Doc{ID: "id"})); err != nil {
					return err
				}
				return queryMetadataKindTips(tx, "binding", model.KindDoc).Put([]byte("id"), malformed)
			}); err != nil {
				t.Fatal(err)
			}
			err := db.Update(func(tx *bolt.Tx) error {
				return applyQueryMetadataDelta(t.Context(), tx, "binding", model.KindDoc, map[model.EntityID]model.SHA{"id": "tip"}, func(context.Context, model.EntityID, model.SHA) (queryMetadata, error) {
					t.Fatal("malformed summary folded a row")
					return queryMetadata{}, nil
				})
			})
			if err == nil {
				t.Fatal("malformed compact summary accepted")
			}
		})
	}
}
