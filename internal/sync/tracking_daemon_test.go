package sync_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
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
	ran, done := filepath.Join(scripts, "ran"), filepath.Join(scripts, "done")
	command := filepath.Join(scripts, "alternate-refs")
	script := fmt.Sprintf("#!/bin/sh\n: > '%s'\nexec 9<&0\n(while [ -d '%s' ] && [ ! -e '%s' ]; do sleep 0.05; done) <&9 >/dev/null 2>&1 &\n", ran, scripts, done)
	if err := os.WriteFile(command, []byte(script), 0o700); err != nil {
		t.Fatalf("write alternate refs command: %v", err)
	}
	gittest.Git(t, b.Git.Dir, "config", "core.alternateRefsCommand", command)
	t.Cleanup(func() { _ = os.WriteFile(done, nil, 0o600) })

	sync(t, b)

	if _, err := os.Stat(ran); err != nil {
		t.Fatalf("the fetch never ran core.alternateRefsCommand: %v", err)
	}
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
