package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

type queryWorkContext struct {
	context.Context
	checks atomic.Int64
}

func (c *queryWorkContext) Err() error {
	c.checks.Add(1)
	return c.Context.Err()
}

func queryIndexGeneration(t *testing.T, q queryMetadataSource) (string, int) {
	t.Helper()
	db, err := bolt.Open(q.indexPath(), 0o600, &bolt.Options{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	var generation string
	var id int
	if err := db.View(func(tx *bolt.Tx) error {
		generation = string(queryMetadataBinding(tx, q.binding).Get(queryMetadataGenerationKey))
		id = tx.ID()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return generation, id
}

func unqualifyQueryGeneration(t *testing.T, q queryMetadataSource) {
	t.Helper()
	db, err := bolt.Open(q.indexPath(), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	if err := db.Update(func(tx *bolt.Tx) error {
		return queryMetadataBinding(tx, q.binding).Delete(queryMetadataGenerationKey)
	}); err != nil {
		t.Fatal(err)
	}
}

func settleSupersession(t *testing.T, s *Store, id model.EntityID) queryMetadataSource {
	t.Helper()
	time.Sleep(2100 * time.Millisecond)
	if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, id); err != nil || !hit {
		t.Fatalf("settled = %v %v", hit, err)
	}
	q, err := queryMetadataIdentity(t.Context(), s, model.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	if generation, _ := queryIndexGeneration(t, q); generation == "" {
		t.Fatal("settled complete source did not publish generation")
	}
	return q
}

func TestSettledSupersessionSkipsNamespaceAndSummaryWalks(t *testing.T) {
	for _, count := range []int{8, 80} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := initStore(t)
			a := create(t, s, docOps("a")).EntityID()
			b := create(t, s, docOps("b")).EntityID()
			appendOps(t, s, refs.For(model.KindDoc, a), model.AddSupersededBy{ID: b})
			var unrelated model.EntityID
			for range count {
				unrelated = create(t, s, docOps("unrelated")).EntityID()
			}
			q := settleSupersession(t, s, a)
			db, err := bolt.Open(q.indexPath(), 0o600, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Update(func(tx *bolt.Tx) error {
				if err := queryMetadataKindTips(tx, q.binding, q.kind).Put([]byte(unrelated), []byte{}); err != nil {
					return err
				}
				return queryMetadataBinding(tx, q.binding).Bucket(queryMetadataRowsBucket).Put([]byte(unrelated), []byte("not JSON"))
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(s.cache.dir); err != nil {
				t.Fatal(err)
			}
			tip, err := s.Repo.Tip(t.Context(), refs.For(model.KindDoc, a))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(s.recordsCommonDir, "objects", string(tip)[:2], string(tip)[2:])); err != nil {
				t.Fatal(err)
			}
			_, before := queryIndexGeneration(t, q)
			ctx := &queryWorkContext{Context: t.Context()}
			incoming, heads, hit, err := s.IndexedSupersession(ctx, model.KindDoc, a)
			if err != nil || !hit || incoming != nil || !slices.Equal(heads, []model.EntityID{b}) {
				t.Fatalf("warm = %v %v %v %v", incoming, heads, hit, err)
			}
			if checks := ctx.checks.Load(); checks > 32 {
				t.Fatalf("warm query performed %d cancellation checkpoints across %d unrelated entities", checks, count)
			}
			if _, after := queryIndexGeneration(t, q); after != before {
				t.Fatalf("warm query wrote transaction %d after %d", after, before)
			}
		})
	}
}

func TestQueryGenerationRecentUpdatesAndPackRefs(t *testing.T) {
	s := initStore(t)
	a := create(t, s, docOps("a")).EntityID()
	b := create(t, s, docOps("b")).EntityID()
	q := settleSupersession(t, s, a)
	initial, _ := queryIndexGeneration(t, q)
	appendOps(t, s, refs.For(model.KindDoc, a), model.AddSupersededBy{ID: b})
	if _, heads, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || !slices.Equal(heads, []model.EntityID{b}) {
		t.Fatalf("recent update = %v %v %v", heads, hit, err)
	}
	if generation, _ := queryIndexGeneration(t, q); generation != "" {
		t.Fatalf("recent source published generation %q", generation)
	}
	settleSupersession(t, s, a)
	updated, _ := queryIndexGeneration(t, q)
	if updated == initial {
		t.Fatalf("updated generation equals initial %q", initial)
	}
	gittest.Git(t, s.Git.Dir, "pack-refs", "--all", "--prune")
	if _, heads, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || !slices.Equal(heads, []model.EntityID{b}) {
		t.Fatalf("packed = %v %v %v", heads, hit, err)
	}
	settleSupersession(t, s, a)
	if packed, _ := queryIndexGeneration(t, q); packed == updated {
		t.Fatalf("packed generation equals loose %q", updated)
	}
}

func TestQueryGenerationPromotionPreservesReadOnlyHit(t *testing.T) {
	s := initStore(t)
	a := create(t, s, docOps("a")).EntityID()
	b := create(t, s, docOps("b")).EntityID()
	appendOps(t, s, refs.For(model.KindDoc, a), model.AddSupersededBy{ID: b})
	if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit {
		t.Fatalf("prime = %v %v", hit, err)
	}
	q, err := queryMetadataIdentity(t.Context(), s, model.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	unqualifyQueryGeneration(t, q)
	if generation, _ := queryIndexGeneration(t, q); generation != "" {
		t.Fatalf("recent source unexpectedly qualified %q", generation)
	}
	time.Sleep(2100 * time.Millisecond)
	if err := os.Chmod(q.indexPath(), 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(q.indexPath(), 0o600) })
	if _, heads, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || !slices.Equal(heads, []model.EntityID{b}) {
		t.Fatalf("readonly promotion = %v %v %v", heads, hit, err)
	}
}

func TestQueryGenerationRechecksAfterRead(t *testing.T) {
	s := initStore(t)
	a := create(t, s, docOps("a")).EntityID()
	b := create(t, s, docOps("b")).EntityID()
	settleSupersession(t, s, a)
	calls := 0
	heads, hit, err := indexedMetadataQuery(t.Context(), s, model.KindDoc, func(tx *bolt.Tx, q queryMetadataSource) ([]model.EntityID, error) {
		calls++
		if calls == 1 {
			appendOps(t, s, refs.For(model.KindDoc, a), model.AddSupersededBy{ID: b})
		}
		return querySupersedeHeads(t.Context(), tx, q, a)
	})
	if err != nil || !hit || calls != 2 || !slices.Equal(heads, []model.EntityID{b}) {
		t.Fatalf("concurrent update = %v %v %v calls=%d", heads, hit, err, calls)
	}
}

func TestSettledQueryGenerationPreservesPinnedViews(t *testing.T) {
	s := initStore(t)
	a := create(t, s, docOps("a")).EntityID()
	b := create(t, s, docOps("b")).EntityID()
	q := settleSupersession(t, s, a)
	old, err := s.PinnedKinds(t.Context(), model.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	_, before := queryIndexGeneration(t, q)
	if _, heads, hit, err := old.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || heads != nil {
		t.Fatalf("matching pinned = %v %v %v", heads, hit, err)
	}
	if _, _, hit, err := s.Pinned(map[string]model.SHA{}).IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || hit {
		t.Fatalf("empty pinned = %v %v", hit, err)
	}
	if _, after := queryIndexGeneration(t, q); after != before {
		t.Fatalf("pinned queries wrote transaction %d after %d", after, before)
	}
	appendOps(t, s, refs.For(model.KindDoc, a), model.AddSupersededBy{ID: b})
	if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit {
		t.Fatalf("changed live = %v %v", hit, err)
	}
	if _, _, hit, err := old.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || hit {
		t.Fatalf("historical mismatch = %v %v", hit, err)
	}
}

func TestSettledQueryGenerationRechecksRecordsAuthority(t *testing.T) {
	f := newSharedFixture(t)
	f.bind(t)
	backend, err := Open(f.source)
	if err != nil {
		t.Fatal(err)
	}
	a := create(t, backend, docOps("a")).EntityID()
	s, err := Open(f.worktreeA)
	if err != nil {
		t.Fatal(err)
	}
	settleSupersession(t, s, a)
	value, hit, err := indexedMetadataQuery(t.Context(), s, model.KindDoc, func(_ *bolt.Tx, _ queryMetadataSource) ([]model.EntityID, error) {
		replaceRepo(t, f.source)
		return []model.EntityID{a}, nil
	})
	if value != nil || hit || err == nil {
		t.Fatalf("replaced records = %v %v %v", value, hit, err)
	}
}

func TestPartialQueryMetadataNeverPublishesGeneration(t *testing.T) {
	s := initStore(t)
	a := create(t, s, answerOps("a")).(model.Answer)
	tip, err := s.Repo.Tip(t.Context(), answerRef(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	sig, _, err := s.signature(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	partial, err := s.Repo.WriteOpsCommit(t.Context(), []model.SHA{tip}, sig, "partial", model.Pack{Lamport: 2, Ops: []model.Op{model.SetStatus{Status: model.StatusDone}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordsGit.UpdateRef(t.Context(), answerRef(a.ID), partial, tip); err != nil {
		t.Fatal(err)
	}
	time.Sleep(2100 * time.Millisecond)
	q, err := queryMetadataIdentity(t.Context(), s, model.KindAnswer)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		skipped, hit, err := readQueryMetadataIndex(t.Context(), q, func(tx *bolt.Tx) (int, error) {
			var row queryMetadata
			err := json.Unmarshal(readQueryMetadata(tx, q.binding, a.ID), &row)
			return row.SkippedOps, err
		})
		if err != nil || !hit || skipped != 1 {
			t.Fatalf("partial = %d %v %v", skipped, hit, err)
		}
		if generation, _ := queryIndexGeneration(t, q); generation != "" {
			t.Fatalf("partial source published generation %q", generation)
		}
	}
}

func TestQueryGenerationPromotionTransactionFailurePreservesReadOnlyHit(t *testing.T) {
	s := initStore(t)
	a := create(t, s, docOps("a")).EntityID()
	if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit {
		t.Fatalf("prime = %v %v", hit, err)
	}
	q, err := queryMetadataIdentity(t.Context(), s, model.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	unqualifyQueryGeneration(t, q)
	time.Sleep(2100 * time.Millisecond)
	calls := 0
	value, hit, err := readQueryMetadataIndex(t.Context(), q, func(tx *bolt.Tx) ([]model.EntityID, error) {
		calls++
		if tx.Writable() {
			return nil, errors.New("derived transaction failure")
		}
		return []model.EntityID{a}, nil
	})
	if err != nil || !hit || !slices.Equal(value, []model.EntityID{a}) || calls != 2 {
		t.Fatalf("failed promotion = %v %v %v calls=%d", value, hit, err, calls)
	}
	if generation, _ := queryIndexGeneration(t, q); generation != "" {
		t.Fatalf("failed promotion committed generation %q", generation)
	}
}
