package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync/atomic"
	"testing"

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
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		generation = strconv.FormatUint(root.Sequence(), 10)
		id = tx.ID()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return generation, id
}

func settleSupersession(t *testing.T, s *Store, id model.EntityID) queryMetadataSource {
	t.Helper()
	if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, id); err != nil || !hit {
		t.Fatalf("settled = %v %v", hit, err)
	}
	q, err := queryMetadataIdentity(t.Context(), s, model.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	if generation, _ := queryIndexGeneration(t, q); generation == "" {
		t.Fatal("managed source has no logical generation")
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
			for i := range count {
				unrelated = create(t, s, docOps(fmt.Sprintf("unrelated %d", i))).EntityID()
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
	updated, transaction := queryIndexGeneration(t, q)
	if updated == initial {
		t.Fatalf("managed update did not change logical generation %q", initial)
	}
	gittest.Git(t, s.Git.Dir, "pack-refs", "--all", "--prune")
	if _, heads, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || !slices.Equal(heads, []model.EntityID{b}) {
		t.Fatalf("packed = %v %v %v", heads, hit, err)
	}
	if generation, after := queryIndexGeneration(t, q); generation != updated || after != transaction {
		t.Fatalf("packing changed managed authority from %s/%d to %s/%d", updated, transaction, generation, after)
	}
}

func TestQueryGenerationPreservesReadOnlyHit(t *testing.T) {
	s := initStore(t)
	a := create(t, s, docOps("a")).EntityID()
	b := create(t, s, docOps("b")).EntityID()
	appendOps(t, s, refs.For(model.KindDoc, a), model.AddSupersededBy{ID: b})
	q := settleSupersession(t, s, a)
	generation, transaction := queryIndexGeneration(t, q)
	if err := os.Chmod(q.indexPath(), 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(q.indexPath(), 0o600) })
	if _, heads, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || !slices.Equal(heads, []model.EntityID{b}) {
		t.Fatalf("readonly managed query = %v %v %v", heads, hit, err)
	}
	if afterGeneration, afterTransaction := queryIndexGeneration(t, q); afterGeneration != generation || afterTransaction != transaction {
		t.Fatalf("readonly query changed authority from %s/%d to %s/%d", generation, transaction, afterGeneration, afterTransaction)
	}
}

func TestQueryGenerationRetainsOneViewWhileWriterWaits(t *testing.T) {
	s := initStore(t)
	a := create(t, s, docOps("a")).EntityID()
	b := create(t, s, docOps("b")).EntityID()
	settleSupersession(t, s, a)
	entered, ready := make(chan struct{}), make(chan struct{})
	written := make(chan error, 1)
	go func() {
		select {
		case <-entered:
		case <-t.Context().Done():
			written <- t.Context().Err()
			return
		}
		close(ready)
		_, err := s.Append(t.Context(), refs.For(model.KindDoc, a), []model.Op{model.AddSupersededBy{ID: b}})
		written <- err
	}()
	calls := 0
	heads, hit, err := indexedMetadataQuery(t.Context(), s, model.KindDoc, func(tx *bolt.Tx, q queryMetadataSource) ([]model.EntityID, error) {
		calls++
		close(entered)
		<-ready
		return querySupersedeHeads(t.Context(), tx, q, a)
	})
	if err != nil || !hit || calls != 1 || heads != nil {
		t.Fatalf("captured read = %v %v %v calls=%d", heads, hit, err, calls)
	}
	if err := <-written; err != nil {
		t.Fatal(err)
	}
	if _, heads, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || !slices.Equal(heads, []model.EntityID{b}) {
		t.Fatalf("following read = %v %v %v", heads, hit, err)
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
	t.Cleanup(func() { _ = backend.Close() })
	a := create(t, backend, docOps("a")).EntityID()
	s, err := Open(f.worktreeA)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	settleSupersession(t, s, a)
	value, hit, err := indexedMetadataQuery(t.Context(), s, model.KindDoc, func(_ *bolt.Tx, _ queryMetadataSource) ([]model.EntityID, error) {
		replaceRepo(t, f.source)
		return []model.EntityID{a}, nil
	})
	if value != nil || hit || err == nil {
		t.Fatalf("replaced records = %v %v %v", value, hit, err)
	}
}

func TestPartialQueryMetadataPreservesManagedGeneration(t *testing.T) {
	s := initStore(t)
	a := create(t, s, answerOps("a")).(model.Answer)
	sig, _, err := s.signature(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	partial, err := s.Repo.WriteOpsCommit(t.Context(), []model.SHA{a.Head}, sig, "partial", model.Pack{Lamport: 2, Ops: []model.Op{model.SetStatus{Status: model.StatusDone}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRef(t.Context(), answerRef(a.ID), partial, a.Head); err != nil {
		t.Fatal(err)
	}
	q, err := queryMetadataIdentity(t.Context(), s, model.KindAnswer)
	if err != nil {
		t.Fatal(err)
	}
	generation, transaction := queryIndexGeneration(t, q)
	for range 2 {
		skipped, hit, err := readQueryMetadataIndex(t.Context(), q, func(tx *bolt.Tx) (int, error) {
			var row queryMetadata
			err := json.Unmarshal(readQueryMetadata(tx, q.binding, a.ID), &row)
			return row.SkippedOps, err
		})
		if err != nil || !hit || skipped != 1 {
			t.Fatalf("partial = %d %v %v", skipped, hit, err)
		}
		if afterGeneration, after := queryIndexGeneration(t, q); afterGeneration != generation || after != transaction {
			t.Fatalf("partial query mutated managed authority from %s/%d to %s/%d", generation, transaction, afterGeneration, after)
		}
	}
}

func TestQueryGenerationReadFailurePreservesAuthority(t *testing.T) {
	s := initStore(t)
	a := create(t, s, docOps("a")).EntityID()
	q := settleSupersession(t, s, a)
	generation, transaction := queryIndexGeneration(t, q)
	failed := errors.New("requested read failure")
	value, hit, err := readQueryMetadataIndex(t.Context(), q, func(tx *bolt.Tx) ([]model.EntityID, error) {
		if tx.Writable() {
			t.Error("ordinary managed query opened a writable transaction")
		}
		return []model.EntityID{a}, failed
	})
	if !errors.Is(err, failed) || hit || value != nil {
		t.Fatalf("failed read = %v %v %v", value, hit, err)
	}
	if afterGeneration, after := queryIndexGeneration(t, q); afterGeneration != generation || after != transaction {
		t.Fatalf("failed read mutated authority from %s/%d to %s/%d", generation, transaction, afterGeneration, after)
	}
	if _, heads, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || heads != nil {
		t.Fatalf("read after failure = %v %v %v", heads, hit, err)
	}
}
