package notes_test

import (
	"slices"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// findChange returns the entry's delta for field, if the entry carries one.
func findChange(e notes.HistoryEntry, field string) (notes.FieldChange, bool) {
	for _, ch := range e.Changes {
		if ch.Field == field {
			return ch, true
		}
	}
	return notes.FieldChange{}, false
}

// ptrEq reports whether two optional strings are equal, treating nil as unset.
func ptrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sp(s string) *string { return &s }

// wantScalar fails unless the change is a scalar From→To pair matching from/to.
func wantScalar(t *testing.T, ch notes.FieldChange, from, to *string) {
	t.Helper()
	if len(ch.Added) != 0 || len(ch.Removed) != 0 {
		t.Errorf("%s: got set delta added=%v removed=%v, want scalar", ch.Field, ch.Added, ch.Removed)
	}
	if !ptrEq(ch.From, from) {
		t.Errorf("%s: From = %v, want %v", ch.Field, deref(ch.From), deref(from))
	}
	if !ptrEq(ch.To, to) {
		t.Errorf("%s: To = %v, want %v", ch.Field, deref(ch.To), deref(to))
	}
}

func deref(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// TestHistoryNoteLifecycle drives a note through create, a born-verify, and three
// edits, then pins the exact projected History deltas: create scalars with a
// cleared From, the born-verify time field as RFC3339 UTC, a scalar rename, a
// simple-set (tags) add/remove, and an object-set (anchors) add rendered
// kind:value. Entries come back oldest-first, unreversed.
func TestHistoryNoteLifecycle(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	commitFile(t, dir, "scripts/vm/setup.sh", "echo hi\n")

	note, _, err := c.CreateNote(ctx, notes.NoteSpec{
		Title:   "orig",
		Body:    "b1",
		Tags:    []string{"alpha"},
		Anchors: notes.AnchorSpec{Dirs: []string{"scripts/vm"}},
	})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	if _, err := c.EditNote(ctx, note.ID, notes.NoteEdit{Title: sp("renamed")}); err != nil {
		t.Fatalf("EditNote rename: %v", err)
	}
	if _, err := c.EditNote(ctx, note.ID, notes.NoteEdit{AddTags: []string{"zebra"}, RemoveTags: []string{"alpha"}}); err != nil {
		t.Fatalf("EditNote tags: %v", err)
	}
	if _, err := c.EditNote(ctx, note.ID, notes.NoteEdit{AddAnchors: notes.AnchorSpec{Paths: []string{"scripts/vm/setup.sh"}}}); err != nil {
		t.Fatalf("EditNote anchors: %v", err)
	}

	entries, err := c.History(ctx, note.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}

	if len(entries) != 5 {
		t.Fatalf("entries = %d, want 5 (create, born-verify, rename, tags, anchors)", len(entries))
	}
	wantKinds := []string{"create", "edit", "edit", "edit", "edit"}
	for i, want := range wantKinds {
		if entries[i].Kind != want {
			t.Fatalf("entries[%d].Kind = %q, want %q", i, entries[i].Kind, want)
		}
	}
	if entries[0].Author != model.Actor(testActor) {
		t.Errorf("create Author = %q, want %q", entries[0].Author, testActor)
	}

	// Create: scalars carry a cleared From; the initial sets are pure adds.
	create := entries[0]
	if ch, ok := findChange(create, "title"); !ok {
		t.Error("create missing title change")
	} else {
		wantScalar(t, ch, nil, sp("orig"))
	}
	if ch, ok := findChange(create, "body"); !ok {
		t.Error("create missing body change")
	} else {
		wantScalar(t, ch, nil, sp("b1"))
	}
	if ch, ok := findChange(create, "tags"); !ok {
		t.Error("create missing tags change")
	} else if !slices.Equal(ch.Added, []string{"alpha"}) || len(ch.Removed) != 0 {
		t.Errorf("create tags = added %v removed %v, want added [alpha] removed []", ch.Added, ch.Removed)
	}
	if ch, ok := findChange(create, "anchors"); !ok {
		t.Error("create missing anchors change")
	} else if !slices.Equal(ch.Added, []string{"dir:scripts/vm"}) || len(ch.Removed) != 0 {
		t.Errorf("create anchors = added %v removed %v, want added [dir:scripts/vm]", ch.Added, ch.Removed)
	}

	// Born-verify: the verified_at time field renders as RFC3339 UTC, not raw
	// unix seconds; its From is nil (was unset).
	verify := entries[1]
	ch, ok := findChange(verify, "verified_at")
	if !ok {
		t.Fatalf("born-verify entry missing verified_at change: %+v", verify)
	}
	wantTime := time.Unix(note.VerifiedAt, 0).UTC().Format(time.RFC3339)
	wantScalar(t, ch, nil, sp(wantTime))

	// Rename: a scalar From→To pair.
	if ch, ok := findChange(entries[2], "title"); !ok {
		t.Errorf("rename entry missing title change: %+v", entries[2])
	} else {
		wantScalar(t, ch, sp("orig"), sp("renamed"))
	}

	// Tags edit: a simple set with both an add and a remove.
	if ch, ok := findChange(entries[3], "tags"); !ok {
		t.Errorf("tags entry missing tags change: %+v", entries[3])
	} else {
		if !slices.Equal(ch.Added, []string{"zebra"}) {
			t.Errorf("tags Added = %v, want [zebra]", ch.Added)
		}
		if !slices.Equal(ch.Removed, []string{"alpha"}) {
			t.Errorf("tags Removed = %v, want [alpha]", ch.Removed)
		}
	}

	// Anchor edit: an object set rendered kind:value.
	if ch, ok := findChange(entries[4], "anchors"); !ok {
		t.Errorf("anchor entry missing anchors change: %+v", entries[4])
	} else if !slices.Equal(ch.Added, []string{"path:scripts/vm/setup.sh"}) || len(ch.Removed) != 0 {
		t.Errorf("anchor Added = %v Removed = %v, want added [path:scripts/vm/setup.sh]", ch.Added, ch.Removed)
	}
}

// TestHistoryCriterionNoteSurfaced pins that a criterion's evidence note reaches
// the projected History delta: without it a note-only verdict renders identical
// to the prior state and the change vanishes.
func TestHistoryCriterionNoteSurfaced(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	task := mustTask(t, c, notes.TaskSpec{Title: "crit", Branch: "main"})
	added, err := c.AddCriterion(ctx, task.ID, "tests pass", "")
	if err != nil {
		t.Fatalf("AddCriterion: %v", err)
	}
	crit := added.Criteria[0].ID
	if _, err := c.SetCriterionStatus(ctx, task.ID, crit, model.CriterionMet, "go test: 12 passed"); err != nil {
		t.Fatalf("SetCriterionStatus: %v", err)
	}

	entries, err := c.History(ctx, task.ID)
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	last := entries[len(entries)-1]
	ch, ok := findChange(last, "criteria")
	if !ok {
		t.Fatalf("last entry has no criteria change: %+v", last)
	}
	want := `"tests pass" [met] note "go test: 12 passed"`
	if !slices.Contains(ch.Added, want) {
		t.Fatalf("criteria Added = %v, want an element %q", ch.Added, want)
	}
}

// TestHistoryNotFound checks that an unknown id fails with ErrNotFound rather
// than returning an empty trail.
func TestHistoryNotFound(t *testing.T) {
	c, _ := newClient(t)
	if _, err := c.History(t.Context(), model.EntityID("0123456789abcdef0123456789abcdef01234567")); err == nil {
		t.Fatal("History(unknown id) = nil error, want ErrNotFound")
	}
}
