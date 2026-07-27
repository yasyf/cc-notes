package stale

import (
	"slices"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
)

func TestChurnLogParsesNumstat(t *testing.T) {
	_, dir := openRepo(t)
	writeFile(t, dir, "pkg/rotor.go", "package pkg\n\nfunc Rotor() int { return 2 }\n")
	writeFile(t, dir, "pkg/widget.go", widgetSource+"\nfunc (w Widget) Stop() {}\n")
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "add the rotor, extend the widget")

	touches, err := churnLog(t.Context(), dir, time.Unix(0, 0))
	if err != nil {
		t.Fatalf("churnLog: %v", err)
	}
	paths := make([]string, len(touches))
	for i, tc := range touches {
		paths[i] = tc.Path
	}
	slices.Sort(paths)
	want := []string{"pkg/rotor.go", "pkg/widget.go", "pkg/widget.go"}
	if !slices.Equal(paths, want) {
		t.Fatalf("churnLog paths = %v, want %v (the root commit plus both edits)", paths, want)
	}
	for _, tc := range touches {
		if tc.Lines <= 0 {
			t.Errorf("touch %+v has no lines; the numstat columns did not parse", tc)
		}
		if tc.TS <= 0 {
			t.Errorf("touch %+v has no commit time; the commit header did not parse", tc)
		}
	}
}

func TestChurnLogWindowExcludesOlderCommits(t *testing.T) {
	_, dir := openRepo(t)
	cutoff := time.Now().Add(time.Hour)
	t.Setenv("GIT_COMMITTER_DATE", cutoff.Add(time.Hour).Format(time.RFC3339))
	writeFile(t, dir, "pkg/rotor.go", "package pkg\n\nfunc Rotor() int { return 2 }\n")
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "add the rotor")

	touches, err := churnLog(t.Context(), dir, cutoff)
	if err != nil {
		t.Fatalf("churnLog: %v", err)
	}
	if len(touches) != 1 || touches[0].Path != "pkg/rotor.go" {
		t.Fatalf("churnLog since the cutoff = %+v, want only the later commit", touches)
	}
}
