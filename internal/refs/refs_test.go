package refs

import (
	"errors"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/model"
)

const (
	hex40      = "0123456789abcdef0123456789abcdef01234567"
	otherHex40 = "fedcba9876543210fedcba9876543210fedcba98"
	hex64      = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
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
		{"task plain branch", Task("main", hex40), "refs/cc-notes/tasks/main/" + hex40},
		{"task slashed branch", Task("feature/x", hex40), "refs/cc-notes/tasks/feature/x/" + hex40},
		{"notes prefix", NotesPrefix, "refs/cc-notes/notes/"},
		{"tasks prefix", TasksPrefix("feature/x"), "refs/cc-notes/tasks/feature/x/"},
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
		{"task plain branch", Task("main", hex40), Ref{Kind: KindTask, Branch: "main", ID: hex40}},
		{"task slashed branch", Task("feature/x", hex40), Ref{Kind: KindTask, Branch: "feature/x", ID: hex40}},
		{
			"task branch whose last component is 40-hex",
			Task(model.Branch("release/"+otherHex40), hex40),
			Ref{Kind: KindTask, Branch: model.Branch("release/" + otherHex40), ID: hex40},
		},
		{
			"task branch that is itself 40-hex",
			Task(model.Branch(otherHex40), hex40),
			Ref{Kind: KindTask, Branch: model.Branch(otherHex40), ID: hex40},
		},
		{"task sha256 id", Task("main", hex64), Ref{Kind: KindTask, Branch: "main", ID: hex64}},
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
		{"task missing branch", "refs/cc-notes/tasks/" + hex40, ErrEmptyBranch},
		{"task empty branch", "refs/cc-notes/tasks//" + hex40, ErrEmptyBranch},
		{"task missing id", "refs/cc-notes/tasks/main/", ErrMalformed},
		{"task uppercase hex id", "refs/cc-notes/tasks/main/" + strings.ToUpper(hex40), ErrMalformed},
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
		{"task under its branch prefix", TasksPrefix("a"), Task("a", hex40), true},
		{"sub-branch task excluded from parent branch", TasksPrefix("a"), Task("a/b", hex40), false},
		{"task under slashed branch prefix", TasksPrefix("a/b"), Task("a/b", hex40), true},
		{"task not under notes prefix", NotesPrefix, Task("main", hex40), false},
		{"other branch", TasksPrefix("main"), Task("dev", hex40), false},
		{"ref equal to prefix", TasksPrefix("a"), TasksPrefix("a"), false},
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
		{
			"task slashed branch",
			"upstream",
			Task("feature/x", hex40),
			"refs/cc-notes-sync/upstream/tasks/feature/x/" + hex40,
		},
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
