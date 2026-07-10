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
	"github.com/yasyf/fusekit/mountd"
)

// fakeHolder serves canned mountd responses over a short /tmp unix socket,
// speaking proto-1 by hand so the CLI's holder driving is pinned independently
// of the real server. respond maps each decoded request to a response; the
// helper stamps the proto onto every reply.
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

// okHolder responds success to every op (an empty List/Reclaim). It stands in
// for a live holder that accepts each request: the detached mount path drives it
// with no version negotiation, since the holder owns its own upgrade lifecycle.
func okHolder(req mountd.Request) mountd.Response {
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
			if r.Owner != "cc-notes" {
				t.Errorf("mount Owner = %q, want %q so the shared holder scopes cc-notes' mounts", r.Owner, "cc-notes")
			}
		}
	}
	if mounts != 1 {
		t.Errorf("OpMount count = %d, want 1", mounts)
	}
}

// TestMountDetachedNoConverge proves the detached mount path no longer converges
// a version-skewed holder: it just mounts. A holder reporting a version the
// current binary will never match is used as-is — the shared cask holder owns its
// own upgrade lifecycle — so the mount succeeds and the CLI sends no OpHealth,
// OpShutdown, or OpReclaim (the retired converge dance). Only OpMount is sent.
func TestMountDetachedNoConverge(t *testing.T) {
	repo := initRepo(t)
	sock, requests := fakeHolder(t, func(req mountd.Request) mountd.Response {
		if req.Op == mountd.OpHealth {
			return mountd.Response{OK: true, Version: "v0.0.0-stale"}
		}
		return mountd.Response{OK: true}
	})
	mp := filepath.Join(t.TempDir(), "mnt")

	if _, _, err := runCLI(t, repo, "mount", "--socket", sock, mp); err != nil {
		t.Fatalf("mount against a skewed holder should just mount, got: %v", err)
	}
	for _, r := range requests() {
		switch r.Op {
		case mountd.OpHealth, mountd.OpShutdown, mountd.OpReclaim:
			t.Errorf("detached mount sent %s; the converge dance must be gone", r.Op)
		}
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

// TestMountAutoRejectsMountpoint proves --auto is a self-contained mode: it takes
// no MOUNTPOINT and cannot be combined with another mode.
func TestMountAutoRejectsMountpoint(t *testing.T) {
	dir := initRepo(t)
	_, _, err := runCLI(t, dir, "mount", "--auto", filepath.Join(t.TempDir(), "mnt"))
	if cli.ExitCode(err) != 2 {
		t.Fatalf("mount --auto MOUNTPOINT err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}
}

func TestMountList(t *testing.T) {
	repo := initRepo(t)
	sock, requests := fakeHolder(t, func(req mountd.Request) mountd.Response {
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
	// The listing is owner-scoped so the shared holder never leaks another
	// tenant's mounts into cc-notes' --list.
	var sawList bool
	for _, r := range requests() {
		if r.Op == mountd.OpList {
			sawList = true
			if r.Owner != "cc-notes" {
				t.Errorf("--list Owner = %q, want %q (owner-scoped)", r.Owner, "cc-notes")
			}
		}
	}
	if !sawList {
		t.Error("--list sent no OpList to the holder")
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

// TestMountShutdownReclaims proves `mount --shutdown` reclaims cc-notes' OWN
// mounts (per-owner OpReclaim) rather than stopping the holder: the shared cask
// holder hosts other tenants, so a cross-owner OpShutdown would tear their
// mounts out. It must send OpReclaim scoped to Owner "cc-notes" and NEVER
// OpShutdown.
func TestMountShutdownReclaims(t *testing.T) {
	repo := initRepo(t)
	sock, requests := fakeHolder(t, okHolder)

	if _, _, err := runCLI(t, repo, "mount", "--socket", sock, "--shutdown"); err != nil {
		t.Fatalf("mount --shutdown: %v", err)
	}
	var sawReclaim bool
	for _, r := range requests() {
		if r.Op == mountd.OpShutdown {
			t.Error("--shutdown sent OpShutdown; it must reclaim per-owner and leave the shared holder running")
		}
		if r.Op == mountd.OpReclaim {
			sawReclaim = true
			if r.Owner != "cc-notes" {
				t.Errorf("reclaim Owner = %q, want %q so only cc-notes' mounts are torn down", r.Owner, "cc-notes")
			}
		}
	}
	if !sawReclaim {
		t.Error("--shutdown did not send OpReclaim to the holder")
	}
}

// TestMountShutdownReclaimWedgedExits1 proves a reclaim that leaves a mount
// wedged (the holder reports it in the failed set) surfaces ErrUnmountWedged and
// exits 1 — never a false success.
func TestMountShutdownReclaimWedgedExits1(t *testing.T) {
	repo := initRepo(t)
	sock, _ := fakeHolder(t, func(req mountd.Request) mountd.Response {
		if req.Op == mountd.OpReclaim {
			return mountd.Response{OK: true, Mounts: []mountd.MountInfo{
				{Dir: "/m/wedged", Base: "/r/wedged", Live: true},
			}}
		}
		return okHolder(req)
	})

	_, _, err := runCLI(t, repo, "mount", "--socket", sock, "--shutdown")
	if err == nil {
		t.Fatal("--shutdown succeeded despite a wedged reclaim, want a failure")
	}
	if code := cli.ExitCode(err); code != 1 {
		t.Errorf("exit = %d, want 1 (wedged); err = %v", code, err)
	}
}

// TestMountShutdownRemovesNotesSymlinks proves --shutdown removes the .notes
// symlinks presenting cc-notes' reclaimed mounts. It snapshots the owner's
// mounts via List, reclaims them, then unlinks each presenting symlink.
func TestMountShutdownRemovesNotesSymlinks(t *testing.T) {
	repo := initRepo(t)
	repoRoot := mustGit(t, repo, "rev-parse", "--show-toplevel")
	mp := filepath.Join(t.TempDir(), "mnt")
	if err := os.MkdirAll(mp, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repoRoot, ".notes")
	if err := os.Symlink(mp, link); err != nil {
		t.Fatal(err)
	}

	sock, _ := fakeHolder(t, func(req mountd.Request) mountd.Response {
		if req.Op == mountd.OpList {
			// Base is the repo root that presents .notes; Dir is the managed mount.
			return mountd.Response{OK: true, Mounts: []mountd.MountInfo{
				{Dir: mp, Base: repoRoot, Live: true, Owner: "cc-notes"},
			}}
		}
		return okHolder(req)
	})

	if _, _, err := runCLI(t, repo, "mount", "--socket", sock, "--shutdown"); err != nil {
		t.Fatalf("mount --shutdown: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf(".notes symlink not removed after reclaim: err=%v", err)
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
