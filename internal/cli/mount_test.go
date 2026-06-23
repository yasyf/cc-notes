package cli_test

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/fusekit/mountd"
)

// fakeHolder serves canned mountd responses over a short /tmp unix socket,
// speaking proto-1 by hand so the CLI's holder driving is pinned independently
// of the real server. respond maps each decoded request to a response; the
// helper stamps the proto and, on a successful OpShutdown, closes the listener
// so the CLI's post-shutdown WaitGone sees the socket go away promptly.
func fakeHolder(t *testing.T, respond func(req mountd.Request) mountd.Response) (socket string, requests func() []mountd.Request) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "ccn-mountd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket = filepath.Join(dir, "m.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	var mu sync.Mutex
	var reqs []mountd.Request
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				var req mountd.Request
				// A bare Available() dial closes without sending; the EOF here
				// keeps those probes out of the recorded requests.
				if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
					return
				}
				mu.Lock()
				reqs = append(reqs, req)
				mu.Unlock()
				resp := respond(req)
				resp.Proto = mountd.MountProtoVersion
				_ = json.NewEncoder(conn).Encode(resp)
				if req.Op == mountd.OpShutdown && resp.OK {
					_ = ln.Close()
				}
			}(conn)
		}
	}()

	requests = func() []mountd.Request {
		mu.Lock()
		defer mu.Unlock()
		return append([]mountd.Request(nil), reqs...)
	}
	return socket, requests
}

// okHolder responds success to every op (an empty List). Its OpHealth reports
// THIS binary's version, so the detached mount path's Converge sees no version
// skew and is the cheap no-op (the production-common case) — a holder reporting
// a different version would route into the retire-and-replace path, which a pure
// test binary cannot respawn through (TestMountDetachedConvergesStaleHolder
// exercises that skew leg deliberately).
func okHolder(req mountd.Request) mountd.Response {
	if req.Op == mountd.OpHealth {
		return mountd.Response{OK: true, Version: version.String()}
	}
	return mountd.Response{OK: true}
}

func TestMountDetachedSucceeds(t *testing.T) {
	repo := initRepo(t)
	sock, requests := fakeHolder(t, okHolder)
	mp := filepath.Join(t.TempDir(), "mnt")

	stdout, _, err := runCLI(t, repo, "mount", "--socket", sock, mp)
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	if got := strings.TrimSpace(stdout); got != mp {
		t.Errorf("stdout = %q, want the mountpoint %q", got, mp)
	}
	var mounts int
	for _, r := range requests() {
		if r.Op == mountd.OpMount {
			mounts++
			if r.Dir != mp {
				t.Errorf("mount Dir = %q, want %q", r.Dir, mp)
			}
			if r.Base == "" || r.Base == r.Dir {
				t.Errorf("mount Base = %q, want the repo root (non-empty, != dir)", r.Base)
			}
		}
	}
	if mounts != 1 {
		t.Errorf("OpMount count = %d, want 1", mounts)
	}
}

// TestMountDetachedConvergesStaleHolder proves the detached mount path converges
// a holder left running at an older cc-notes version before serving: Converge
// sees the version skew and retires the stale holder (OpShutdown). Because a pure
// test binary cannot respawn the successor, the mount then fails with
// ErrCannotHost (exit 1) after a "converge warning" on stderr — a real fuse
// binary would respawn the current version and remount. The full
// replace+remount is covered in fusekit's RemoteHost.Converge tests; this pins
// the cc-notes integration (Converge runs before Setup and fires on skew).
func TestMountDetachedConvergesStaleHolder(t *testing.T) {
	repo := initRepo(t)
	sock, requests := fakeHolder(t, func(req mountd.Request) mountd.Response {
		if req.Op == mountd.OpHealth {
			// A version the current binary will never report, so Converge treats
			// the holder as skewed and retires it.
			return mountd.Response{OK: true, Version: "v0.0.0-stale"}
		}
		return mountd.Response{OK: true}
	})
	mp := filepath.Join(t.TempDir(), "mnt")

	_, stderr, err := runCLI(t, repo, "mount", "--socket", sock, mp)
	if err == nil {
		t.Fatal("mount succeeded against a stale holder a pure binary cannot replace, want a failure")
	}
	if code := cli.ExitCode(err); code != 1 {
		t.Errorf("exit = %d, want 1 (cannot respawn the successor on a pure build); err = %v", code, err)
	}
	var sawShutdown bool
	for _, r := range requests() {
		if r.Op == mountd.OpShutdown {
			sawShutdown = true
		}
	}
	if !sawShutdown {
		t.Error("Converge did not retire the stale-version holder (no OpShutdown sent)")
	}
	if !strings.Contains(stderr, "converge warning") {
		t.Errorf("stderr = %q, want a converge warning for the failed respawn", stderr)
	}
}

func TestMountDetachedIdempotentRemount(t *testing.T) {
	repo := initRepo(t)
	sock, _ := fakeHolder(t, okHolder)
	mp := filepath.Join(t.TempDir(), "mnt")

	for i := range 2 {
		if _, _, err := runCLI(t, repo, "mount", "--socket", sock, mp); err != nil {
			t.Fatalf("mount #%d: %v", i+1, err)
		}
	}
}

func TestMountBusyExits4(t *testing.T) {
	repo := initRepo(t)
	sock, _ := fakeHolder(t, func(req mountd.Request) mountd.Response {
		if req.Op == mountd.OpMount {
			return mountd.Response{OK: false, ErrClass: mountd.ClassBusy, Error: "busy: another op in flight"}
		}
		return okHolder(req)
	})
	mp := filepath.Join(t.TempDir(), "mnt")

	_, _, err := runCLI(t, repo, "mount", "--socket", sock, mp)
	if err == nil {
		t.Fatal("mount succeeded, want a busy conflict")
	}
	if code := cli.ExitCode(err); code != 4 {
		t.Errorf("exit = %d, want 4 (conflict); err = %v", code, err)
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

// TestMountAutoRejectsMountpoint proves --auto is a self-contained mode: it takes
// no MOUNTPOINT and cannot be combined with another mode.
func TestMountAutoRejectsMountpoint(t *testing.T) {
	dir := initRepo(t)
	_, _, err := runCLI(t, dir, "mount", "--auto", filepath.Join(t.TempDir(), "mnt"))
	if cli.ExitCode(err) != 2 {
		t.Fatalf("mount --auto MOUNTPOINT err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}
}

func TestMountHolderDownExits1(t *testing.T) {
	repo := initRepo(t)
	sock := filepath.Join(t.TempDir(), "never-bound.sock")
	mp := filepath.Join(t.TempDir(), "mnt")

	// A pure test binary cannot spawn a holder, so an unreachable socket fails
	// with ErrCannotHost — a plain error (exit 1), never a conflict.
	_, _, err := runCLI(t, repo, "mount", "--socket", sock, mp)
	if err == nil {
		t.Fatal("mount succeeded with no holder, want a failure")
	}
	if code := cli.ExitCode(err); code != 1 {
		t.Errorf("exit = %d, want 1; err = %v", code, err)
	}
}

// TestMountDetachedCreatesStateDir guards the first-run holder spawn: the
// detached path must create cc-notes' state dir (~/.cc-notes) before handing
// off to the holder, because that dir homes the spawn log and the default
// socket, and fusekit treats their parent dirs as the caller's to create. An
// explicit mountpoint never creates ~/.cc-notes on its own, so without this the
// first `cc-notes mount DIR` on a fresh machine dies with "open mount holder
// log: no such file or directory". A pure test binary can't spawn a holder, so
// the mount itself fails (ErrCannotHost) — but the state dir must already exist
// by the time it does.
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
	if _, _, err := runCLI(t, repo, "mount", "--socket", sock, mp); err == nil {
		t.Fatal("mount with no holder succeeded on a pure build, want a failure")
	}
	if _, err := os.Stat(stateDir); err != nil {
		t.Fatalf("detached mount did not create state dir %s: %v", stateDir, err)
	}
}

func TestMountList(t *testing.T) {
	repo := initRepo(t)
	sock, _ := fakeHolder(t, func(req mountd.Request) mountd.Response {
		if req.Op == mountd.OpList {
			return mountd.Response{OK: true, Mounts: []mountd.MountInfo{
				{Dir: "/m/alpha", Base: "/r/alpha", Live: true},
				{Dir: "/m/beta", Base: "/r/beta", Live: false},
			}}
		}
		return okHolder(req)
	})

	stdout, _, err := runCLI(t, repo, "mount", "--socket", sock, "--list")
	if err != nil {
		t.Fatalf("mount --list: %v", err)
	}
	for _, want := range []string{"/m/alpha", "/r/alpha", "live", "/m/beta", "/r/beta", "dead"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("--list output %q missing %q", stdout, want)
		}
	}
}

func TestMountListHolderDownExits1(t *testing.T) {
	repo := initRepo(t)
	sock := filepath.Join(t.TempDir(), "never-bound.sock")

	_, _, err := runCLI(t, repo, "mount", "--socket", sock, "--list")
	if err == nil {
		t.Fatal("--list succeeded with no holder, want a failure")
	}
	if code := cli.ExitCode(err); code != 1 {
		t.Errorf("exit = %d, want 1; err = %v", code, err)
	}
}

func TestMountStopNotMountedNoOp(t *testing.T) {
	repo := initRepo(t)
	sock, requests := fakeHolder(t, okHolder)
	stopDir := filepath.Join(t.TempDir(), "mnt")
	if err := os.MkdirAll(stopDir, 0o700); err != nil {
		t.Fatal(err)
	}

	// Nothing is mounted at stopDir, so Teardown short-circuits to a no-op
	// without ever contacting the holder.
	if _, _, err := runCLI(t, repo, "mount", "--socket", sock, "--stop", stopDir); err != nil {
		t.Fatalf("mount --stop: %v", err)
	}
	if got := requests(); len(got) != 0 {
		t.Errorf("--stop contacted the holder for an unmounted dir: %v", got)
	}
}

func TestMountShutdown(t *testing.T) {
	repo := initRepo(t)
	sock, requests := fakeHolder(t, okHolder)

	if _, _, err := runCLI(t, repo, "mount", "--socket", sock, "--shutdown"); err != nil {
		t.Fatalf("mount --shutdown: %v", err)
	}
	var sawShutdown bool
	for _, r := range requests() {
		if r.Op == mountd.OpShutdown {
			sawShutdown = true
		}
	}
	if !sawShutdown {
		t.Error("--shutdown did not send OpShutdown to the holder")
	}
}

// TestMountDetachedDefaultLinksNotes proves the no-argument detached mount is
// served at the managed default and presented in the repo as a .notes symlink
// into it, kept out of git via .git/info/exclude (never a tracked .gitignore),
// with the .notes path — not the opaque managed path — printed to stdout.
func TestMountDetachedDefaultLinksNotes(t *testing.T) {
	repo := initRepo(t)
	repoRoot := mustGit(t, repo, "rev-parse", "--show-toplevel")
	sock, requests := fakeHolder(t, okHolder)

	stdout, _, err := runCLI(t, repo, "mount", "--socket", sock)
	if err != nil {
		t.Fatalf("mount: %v", err)
	}

	link := filepath.Join(repoRoot, ".notes")
	if got := strings.TrimSpace(stdout); got != link {
		t.Errorf("stdout = %q, want the .notes symlink %q", got, link)
	}
	target, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("readlink %s: %v", link, err)
	}

	var mountDir string
	for _, r := range requests() {
		if r.Op == mountd.OpMount {
			mountDir = r.Dir
		}
	}
	if mountDir == "" {
		t.Fatal("no OpMount sent to the holder")
	}
	if target != mountDir {
		t.Errorf(".notes -> %q, want the managed mountpoint %q", target, mountDir)
	}

	exclude := filepath.Join(repoRoot, ".git", "info", "exclude")
	if data, err := os.ReadFile(exclude); err != nil {
		t.Fatalf("read %s: %v", exclude, err)
	} else if !strings.Contains(string(data), "/.notes") {
		t.Errorf("exclude %q missing /.notes", data)
	}
	if _, err := os.Stat(filepath.Join(repoRoot, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf(".gitignore should not be created; stat err = %v", err)
	}
}

// TestMountStopRemovesNotesSymlink proves `mount --stop .notes` resolves the
// in-repo symlink to the managed mountpoint it points at (so teardown matches
// the holder's registry) and removes the symlink. Nothing is actually mounted,
// so teardown short-circuits locally without contacting the holder.
func TestMountStopRemovesNotesSymlink(t *testing.T) {
	repo := initRepo(t)
	repoRoot := mustGit(t, repo, "rev-parse", "--show-toplevel")
	sock, requests := fakeHolder(t, okHolder)

	mp := filepath.Join(t.TempDir(), "mnt")
	if err := os.MkdirAll(mp, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repoRoot, ".notes")
	if err := os.Symlink(mp, link); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runCLI(t, repo, "mount", "--socket", sock, "--stop", ".notes"); err != nil {
		t.Fatalf("mount --stop .notes: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf(".notes symlink not removed: err=%v", err)
	}
	if got := requests(); len(got) != 0 {
		t.Errorf("--stop contacted the holder for an unmounted target: %v", got)
	}
}

func TestMountFlagConflictsExit2(t *testing.T) {
	repo := initRepo(t)
	for _, args := range [][]string{
		{"mount", "--list", "--shutdown"},
		{"mount", "--list", "somewhere"},
		{"mount", "--shutdown", "--foreground"},
		{"mount", "--stop", "/x", "--list"},
		{"mount", "--stop", "/x", "somewhere"},
	} {
		_, _, err := runCLI(t, repo, args...)
		if err == nil {
			t.Errorf("%v succeeded, want a usage error", args)
			continue
		}
		if code := cli.ExitCode(err); code != 2 {
			t.Errorf("%v exit = %d, want 2 (usage); err = %v", args, code, err)
		}
	}
}
