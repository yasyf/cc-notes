package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

type firstReads struct {
	answers []model.Answer
	plan    model.Snapshot
	tasks   []model.Task
}

func readFirst(ctx context.Context, t *testing.T, s *Store, plan model.EntityID) firstReads {
	t.Helper()
	answers, err := s.ListAnswers(ctx, false, false)
	if err != nil {
		t.Fatalf("ListAnswers: %v", err)
	}
	ref, err := s.Resolve(ctx, model.KindPlan, string(plan)[:8])
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	loaded, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	return firstReads{answers: answers, plan: loaded, tasks: tasks}
}

func TestReadsWhileImportingDoTheSameWorkAtOneAndThirtyNineLedgers(t *testing.T) {
	type work struct{ refEntries, packDecodes int64 }
	measured := map[int]work{}
	for _, ledgers := range []int{1, 39} {
		s := initStore(t)
		for i := range ledgers {
			ledger := create(t, s, ledgerOps(fmt.Sprintf("ledger %d", i))).(model.Ledger)
			for row := range 3 {
				appendOps(t, s, refs.For(model.KindLedger, ledger.ID), model.UpsertRow{Key: fmt.Sprintf("r%d", row), Fields: map[string]string{"state": "open"}, Position: fmt.Sprintf("p%d", row+1)})
			}
		}
		for i := range 3 {
			create(t, s, answerOps(fmt.Sprintf("answer %d", i)))
		}
		plan := create(t, s, planOps("rollout")).EntityID()
		for i := range 2 {
			task := create(t, s, taskOps(fmt.Sprintf("task %d", i), "main")).EntityID()
			appendOps(t, s, refs.For(model.KindTask, task), model.SetPlan{Plan: plan})
		}
		want := readFirst(t.Context(), t, s, plan)
		if err := os.Remove(managed.Path(s.recordsCommonDir)); err != nil {
			t.Fatal(err)
		}
		fresh, err := Open(s.Git.Dir)
		if err != nil {
			t.Fatalf("Open: %v", err)
		}
		t.Cleanup(func() { _ = fresh.Close() })
		fresh.cache = newFoldCache(t.TempDir(), foldCacheCap)
		fresh.ReadWhileImporting(func(context.Context) error {
			t.Error("a read waited for the import")
			return errors.New("waited for the import")
		})
		ctx, counter := gitobj.WithReadWork(t.Context())
		got := readFirst(ctx, t, fresh, plan)
		_, entries, decodes := counter.Snapshot()
		measured[ledgers] = work{refEntries: entries, packDecodes: decodes}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("reads with %d ledgers and no authority =\n%+v\nwant the authority's\n%+v", ledgers, got, want)
		}
		if fresh.AuthorityExists() {
			t.Fatalf("reads with %d ledgers imported the authority", ledgers)
		}
	}
	if measured[1] != measured[39] {
		t.Fatalf("reads with no authority listed and decoded %+v at 1 ledger and %+v at 39, want the same", measured[1], measured[39])
	}
}
