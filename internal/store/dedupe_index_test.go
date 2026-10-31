package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func duplicateKindOperations() map[model.Kind]func(string) []model.Op {
	return map[model.Kind]func(string) []model.Op{
		model.KindNote: noteOps, model.KindDoc: docOps, model.KindAnswer: answerOps,
		model.KindLog: logOps, model.KindTask: func(title string) []model.Op {
			return []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: title, Type: model.TypeTask, Branch: "main", Labels: []string{"selected"}}}
		},
		model.KindSprint: sprintOps, model.KindProject: projectOps, model.KindRunbook: runbookOps,
		model.KindInvestigation: investigationOps, model.KindPlan: planOps, model.KindLedger: ledgerOps,
	}
}

func TestIndexedDuplicateAllKindsAndHeldIndex(t *testing.T) {
	operations := duplicateKindOperations()
	if len(operations) != len(model.Kinds()) {
		t.Fatal("duplicate fixture does not cover every kind")
	}
	for _, kind := range model.Kinds() {
		t.Run(string(kind), func(t *testing.T) {
			s := initStore(t)
			ops := operations[kind]
			first, err := s.CreateExact(t.Context(), ops("same content"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := s.CreateExact(t.Context(), ops("unrelated content")); err != nil {
				t.Fatal(err)
			}
			fingerprint := duplicateFingerprint(first)
			values, hit, err := indexedDuplicateList(t.Context(), s, kind, fingerprint, fold.Fold)
			if err != nil || !hit || len(values) != 1 || values[0].EntityID() != first.EntityID() {
				t.Fatalf("selected duplicate = %v, %t, %v", values, hit, err)
			}
			wipeCache(t, s)
			if got := mustDedupe(t, s, ops("same content")); got.EntityID() != first.EntityID() {
				t.Fatal("cold cache changed the duplicate survivor")
			}
			q, err := queryMetadataIdentity(t.Context(), s, kind)
			if err != nil {
				t.Fatal(err)
			}
			db, err := bolt.Open(q.indexPath(), 0o600, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if _, hit, err := indexedDuplicateList(ctx, s, kind, fingerprint, fold.Fold); err != nil || hit {
				t.Fatalf("held index = hit %t, error %v", hit, err)
			}
			got, err := s.Create(ctx, ops("same content"))
			var duplicate *DuplicateError
			if got != nil || !errors.As(err, &duplicate) || duplicate.Existing.EntityID() != first.EntityID() || ctx.Err() != nil {
				t.Fatalf("held index lost authoritative survivor: got %v, error %v, context %v", got, err, ctx.Err())
			}
		})
	}
}

func TestIndexedDuplicatePostingLifecycle(t *testing.T) {
	db := newQueryMetadataDB(t)
	old := projectQueryMetadata("binding", "first", model.Note{ID: "selected", Title: "old"})
	updated := projectQueryMetadata("binding", "second", model.Note{ID: "selected", Title: "new"})
	walk := func(row queryMetadata) int {
		t.Helper()
		count := 0
		if err := db.View(func(tx *bolt.Tx) error {
			return walkQueryMetadataPostings(tx, row.Binding, queryMetadataDuplicatesBucket, queryMetadataLabelPrefix(row.Kind, row.DuplicateContent), false, func(got queryMetadata) bool {
				return got.Kind == row.Kind && got.DuplicateContent == row.DuplicateContent
			}, func(queryMetadata) error { count++; return nil })
		}); err != nil {
			t.Fatal(err)
		}
		return count
	}
	for _, row := range []queryMetadata{old, updated} {
		if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(tx, row) }); err != nil {
			t.Fatal(err)
		}
		if walk(row) != 1 {
			t.Fatal("current fingerprint posting missing")
		}
	}
	if walk(old) != 0 {
		t.Fatal("old fingerprint posting survived replacement")
	}
	abort := errors.New("abort fingerprint update")
	if err := db.Update(func(tx *bolt.Tx) error {
		if err := replaceQueryMetadata(tx, old); err != nil {
			return err
		}
		return abort
	}); !errors.Is(err, abort) {
		t.Fatal(err)
	}
	if walk(old) != 0 || walk(updated) != 1 {
		t.Fatal("aborted replacement changed fingerprint postings")
	}
	if err := db.Update(func(tx *bolt.Tx) error { return deleteQueryMetadata(tx, updated.Binding, updated.ID) }); err != nil {
		t.Fatal(err)
	}
	if walk(updated) != 0 {
		t.Fatal("deleted row retained a fingerprint posting")
	}
}

func TestIndexedDuplicateSurvivorOrderCollisionAndPins(t *testing.T) {
	s := initStore(t)
	same := make([]model.Snapshot, 0, 2)
	for range 2 {
		value, err := s.CreateExact(t.Context(), noteOps("same"))
		if err != nil {
			t.Fatal(err)
		}
		same = append(same, value)
	}
	slices.SortFunc(same, func(a, b model.Snapshot) int {
		if order := a.Meta().CreatedAt.Compare(b.Meta().CreatedAt); order != 0 {
			return order
		}
		return strings.Compare(string(a.EntityID()), string(b.EntityID()))
	})
	other, err := s.CreateExact(t.Context(), noteOps("different"))
	if err != nil {
		t.Fatal(err)
	}
	fingerprint := duplicateFingerprint(same[0])
	if values, hit, err := indexedDuplicateList(t.Context(), s, model.KindNote, fingerprint, fold.Note); err != nil || !hit || len(values) != 2 || values[0].ID != same[0].EntityID() {
		t.Fatalf("ordered candidates = %v, %t, %v", values, hit, err)
	}
	pinned, err := s.PinnedKinds(t.Context(), model.KindNote)
	if err != nil {
		t.Fatal(err)
	}
	if values, hit, err := indexedDuplicateList(t.Context(), pinned, model.KindNote, fingerprint, fold.Note); err != nil || !hit || len(values) != 2 {
		t.Fatalf("matching pinned = %v, %t, %v", values, hit, err)
	}
	q, err := queryMetadataIdentity(t.Context(), s, model.KindNote)
	if err != nil {
		t.Fatal(err)
	}
	func() {
		db, err := bolt.Open(q.indexPath(), 0o600, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := db.Close(); err != nil {
				t.Error(err)
			}
		}()
		row := projectQueryMetadata(q.binding, other.Meta().Head, other)
		row.DuplicateContent = fingerprint
		if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(tx, row) }); err != nil {
			t.Fatal(err)
		}
	}()
	if got := mustDedupe(t, s, noteOps("same")); got.EntityID() != same[0].EntityID() {
		t.Fatal("fingerprint collision or posting order changed the exact survivor")
	}
	appendOps(t, s, refs.For(model.KindNote, same[0].EntityID()), model.SetTitle{Title: "changed"})
	if values, hit, err := indexedDuplicateList(t.Context(), s, model.KindNote, fingerprint, fold.Note); err != nil || !hit || len(values) != 2 || values[0].ID == same[0].EntityID() {
		t.Fatalf("refreshed candidates = %v, %t, %v", values, hit, err)
	}
	if _, hit, err := indexedDuplicateList(t.Context(), pinned, model.KindNote, fingerprint, fold.Note); err != nil || hit {
		t.Fatalf("stale pinned = %t, %v", hit, err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, hit, err := indexedDuplicateList(canceled, s, model.KindNote, fingerprint, fold.Note); hit || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled duplicate lookup = %t, %v", hit, err)
	}
}

func TestQueryDuplicateSelectedWork(t *testing.T) {
	for _, count := range []int{32, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := initStore(t)
			signature := gitobj.Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1700000000, 0)}
			packed := make([]string, 0, count)
			var selected model.Snapshot
			for i := range count {
				tip, err := s.Repo.WriteOpsCommit(t.Context(), nil, signature, "duplicate fixture", model.Pack{Lamport: 1, Ops: noteOps(fmt.Sprintf("fixture %d", i))})
				if err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					selected, err = s.LoadAt(t.Context(), tip)
					if err != nil {
						t.Fatal(err)
					}
				}
				packed = append(packed, string(tip)+" "+refs.For(model.KindNote, model.EntityID(tip)))
			}
			slices.SortFunc(packed, func(a, b string) int { return strings.Compare(strings.Fields(a)[1], strings.Fields(b)[1]) })
			if err := os.WriteFile(filepath.Join(s.recordsCommonDir, "packed-refs"), []byte("# pack-refs with: peeled fully-peeled sorted \n"+strings.Join(packed, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			q, err := queryMetadataIdentity(t.Context(), s, model.KindNote)
			if err != nil {
				t.Fatal(err)
			}
			fingerprint := duplicateFingerprint(selected)
			if _, hit, err := indexedDuplicateList(t.Context(), s, model.KindNote, fingerprint, fold.Fold); err != nil || !hit {
				t.Fatalf("prime = %t %v", hit, err)
			}
			if generation, _ := queryIndexGeneration(t, q); generation == "" {
				if os.Getenv("CCN_REQUIRE_NATIVE_REF_COUNTER") == "1" {
					t.Fatal("required native fingerprint generation is unavailable")
				}
				t.Skip("filesystem does not qualify recent native generations")
			}
			ctx := &queryWorkContext{Context: t.Context()}
			got := mustDedupeWithContext(ctx, t, s, noteOps("fixture 0"))
			if got.EntityID() != selected.EntityID() || ctx.checks.Load() > 40 {
				t.Fatalf("KEYED_DUPLICATE_WORK: count=%d survivor=%s checks=%d", count, got.EntityID(), ctx.checks.Load())
			}
			t.Logf("stored=%d selected=1 checks=%d", count, ctx.checks.Load())
		})
	}
}

func mustDedupeWithContext(ctx context.Context, t *testing.T, s *Store, ops []model.Op) model.Snapshot {
	t.Helper()
	_, err := s.Create(ctx, ops)
	var duplicate *DuplicateError
	if !errors.As(err, &duplicate) || !errors.Is(err, ErrDuplicate) {
		t.Fatalf("expected DuplicateError, got %v", err)
	}
	return duplicate.Existing
}
