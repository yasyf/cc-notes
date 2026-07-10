//go:build !fuse

// These tests pin the PURE build's refusal semantics: a binary that cannot
// host fuse fails a caskless holder spawn with ErrCannotHost and never
// auto-mounts. They are excluded by build tag, not just by assertion, because
// under -tags fuse their spawn paths are real: the caskless path would
// self-exec THIS TEST BINARY as the holder (re-entering the suite — the fork
// storm, ccn doc ef281ea) and --auto would drive the installed shared cask
// holder. They must not even compile into a fuse test binary.

package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
)

func TestMountHolderDownExits1(t *testing.T) {
	repo := initRepo(t)
	sock := filepath.Join(t.TempDir(), "never-bound.sock")
	mp := filepath.Join(t.TempDir(), "mnt")

	// --caskless drives the self-exec holder, whose spawn a pure test binary
	// cannot host (ErrCannotHost) — a deterministic exit 1 that never launches
	// the shared cask app. (Without --caskless the default would try to `open -g`
	// the installed cask, which a test must never do.)
	_, _, err := runCLI(t, repo, "mount", "--caskless", "--socket", sock, mp)
	if err == nil {
		t.Fatal("mount succeeded with no holder, want a failure")
	}
	if code := cli.ExitCode(err); code != 1 {
		t.Errorf("exit = %d, want 1; err = %v", code, err)
	}
}

// TestMountDetachedCreatesStateDir guards the first-run holder spawn: the
// detached path must create cc-notes' state dir (~/.cc-notes) before handing
// off to the holder, because that dir homes the spawn log (and, in cask-less
// mode, the default socket), and fusekit treats their parent dirs as the
// caller's to create. An explicit mountpoint never creates ~/.cc-notes on its
// own, so without this the first `cc-notes mount DIR` on a fresh machine dies
// with "open mount holder log: no such file or directory". A pure test binary
// can't spawn a holder, so the mount itself fails (ErrCannotHost) — but the
// state dir must already exist by the time it does. --caskless keeps the spawn
// on the deterministic self-exec path that never launches the shared cask app.
func TestMountDetachedCreatesStateDir(t *testing.T) {
	repo := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	stateDir := filepath.Join(home, ".cc-notes")
	if _, err := os.Stat(stateDir); !os.IsNotExist(err) {
		t.Fatalf("precondition: state dir %s should be absent, stat err = %v", stateDir, err)
	}

	sock := filepath.Join(t.TempDir(), "never-bound.sock")
	mp := filepath.Join(t.TempDir(), "mnt")
	if _, _, err := runCLI(t, repo, "mount", "--caskless", "--socket", sock, mp); err == nil {
		t.Fatal("mount with no holder succeeded on a pure build, want a failure")
	}
	if _, err := os.Stat(stateDir); err != nil {
		t.Fatalf("detached mount did not create state dir %s: %v", stateDir, err)
	}
}

// TestMountAutoQuietNoOpWithoutFuse proves the session-start ensure-mount
// (`mount --auto`) is a silent, successful no-op on a binary that cannot host
// fuse — even with the repo opted in (cc-notes.autoMount=true). It must never
// contact a holder or print anything, so the SessionStart hook can call it in
// any repo without risk of disturbing a running holder.
func TestMountAutoQuietNoOpWithoutFuse(t *testing.T) {
	dir := initRepo(t)
	mustGit(t, dir, "config", "cc-notes.autoMount", "true")

	stdout, stderr, err := runCLI(t, dir, "mount", "--auto")
	if err != nil {
		t.Fatalf("mount --auto: %v", err)
	}
	if stdout != "" || stderr != "" {
		t.Errorf("mount --auto output = (stdout %q, stderr %q), want silent", stdout, stderr)
	}
}
