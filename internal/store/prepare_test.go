package store

import (
	"errors"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestPrepareCreateExactDoesNotPublishRef(t *testing.T) {
	s := initStore(t)
	t.Setenv("CC_NOTES_SESSION_ID", "operation-create")
	prepared, err := s.PrepareCreateExact(t.Context(), noteOps("prepared"))
	if err != nil {
		t.Fatalf("PrepareCreateExact: %v", err)
	}
	if prepared.Old != "" || prepared.New == "" || prepared.Ref == "" {
		t.Fatalf("prepared = %+v", prepared)
	}
	if _, err := s.Repo.Tip(t.Context(), prepared.Ref); !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Fatalf("prepared ref resolved before commit: %v", err)
	}
	if err := s.Git.UpdateRefs(t.Context(), []gitcmd.RefUpdate{prepared.RefUpdate()}); err != nil {
		t.Fatalf("publish prepared ref: %v", err)
	}
	s.RememberPrepared(prepared)
	loaded, err := s.Load(t.Context(), prepared.Ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.(model.Note).Title != "prepared" {
		t.Fatalf("title = %q, want prepared", loaded.(model.Note).Title)
	}
	applied, err := s.HasSession(t.Context(), prepared.Ref, "operation-create")
	if err != nil || !applied {
		t.Fatalf("HasSession = %v, %v", applied, err)
	}
}

func TestPrepareAppendAtPinsExpectedTipWithoutPublishing(t *testing.T) {
	s := initStore(t)
	created := create(t, s, noteOps("before")).(model.Note)
	ref := refs.For(model.KindNote, created.ID)
	before, err := s.Repo.Tip(t.Context(), ref)
	if err != nil {
		t.Fatalf("Tip: %v", err)
	}
	t.Setenv("CC_NOTES_SESSION_ID", "operation-append")
	prepared, err := s.PrepareAppendAt(t.Context(), ref, before, []model.Op{model.SetTitle{Title: "after"}})
	if err != nil {
		t.Fatalf("PrepareAppendAt: %v", err)
	}
	if got, err := s.Repo.Tip(t.Context(), ref); err != nil || got != before {
		t.Fatalf("ref moved during prepare to %s, %v; want %s", got, err, before)
	}
	if prepared.Snapshot.(model.Note).Title != "after" {
		t.Fatalf("prepared title = %q, want after", prepared.Snapshot.(model.Note).Title)
	}
	if err := s.Git.UpdateRefs(t.Context(), []gitcmd.RefUpdate{prepared.RefUpdate()}); err != nil {
		t.Fatalf("publish prepared append: %v", err)
	}
	if applied, err := s.HasSession(t.Context(), ref, "operation-append"); err != nil || !applied {
		t.Fatalf("HasSession = %v, %v", applied, err)
	}
}

func TestPreparedAppendStaleCASLeavesCurrentRefUntouched(t *testing.T) {
	s := initStore(t)
	created := create(t, s, noteOps("before")).(model.Note)
	ref := refs.For(model.KindNote, created.ID)
	before, err := s.Repo.Tip(t.Context(), ref)
	if err != nil {
		t.Fatalf("Tip: %v", err)
	}
	prepared, err := s.PrepareAppendAt(t.Context(), ref, before, []model.Op{model.SetTitle{Title: "prepared"}})
	if err != nil {
		t.Fatalf("PrepareAppendAt: %v", err)
	}
	if _, err := s.Append(t.Context(), ref, []model.Op{model.SetTitle{Title: "winner"}}); err != nil {
		t.Fatalf("Append winner: %v", err)
	}
	winner, err := s.Repo.Tip(t.Context(), ref)
	if err != nil {
		t.Fatalf("Tip winner: %v", err)
	}
	if err := s.Git.UpdateRefs(t.Context(), []gitcmd.RefUpdate{prepared.RefUpdate()}); !errors.Is(err, gitcmd.ErrCASMismatch) {
		t.Fatalf("stale publish = %v, want ErrCASMismatch", err)
	}
	if got, err := s.Repo.Tip(t.Context(), ref); err != nil || got != winner {
		t.Fatalf("ref = %s, %v; want winner %s", got, err, winner)
	}
}
