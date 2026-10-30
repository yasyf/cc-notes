package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

func queryIndexedLabels(ctx context.Context, t *testing.T, source queryMetadataSource, label string) ([]model.EntityID, bool, error) {
	t.Helper()
	return readQueryMetadataIndex(ctx, source, func(tx *bolt.Tx) ([]model.EntityID, error) {
		ids := []model.EntityID{}
		err := walkQueryMetadataLabel(tx, source.binding, source.kind, label, false, func(row queryMetadata) error {
			ids = append(ids, row.ID)
			return nil
		})
		slices.Sort(ids)
		return ids, err
	})
}

func captureAnswerMetadata(t *testing.T, s *Store) queryMetadataSource {
	t.Helper()
	source, err := captureQueryMetadata(t.Context(), s, model.KindAnswer)
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func TestQueryMetadataIndexRefreshAndPinnedViews(t *testing.T) {
	s := initStore(t)
	a := create(t, s, answerOps("a")).(model.Answer)
	old := captureAnswerMetadata(t, s)
	oldPins := s.Pinned(old.store.pins)
	if ids, hit, err := queryIndexedLabels(t.Context(), t, old, "selected"); err != nil || !hit || len(ids) != 0 {
		t.Fatalf("initial = %v %v %v", ids, hit, err)
	}
	exact := captureAnswerMetadata(t, oldPins)
	if _, hit, err := queryIndexedLabels(t.Context(), t, exact, "selected"); err != nil || !hit {
		t.Fatalf("exact pinned = %v %v", hit, err)
	}
	appendOps(t, s, answerRef(a.ID), model.AddTag{Tag: "selected"})
	current := captureAnswerMetadata(t, s)
	if ids, hit, err := queryIndexedLabels(t.Context(), t, current, "selected"); err != nil || !hit || !reflect.DeepEqual(ids, []model.EntityID{a.ID}) {
		t.Fatalf("updated = %v %v %v", ids, hit, err)
	}
	before, err := os.ReadFile(current.indexPath())
	if err != nil {
		t.Fatal(err)
	}
	if _, hit, err := queryIndexedLabels(t.Context(), t, exact, "selected"); err != nil || hit {
		t.Fatalf("old pinned = %v %v", hit, err)
	}
	if _, hit, err := queryIndexedLabels(t.Context(), t, captureAnswerMetadata(t, s.Pinned(map[string]model.SHA{})), "selected"); err != nil || hit {
		t.Fatalf("empty historical = %v %v", hit, err)
	}
	after, err := os.ReadFile(current.indexPath())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(before, after) {
		t.Fatal("historical pinned views mutated live database")
	}
}

func TestQueryMetadataIndexEmptyLiveAndMissingPinned(t *testing.T) {
	s := initStore(t)
	pinned := captureAnswerMetadata(t, s.Pinned(map[string]model.SHA{}))
	if _, hit, err := queryIndexedLabels(t.Context(), t, pinned, "none"); err != nil || hit {
		t.Fatalf("missing pinned = %v %v", hit, err)
	}
	if _, err := os.Stat(pinned.indexPath()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pinned created cache: %v", err)
	}
	live := captureAnswerMetadata(t, s)
	if _, hit, err := queryIndexedLabels(t.Context(), t, live, "none"); err != nil || !hit {
		t.Fatalf("empty live = %v %v", hit, err)
	}
	if _, hit, err := queryIndexedLabels(t.Context(), t, pinned, "none"); err != nil || !hit {
		t.Fatalf("exact empty pinned = %v %v", hit, err)
	}
}

func TestQueryMetadataIndexCacheFailuresAreMisses(t *testing.T) {
	for _, kind := range []string{"unavailable", "corrupt", "incomplete"} {
		t.Run(kind, func(t *testing.T) {
			s := initStore(t)
			create(t, s, answerOps("a"))
			source := captureAnswerMetadata(t, s)
			dir := filepath.Dir(source.indexPath())
			if kind == "unavailable" {
				if err := os.WriteFile(dir, []byte("not a directory"), 0o600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.MkdirAll(dir, 0o750); err != nil {
					t.Fatal(err)
				}
				if kind == "corrupt" {
					if err := os.WriteFile(source.indexPath(), []byte("not a database"), 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					db, err := bolt.Open(source.indexPath(), 0o600, nil)
					if err != nil {
						t.Fatal(err)
					}
					if err := db.Update(func(tx *bolt.Tx) error {
						root, err := tx.CreateBucket(queryMetadataRootBucket)
						if err != nil {
							return err
						}
						_, err = root.CreateBucket(queryMetadataAtom(source.binding))
						return err
					}); err != nil {
						t.Fatal(err)
					}
					if err := db.Close(); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, hit, err := queryIndexedLabels(t.Context(), t, source, "none"); err != nil || hit {
				t.Fatalf("failure %s = %v %v", kind, hit, err)
			}
		})
	}
}

func TestQueryMetadataIndexCancellationRollsBack(t *testing.T) {
	s := initStore(t)
	a := create(t, s, answerOps("a")).(model.Answer)
	source := captureAnswerMetadata(t, s)
	if _, hit, err := queryIndexedLabels(t.Context(), t, source, "none"); err != nil || !hit {
		t.Fatalf("initial = %v %v", hit, err)
	}
	appendOps(t, s, answerRef(a.ID), model.AddTag{Tag: "new"})
	current := captureAnswerMetadata(t, s)
	ctx, cancel := context.WithCancel(t.Context())
	_, hit, err := readQueryMetadataIndex(ctx, current, func(_ *bolt.Tx) (bool, error) { cancel(); return true, nil })
	if hit || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v %v", hit, err)
	}
	oldPinned := captureAnswerMetadata(t, s.Pinned(source.store.pins))
	if _, hit, err := queryIndexedLabels(t.Context(), t, oldPinned, "none"); err != nil || !hit {
		t.Fatalf("rollback original = %v %v", hit, err)
	}
	missing := model.SHA("1111111111111111111111111111111111111111")
	current.tips[a.ID] = missing
	if _, hit, err := queryIndexedLabels(t.Context(), t, current, "none"); err == nil || hit {
		t.Fatalf("authority error = %v %v", hit, err)
	}
}

func TestQueryMetadataIndexConcurrentCapturedWriters(t *testing.T) {
	s := initStore(t)
	a := create(t, s, answerOps("a")).(model.Answer)
	before := captureAnswerMetadata(t, s)
	appendOps(t, s, answerRef(a.ID), model.AddTag{Tag: "new"})
	after := captureAnswerMetadata(t, s)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for _, source := range []queryMetadataSource{before, after} {
		wg.Go(func() {
			<-start
			ids, hit, err := queryIndexedLabels(t.Context(), t, source, "new")
			want := []model.EntityID{}
			if source.tips[a.ID] == after.tips[a.ID] {
				want = append(want, a.ID)
			}
			if err != nil || !hit || !reflect.DeepEqual(ids, want) {
				t.Errorf("captured concurrent = %v %v %v, want %v", ids, hit, err, want)
			}
		})
	}
	close(start)
	wg.Wait()
	if ids, hit, err := queryIndexedLabels(t.Context(), t, after, "new"); err != nil || !hit || !reflect.DeepEqual(ids, []model.EntityID{a.ID}) {
		t.Fatalf("settled = %v %v %v", ids, hit, err)
	}
}

func TestQueryMetadataIndexBindingChangeAfterReaderRollsBack(t *testing.T) {
	f := newSharedFixture(t)
	f.bind(t)
	backend, err := Open(f.source)
	if err != nil {
		t.Fatal(err)
	}
	create(t, backend, answerOps("a"))
	bound, err := Open(f.worktreeA)
	if err != nil {
		t.Fatal(err)
	}
	source := captureAnswerMetadata(t, bound)
	_, hit, err := readQueryMetadataIndex(t.Context(), source, func(_ *bolt.Tx) (bool, error) { replaceRepo(t, f.source); return true, nil })
	if hit || err == nil {
		t.Fatalf("binding replacement = %v %v", hit, err)
	}
}

func TestQueryMetadataIndexReadOnlyUnchangedLiveCacheIsHit(t *testing.T) {
	s := initStore(t)
	create(t, s, answerOps("a"))
	source := captureAnswerMetadata(t, s)
	if _, hit, err := queryIndexedLabels(t.Context(), t, source, "none"); err != nil || !hit {
		t.Fatalf("initial = %v %v", hit, err)
	}
	if err := os.Chmod(source.indexPath(), 0o444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(source.indexPath(), 0o600) })
	if _, hit, err := queryIndexedLabels(t.Context(), t, source, "none"); err != nil || !hit {
		t.Fatalf("readonly unchanged live = %v %v", hit, err)
	}
	pinned := captureAnswerMetadata(t, s.Pinned(source.store.pins))
	if _, hit, err := queryIndexedLabels(t.Context(), t, pinned, "none"); err != nil || !hit {
		t.Fatalf("readonly exact pinned = %v %v", hit, err)
	}
	create(t, s, answerOps("new"))
	changed := captureAnswerMetadata(t, s)
	if _, hit, err := queryIndexedLabels(t.Context(), t, changed, "none"); err != nil || hit {
		t.Fatalf("readonly changed live = %v %v", hit, err)
	}
}

func TestQueryMetadataIndexPartialRefoldsAndPinnedMisses(t *testing.T) {
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
	source := captureAnswerMetadata(t, s)
	for range 2 {
		skipped, hit, err := readQueryMetadataIndex(t.Context(), source, func(tx *bolt.Tx) (int, error) {
			var row queryMetadata
			err := json.Unmarshal(readQueryMetadata(tx, source.binding, a.ID), &row)
			return row.SkippedOps, err
		})
		if err != nil || !hit || skipped != 1 {
			t.Fatalf("partial = %d %v %v", skipped, hit, err)
		}
	}
	pinned := captureAnswerMetadata(t, s.Pinned(source.store.pins))
	if _, hit, err := queryIndexedLabels(t.Context(), t, pinned, "none"); err != nil || hit {
		t.Fatalf("partial pinned = %v %v", hit, err)
	}
}

func TestQueryMetadataIndexUnchangedRowsRemainUnread(t *testing.T) {
	for _, count := range []int{8, 80} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := initStore(t)
			ids := make([]model.EntityID, 0, count)
			for i := range count {
				ids = append(ids, create(t, s, answerOps(fmt.Sprintf("a %d", i))).EntityID())
			}
			source := captureAnswerMetadata(t, s)
			if _, hit, err := queryIndexedLabels(t.Context(), t, source, "absent"); err != nil || !hit {
				t.Fatalf("initial = %v %v", hit, err)
			}
			db, err := bolt.Open(source.indexPath(), 0o600, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Update(func(tx *bolt.Tx) error {
				rows := queryMetadataBinding(tx, source.binding).Bucket(queryMetadataRowsBucket)
				for _, id := range ids {
					if err := rows.Put([]byte(id), []byte("not JSON")); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if got, hit, err := queryIndexedLabels(t.Context(), t, source, "absent"); err != nil || !hit || len(got) != 0 {
				t.Fatalf("unchanged = %v %v %v", got, hit, err)
			}
		})
	}
}

func TestQueryMetadataIndexContendedCacheDoesNotRetryWritable(t *testing.T) {
	s := initStore(t)
	create(t, s, answerOps("a"))
	source := captureAnswerMetadata(t, s)
	if _, hit, err := queryIndexedLabels(t.Context(), t, source, "none"); err != nil || !hit {
		t.Fatalf("initial = %v %v", hit, err)
	}
	owner, err := bolt.Open(source.indexPath(), 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := owner.Close(); err != nil {
			t.Error(err)
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	started := time.Now()
	_, hit, err := queryIndexedLabels(ctx, t, source, "none")
	elapsed := time.Since(started)
	if hit || (err != nil && !errors.Is(err, context.DeadlineExceeded)) {
		t.Fatalf("contended cache = %v %v", hit, err)
	}
	if elapsed > 1800*time.Millisecond {
		t.Fatalf("contended cache took %s for one-second context; reopened after lock deadline", elapsed)
	}
}
