package refs

import (
	"errors"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/model"
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
		{"note", For(model.KindNote, hex40), "refs/cc-notes/notes/" + hex40},
		{"task", For(model.KindTask, hex40), "refs/cc-notes/tasks/" + hex40},
		{"sprint", For(model.KindSprint, hex40), "refs/cc-notes/sprints/" + hex40},
		{"project", For(model.KindProject, hex40), "refs/cc-notes/projects/" + hex40},
		{"doc", For(model.KindDoc, hex40), "refs/cc-notes/docs/" + hex40},
		{"runbook", For(model.KindRunbook, hex40), "refs/cc-notes/runbooks/" + hex40},
		{"notes prefix", Root(model.KindNote), "refs/cc-notes/notes/"},
		{"tasks root", Root(model.KindTask), "refs/cc-notes/tasks/"},
		{"sprints root", Root(model.KindSprint), "refs/cc-notes/sprints/"},
		{"projects root", Root(model.KindProject), "refs/cc-notes/projects/"},
		{"docs root", Root(model.KindDoc), "refs/cc-notes/docs/"},
		{"runbooks root", Root(model.KindRunbook), "refs/cc-notes/runbooks/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.got != tc.want {
				t.Errorf("built %q, want %q", tc.got, tc.want)
			}
		})
	}
}

func TestRootsCoverKinds(t *testing.T) {
	kinds := model.Kinds()
	if got, want := len(roots), len(kinds); got != want {
		t.Fatalf("roots has %d entries, model.Kinds() has %d", got, want)
	}
	for _, k := range kinds {
		if _, ok := roots[k]; !ok {
			t.Errorf("roots missing kind %q", k)
		}
	}
}

func TestParseRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		ref  string
		want Ref
	}{
		{"note", For(model.KindNote, hex40), Ref{Kind: model.KindNote, ID: hex40}},
		{"note sha256 id", For(model.KindNote, hex64), Ref{Kind: model.KindNote, ID: hex64}},
		{"task", For(model.KindTask, hex40), Ref{Kind: model.KindTask, ID: hex40}},
		{"task sha256 id", For(model.KindTask, hex64), Ref{Kind: model.KindTask, ID: hex64}},
		{"sprint", For(model.KindSprint, hex40), Ref{Kind: model.KindSprint, ID: hex40}},
		{"sprint sha256 id", For(model.KindSprint, hex64), Ref{Kind: model.KindSprint, ID: hex64}},
		{"project", For(model.KindProject, hex40), Ref{Kind: model.KindProject, ID: hex40}},
		{"project sha256 id", For(model.KindProject, hex64), Ref{Kind: model.KindProject, ID: hex64}},
		{"doc", For(model.KindDoc, hex40), Ref{Kind: model.KindDoc, ID: hex40}},
		{"doc sha256 id", For(model.KindDoc, hex64), Ref{Kind: model.KindDoc, ID: hex64}},
		{"runbook", For(model.KindRunbook, hex40), Ref{Kind: model.KindRunbook, ID: hex40}},
		{"runbook sha256 id", For(model.KindRunbook, hex64), Ref{Kind: model.KindRunbook, ID: hex64}},
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
		{"sprint missing id", "refs/cc-notes/sprints/", ErrMalformed},
		{"nested sprint", "refs/cc-notes/sprints/sub/" + hex40, ErrMalformed},
		{"sprint uppercase hex id", "refs/cc-notes/sprints/" + strings.ToUpper(hex40), ErrMalformed},
		{"sprint non-hex id", "refs/cc-notes/sprints/" + strings.Repeat("z", 40), ErrMalformed},
		{"project missing id", "refs/cc-notes/projects/", ErrMalformed},
		{"nested project", "refs/cc-notes/projects/sub/" + hex40, ErrMalformed},
		{"project uppercase hex id", "refs/cc-notes/projects/" + strings.ToUpper(hex40), ErrMalformed},
		{"project 41 hex chars", "refs/cc-notes/projects/" + hex40 + "0", ErrMalformed},
		{"doc missing id", "refs/cc-notes/docs/", ErrMalformed},
		{"nested doc", "refs/cc-notes/docs/sub/" + hex40, ErrMalformed},
		{"doc uppercase hex id", "refs/cc-notes/docs/" + strings.ToUpper(hex40), ErrMalformed},
		{"doc non-hex id", "refs/cc-notes/docs/" + strings.Repeat("z", 40), ErrMalformed},
		{"doc 41 hex chars", "refs/cc-notes/docs/" + hex40 + "0", ErrMalformed},
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
		{"note under notes prefix", Root(model.KindNote), For(model.KindNote, hex40), true},
		{"task under tasks root", Root(model.KindTask), For(model.KindTask, hex40), true},
		{"doc under docs root", Root(model.KindDoc), For(model.KindDoc, hex40), true},
		{"task not under notes prefix", Root(model.KindNote), For(model.KindTask, hex40), false},
		{"doc not under notes prefix", Root(model.KindNote), For(model.KindDoc, hex40), false},
		{"ref equal to prefix", Root(model.KindTask), Root(model.KindTask), false},
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
		{"note", "origin", For(model.KindNote, hex40), "refs/cc-notes-sync/origin/notes/" + hex40},
		{"task", "upstream", For(model.KindTask, hex40), "refs/cc-notes-sync/upstream/tasks/" + hex40},
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
