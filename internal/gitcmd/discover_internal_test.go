package gitcmd

import (
	"os"
	"path/filepath"
	"syscall"
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
	included := filepath.Join(root, "included")
	gittest.Git(t, root, "init", "-q", included)
	if err := os.WriteFile(filepath.Join(included, ".git", "bare.config"), []byte("[core]\n\tbare = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, included, "config", "include.path", "bare.config")
	nested := filepath.Join(normal, "nested")
	for _, name := range []string{"objects", "refs"} {
		if err := os.MkdirAll(filepath.Join(nested, ".git", name), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(nested, ".git", "HEAD"), []byte("not a ref\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	future := filepath.Join(root, "future")
	gittest.Git(t, root, "init", "-q", future)
	escaped := filepath.Join(root, "escaped")
	gittest.Git(t, root, "init", "-q", escaped)
	for dir, extra := range map[string]string{future: "[core]\n\trepositoryformatversion = 99\n", escaped: "[x]\n\tv = a\\q\n"} {
		config, err := os.OpenFile(filepath.Join(dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := config.WriteString(extra); err != nil {
			t.Fatal(err)
		}
		if err := config.Close(); err != nil {
			t.Fatal(err)
		}
	}
	hollow := filepath.Join(root, "hollow")
	for _, dir := range []string{filepath.Join(hollow, ".git", "refs"), filepath.Join(root, "empty-common")} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range map[string]string{"HEAD": "ref: refs/heads/main\n", "commondir": "../../empty-common\n"} {
		if err := os.WriteFile(filepath.Join(hollow, ".git", name), []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	piped := filepath.Join(root, "piped")
	if err := os.MkdirAll(piped, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(piped, ".git"), 0o600); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, normal, "branch", "a-bare-name")
	gittest.Git(t, normal, "config", "branch.a-bare-name.description", "bare")
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
	for name, dir := range map[string]string{
		"bare":                   bare,
		"core.bare set":          flagged,
		"core.bare included":     included,
		"inside the git dir":     filepath.Join(normal, ".git", "refs"),
		"an invalid nested HEAD": nested,
		"a future format":        future,
		"an escaped value":       escaped,
		"a hollow common dir":    hollow,
		"a .git FIFO":            piped,
	} {
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
	for name, env := range map[string][]string{
		"a core key":        {"GIT_CONFIG_COUNT", "1", "GIT_CONFIG_KEY_0", "core.bare", "GIT_CONFIG_VALUE_0", "true"},
		"a missing value":   {"GIT_CONFIG_COUNT", "2"},
		"a malformed count": {"GIT_CONFIG_COUNT", "one"},
		"git -c parameters": {"GIT_CONFIG_PARAMETERS", "'core.bare'='true'"},
	} {
		t.Run("declines environment config with "+name, func(t *testing.T) {
			for i := 0; i < len(env); i += 2 {
				t.Setenv(env[i], env[i+1])
			}
			if _, _, found := (Git{Dir: sub}).discoverDirs(); found {
				t.Fatalf("ignored %q", env)
			}
		})
	}
}
