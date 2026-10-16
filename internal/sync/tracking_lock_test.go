package sync_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
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
            guardian=$(ps -o ppid= -p "$PPID" | tr -d ' ')
            printf '%%s %%s %%s %%s\n' "$PPID" "$$" "$guardian" "$ref" > '%s.tmp'
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

type pausedWriter struct{ git, hook, guardian int }

func awaitPausedWriter(t *testing.T, marker string, abandon func() string) pausedWriter {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("git child never ran holding the tracking lock; %s", abandon())
		}
		time.Sleep(20 * time.Millisecond)
	}
	paused, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read paused writer: %v", err)
	}
	fields := strings.Fields(string(paused))
	if len(fields) != 4 || !strings.HasPrefix(fields[3], "refs/cc-notes-sync/origin/") {
		t.Fatalf("paused transaction = %q, want Git, hook, and guardian PIDs and a source tracking ref", paused)
	}
	pids := make([]int, 3)
	for i, field := range fields[:3] {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 1 {
			t.Fatalf("paused transaction = %q, want three live PIDs", paused)
		}
		pids[i] = pid
	}
	writer := pausedWriter{git: pids[0], hook: pids[1], guardian: pids[2]}
	if writer.guardian == writer.git || writer.guardian == writer.hook || writer.git == writer.hook {
		t.Fatalf("paused transaction = %q, want distinct Git, hook, and guardian PIDs", paused)
	}
	t.Logf("Git writer %d under guardian %d paused while preparing %s", writer.git, writer.guardian, fields[3])
	return writer
}

func trackingLockPath(t *testing.T, s *store.Store) string {
	t.Helper()
	lock, err := s.TrackingLock(t.Context(), "origin")
	if err != nil {
		t.Fatalf("TrackingLock to name the lock file: %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("close tracking lock: %v", err)
	}
	return lock.Name()
}

func assertLockHolders(t *testing.T, path string, want ...int) {
	t.Helper()
	lsof, err := exec.LookPath("lsof")
	if err != nil {
		t.Fatalf("lsof lists the tracking lock's holders: %v", err)
	}
	//nolint:gosec // G204: lists the processes holding this test's own lock file with a fixed argv.
	out, err := exec.Command(lsof, "-t", "--", path).Output()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		t.Fatalf("lsof -t -- %s: %v", path, err)
	}
	fields := strings.Fields(string(out))
	got := make([]int, 0, len(fields))
	for _, field := range fields {
		pid, err := strconv.Atoi(field)
		if err != nil {
			t.Fatalf("lsof -t -- %s listed %q, want PIDs", path, out)
		}
		got = append(got, pid)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("processes holding %s = %v, want exactly %v", path, got, want)
	}
}

func killParentWhileHolding(t *testing.T, s *store.Store, marker, lockPath string) {
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
	writer := awaitPausedWriter(t, marker, func() string {
		_ = parent.Process.Kill()
		_ = parent.Wait()
		return "parent output:\n" + output.String()
	})
	if writer.guardian == parent.Process.Pid {
		t.Fatalf("paused Git %d runs directly under parent %d, want a guardian between them", writer.git, parent.Process.Pid)
	}
	assertLockHolders(t, lockPath, parent.Process.Pid, writer.guardian)
	if err := parent.Process.Kill(); err != nil {
		t.Fatalf("kill parent: %v", err)
	}
	var exit *exec.ExitError
	if err := parent.Wait(); !errors.As(err, &exit) || exit.ExitCode() != -1 {
		t.Fatalf("parent Wait = %v, want death by signal; output:\n%s", err, output.String())
	}
	assertLockHolders(t, lockPath, writer.guardian)
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
			lockPath := trackingLockPath(t, b)
			marker, release := slowWhileHolding(t, b)

			killParentWhileHolding(t, b, marker, lockPath)
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
			lockPath := trackingLockPath(t, a)
			marker, release := slowWhileHolding(t, a)

			killParentWhileHolding(t, a, marker, lockPath)
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

func termIgnoringGit(t *testing.T) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("LookPath(git): %v", err)
	}
	dir := t.TempDir()
	stub := fmt.Sprintf("#!/bin/sh\ntrap '' TERM\nexec %q \"$@\"\n", realGit)
	//nolint:gosec // G306: the git stub must be executable (0o755) for the test's PATH override to run it.
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(stub), 0o755); err != nil {
		t.Fatalf("write git stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestSyncCancelReapsItsGitWriterBeforeFreeingTheTrackingLock(t *testing.T) {
	tests := []struct {
		name string
		git  func(t *testing.T)
	}{
		{"native git", func(*testing.T) {}},
		{"git ignoring SIGTERM", termIgnoringGit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bare := gittest.InitBare(t)
			a := clone(t, bare, "Alice", "alice@example.com")
			b := clone(t, bare, "Bob", "bob@example.com")
			createNote(t, a, "fetched by a cancelled sync")
			sync(t, a)
			marker, release := slowWhileHolding(t, b)
			tt.git(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			synced := make(chan error, 1)
			go func() {
				_, err := ccsync.Sync(ctx, b, "origin", false)
				synced <- err
			}()

			writer := awaitPausedWriter(t, marker, func() string {
				cancel()
				select {
				case err := <-synced:
					return fmt.Sprintf("Sync returned %v", err)
				default:
					return "Sync still running"
				}
			})
			cancel()

			select {
			case err := <-synced:
				if err == nil {
					t.Fatal("Sync cancelled mid-fetch returned nil, want an error")
				}
			case <-time.After(30 * time.Second):
				t.Fatal("Sync never returned after its context was cancelled mid-fetch")
			}
			if err := syscall.Kill(writer.git, 0); !errors.Is(err, syscall.ESRCH) {
				t.Fatalf("kill(%d, 0) after the cancelled Sync returned = %v, want ESRCH: its Git writer reaped", writer.git, err)
			}
			assertTrackingLockFree(t, b, "the cancelled transaction's hook")
			if got := gittest.Git(t, b.Git.Dir, "for-each-ref", "--format=%(refname)", "refs/cc-notes-sync/"); got != "" {
				t.Fatalf("tracking refs %q exist after the fetch writing them was cancelled at prepared", got)
			}
			if err := os.WriteFile(release, nil, 0o600); err != nil {
				t.Fatalf("release the orphaned hook: %v", err)
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
