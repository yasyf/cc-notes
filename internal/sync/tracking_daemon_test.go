package sync_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
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

func serveGit(t *testing.T, bare string, authenticate func(*http.Request) bool) string {
	t.Helper()
	gittest.Git(t, bare, "config", "http.receivepack", "true")
	backend := &cgi.Handler{
		Path:       filepath.Join(gittest.Git(t, bare, "--exec-path"), "git-http-backend"),
		Env:        []string{"GIT_PROJECT_ROOT=" + filepath.Dir(bare), "GIT_HTTP_EXPORT_ALL=1"},
		InheritEnv: []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_SYSTEM", "GIT_CONFIG_NOSYSTEM", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if authenticate != nil && !authenticate(r) {
			w.Header().Set("WWW-Authenticate", `Basic realm="cc-notes"`)
			http.Error(w, "authentication required", http.StatusUnauthorized)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server.URL + "/" + filepath.Base(bare)
}

func requirePushCredentials(r *http.Request) bool {
	if r.URL.Query().Get("service") != "git-receive-pack" && !strings.HasSuffix(r.URL.Path, "/git-receive-pack") {
		return true
	}
	user, password, ok := r.BasicAuth()
	return ok && user == "alice" && password == "secret"
}

func coldCredentialCache(t *testing.T, s *store.Store) (cached func() string) {
	t.Helper()
	remote, err := url.Parse(gittest.Git(t, s.Git.Dir, "remote", "get-url", "origin"))
	if err != nil {
		t.Fatalf("parse origin url: %v", err)
	}
	socketDir, err := os.MkdirTemp("", "ccn-cache")
	if err != nil {
		t.Fatalf("socket dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socket := filepath.Join(socketDir, "socket")
	credentials := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(credentials, []byte("http://alice:secret@"+remote.Host+"\n"), 0o600); err != nil {
		t.Fatalf("write credentials: %v", err)
	}
	gittest.Git(t, s.Git.Dir, "config", "credential.helper", "cache --socket "+socket)
	gittest.Git(t, s.Git.Dir, "config", "--add", "credential.helper", "store --file "+credentials)
	t.Cleanup(func() { gittest.Git(t, s.Git.Dir, "credential-cache", "--socket", socket, "exit") })
	return func() string {
		//nolint:gosec // G204: queries this test's own credential-cache daemon with a fixed argv.
		query := exec.Command("git", "-C", s.Git.Dir, "credential-cache", "--socket", socket, "get")
		query.Stdin = strings.NewReader("protocol=http\nhost=" + remote.Host + "\n\n")
		out, err := query.Output()
		if err != nil {
			t.Fatalf("credential-cache get: %v", err)
		}
		for line := range strings.Lines(string(out)) {
			if password, ok := strings.CutPrefix(strings.TrimSpace(line), "password="); ok {
				return password
			}
		}
		return ""
	}
}

func assertTrackingLockFree(t *testing.T, s *store.Store, survivor string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	lock, err := s.TrackingLock(ctx, "origin")
	if err != nil {
		t.Fatalf("TrackingLock after the git writer exited, with %s still running = %v, want the lock free", survivor, err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("close tracking lock: %v", err)
	}
}

func TestSyncPushFreesTheTrackingLockWhileTheCredentialDaemonItStartedLives(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	gittest.Git(t, a.Git.Dir, "remote", "set-url", "origin", serveGit(t, bare, requirePushCredentials))
	cached := coldCredentialCache(t, a)
	if got := cached(); got != "" {
		t.Fatalf("credential cache holds %q before the push, want no daemon running", got)
	}
	createNote(t, a, "pushed with cached credentials")

	sync(t, a)

	if got := cached(); got != "secret" {
		t.Fatalf("credential cache holds %q after the push, want the daemon the push started holding secret", got)
	}
	assertTrackingLockFree(t, a, "the credential-cache daemon")
}

func TestPruneFreesTheTrackingLockWhileTheCredentialDaemonItStartedLives(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	note := createNote(t, a, "pruned with cached credentials")
	sync(t, a)
	ref := refs.For(model.KindNote, note.ID)
	appendOps(t, a, ref, model.DeleteNote{})
	gittest.Git(t, a.Git.Dir, "remote", "set-url", "origin", serveGit(t, bare, requirePushCredentials))
	cached := coldCredentialCache(t, a)
	if got := cached(); got != "" {
		t.Fatalf("credential cache holds %q before the prune, want no daemon running", got)
	}

	pruned, failed, err := a.PruneTombstones(t.Context(), "origin")
	if err != nil || pruned != 1 || failed != 0 {
		t.Fatalf("PruneTombstones = %d pruned, %d failed, %v; want 1, 0, nil", pruned, failed, err)
	}

	if got := gittest.Git(t, bare, "for-each-ref", "--format=%(refname)", ref); got != "" {
		t.Fatalf("remote still has %s after the prune", got)
	}
	if got := cached(); got != "secret" {
		t.Fatalf("credential cache holds %q after the prune, want the daemon the delete started holding secret", got)
	}
	assertTrackingLockFree(t, a, "the credential-cache daemon")
}

func TestSyncPushFreesTheTrackingLockWhileADaemonTheReceiverStartedLives(t *testing.T) {
	bare := gittest.InitBare(t)
	hooks := t.TempDir()
	started, done := filepath.Join(hooks, "started"), filepath.Join(hooks, "done")
	hook := fmt.Sprintf("#!/bin/sh\nexec 9<&0\n: > '%s'\n(while [ -d '%s' ] && [ ! -e '%s' ]; do sleep 0.05; done) <&9 >/dev/null 2>&1 &\n", started, hooks, done)
	if err := os.WriteFile(filepath.Join(hooks, "post-receive"), []byte(hook), 0o700); err != nil {
		t.Fatalf("write post-receive hook: %v", err)
	}
	gittest.Git(t, bare, "config", "core.hooksPath", hooks)
	t.Cleanup(func() { _ = os.WriteFile(done, nil, 0o600) })
	a := clone(t, bare, "Alice", "alice@example.com")
	createNote(t, a, "pushed to a receiver that starts a daemon")

	sync(t, a)

	if _, err := os.Stat(started); err != nil {
		t.Fatalf("the receiver's post-receive hook never ran: %v", err)
	}
	assertTrackingLockFree(t, a, "the receiver's daemon")
}

func leakSnapshot(pidFile string) string {
	ps, err := exec.Command("ps", "-axo", "pid,ppid,command").CombinedOutput()
	snapshot := fmt.Sprintf("ps -axo pid,ppid,command (%v):\n%s", err, ps)
	recorded, err := os.ReadFile(pidFile)
	if err != nil {
		return snapshot + fmt.Sprintf("\nno daemon PID recorded: %v", err)
	}
	pid := strings.TrimSpace(string(recorded))
	//nolint:gosec // G204: lists the open files of this test's own daemon with a fixed argv.
	lsof, err := exec.Command("lsof", "-p", pid).CombinedOutput()
	return snapshot + fmt.Sprintf("\nlsof -p %s (%v):\n%s", pid, err, lsof)
}

func assertDaemonAlive(t *testing.T, pidFile, when string) {
	t.Helper()
	recorded, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("read daemon PID %s: %v", when, err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(recorded)))
	if err != nil || pid <= 1 {
		t.Fatalf("daemon PID = %q, want a live PID", recorded)
	}
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("kill(%d, 0) %s = %v, want the alternate refs command's daemon still alive", pid, when, err)
	}
}

func TestSyncFetchFreesTheTrackingLockWhileADaemonTheAlternateRefsCommandStartedLives(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	createNote(t, a, "fetched past an alternate whose refs command starts a daemon")
	sync(t, a)
	alternate := gittest.InitBare(t)
	info := filepath.Join(b.RecordsCommonDir(), "objects", "info")
	if err := os.MkdirAll(info, 0o750); err != nil {
		t.Fatalf("mkdir objects/info: %v", err)
	}
	if err := os.WriteFile(filepath.Join(info, "alternates"), []byte(filepath.Join(alternate, "objects")+"\n"), 0o600); err != nil {
		t.Fatalf("write objects/info/alternates: %v", err)
	}
	scripts := t.TempDir()
	ran, done, daemon := filepath.Join(scripts, "ran"), filepath.Join(scripts, "done"), filepath.Join(scripts, "daemon")
	command := filepath.Join(scripts, "alternate-refs")
	script := fmt.Sprintf(`#!/bin/sh
: > '%s'
exec 9<&0
( exec 3>&- 4>&- 5>&- 6>&- 7>&- 8>&-; while [ -d '%s' ] && [ ! -e '%s' ]; do sleep 0.05; done ) <&9 9<&- >/dev/null 2>&1 &
echo $! > '%s'
`, ran, scripts, done, daemon)
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatalf("write alternate refs command: %v", err)
	}
	gittest.Git(t, b.Git.Dir, "config", "core.alternateRefsCommand", command)
	t.Cleanup(func() { _ = os.WriteFile(done, nil, 0o600) })

	unheld, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	//nolint:gosec // G204: runs the unheld native fetch this fixture must not block, with a test-controlled argv.
	fetch := exec.CommandContext(unheld, "git", "-C", b.Git.Dir, "fetch", "--recurse-submodules=no", "--refmap=", "origin", "+refs/cc-notes/*:refs/cc-notes-sync/origin/*")
	fetch.WaitDelay = time.Second
	out, err := fetch.CombinedOutput()
	if unheld.Err() != nil {
		t.Fatalf("unheld git fetch still running after 30s: the fixture's daemon blocks git itself\n%s", leakSnapshot(daemon))
	}
	if err != nil {
		t.Fatalf("unheld git fetch: %v\n%s", err, out)
	}
	if _, err := os.Stat(ran); err != nil {
		t.Fatalf("the unheld fetch never ran core.alternateRefsCommand: %v", err)
	}
	assertDaemonAlive(t, daemon, "after the unheld fetch exited")
	if err := os.Remove(ran); err != nil {
		t.Fatalf("reset the alternate refs marker: %v", err)
	}
	createNote(t, a, "fetched by the held sync past the same alternate")
	sync(t, a)

	held, cancelHeld := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancelHeld()
	_, err = ccsync.Sync(held, b, "origin", false)
	if held.Err() != nil {
		t.Fatalf("held Sync still running after 60s\n%s", leakSnapshot(daemon))
	}
	if err != nil {
		t.Fatalf("held Sync(%s): %v", b.Git.Dir, err)
	}

	if _, err := os.Stat(ran); err != nil {
		t.Fatalf("the held fetch never ran core.alternateRefsCommand: %v", err)
	}
	assertDaemonAlive(t, daemon, "after the held Sync returned")
	assertTrackingLockFree(t, b, "the alternate refs command's daemon")
}

func TestSyncFetchFreesTheTrackingLockWhileAHookDaemonHoldsGitsStderr(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	createNote(t, a, "fetched under a hook whose daemon keeps git's stderr")
	sync(t, a)
	hooks := t.TempDir()
	started, done := filepath.Join(hooks, "started"), filepath.Join(hooks, "done")
	hook := fmt.Sprintf(`#!/bin/sh
[ "$1" = committed ] || exit 0
while read -r old new ref; do
    case "$ref" in
        refs/cc-notes-sync/origin/*)
            : > '%s'
            exec 9<&0
            ( while [ -d '%s' ] && [ ! -e '%s' ]; do sleep 0.05; done ) <&9 >/dev/null &
            exit 0
            ;;
    esac
done
`, started, hooks, done)
	if err := os.WriteFile(filepath.Join(hooks, "reference-transaction"), []byte(hook), 0o700); err != nil {
		t.Fatalf("write reference-transaction hook: %v", err)
	}
	gittest.Git(t, b.Git.Dir, "config", "core.hooksPath", hooks)
	t.Cleanup(func() { _ = os.WriteFile(done, nil, 0o600) })

	sync(t, b)

	if _, err := os.Stat(started); err != nil {
		t.Fatalf("the held fetch's reference-transaction hook never started its daemon: %v", err)
	}
	assertTrackingLockFree(t, b, "the hook daemon holding git's stderr")
}

func TestSyncFetchOutputEndsWithGitNotWithAHookLogger(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	createNote(t, a, "fetched under a hook whose logger keeps writing git's stderr")
	sync(t, a)
	hooks := t.TempDir()
	logger := filepath.Join(hooks, "logger")
	hook := fmt.Sprintf(`#!/bin/sh
[ "$1" = committed ] || exit 0
while read -r old new ref; do
    case "$ref" in
        refs/cc-notes-sync/origin/*)
            [ -e '%s' ] && exit 0
            ( while [ -d '%s' ]; do printf x >&2 || exit 0; sleep 0.05; done ) </dev/null >/dev/null &
            echo "$!" > '%s'
            exit 0
            ;;
    esac
done
`, logger, hooks, logger)
	if err := os.WriteFile(filepath.Join(hooks, "reference-transaction"), []byte(hook), 0o700); err != nil {
		t.Fatalf("write reference-transaction hook: %v", err)
	}
	gittest.Git(t, b.Git.Dir, "config", "core.hooksPath", hooks)

	sync(t, b)

	assertTrackingLockFree(t, b, "the hook's stderr logger")
	recorded, err := os.ReadFile(logger)
	if err != nil {
		t.Fatalf("the held fetch's reference-transaction hook never started its logger: %v", err)
	}
	assertExitsOnItsOwn(t, recorded, "hook logger")
}

func assertExitsOnItsOwn(t *testing.T, recorded []byte, what string) {
	t.Helper()
	pid, err := strconv.Atoi(strings.TrimSpace(string(recorded)))
	if err != nil || pid <= 1 {
		t.Fatalf("%s PID = %q, want a live PID", what, recorded)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s %d still alive 10s after Sync returned (kill(pid, 0) = %v), want it gone once git's stderr closed", what, pid, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestSyncFetchOutputStopsAtGitsExitWhileAHookWriterNeverPauses(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	note := createNote(t, a, "fetched under a hook whose writer floods git's stderr")
	sync(t, a)
	noteRef := refs.For(model.KindNote, note.ID)
	tracking := "refs/cc-notes-sync/origin/" + strings.TrimPrefix(noteRef, "refs/cc-notes/")
	lockPath := trackingLockPath(t, b)
	hooks := t.TempDir()
	writer := filepath.Join(hooks, "writer")
	hook := fmt.Sprintf(`#!/bin/sh
[ "$1" = committed ] || exit 0
while read -r old new ref; do
    case "$ref" in
        refs/cc-notes-sync/origin/*)
            [ -e '%s' ] && exit 0
            sh -c 'while [ -d "%s" ]; do printf x >&2 || exit 0; done' </dev/null >/dev/null &
            echo "$!" > '%s'
            exit 0
            ;;
    esac
done
`, writer, hooks, writer)
	if err := os.WriteFile(filepath.Join(hooks, "reference-transaction"), []byte(hook), 0o700); err != nil {
		t.Fatalf("write reference-transaction hook: %v", err)
	}
	gittest.Git(t, b.Git.Dir, "config", "core.hooksPath", hooks)

	sync(t, b)

	//nolint:gosec // G304: the lock is this test's own records repository file, named by TrackingLock.
	lock, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open tracking lock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock(LOCK_EX|LOCK_NB) on the tracking lock right after Sync returned = %v, want it free", err)
	}
	if got, want := gittest.Git(t, b.Git.Dir, "rev-parse", tracking), gittest.Git(t, bare, "rev-parse", noteRef); got != want {
		t.Fatalf("tracking %s = %s after the fetch, want the remote tip %s", tracking, got, want)
	}
	recorded, err := os.ReadFile(writer)
	if err != nil {
		t.Fatalf("the held fetch's reference-transaction hook never started its writer: %v", err)
	}
	assertExitsOnItsOwn(t, recorded, "hook writer")
}
