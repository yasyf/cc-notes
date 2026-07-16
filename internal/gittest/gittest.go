// Package gittest provides the shared real-git fixtures cc-notes tests build
// on: an environment scrub, a git command runner, and repo bootstrappers.
package gittest

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// ScrubEnv clears every git environment knob that could leak host state into
// a test and pins global/system config to /dev/null. t.Setenv with the
// original value registers the restore before os.Unsetenv removes the key.
func ScrubEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_NAMESPACE", "GIT_CEILING_DIRECTORIES",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE",
		"GIT_EDITOR", "EMAIL", "GIT_ASKPASS", "SSH_ASKPASS", "CC_NOTES_ACTOR",
		"CC_NOTES_SESSION_ID", "CLAUDE_CODE_SESSION_ID",
	} {
		if value, ok := os.LookupEnv(key); ok {
			t.Setenv(key, value)
			_ = os.Unsetenv(key)
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

// Git runs a git command in dir and returns its trimmed combined output,
// failing the test on error.
func Git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// InitRepo scrubs the git environment and creates a repository on branch
// main with a local "Test User <test@example.com>" identity, returning its
// directory.
func InitRepo(t *testing.T) string {
	t.Helper()
	ScrubEnv(t)
	dir := t.TempDir()
	Git(t, dir, "init", "-q", "-b", "main")
	Git(t, dir, "config", "user.name", "Test User")
	Git(t, dir, "config", "user.email", "test@example.com")
	return dir
}

// InitBare scrubs the git environment and creates a bare repository,
// returning its directory.
func InitBare(t *testing.T) string {
	t.Helper()
	ScrubEnv(t)
	dir := t.TempDir()
	Git(t, dir, "init", "-q", "--bare")
	return dir
}
