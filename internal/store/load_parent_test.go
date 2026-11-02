package store

import (
	"bytes"
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func loadParentCommit(t *testing.T, s *Store, parents []model.SHA, lamport model.Lamport, ops ...model.Op) model.SHA {
	t.Helper()
	head, err := s.Repo.WriteOpsCommit(t.Context(), parents, gitobj.Signature{Name: testName, Email: testEmail, When: time.Unix(1_700_000_005, 0)}, "load parent fixture", model.Pack{Lamport: lamport, Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	return head
}

func loadParentFold(t *testing.T, s *Store, head model.SHA) (model.Snapshot, []model.PackCommit, error) {
	t.Helper()
	chain, err := s.Repo.ReadChain(t.Context(), head)
	if err != nil {
		return nil, nil, err
	}
	snapshot, err := fold.Fold(chain)
	return snapshot, chain, err
}

func TestLoadCachedParentMatchesFullFold(t *testing.T) {
	for _, script := range appendScripts() {
		t.Run(script.name, func(t *testing.T) {
			s := initStore(t)
			s.now = steppedClock()
			base := create(t, s, script.create)
			ref := refs.For(base.Meta().Kind, base.EntityID())
			for _, ops := range script.appends {
				parent := base.Meta().Head
				before, top, floor, hit := s.cache.getTop(parent)
				if !hit {
					t.Fatal("parent snapshot was not cached")
				}
				prepared, err := s.PrepareAppendAt(t.Context(), ref, parent, ops)
				if err != nil {
					t.Fatal(err)
				}
				if err := s.PublishRef(t.Context(), ref, prepared.New, parent); err != nil {
					t.Fatal(err)
				}
				if _, hit := s.cache.get(prepared.New); hit {
					t.Fatal("preparation or publication cached the unread child")
				}
				if _, hit := s.cache.get(parent); !hit {
					t.Fatal("preparation or publication evicted the cached parent")
				}
				want, chain, err := loadParentFold(t, s, prepared.New)
				if err != nil {
					t.Fatal(err)
				}
				for _, mode := range []string{"ref", "head"} {
					s.cache.delete(prepared.New)
					s.cache.put(parent, before, top, floor)
					var got model.Snapshot
					if mode == "ref" {
						got, err = s.Load(t.Context(), ref)
					} else {
						got, err = s.LoadAt(t.Context(), prepared.New)
					}
					if err != nil || !reflect.DeepEqual(got, want) {
						t.Fatalf("%s snapshot = %#v, %v; full fold = %#v", mode, got, err, want)
					}
					if _, cachedTop, cachedFloor, ok := s.cache.getTop(prepared.New); !ok || cachedTop != topLamport(chain) || cachedFloor != fold.ExtendFloor(chain) {
						t.Fatalf("%s cache top/floor = %d/%d hit=%v", mode, cachedTop, cachedFloor, ok)
					}
					if _, ok := s.cache.get(parent); ok {
						t.Fatalf("%s load left the superseded parent cached", mode)
					}
					cached, _ := s.cache.get(prepared.New)
					if ledger, ok := got.(model.Ledger); ok && len(ledger.Rows) != 0 {
						ledger.Rows[0].Fields["state"] = "mutated result"
					}
					if after, ok := s.cache.get(prepared.New); !ok || !reflect.DeepEqual(cached, after) {
						t.Fatal("mutating the loaded child changed its cached snapshot")
					}
				}
				base = want
			}
		})
	}
}

func TestLoadCachedParentRefusalsMatchFullFold(t *testing.T) {
	for _, name := range []string{"below-top", "wrapped-zero", "floor-below", "floor-equal", "floor-above", "checkpoint", "merge", "wrong-kind", "duplicate-create", "parent-head", "absent", "corrupt", "generation", "foreign-checkpoint"} {
		t.Run(name, func(t *testing.T) {
			s := initStore(t)
			root := create(t, s, noteOps("root")).(model.Note)
			parent := root.Head
			lamport := model.Lamport(2)
			ops := []model.Op{model.SetTitle{Title: "child"}}
			switch name {
			case "below-top", "wrapped-zero":
				peak := model.Lamport(9)
				lamport = 3
				if name == "wrapped-zero" {
					peak, lamport = math.MaxUint64, 0
				}
				parent = loadParentCommit(t, s, []model.SHA{root.Head}, peak, model.SetTitle{Title: "parent"})
			case "floor-below", "floor-equal", "floor-above":
				forged := root
				forged.Title = "checkpoint"
				parent = loadParentCommit(t, s, []model.SHA{root.Head}, 2, model.Checkpoint{EntityID: root.ID, State: forged, CoversLamport: 10, CoversShas: []model.SHA{root.Head}})
				lamport = map[string]model.Lamport{"floor-below": 3, "floor-equal": 10, "floor-above": 11}[name]
				ops = []model.Op{model.SetBody{Body: "child"}}
			case "checkpoint":
				ops = []model.Op{model.Checkpoint{EntityID: root.ID, State: root, CoversLamport: 1, CoversShas: []model.SHA{root.Head}}}
			case "wrong-kind":
				ops = []model.Op{model.SetStatus{Status: model.StatusDone}}
			case "duplicate-create":
				ops = noteOps("duplicate")
			case "foreign-checkpoint":
				decoy := create(t, s, taskOps("decoy", "main")).(model.Task)
				decoy.ID = root.ID
				parent = loadParentCommit(t, s, []model.SHA{root.Head}, 2, model.Checkpoint{EntityID: root.ID, State: decoy, CoversLamport: 1, CoversShas: []model.SHA{root.Head}})
				chain, err := s.Repo.ReadChain(t.Context(), parent)
				if err != nil {
					t.Fatal(err)
				}
				foreign, err := fold.Task(chain)
				if err != nil {
					t.Fatal(err)
				}
				s.cache.putChain(parent, foreign, chain)
			}
			if name != "foreign-checkpoint" {
				if _, err := s.LoadAt(t.Context(), parent); err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "parent-head":
				wrong := root
				wrong.Head = model.SHA(strings.Repeat("1", 40))
				s.cache.put(parent, wrong, 1, 0)
			case "absent":
				s.cache.delete(parent)
			case "corrupt":
				s.cache.write(string(parent), []byte("broken"))
			case "generation":
				data, ok := encodeFoldEntry(root, 1, 0)
				if !ok {
					t.Fatal("encode parent")
				}
				s.cache.write(string(parent), bytes.Replace(data, []byte(foldCacheGeneration), []byte(strings.Repeat("x", len(foldCacheGeneration))), 1))
			}
			parents := []model.SHA{parent}
			if name == "merge" {
				other := loadParentCommit(t, s, []model.SHA{root.Head}, 2, model.AddTag{Tag: "other"})
				parents, lamport = []model.SHA{parent, other}, 3
			}
			head := loadParentCommit(t, s, parents, lamport, ops...)
			want, chain, wantErr := loadParentFold(t, s, head)
			got, err := s.LoadAt(t.Context(), head)
			if wantErr != nil {
				if err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
					t.Fatalf("LoadAt error = %v; full fold = %v", err, wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("LoadAt = %#v, %v; full fold = %#v", got, err, want)
			}
			_, top, floor, hit := s.cache.getTop(head)
			if want.Meta().SkippedOps > 0 {
				if hit {
					t.Fatal("partial fold was cached")
				}
			} else if !hit || top != topLamport(chain) || floor != fold.ExtendFloor(chain) {
				t.Fatalf("cached top/floor = %d/%d hit=%v; want %d/%d", top, floor, hit, topLamport(chain), fold.ExtendFloor(chain))
			}
		})
	}
}

func TestLoadCachedParentUnknownAndCancellation(t *testing.T) {
	s := initStore(t)
	root := create(t, s, noteOps("root")).(model.Note)
	path := filepath.Join(s.Git.Dir, "ops.json")
	if err := os.WriteFile(path, []byte(`{"v":1,"lamport":2,"ops":[{"kind":"future_op"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, s.Git.Dir, "add", "ops.json")
	tree := gittest.Git(t, s.Git.Dir, "write-tree")
	head := model.SHA(gittest.Git(t, s.Git.Dir, "commit-tree", tree, "-p", string(root.Head), "-m", "unknown op"))
	if _, err := s.LoadAt(t.Context(), head); !errors.Is(err, model.ErrUnknownKind) {
		t.Fatalf("unknown operation = %v", err)
	}
	valid := loadParentCommit(t, s, []model.SHA{root.Head}, 2, model.AddTag{Tag: "valid"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.LoadAt(ctx, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled load = %v", err)
	}
	if _, hit := s.cache.get(valid); hit {
		t.Fatal("cancelled load published a cache entry")
	}
	if _, err := s.LoadAt(t.Context(), valid); err != nil {
		t.Fatal(err)
	}
}

func TestLoadCachedParentDefersQueryPublication(t *testing.T) {
	s := initStore(t)
	a := create(t, s, answerOps("answer")).(model.Answer)
	head := loadParentCommit(t, s, []model.SHA{a.Head}, 2, model.AddTag{Tag: "new"})
	for _, kind := range []model.Kind{model.KindNote, model.KindAnswer} {
		source, err := captureQueryMetadata(t.Context(), s.Pinned(map[string]model.SHA{refs.For(kind, a.ID): head}), kind)
		if err != nil {
			t.Fatal(err)
		}
		_, publication, err := source.load(t.Context(), a.ID, head)
		if kind == model.KindNote {
			if err == nil || publication != nil {
				t.Fatalf("wrong kind = %v, publication=%v", err, publication)
			}
		} else if err != nil || publication == nil {
			t.Fatalf("valid query = %v, publication=%v", err, publication)
		}
		if _, hit := s.cache.get(head); hit {
			t.Fatal("query load published before its caller accepted the row")
		}
	}
}
