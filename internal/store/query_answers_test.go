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

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestIndexedAnswersRetainsLabelAndLivenessSemantics(t *testing.T) {
	s := initStore(t)
	ids := make(map[string]model.EntityID)
	for _, title := range []string{"both", "first", "second", "deleted", "superseded"} {
		ops := append(answerOps(title), model.AddTag{Tag: "first"})
		if title == "both" || title == "second" {
			ops = append(answerOps(title), model.AddTag{Tag: "second"})
		}
		if title == "both" {
			ops = append(ops, model.AddTag{Tag: "first"})
		}
		ids[title] = create(t, s, ops).EntityID()
	}
	appendOps(t, s, refs.For(model.KindAnswer, ids["deleted"]), model.DeleteNote{})
	appendOps(t, s, refs.For(model.KindAnswer, ids["superseded"]), model.AddSupersededBy{ID: ids["both"]})
	for _, tc := range []struct {
		labels []string
		opts   ListOpts
		want   []model.EntityID
	}{
		{[]string{"first", "second"}, ListOpts{}, []model.EntityID{ids["both"]}},
		{[]string{"first", "first"}, ListOpts{}, []model.EntityID{ids["both"], ids["first"]}},
		{[]string{"first"}, ListOpts{IncludeDeleted: true}, []model.EntityID{ids["both"], ids["first"], ids["deleted"]}},
		{[]string{"first"}, ListOpts{IncludeSuperseded: true}, []model.EntityID{ids["both"], ids["first"], ids["superseded"]}},
		{[]string{"absent"}, ListOpts{}, []model.EntityID{}},
	} {
		got, hit, err := s.IndexedAnswers(t.Context(), tc.labels, tc.opts)
		if err != nil || !hit {
			t.Fatalf("query %v %v = hit %v, %v", tc.labels, tc.opts, hit, err)
		}
		var gotIDs []model.EntityID
		for _, answer := range got {
			gotIDs = append(gotIDs, answer.ID)
		}
		slices.Sort(gotIDs)
		slices.Sort(tc.want)
		if !slices.Equal(gotIDs, tc.want) || got == nil {
			t.Fatalf("query %v %v = %v; want %v", tc.labels, tc.opts, gotIDs, tc.want)
		}
	}
}

func TestIndexedAnswersWarmReadsAvoidUnrelatedCorpus(t *testing.T) {
	for _, count := range []int{8, 80} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := initStore(t)
			selected := create(t, s, append(answerOps("selected"), model.AddTag{Tag: "target"})).EntityID()
			var unrelated model.EntityID
			for range count {
				unrelated = create(t, s, answerOps("unrelated")).EntityID()
			}
			time.Sleep(2100 * time.Millisecond)
			if _, hit, err := s.IndexedAnswers(t.Context(), []string{"target"}, ListOpts{}); err != nil || !hit {
				t.Fatalf("warm = %v %v", hit, err)
			}
			q, err := captureQueryMetadata(t.Context(), s, model.KindAnswer)
			if err != nil {
				t.Fatal(err)
			}
			db, err := bolt.Open(q.indexPath(), 0o600, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Update(func(tx *bolt.Tx) error {
				if err := queryMetadataKindTips(tx, q.binding, q.kind).Put([]byte(unrelated), nil); err != nil {
					return err
				}
				return queryMetadataBinding(tx, q.binding).Bucket(queryMetadataRowsBucket).Put([]byte(unrelated), []byte("not JSON"))
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			for _, tip := range q.tips {
				if err := os.Remove(filepath.Join(s.recordsCommonDir, "objects", string(tip)[:2], string(tip)[2:])); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.RemoveAll(s.cache.dir); err != nil {
				t.Fatal(err)
			}
			fresh, err := Open(s.Git.Dir)
			if err != nil {
				t.Fatal(err)
			}
			_, before := queryIndexGeneration(t, q)
			ctx := &queryWorkContext{Context: t.Context()}
			got, hit, err := fresh.IndexedAnswers(ctx, []string{"target"}, ListOpts{})
			if err != nil || !hit || len(got) != 1 || got[0].ID != selected || got[0].Body != "Redis" {
				t.Fatalf("warm unreadable bodies = %v %v %v", got, hit, err)
			}
			if checks := ctx.checks.Load(); checks > 40 {
				t.Fatalf("warm query performed %d checkpoints over %d unrelated answers", checks, count)
			}
			if _, after := queryIndexGeneration(t, q); before != after {
				t.Fatalf("warm query wrote transaction %d after %d", after, before)
			}
			if got, hit, err := fresh.IndexedAnswers(t.Context(), []string{"target", "target"}, ListOpts{}); got != nil || hit || err == nil {
				t.Fatalf("uncached selected unreadable body = %v %v %v", got, hit, err)
			}
			if _, err := fresh.ListAnswers(t.Context(), false, false); err == nil {
				t.Fatal("authoritative corpus remained readable")
			}
		})
	}
}

func TestIndexedAnswersRefreshesOnlyChangedTips(t *testing.T) {
	s := initStore(t)
	selected := create(t, s, append(answerOps("selected"), model.AddTag{Tag: "target"})).EntityID()
	unrelated := create(t, s, answerOps("unrelated")).EntityID()
	if _, hit, err := s.IndexedAnswers(t.Context(), []string{"target"}, ListOpts{}); err != nil || !hit {
		t.Fatalf("warm = %v %v", hit, err)
	}
	tip, err := s.Repo.Tip(t.Context(), refs.For(model.KindAnswer, unrelated))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(s.recordsCommonDir, "objects", string(tip)[:2], string(tip)[2:])); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(s.cache.dir); err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(s.Git.Dir)
	if err != nil {
		t.Fatal(err)
	}
	added := create(t, fresh, append(answerOps("added"), model.AddTag{Tag: "target"})).EntityID()
	got, hit, err := fresh.IndexedAnswers(t.Context(), []string{"target"}, ListOpts{})
	if err != nil || !hit || len(got) != 2 {
		t.Fatalf("changed tip = %v %v %v", got, hit, err)
	}
	gotIDs := []model.EntityID{got[0].ID, got[1].ID}
	if !slices.Contains(gotIDs, selected) || !slices.Contains(gotIDs, added) {
		t.Fatalf("changed tip = %v; want %s and %s", gotIDs, selected, added)
	}
}

func TestIndexedAnswersCancellationAndUnavailableCache(t *testing.T) {
	s := initStore(t)
	create(t, s, append(answerOps("selected"), model.AddTag{Tag: "target"}))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, hit, err := s.IndexedAnswers(ctx, []string{"target"}, ListOpts{}); got != nil || hit || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled = %v %v %v", got, hit, err)
	}
	if err := os.WriteFile(filepath.Join(s.recordsCommonDir, "cc-notes-query"), []byte("unavailable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, hit, err := s.IndexedAnswers(t.Context(), []string{"target"}, ListOpts{}); got != nil || hit || err != nil {
		t.Fatalf("unavailable = %v %v %v", got, hit, err)
	}
}

func TestIndexedAnswersNeverCachesPartialMatches(t *testing.T) {
	s := initStore(t)
	a := create(t, s, append(answerOps("partial"), model.AddTag{Tag: "target"})).(model.Answer)
	ref := refs.For(model.KindAnswer, a.ID)
	tip, err := s.Repo.Tip(t.Context(), ref)
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
	if err := s.RecordsGit.UpdateRef(t.Context(), ref, partial, tip); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		got, hit, err := s.IndexedAnswers(t.Context(), []string{"target"}, ListOpts{})
		if err != nil || !hit || len(got) != 1 || got[0].ID != a.ID || got[0].SkippedOps != 1 {
			t.Fatalf("partial = %v %v %v", got, hit, err)
		}
	}
	entries, err := os.ReadDir(s.relevant.dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "list-") {
			t.Fatalf("partial answer was stored in query-result cache %s", entry.Name())
		}
	}
}
