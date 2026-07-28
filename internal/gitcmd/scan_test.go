package gitcmd_test

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func commitFile(t *testing.T, g gitcmd.Git, path, content string) model.SHA {
	t.Helper()
	full := filepath.Join(g.Dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(full), err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	gittest.Git(t, g.Dir, "add", path)
	gittest.Git(t, g.Dir, "commit", "-q", "-m", "edit "+path)
	return resolve(t, g.Dir, "HEAD")
}

func TestTrackedFiles(t *testing.T) {
	g := initRepo(t)
	commitFile(t, g, "pkg/widget.go", "package pkg\n")
	commitFile(t, g, "README.md", "# hi\n")
	if err := os.WriteFile(filepath.Join(g.Dir, "untracked.txt"), []byte("no\n"), 0o600); err != nil {
		t.Fatalf("write untracked: %v", err)
	}

	got, err := g.TrackedFiles(t.Context())
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	if want := []string{"README.md", "pkg/widget.go"}; !slices.Equal(got, want) {
		t.Fatalf("TrackedFiles = %v, want %v", got, want)
	}
}

// TestTrackedFilesFromASubdirectoryStaysRootRelative pins the documented
// contract at the seam every caller joins onto the repository root: a bare
// ls-files under `git -C <subdir>` reports paths relative to that
// subdirectory, and every join would then miss.
func TestTrackedFilesFromASubdirectoryStaysRootRelative(t *testing.T) {
	g := initRepo(t)
	commitFile(t, g, "pkg/widget.go", "package pkg\n")
	commitFile(t, g, "README.md", "# hi\n")

	got, err := gitcmd.Git{Dir: filepath.Join(g.Dir, "pkg")}.TrackedFiles(t.Context())
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	if want := []string{"README.md", "pkg/widget.go"}; !slices.Equal(got, want) {
		t.Fatalf("TrackedFiles from pkg/ = %v, want %v", got, want)
	}
}

func TestTrackedFilesEmptyIndex(t *testing.T) {
	got, err := initRepo(t).TrackedFiles(t.Context())
	if err != nil {
		t.Fatalf("TrackedFiles: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("TrackedFiles = %v, want none", got)
	}
}

func TestNumstatLogCountsChurnPerCommit(t *testing.T) {
	g := initRepo(t)
	commitFile(t, g, "a.go", "one\ntwo\n")
	commitFile(t, g, "a.go", "one\ntwo\nthree\n")

	got, err := g.NumstatLog(t.Context(), gitcmd.NumstatScope{})
	if err != nil {
		t.Fatalf("NumstatLog: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("NumstatLog = %+v, want 2 commits newest first", got)
	}
	if want := []gitcmd.FileChurn{{Path: "a.go", Added: 1}}; !slices.Equal(got[0].Files, want) {
		t.Errorf("newest commit files = %+v, want %+v", got[0].Files, want)
	}
	if want := []gitcmd.FileChurn{{Path: "a.go", Added: 2}}; !slices.Equal(got[1].Files, want) {
		t.Errorf("root commit files = %+v, want %+v", got[1].Files, want)
	}
	if got[0].Time.Before(got[1].Time) {
		t.Errorf("commit times %v then %v, want newest first", got[0].Time, got[1].Time)
	}
}

// TestNumstatLogBinaryFileChurnsNothing pins the "-" column: a binary blob has
// no line counts, and reading them as zero must not fail the whole scan.
func TestNumstatLogBinaryFileChurnsNothing(t *testing.T) {
	g := initRepo(t)
	commitFile(t, g, "logo.png", "\x89PNG\r\n\x1a\n\x00\x00rawbytes")

	got, err := g.NumstatLog(t.Context(), gitcmd.NumstatScope{})
	if err != nil {
		t.Fatalf("NumstatLog: %v", err)
	}
	want := []gitcmd.FileChurn{{Path: "logo.png"}}
	if len(got) != 1 || !slices.Equal(got[0].Files, want) {
		t.Fatalf("NumstatLog = %+v, want one commit churning %+v", got, want)
	}
}

func TestNumstatLogScopeSinceLimitAndPaths(t *testing.T) {
	g := initRepo(t)
	commitFile(t, g, "old.go", "one\n")
	cutoff := time.Now().Add(time.Hour)
	t.Setenv("GIT_COMMITTER_DATE", cutoff.Add(time.Hour).Format(time.RFC3339))
	commitFile(t, g, "new.go", "two\n")
	commitFile(t, g, "other.go", "three\n")

	since, err := g.NumstatLog(t.Context(), gitcmd.NumstatScope{Since: cutoff})
	if err != nil {
		t.Fatalf("NumstatLog since: %v", err)
	}
	if len(since) != 2 {
		t.Errorf("Since = %+v, want only the two commits after the cutoff", since)
	}

	limited, err := g.NumstatLog(t.Context(), gitcmd.NumstatScope{Limit: 1})
	if err != nil {
		t.Fatalf("NumstatLog limit: %v", err)
	}
	if len(limited) != 1 || limited[0].Files[0].Path != "other.go" {
		t.Errorf("Limit = %+v, want only the newest commit", limited)
	}

	scoped, err := g.NumstatLog(t.Context(), gitcmd.NumstatScope{Paths: []string{"old.go"}})
	if err != nil {
		t.Fatalf("NumstatLog paths: %v", err)
	}
	if len(scoped) != 1 || scoped[0].Files[0].Path != "old.go" {
		t.Errorf("Paths = %+v, want only the commit touching old.go", scoped)
	}
}

func TestCommitPaths(t *testing.T) {
	g := initRepo(t)
	root := commitFile(t, g, "a.go", "one\n")
	gittest.Git(t, g.Dir, "checkout", "-q", "-b", "side")
	side := commitFile(t, g, "b.go", "two\n")
	gittest.Git(t, g.Dir, "checkout", "-q", "main")
	main := commitFile(t, g, "a.go", "one\ntwo\n")
	gittest.Git(t, g.Dir, "merge", "-q", "--no-ff", "-m", "merge side", "side")
	merge := resolve(t, g.Dir, "HEAD")

	const absent = "1111111111111111111111111111111111111111"
	got, err := g.CommitPaths(t.Context(), []model.SHA{root, side, main, merge, absent})
	if err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	want := map[model.SHA][]string{
		root: {"a.go"},
		side: {"b.go"},
		main: {"a.go"},
	}
	if len(got) != len(want) {
		t.Fatalf("CommitPaths = %v, want %v — a merge and an absent sha contribute nothing", got, want)
	}
	for sha, paths := range want {
		if !slices.Equal(got[sha], paths) {
			t.Errorf("CommitPaths[%s] = %v, want %v", sha, got[sha], paths)
		}
	}
}

func TestCommitPathsNoCommits(t *testing.T) {
	got, err := initRepo(t).CommitPaths(t.Context(), nil)
	if err != nil {
		t.Fatalf("CommitPaths: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("CommitPaths = %v, want none", got)
	}
}
