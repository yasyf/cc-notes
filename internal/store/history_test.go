package store

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// TestHistory builds a real note chain via Create then two Appends and checks
// the folded trail: one step per commit, oldest first, with a strictly
// increasing lamport and the expected field progression.
func TestHistory(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	id := create(t, s, noteOps("v1")).EntityID()
	ref := refs.For(model.KindNote, id)
	if _, err := s.Append(ctx, ref, []model.Op{model.SetTitle{Title: "v2"}}); err != nil {
		t.Fatalf("Append set_title: %v", err)
	}
	if _, err := s.Append(ctx, ref, []model.Op{model.SetBody{Body: "body"}}); err != nil {
		t.Fatalf("Append set_body: %v", err)
	}

	steps, err := s.History(ctx, ref)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	want := []struct{ title, body string }{
		{"v1", ""},
		{"v2", ""},
		{"v2", "body"},
	}
	if len(steps) != len(want) {
		t.Fatalf("len(steps) = %d, want %d", len(steps), len(want))
	}
	for i, w := range want {
		n, ok := steps[i].Snapshot.(model.Note)
		if !ok {
			t.Fatalf("step %d snapshot = %T, want model.Note", i, steps[i].Snapshot)
		}
		if n.Title != w.title {
			t.Errorf("step %d title = %q, want %q", i, n.Title, w.title)
		}
		if n.Body != w.body {
			t.Errorf("step %d body = %q, want %q", i, n.Body, w.body)
		}
		if steps[i].Commit.Author == "" {
			t.Errorf("step %d author empty", i)
		}
		if i > 0 && steps[i].Commit.Pack.Lamport <= steps[i-1].Commit.Pack.Lamport {
			t.Errorf("step %d lamport %d not greater than prior %d", i, steps[i].Commit.Pack.Lamport, steps[i-1].Commit.Pack.Lamport)
		}
	}
	if steps[0].Snapshot.EntityID() != id {
		t.Errorf("step 0 entity id = %q, want %q", steps[0].Snapshot.EntityID(), id)
	}

	missing := refs.For(model.KindNote, "0000000000000000000000000000000000000000")
	if _, err := s.History(ctx, missing); !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Fatalf("History(missing) error = %v, want ErrRefNotFound", err)
	}
}

func TestWalkMatchesHistoryAndStopsOnCancel(t *testing.T) {
	s := initStore(t)
	id := create(t, s, noteOps("v1")).EntityID()
	ref := refs.For(model.KindNote, id)
	for _, op := range []model.Op{model.SetTitle{Title: "v2"}, model.SetBody{Body: "body"}} {
		if _, err := s.Append(t.Context(), ref, []model.Op{op}); err != nil {
			t.Fatalf("Append %s: %v", op.OpKind(), err)
		}
	}
	steps, err := s.History(t.Context(), ref)
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	n := 0
	if err := s.Walk(t.Context(), ref, func(st fold.Step) error {
		if st.Commit.SHA != steps[n].Commit.SHA || !reflect.DeepEqual(st.Snapshot, steps[n].Snapshot) {
			t.Fatalf("step %d = %s %+v, want %s %+v", n, st.Commit.SHA, st.Snapshot, steps[n].Commit.SHA, steps[n].Snapshot)
		}
		n++
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if n != len(steps) {
		t.Fatalf("Walk stepped %d commits, History %d", n, len(steps))
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	err = s.Walk(ctx, ref, func(fold.Step) error {
		calls++
		cancel()
		return nil
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("Walk after cancel = %v after %d calls, want context.Canceled after 1", err, calls)
	}

	missing := refs.For(model.KindNote, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
	if err := s.Walk(t.Context(), missing, func(fold.Step) error { return nil }); !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Fatalf("Walk(missing) error = %v, want ErrRefNotFound", err)
	}
}
