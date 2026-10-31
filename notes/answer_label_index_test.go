package notes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestAnswerLabelsMatchAuthoritativeFiltersAndOrdering(t *testing.T) {
	c, _ := newWBClient(t)
	ids := make(map[string]model.EntityID)
	for _, spec := range []NoteSpec{
		{Title: "both", Body: "complete answer", Tags: []string{"a", "b"}, Anchors: AnchorSpec{Branches: []string{"main"}}},
		{Title: "first", Tags: []string{"a"}},
		{Title: "second", Tags: []string{"b"}},
		{Title: "deleted", Tags: []string{"a", "b"}},
		{Title: "superseded", Tags: []string{"a", "b"}},
		{Title: "expired", Tags: []string{"a", "b"}},
	} {
		a, _, err := c.CreateAnswer(t.Context(), spec)
		if err != nil {
			t.Fatal(err)
		}
		ids[spec.Title] = a.ID
	}
	if _, err := c.RemoveAnswer(t.Context(), ids["deleted"]); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SupersedeAnswer(t.Context(), ids["superseded"], ids["both"]); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExpireAnswer(t.Context(), ids["expired"], "still listed"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []DocumentFilter{
		{},
		{Labels: []string{"a"}},
		{Labels: []string{"a", "b"}},
		{Labels: []string{"a", "a"}},
		{Labels: []string{"missing"}},
		{Labels: []string{""}},
		{Labels: []string{"a"}, Anchor: AnchorFilter{Branch: "main"}},
		{Labels: []string{"a"}, IncludeTombstoned: true},
		{Labels: []string{"a"}, IncludeSuperseded: true},
		{Labels: []string{"a"}, IncludeTombstoned: true, IncludeSuperseded: true},
	} {
		want, err := c.s.ListAnswers(t.Context(), f.IncludeTombstoned, f.IncludeSuperseded)
		if err != nil {
			t.Fatal(err)
		}
		want = filteredAnswers(want, f)
		got, err := c.Answers(t.Context(), f)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Answers(%+v) = %+v, %v; want %+v", f, got, err, want)
		}
	}
}

func TestAnswerLabelCacheMissPreservesOldAndEmptyPins(t *testing.T) {
	c, _ := newWBClient(t)
	a, _, err := c.CreateAnswer(t.Context(), NoteSpec{Title: "old", Tags: []string{"target"}})
	if err != nil {
		t.Fatal(err)
	}
	tip, err := c.s.Repo.Tip(t.Context(), refs.For(model.KindAnswer, a.ID))
	if err != nil {
		t.Fatal(err)
	}
	pinned := FromStore(c.s.Pinned(map[string]model.SHA{refs.For(model.KindAnswer, a.ID): tip}))
	if got, err := c.Answers(t.Context(), DocumentFilter{Labels: []string{"target"}}); err != nil || len(got) != 1 {
		t.Fatalf("warm = %v %v", got, err)
	}
	if _, err := c.EditAnswer(t.Context(), a.ID, NoteEdit{RemoveTags: []string{"target"}}); err != nil {
		t.Fatal(err)
	}
	if got, err := c.Answers(t.Context(), DocumentFilter{Labels: []string{"target"}}); err != nil || len(got) != 0 {
		t.Fatalf("live = %v %v", got, err)
	}
	for _, f := range []DocumentFilter{{Labels: []string{"target"}}, {}} {
		got, err := pinned.Answers(t.Context(), f)
		if err != nil || len(got) != 1 || got[0].ID != a.ID || !reflect.DeepEqual(got[0].Tags, []string{"target"}) {
			t.Fatalf("pinned %+v = %v %v", f, got, err)
		}
		got, err = FromStore(c.s.Pinned(map[string]model.SHA{})).Answers(t.Context(), f)
		if err != nil || got == nil || len(got) != 0 {
			t.Fatalf("empty pinned %+v = %v %v", f, got, err)
		}
	}
}

func TestAnswerLabelUnavailableIndexUsesExistingAuthoritativeRead(t *testing.T) {
	c, _ := newWBClient(t)
	a, _, err := c.CreateAnswer(t.Context(), NoteSpec{Title: "selected", Tags: []string{"target"}})
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(c.s.CommonDir(), "cc-notes-query")
	if err := os.RemoveAll(cachePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachePath, []byte("unavailable"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := c.Answers(t.Context(), DocumentFilter{Labels: []string{"target"}})
	if err != nil || len(got) != 1 || got[0].ID != a.ID {
		t.Fatalf("unavailable index = %v %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := c.Answers(ctx, DocumentFilter{Labels: []string{"target"}}); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel = %v %v", got, err)
	}
}
