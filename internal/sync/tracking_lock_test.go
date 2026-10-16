package sync_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
)

const lockParentEnv = "CC_NOTES_TEST_TRACKING_LOCK_PARENT"

func TestTrackingLockParentProcess(t *testing.T) {
	dir := os.Getenv(lockParentEnv)
	if dir == "" {
		t.Skip("runs only as the parent the tracking-lock tests kill mid-sync")
	}
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	_, err = ccsync.Sync(t.Context(), s, "origin", false)
	t.Fatalf("Sync returned before its parent was killed: %v", err)
}

func slowWhileHolding(t *testing.T, s *store.Store) (marker, release string) {
	t.Helper()
	dir := t.TempDir()
	marker, release = filepath.Join(dir, "holding"), filepath.Join(dir, "release")
	hook := fmt.Sprintf(`#!/bin/sh
[ "$1" = prepared ] || exit 0
while read -r old new ref; do
    case "$ref" in
        refs/cc-notes-sync/origin/*)
            printf '%%s %%s\n' "$PPID" "$ref" > '%s.tmp'
            mv '%s.tmp' '%s'
            while [ ! -e '%s' ]; do sleep 0.02; done
            exit 0
            ;;
    esac
done
`, marker, marker, marker, release)
	if err := os.WriteFile(filepath.Join(dir, "reference-transaction"), []byte(hook), 0o755); err != nil {
		t.Fatalf("write reference-transaction hook: %v", err)
	}
	gittest.Git(t, s.Git.Dir, "config", "core.hooksPath", dir)
	t.Cleanup(func() { _ = os.WriteFile(release, nil, 0o600) })
	return marker, release
}

func killParentWhileHolding(t *testing.T, s *store.Store, marker string) {
	t.Helper()
	var output bytes.Buffer
	//nolint:gosec // G204: re-executes this test binary as the parent process under test.
	parent := exec.Command(os.Args[0], "-test.run=^TestTrackingLockParentProcess$")
	parent.Env = append(os.Environ(), lockParentEnv+"="+s.Git.Dir)
	parent.Stdout, parent.Stderr = &output, &output
	if err := parent.Start(); err != nil {
		t.Fatalf("start parent: %v", err)
	}
	t.Cleanup(func() {
		if parent.ProcessState == nil {
			_ = parent.Process.Kill()
			_ = parent.Wait()
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = parent.Process.Kill()
			_ = parent.Wait()
			t.Fatalf("git child never ran holding the tracking lock; parent output:\n%s", output.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	paused, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read paused writer: %v", err)
	}
	writer := strings.Fields(string(paused))
	if len(writer) != 2 || !strings.HasPrefix(writer[1], "refs/cc-notes-sync/origin/") {
		t.Fatalf("paused transaction = %q, want Git PID and source tracking ref", paused)
	}
	pid, err := strconv.Atoi(writer[0])
	if err != nil || pid <= 0 || pid == parent.Process.Pid {
		t.Fatalf("paused writer PID = %q, want a Git child of parent %d", writer[0], parent.Process.Pid)
	}
	t.Logf("Git writer %d paused while preparing %s", pid, writer[1])
	if err := parent.Process.Kill(); err != nil {
		t.Fatalf("kill parent: %v", err)
	}
	var exit *exec.ExitError
	if err := parent.Wait(); !errors.As(err, &exit) || exit.ExitCode() != -1 {
		t.Fatalf("parent Wait = %v, want death by signal; output:\n%s", err, output.String())
	}
}

func assertOrphanHoldsThenReleases(t *testing.T, s *store.Store, release string) {
	t.Helper()
	held, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if lock, err := s.TrackingLock(held, "origin"); !errors.Is(err, context.DeadlineExceeded) {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("TrackingLock with the parent dead = %v, want context.DeadlineExceeded while its git writer is paused", err)
	}
	if got := gittest.Git(t, s.Git.Dir, "for-each-ref", "--format=%(refname)", "refs/cc-notes-sync/"); got != "" {
		t.Fatalf("tracking refs %q visible while their transaction is paused at prepared", got)
	}
	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("release prepared tracking transaction: %v", err)
	}
	released, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	lock, err := s.TrackingLock(released, "origin")
	if err != nil {
		t.Fatalf("TrackingLock once the git child exits: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("close tracking lock: %v", err)
	}
}

var transports = []struct {
	name string
	url  func(t *testing.T, bare string) string
}{
	{"file", func(_ *testing.T, bare string) string { return bare }},
	{"http", func(t *testing.T, bare string) string { return serveGit(t, bare, nil) }},
}

func TestSyncFetchKeepsTheTrackingLockPastItsParent(t *testing.T) {
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			bare := gittest.InitBare(t)
			a := clone(t, bare, "Alice", "alice@example.com")
			b := clone(t, bare, "Bob", "bob@example.com")
			note := createNote(t, a, "fetched by an orphan")
			sync(t, a)
			gittest.Git(t, b.Git.Dir, "remote", "set-url", "origin", tr.url(t, bare))
			noteRef := refs.For(model.KindNote, note.ID)
			tracking := "refs/cc-notes-sync/origin/" + strings.TrimPrefix(noteRef, "refs/cc-notes/")
			marker, release := slowWhileHolding(t, b)

			killParentWhileHolding(t, b, marker)
			assertOrphanHoldsThenReleases(t, b, release)

			if got, want := gittest.Git(t, b.Git.Dir, "rev-parse", tracking), gittest.Git(t, bare, "rev-parse", noteRef); got != want {
				t.Fatalf("tracking %s = %s after the orphaned fetch, want the remote tip %s", tracking, got, want)
			}
		})
	}
}

func TestSyncPushKeepsTheTrackingLockPastItsParent(t *testing.T) {
	for _, tr := range transports {
		t.Run(tr.name, func(t *testing.T) {
			bare := gittest.InitBare(t)
			a := clone(t, bare, "Alice", "alice@example.com")
			gittest.Git(t, a.Git.Dir, "remote", "set-url", "origin", tr.url(t, bare))
			note := createNote(t, a, "pushed by an orphan")
			noteRef := refs.For(model.KindNote, note.ID)
			if _, err := ccsync.Install(t.Context(), a.Git, "origin"); err != nil {
				t.Fatalf("Install source tracking refspec: %v", err)
			}
			marker, release := slowWhileHolding(t, a)

			killParentWhileHolding(t, a, marker)
			assertOrphanHoldsThenReleases(t, a, release)

			if got, want := gittest.Git(t, bare, "rev-parse", noteRef), gittest.Git(t, a.Git.Dir, "rev-parse", noteRef); got != want {
				t.Fatalf("remote %s = %s after the orphaned push, want the local tip %s", noteRef, got, want)
			}
			tracking := "refs/cc-notes-sync/origin/" + strings.TrimPrefix(noteRef, "refs/cc-notes/")
			if got, want := gittest.Git(t, a.Git.Dir, "rev-parse", tracking), gittest.Git(t, a.Git.Dir, "rev-parse", noteRef); got != want {
				t.Fatalf("source tracking %s = %s after the orphaned push, want %s", tracking, got, want)
			}
		})
	}
}

func TestTrackingLockIsSharedByLinkedWorktrees(t *testing.T) {
	bare := gittest.InitBare(t)
	primary := clone(t, bare, "Alice", "alice@example.com")
	gittest.Git(t, primary.Git.Dir, "commit", "--allow-empty", "-q", "-m", "base")
	linkedDir := filepath.Join(t.TempDir(), "linked")
	gittest.Git(t, primary.Git.Dir, "worktree", "add", "-q", linkedDir)
	linked, err := store.Open(linkedDir)
	if err != nil {
		t.Fatalf("Open(%s): %v", linkedDir, err)
	}
	linkedInfo, err := os.Stat(linked.RecordsCommonDir())
	if err != nil {
		t.Fatalf("stat linked RecordsCommonDir: %v", err)
	}
	primaryInfo, err := os.Stat(primary.RecordsCommonDir())
	if err != nil {
		t.Fatalf("stat primary RecordsCommonDir: %v", err)
	}
	if !os.SameFile(linkedInfo, primaryInfo) {
		t.Fatalf("linked RecordsCommonDir %s is not the primary checkout's %s", linked.RecordsCommonDir(), primary.RecordsCommonDir())
	}
	held, err := primary.TrackingLock(t.Context(), "origin")
	if err != nil {
		t.Fatalf("primary TrackingLock: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if lock, err := linked.TrackingLock(ctx, "origin"); !errors.Is(err, context.DeadlineExceeded) {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("linked TrackingLock while primary holds it = %v, want context.DeadlineExceeded", err)
	}
	if err := held.Close(); err != nil {
		t.Fatalf("release primary lock: %v", err)
	}
	again, err := linked.TrackingLock(t.Context(), "origin")
	if err != nil {
		t.Fatalf("linked TrackingLock after release: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Fatalf("release linked lock: %v", err)
	}
}

func TestTrackingLockGivesEachRemoteNameOneBoundedFile(t *testing.T) {
	bare := gittest.InitBare(t)
	s := clone(t, bare, "Alice", "alice@example.com")
	long := strings.Repeat("r", 250)
	gittest.Git(t, s.Git.Dir, "check-ref-format", "refs/cc-notes-sync/"+long+"/notes/0123")
	remotes := []string{"origin", long, "team/upstream", "team%2Fupstream", "team-upstream", "team_upstream"}
	held := make([]*os.File, 0, len(remotes))
	t.Cleanup(func() {
		for _, lock := range held {
			_ = lock.Close()
		}
	})
	files := map[string]string{}
	for _, remote := range remotes {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		lock, err := s.TrackingLock(ctx, remote)
		cancel()
		if err != nil {
			t.Fatalf("TrackingLock(%q) while %d other remotes hold their locks: %v", remote, len(held), err)
		}
		held = append(held, lock)
		name := filepath.Base(lock.Name())
		if len(name) > 255 {
			t.Fatalf("TrackingLock(%q) file name is %d bytes, over NAME_MAX 255", remote, len(name))
		}
		if other, shared := files[name]; shared {
			t.Fatalf("remotes %q and %q share lock file %s", other, remote, name)
		}
		files[name] = remote
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if lock, err := s.TrackingLock(ctx, long); !errors.Is(err, context.DeadlineExceeded) {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("second TrackingLock for the %d-char remote while held = %v, want context.DeadlineExceeded", len(long), err)
	}
}

func TestTrackingLockWaitFailsWhenItsContextEnds(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	held, err := a.TrackingLock(t.Context(), "origin")
	if err != nil {
		t.Fatalf("first TrackingLock: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if lock, err := a.TrackingLock(ctx, "origin"); !errors.Is(err, context.DeadlineExceeded) {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("contended TrackingLock = %v, want context.DeadlineExceeded", err)
	}
	if err := held.Close(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}
	cancelled, cancelNow := context.WithCancel(t.Context())
	cancelNow()
	if lock, err := a.TrackingLock(cancelled, "origin"); !errors.Is(err, context.Canceled) {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("TrackingLock with a cancelled context and a free lock = %v, want context.Canceled", err)
	}
	again, err := a.TrackingLock(t.Context(), "origin")
	if err != nil {
		t.Fatalf("TrackingLock after release: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Fatalf("release second lock: %v", err)
	}
}
