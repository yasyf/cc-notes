package gitcmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
)

func TestDiscoverDirsMatchesGitOrDeclines(t *testing.T) {
	gittest.ScrubEnv(t)
	root := t.TempDir()
	normal := filepath.Join(root, "normal")
	gittest.Git(t, root, "init", "-q", "-b", "main", normal)
	gittest.Git(t, normal, "-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "base")
	sub := filepath.Join(normal, "a", "b")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(root, "linked")
	gittest.Git(t, normal, "worktree", "add", "-q", "-b", "linked", linked)
	bare := filepath.Join(root, "bare.git")
	gittest.Git(t, root, "init", "-q", "--bare", bare)
	flagged := filepath.Join(root, "flagged")
	gittest.Git(t, root, "init", "-q", flagged)
	gittest.Git(t, flagged, "config", "core.bare", "true")
	for _, dir := range []string{normal, sub, linked} {
		t.Run("matches "+filepath.Base(dir), func(t *testing.T) {
			gitDir, commonDir, found := (Git{Dir: dir}).discoverDirs()
			if !found {
				t.Fatal("declined a plain working tree")
			}
			wantGit, wantCommon, wantBare, err := (Git{Dir: dir}).dirs(t.Context(), os.Environ())
			if err != nil {
				t.Fatal(err)
			}
			if gitDir != wantGit || commonDir != wantCommon || wantBare {
				t.Fatalf("got %q %q, git says %q %q bare=%t", gitDir, commonDir, wantGit, wantCommon, wantBare)
			}
		})
	}
	for name, dir := range map[string]string{"bare": bare, "core.bare set": flagged, "inside the git dir": filepath.Join(normal, ".git", "refs")} {
		t.Run("declines "+name, func(t *testing.T) {
			if _, _, found := (Git{Dir: dir}).discoverDirs(); found {
				t.Fatal("answered for a layout it must leave to git")
			}
		})
	}
	t.Run("declines a routing variable", func(t *testing.T) {
		t.Setenv("GIT_DIR", filepath.Join(normal, ".git"))
		if _, _, found := (Git{Dir: sub}).discoverDirs(); found {
			t.Fatal("ignored GIT_DIR")
		}
	})
}
