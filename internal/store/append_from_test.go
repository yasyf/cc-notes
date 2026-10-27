package store

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestAppendFromRebuildsOnTheTipThatBeatIt(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	note := create(t, s, noteOps("progress")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)

	var seen []string
	snap, err := s.AppendFrom(ctx, ref, func(current model.Snapshot) ([]model.Op, error) {
		body := current.(model.Note).Body
		seen = append(seen, body)
		if len(seen) == 1 {
			if _, err := s.Append(ctx, ref, []model.Op{model.SetBody{Body: "theirs"}}); err != nil {
				return nil, err
			}
		}
		return []model.Op{model.SetBody{Body: body + "+ours"}}, nil
	})
	if err != nil {
		t.Fatalf("AppendFrom: %v", err)
	}
	if len(seen) != 2 || seen[1] != "theirs" {
		t.Fatalf("builder saw bodies %q, want a second build over the concurrent %q", seen, "theirs")
	}
	if got := snap.(model.Note).Body; got != "theirs+ours" {
		t.Fatalf("body = %q, want the concurrent write kept under ours", got)
	}
}
