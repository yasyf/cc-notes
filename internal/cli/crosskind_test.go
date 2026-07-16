package cli_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// TestCrossKindNotFoundHint drives a noun-scoped `doc show` whose id belongs to
// another kind (or none) and pins the enrichment: the miss stays not-found (exit
// 3) but carries a hint naming the sibling kind(s) and the kind-agnostic
// `cc-notes show`, while a prefix matching nothing anywhere stays hint-free.
func TestCrossKindNotFoundHint(t *testing.T) {
	for _, tc := range []struct {
		name     string
		setup    func(t *testing.T, dir string) string // returns the prefix to `doc show`
		wantHint []string                              // substrings; nil means the hint must be ""
	}{
		{
			name: "single other kind",
			setup: func(t *testing.T, dir string) string {
				n := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "A note", "--json"))
				return n.ID
			},
			wantHint: []string{"note", "cc-notes show"},
		},
		{
			name:     "two other kinds",
			setup:    func(t *testing.T, dir string) string { return pigeonholeNoteAndTask(t, dir) },
			wantHint: []string{"note", "task", "cc-notes show"},
		},
		{
			name: "no match anywhere",
			setup: func(t *testing.T, dir string) string {
				n := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "A note", "--json"))
				// One hex longer than a full id: prefixes nothing in any kind.
				return n.ID + "0"
			},
			wantHint: nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := initRepo(t)
			prefix := tc.setup(t, dir)
			_, _, err := runCLI(t, dir, "doc", "show", prefix)
			if !errors.Is(err, store.ErrNotFound) {
				t.Fatalf("doc show %q err = %v, want ErrNotFound", prefix, err)
			}
			if got := cli.ExitCode(err); got != 3 {
				t.Fatalf("doc show %q exit = %d, want 3 (not-found)", prefix, got)
			}
			if got := cli.Label(err); got != "not-found" {
				t.Fatalf("doc show %q label = %q, want not-found", prefix, got)
			}
			hint := cli.Hint(err)
			if len(tc.wantHint) == 0 {
				if hint != "" {
					t.Fatalf("doc show %q hint = %q, want empty (no cross-kind match)", prefix, hint)
				}
				return
			}
			for _, want := range tc.wantHint {
				if !strings.Contains(hint, want) {
					t.Fatalf("doc show %q hint = %q, missing %q", prefix, hint, want)
				}
			}
		})
	}
}

// TestGlobalShowAmbiguousKindLabels: a prefix spanning a note and a task makes
// top-level `show` surface a *notes.AmbiguousKindsError whose matches carry the
// CORRECT per-kind labels. The deleted ambiguousAcrossKinds hardcoded every
// match as a note, so reverting the resolver rewire fails this.
func TestGlobalShowAmbiguousKindLabels(t *testing.T) {
	dir := initRepo(t)
	prefix := pigeonholeNoteAndTask(t, dir)

	_, _, err := runCLI(t, dir, "show", prefix)
	if err == nil {
		t.Fatal("show of a cross-kind prefix returned nil error")
	}
	var amb *notes.AmbiguousKindsError
	if !errors.As(err, &amb) {
		t.Fatalf("show %q err = %v (%T), want *notes.AmbiguousKindsError", prefix, err, err)
	}
	if got := cli.ExitCode(err); got != 5 {
		t.Fatalf("show %q exit = %d, want 5 (ambiguous)", prefix, got)
	}
	seen := map[model.Kind]bool{}
	for _, m := range amb.Matches {
		seen[m.Kind] = true
	}
	if !seen[model.KindNote] || !seen[model.KindTask] {
		t.Fatalf("matches = %+v, want both note and task (not all mislabeled as note)", amb.Matches)
	}
	if msg := err.Error(); !strings.Contains(msg, "note") || !strings.Contains(msg, "task") {
		t.Fatalf("ambiguity error %q must name both the note and task kinds", msg)
	}
}

// TestCompactAmbiguousKindLabels mirrors TestGlobalShowAmbiguousKindLabels for
// the top-level `compact` command, rewired onto notes.Client.ResolveEntity in
// the same change. A prefix spanning a note and a task must surface a
// *notes.AmbiguousKindsError whose matches carry the CORRECT per-kind labels; a
// resolver that mislabels every match as a note fails the kind-set assertion.
func TestCompactAmbiguousKindLabels(t *testing.T) {
	dir := initRepo(t)
	prefix := pigeonholeNoteAndTask(t, dir)

	_, _, err := runCLI(t, dir, "compact", prefix)
	if err == nil {
		t.Fatal("compact of a cross-kind prefix returned nil error")
	}
	var amb *notes.AmbiguousKindsError
	if !errors.As(err, &amb) {
		t.Fatalf("compact %q err = %v (%T), want *notes.AmbiguousKindsError", prefix, err, err)
	}
	if got := cli.ExitCode(err); got != 5 {
		t.Fatalf("compact %q exit = %d, want 5 (ambiguous)", prefix, got)
	}
	seen := map[model.Kind]bool{}
	for _, m := range amb.Matches {
		seen[m.Kind] = true
	}
	if !seen[model.KindNote] || !seen[model.KindTask] {
		t.Fatalf("matches = %+v, want both note and task (not all mislabeled as note)", amb.Matches)
	}
	if msg := err.Error(); !strings.Contains(msg, "note") || !strings.Contains(msg, "task") {
		t.Fatalf("ambiguity error %q must name both the note and task kinds", msg)
	}
}

// pigeonholeNoteAndTask creates notes and tasks until a leading hex char holds
// exactly one note and one task, then returns that 1-char prefix. No docs are
// created, so `doc show <prefix>` misses in the doc namespace and resolves the
// prefix across exactly a note and a task. Mirrors the shared-prefix
// construction in TestGlobalShow.
func pigeonholeNoteAndTask(t *testing.T, dir string) string {
	t.Helper()
	notesByChar := map[byte][]string{}
	tasksByChar := map[byte][]string{}
	for i := 0; i < 32; i++ {
		n := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", fmt.Sprintf("note-%d", i), "--json"))
		notesByChar[n.ID[0]] = append(notesByChar[n.ID[0]], n.ID)
		tk := addTask(t, dir, fmt.Sprintf("task-%d", i))
		tasksByChar[tk.ID[0]] = append(tasksByChar[tk.ID[0]], tk.ID)
		for ch, ns := range notesByChar {
			if len(ns) == 1 && len(tasksByChar[ch]) == 1 {
				return string(ch)
			}
		}
	}
	t.Fatal("no leading char with exactly one note and one task after 32 rounds")
	return ""
}
