package store

import (
	"errors"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func TestCreateExactDuplicateAfterColdCache(t *testing.T) {
	for _, kind := range []struct {
		name string
		ops  func(string) []model.Op
	}{
		{"note", noteOps},
		{"doc", docOps},
		{"answer", answerOps},
	} {
		for _, reopen := range []bool{false, true} {
			name := kind.name + "/same-owner"
			if reopen {
				name = kind.name + "/reopened"
			}
			t.Run(name, func(t *testing.T) {
				s := initStore(t)
				first := create(t, s, kind.ops("same exact payload"))
				wipeCache(t, s)
				if reopen {
					fresh, err := OpenContext(t.Context(), s.Git.Dir)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = fresh.Close() })
					s = fresh
				}
				got, err := s.Create(t.Context(), kind.ops("same exact payload"))
				var duplicate *DuplicateError
				if !errors.As(err, &duplicate) || !errors.Is(err, ErrDuplicate) {
					t.Fatalf("COLD_DUPLICATE_REGRESSION: first=%s got=%v err=%v, want original DuplicateError", first.EntityID(), got, err)
				}
				if duplicate.Existing.EntityID() != first.EntityID() {
					t.Fatalf("COLD_DUPLICATE_SURVIVOR: got %s, want %s", duplicate.Existing.EntityID(), first.EntityID())
				}
			})
		}
	}
}
