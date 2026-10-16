package sync_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

func slowWhileHolding(t *testing.T, s *store.Store, key, program string) (marker string) {
	t.Helper()
	marker = filepath.Join(t.TempDir(), "holding")
	gittest.Git(t, s.Git.Dir, "config", "remote.origin."+key,
		fmt.Sprintf("if [ -e /dev/fd/3 ]; then : > '%s'; sleep 5; fi; git %s", marker, program))
	return marker
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
	if err := parent.Process.Kill(); err != nil {
		t.Fatalf("kill parent: %v", err)
	}
	var exit *exec.ExitError
	if err := parent.Wait(); !errors.As(err, &exit) || exit.ExitCode() != -1 {
		t.Fatalf("parent Wait = %v, want death by signal; output:\n%s", err, output.String())
	}
}

func assertOrphanHoldsThenReleases(t *testing.T, s *store.Store) {
	t.Helper()
	held, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	if lock, err := s.TrackingLock(held, "origin"); !errors.Is(err, context.DeadlineExceeded) {
		if lock != nil {
			_ = lock.Close()
		}
		t.Fatalf("TrackingLock with the parent dead = %v, want context.DeadlineExceeded while its git child runs", err)
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

func TestSyncFetchKeepsTheTrackingLockPastItsParent(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	note := createNote(t, a, "fetched by an orphan")
	sync(t, a)
	noteRef := refs.For(model.KindNote, note.ID)
	tracking := "refs/cc-notes-sync/origin/" + strings.TrimPrefix(noteRef, "refs/cc-notes/")
	marker := slowWhileHolding(t, b, "uploadpack", "upload-pack")

	killParentWhileHolding(t, b, marker)
	assertOrphanHoldsThenReleases(t, b)

	if got, want := gittest.Git(t, b.Git.Dir, "rev-parse", tracking), gittest.Git(t, bare, "rev-parse", noteRef); got != want {
		t.Fatalf("tracking %s = %s after the orphaned fetch, want the remote tip %s", tracking, got, want)
	}
}

func TestSyncPushKeepsTheTrackingLockPastItsParent(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	note := createNote(t, a, "pushed by an orphan")
	noteRef := refs.For(model.KindNote, note.ID)
	marker := slowWhileHolding(t, a, "receivepack", "receive-pack")

	killParentWhileHolding(t, a, marker)
	assertOrphanHoldsThenReleases(t, a)

	if got, want := gittest.Git(t, bare, "rev-parse", noteRef), gittest.Git(t, a.Git.Dir, "rev-parse", noteRef); got != want {
		t.Fatalf("remote %s = %s after the orphaned push, want the local tip %s", noteRef, got, want)
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
	again, err := a.TrackingLock(t.Context(), "origin")
	if err != nil {
		t.Fatalf("TrackingLock after release: %v", err)
	}
	if err := again.Close(); err != nil {
		t.Fatalf("release second lock: %v", err)
	}
}
