package store

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/model"
)

func TestTaskDedupeWarmReadsAvoidUnrelatedCorpus(t *testing.T) {
	for _, count := range []int{32, 320} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := initStore(t)
			ops := func(title, label string) []model.Op {
				return []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: title, Type: model.TypeTask, Priority: 2, Labels: []string{label}}}
			}
			selected := create(t, s, ops("selected", "target")).EntityID()
			for i := range count {
				if _, err := s.CreateExact(t.Context(), ops(fmt.Sprintf("unrelated %d", i), "other")); err != nil {
					t.Fatal(err)
				}
			}
			time.Sleep(2100 * time.Millisecond)
			if _, hit, err := s.IndexedTasks(t.Context(), []string{"target"}); err != nil || !hit {
				t.Fatalf("warm = %v %v", hit, err)
			}
			fresh, err := Open(s.Git.Dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := fresh.Close(); err != nil {
					t.Error(err)
				}
			})
			ctx := &queryWorkContext{Context: t.Context()}
			_, err = fresh.Create(ctx, ops("selected", "target"))
			var dup *DuplicateError
			if !errors.As(err, &dup) || dup.Existing.EntityID() != selected {
				t.Fatalf("duplicate = %v; want %s", err, selected)
			}
			if checks := ctx.checks.Load(); checks > 40 {
				t.Fatalf("warm create performed %d checkpoints over %d unrelated tasks", checks, count)
			}
		})
	}
}

func TestTaskDedupeIndexedSurvivorUsesCreationOrder(t *testing.T) {
	s := initStore(t)
	ops := func() []model.Op {
		return []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: "same", Type: model.TypeTask, Priority: 2, Labels: []string{"target"}}}
	}
	for range 4 {
		if _, err := s.CreateExact(t.Context(), ops()); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.ListTasks(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, hit, err := s.IndexedTasks(t.Context(), []string{"target"}); err != nil || !hit {
		t.Fatalf("warm = %v %v", hit, err)
	}
	if got := mustDedupe(t, s, ops()); got.EntityID() != all[0].ID {
		t.Fatalf("survivor = %s; want %s", got.EntityID(), all[0].ID)
	}
}
