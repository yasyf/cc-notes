package refs

import (
	"errors"
	"strings"
	"testing"
)

const (
	hex40 = "0123456789abcdef0123456789abcdef01234567"
	hex64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

func TestBuildGolden(t *testing.T) {
	// The ref layout is part of the storage format: changing it strands
	// existing entities, so the exact strings are pinned.
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"note", Note(hex40), "refs/cc-notes/notes/" + hex40},
		{"task", Task(hex40), "refs/cc-notes/tasks/" + hex40},
		{"notes prefix", NotesPrefix, "refs/cc-notes/notes/"},
		{"tasks root", TasksRoot, "refs/cc-notes/tasks/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("built %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestParseRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		ref  string
		want Ref
	}{
		{"note", Note(hex40), Ref{Kind: KindNote, ID: hex40}},
		{"note sha256 id", Note(hex64), Ref{Kind: KindNote, ID: hex64}},
		{"task", Task(hex40), Ref{Kind: KindTask, ID: hex40}},
		{"task sha256 id", Task(hex64), Ref{Kind: KindTask, ID: hex64}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.ref)
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.ref, err)
			}
			if got != tc.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tc.ref, got, tc.want)
			}
		})
	}
}

func TestParseRejects(t *testing.T) {
	cases := []struct {
		name    string
		ref     string
		wantErr error
	}{
		{"branch ref", "refs/heads/main", ErrNotCCNotes},
		{"tracking ref", "refs/cc-notes-sync/origin/notes/" + hex40, ErrNotCCNotes},
		{"namespace root", "refs/cc-notes/", ErrMalformed},
		{"missing id", "refs/cc-notes/notes", ErrMalformed},
		{"empty id", "refs/cc-notes/notes/", ErrMalformed},
		{"uppercase hex id", "refs/cc-notes/notes/" + strings.ToUpper(hex40), ErrMalformed},
		{"39 hex chars", "refs/cc-notes/notes/" + hex40[:39], ErrMalformed},
		{"41 hex chars", "refs/cc-notes/notes/" + hex40 + "0", ErrMalformed},
		{"non-hex id", "refs/cc-notes/notes/" + strings.Repeat("z", 40), ErrMalformed},
		{"nested note", "refs/cc-notes/notes/sub/" + hex40, ErrMalformed},
		{"unknown namespace", "refs/cc-notes/widgets/" + hex40, ErrMalformed},
		{"task missing id", "refs/cc-notes/tasks/", ErrMalformed},
		{"nested task", "refs/cc-notes/tasks/sub/" + hex40, ErrMalformed},
		{"task uppercase hex id", "refs/cc-notes/tasks/" + strings.ToUpper(hex40), ErrMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.ref)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Parse(%q) error = %v, want errors.Is %v", tc.ref, err, tc.wantErr)
			}
			if got != (Ref{}) {
				t.Errorf("Parse(%q) = %+v, want zero Ref on error", tc.ref, got)
			}
		})
	}
}

func TestDirectChild(t *testing.T) {
	cases := []struct {
		name   string
		prefix string
		ref    string
		want   bool
	}{
		{"note under notes prefix", NotesPrefix, Note(hex40), true},
		{"task under tasks root", TasksRoot, Task(hex40), true},
		{"task not under notes prefix", NotesPrefix, Task(hex40), false},
		{"ref equal to prefix", TasksRoot, TasksRoot, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DirectChild(tc.prefix, tc.ref); got != tc.want {
				t.Errorf("DirectChild(%q, %q) = %v, want %v", tc.prefix, tc.ref, got, tc.want)
			}
		})
	}
}

func TestTrackingRoundTrip(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		ref    string
		want   string
	}{
		{"note", "origin", Note(hex40), "refs/cc-notes-sync/origin/notes/" + hex40},
		{"task", "upstream", Task(hex40), "refs/cc-notes-sync/upstream/tasks/" + hex40},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tracking, err := Tracking(tc.remote, tc.ref)
			if err != nil {
				t.Fatalf("Tracking(%q, %q) error: %v", tc.remote, tc.ref, err)
			}
			if tracking != tc.want {
				t.Fatalf("Tracking(%q, %q) = %q, want %q", tc.remote, tc.ref, tracking, tc.want)
			}
			remote, ref, err := ParseTracking(tracking)
			if err != nil {
				t.Fatalf("ParseTracking(%q) error: %v", tracking, err)
			}
			if remote != tc.remote || ref != tc.ref {
				t.Errorf("ParseTracking(%q) = (%q, %q), want (%q, %q)", tracking, remote, ref, tc.remote, tc.ref)
			}
		})
	}
}

func TestTrackingRejects(t *testing.T) {
	if _, err := Tracking("origin", "refs/heads/main"); !errors.Is(err, ErrNotCCNotes) {
		t.Errorf("Tracking on branch ref error = %v, want errors.Is ErrNotCCNotes", err)
	}
}

func TestParseTrackingRejects(t *testing.T) {
	cases := []struct {
		name     string
		tracking string
		wantErr  error
	}{
		{"cc-notes ref", "refs/cc-notes/notes/" + hex40, ErrNotCCNotes},
		{"branch ref", "refs/heads/main", ErrNotCCNotes},
		{"missing suffix", "refs/cc-notes-sync/origin", ErrMalformed},
		{"empty suffix", "refs/cc-notes-sync/origin/", ErrMalformed},
		{"empty remote", "refs/cc-notes-sync//notes/" + hex40, ErrMalformed},
		{"namespace root", "refs/cc-notes-sync/", ErrMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			remote, ref, err := ParseTracking(tc.tracking)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ParseTracking(%q) error = %v, want errors.Is %v", tc.tracking, err, tc.wantErr)
			}
			if remote != "" || ref != "" {
				t.Errorf("ParseTracking(%q) = (%q, %q), want empty results on error", tc.tracking, remote, ref)
			}
		})
	}
}
