package cli_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/store"
)

func TestCompactNoteJSONAndLean(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Runbook", "--label", "ops", "--json"))
	mustRun(t, dir, "note", "edit", added.ID, "--title", "Runbook v2", "--json")

	got := mustJSON[noteJSON](t, mustRun(t, dir, "compact", added.ID, "--json"))
	if got.ID != added.ID {
		t.Fatalf("compact id = %s, want %s (id is immutable)", got.ID, added.ID)
	}
	if got.Title != "Runbook v2" {
		t.Fatalf("compact title = %q, want Runbook v2 (state preserved)", got.Title)
	}
	if want := []string{"ops"}; len(got.Tags) != 1 || got.Tags[0] != want[0] {
		t.Fatalf("compact tags = %v, want %v", got.Tags, want)
	}

	lean := mustRun(t, dir, "compact", added.ID)
	if !strings.HasPrefix(lean, added.ID[:7]) {
		t.Fatalf("compact lean = %q, want prefix %s", lean, added.ID[:7])
	}
	if !strings.Contains(lean, "Runbook v2") {
		t.Fatalf("compact lean = %q, want it to carry the title", lean)
	}
}

func TestCompactTaskAndUnknownID(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "Ship it")
	mustRun(t, dir, "task", "start", task.ID, "--json")

	got := mustJSON[taskJSON](t, mustRun(t, dir, "compact", task.ID, "--json"))
	if got.ID != task.ID {
		t.Fatalf("compact id = %s, want %s", got.ID, task.ID)
	}
	if got.Status != "in_progress" {
		t.Fatalf("compact status = %q, want in_progress (state preserved)", got.Status)
	}

	if _, _, err := runCLI(t, dir, "compact", "ffffffff"); err == nil {
		t.Fatal("compact of an unknown id returned nil error")
	}
}

func TestCompactDocJSONAndLean(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[noteJSON](t, mustRun(t, dir, "doc", "add", "Deploy guide", "--body", "x", "--label", "ops", "--json"))
	mustRun(t, dir, "doc", "edit", added.ID, "--title", "Deploy guide v2", "--json")

	got := mustJSON[noteJSON](t, mustRun(t, dir, "compact", added.ID, "--json"))
	if got.ID != added.ID {
		t.Fatalf("compact id = %s, want %s (id is immutable)", got.ID, added.ID)
	}
	if got.Title != "Deploy guide v2" {
		t.Fatalf("compact title = %q, want Deploy guide v2 (state preserved)", got.Title)
	}
	if want := []string{"ops"}; len(got.Tags) != 1 || got.Tags[0] != want[0] {
		t.Fatalf("compact tags = %v, want %v", got.Tags, want)
	}

	lean := mustRun(t, dir, "compact", added.ID)
	if !strings.HasPrefix(lean, added.ID[:7]) {
		t.Fatalf("compact lean = %q, want prefix %s", lean, added.ID[:7])
	}
	if !strings.Contains(lean, "Deploy guide v2") {
		t.Fatalf("compact lean = %q, want it to carry the title", lean)
	}
}

func TestCompactAmbiguousAcrossNoteAndDoc(t *testing.T) {
	dir := initRepo(t)

	// Track the full id list per leading char for each kind, not last-write-wins.
	// resolveEntity resolves a kind-agnostic prefix against the note, task, and
	// doc namespaces and only reports CROSS-KIND ambiguity (the path under test)
	// when the prefix matches EXACTLY ONE note and EXACTLY ONE doc — two or more
	// within either kind short-circuits with an intra-kind ambiguity error that
	// lists only that kind. So the chosen leading char must hold exactly one note
	// and exactly one doc (the test creates no tasks, so the task namespace is
	// always empty). 16 hex buckets and one note + one doc per round make such a
	// char near-certain well within the bound (mirrors store_test.go's pigeonhole
	// shared-prefix construction).
	notesByChar := map[byte][]string{}
	docsByChar := map[byte][]string{}
	var noteID, docID string
	pick := func() bool {
		for ch, notes := range notesByChar {
			if len(notes) == 1 && len(docsByChar[ch]) == 1 {
				noteID, docID = notes[0], docsByChar[ch][0]
				return true
			}
		}
		return false
	}
	for i := 0; i < 32; i++ {
		n := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", fmt.Sprintf("note-%d", i), "--json"))
		notesByChar[n.ID[0]] = append(notesByChar[n.ID[0]], n.ID)
		d := mustJSON[noteJSON](t, mustRun(t, dir, "doc", "add", fmt.Sprintf("doc-%d", i), "--body", "x", "--json"))
		docsByChar[d.ID[0]] = append(docsByChar[d.ID[0]], d.ID)
		if pick() {
			break
		}
	}
	if noteID == "" {
		t.Fatal("no leading char with exactly one note and one doc after 32 rounds")
	}

	// The 1-char prefix matches exactly one note and one doc (no intra-kind
	// collision on this char), so compact takes the cross-kind ambiguity path and
	// the error lists both the chosen note's and doc's short ids.
	prefix := noteID[:1]
	_, _, err := runCLI(t, dir, "compact", prefix)
	if err == nil {
		t.Fatalf("compact %q spanning a note and a doc returned nil error", prefix)
	}
	if !errors.Is(err, store.ErrAmbiguous) {
		t.Fatalf("compact %q error = %v, want ErrAmbiguous", prefix, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, noteID[:7]) || !strings.Contains(msg, docID[:7]) {
		t.Fatalf("ambiguity error %q must list both note %s and doc %s", msg, noteID[:7], docID[:7])
	}
}
