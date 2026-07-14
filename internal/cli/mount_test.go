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
	"time"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/fusekit"
	"github.com/yasyf/fusekit/mountd"
)

// fakeHolder serves canned mountd responses over a short /tmp unix socket,
// speaking the proto by hand so the CLI's holder driving is pinned independently
// of the real server. respond maps each decoded request to a response; the
// helper stamps the proto version onto every reply.
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

// okHolder responds success to every op. Its OpHello advertises the full v2
// feature set, so the detached mount path's requireHolder negotiation passes;
// every other op is a bare OK (an empty List/Reclaim).
func okHolder(req mountd.Request) mountd.Response {
	switch req.Op {
	case mountd.OpHello:
		return mountd.Response{OK: true, Version: version.String(), Features: mountd.HolderFeatures}
	case mountd.OpHealth:
		return mountd.Response{OK: true, Version: version.String()}
	}
	return mountd.Response{OK: true}
}

// shortHome overrides initRepo's HOME with one rooted under /tmp, so a socket
// bound at $HOME/.cc-notes fits macOS's ~104-char sun_path limit (the default
// /var/folders t.TempDir blows past it). initRepo already isolates HOME for the
// mounts that never bind under it; this is only for the incumbent probe.
func shortHome(t *testing.T) {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "ccn-home")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("HOME", home)
}

// mountDarwin pins hostGOOS to "darwin" for a detached-mount test, so the
// darwin-only serveDetached path runs on any CI platform; Linux CI would
// otherwise hit the !darwin fail-fast before the fake holder engages.
func mountDarwin(t *testing.T) {
	t.Helper()
	t.Cleanup(cli.SetHostGOOSForTest("darwin"))
}

func TestMountDetachedSucceeds(t *testing.T) {
	mountDarwin(t)
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
			if r.ContentMode != fusekit.ContentModeTree {
				t.Errorf("mount ContentMode = %q, want %q (tree dial)", r.ContentMode, fusekit.ContentModeTree)
			}
			if r.Owner != "cc-notes" {
				t.Errorf("mount Owner = %q, want %q so the shared holder scopes cc-notes' mounts", r.Owner, "cc-notes")
			}
			if r.ContentSocket == "" {
				t.Error("mount ContentSocket empty, want contentd's bridge socket")
			}
			if r.Domain != r.Base {
				t.Errorf("mount Domain = %q, want the repo root %q (contentd keys its renderer on it)", r.Domain, r.Base)
			}
			if r.ProbePath != "/notes" {
				t.Errorf("mount ProbePath = %q, want %q so the holder's ready gate exercises the tree before going live", r.ProbePath, "/notes")
			}
			if r.AttrCache {
				t.Error("mount AttrCache = true, want false (noattrcache is cc-notes' only coherence lever)")
			}
		}
	}
	if mounts != 1 {
		t.Errorf("OpMount count = %d, want 1", mounts)
	}
}

// TestMountDetachedNegotiatesFeatures proves the detached mount path refuses a
// holder missing a required capability with a crisp cask-upgrade message rather
// than mounting into a holder that cannot serve tree mode.
func TestMountDetachedNegotiatesFeatures(t *testing.T) {
	mountDarwin(t)
	repo := initRepo(t)
	sock, requests := fakeHolder(t, func(req mountd.Request) mountd.Response {
		if req.Op == mountd.OpHello {
			// A proto-2 holder that predates tree mode: has lease-gate but not tree.
			return mountd.Response{OK: true, Version: "v0.40.0", Features: []string{mountd.FeatureLeaseGate, mountd.FeatureWarning}}
		}
		return okHolder(req)
	})
	mp := filepath.Join(t.TempDir(), "mnt")

	_, _, err := runCLI(t, repo, "mount", "--socket", sock, mp)
	if err == nil {
		t.Fatal("mount succeeded against a holder lacking FeatureTree, want a refusal")
	}
	if !strings.Contains(err.Error(), "brew upgrade --cask fusekit-holder") {
		t.Errorf("err = %v, want the cask-upgrade remediation", err)
	}
	for _, r := range requests() {
		if r.Op == mountd.OpMount {
			t.Error("a feature-negotiation failure still sent OpMount; it must refuse before mounting")
		}
	}
}

func TestMountDetachedIdempotentRemount(t *testing.T) {
	mountDarwin(t)
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
	mountDarwin(t)
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
	gittest.Git(t, dir, "config", "cc-notes.autoMount", "true")

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
	mountDarwin(t)
	repo := initRepo(t)
	sock := filepath.Join(t.TempDir(), "never-bound.sock")
	mp := filepath.Join(t.TempDir(), "mnt")

	// A pure test binary cannot spawn a holder and the cask is not installed, so
	// an unreachable socket fails with ErrCannotHost — a plain error (exit 1),
	// never a conflict.
	_, _, err := runCLI(t, repo, "mount", "--socket", sock, mp)
	if err == nil {
		t.Fatal("mount succeeded with no holder, want a failure")
	}
	if code := cli.ExitCode(err); code != 1 {
		t.Errorf("exit = %d, want 1; err = %v", code, err)
	}
}

// TestMountRefusesLegacyIncumbent proves the detached mount path refuses to
// serve through the shared holder while a pre-cutover private mount holder is
// still answering its own socket — the two would fight over the same
// mountpoints. It prints the graceful displacement recipe and never mounts.
func TestMountRefusesLegacyIncumbent(t *testing.T) {
	mountDarwin(t)
	repo := initRepo(t)
	shortHome(t) // override initRepo's long HOME so the legacy socket path fits sun_path
	sock, requests := fakeHolder(t, okHolder)

	// Bind the legacy private-holder socket so the incumbent probe finds it live.
	legacy := filepath.Join(os.Getenv("HOME"), ".cc-notes", "mounts.sock")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", legacy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	mp := filepath.Join(t.TempDir(), "mnt")
	_, _, err = runCLI(t, repo, "mount", "--socket", sock, mp)
	if err == nil {
		t.Fatal("mount succeeded while a legacy incumbent was serving, want a refusal")
	}
	if !strings.Contains(err.Error(), "--shutdown") {
		t.Errorf("err = %v, want the graceful displacement recipe (old CLI's `mount --shutdown`)", err)
	}
	for _, r := range requests() {
		if r.Op == mountd.OpMount {
			t.Error("refused mount still sent OpMount to the shared holder")
		}
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

	stdout, _, err := runCLI(t, repo, "mount", "--socket", sock, "list")
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

	_, _, err := runCLI(t, repo, "mount", "--socket", sock, "list")
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
	if _, _, err := runCLI(t, repo, "mount", "--socket", sock, "stop", stopDir); err != nil {
		t.Fatalf("mount stop: %v", err)
	}
	if got := requests(); len(got) != 0 {
		t.Errorf("--stop contacted the holder for an unmounted dir: %v", got)
	}
}

// TestMountShutdown proves `mount --shutdown` reclaims cc-notes' OWN mounts
// (per-owner OpReclaim) rather than stopping the shared holder: the cask holder
// hosts other tenants, so a cross-owner OpShutdown would tear their mounts out.
// It must send OpReclaim scoped to Owner "cc-notes" and NEVER OpShutdown.
func TestMountShutdown(t *testing.T) {
	repo := initRepo(t)
	sock, requests := fakeHolder(t, okHolder)

	if _, _, err := runCLI(t, repo, "mount", "--socket", sock, "shutdown"); err != nil {
		t.Fatalf("mount shutdown: %v", err)
	}
	var sawReclaim bool
	for _, r := range requests() {
		if r.Op == mountd.OpReclaim {
			sawReclaim = true
			if r.Owner != "cc-notes" {
				t.Errorf("OpReclaim Owner = %q, want %q", r.Owner, "cc-notes")
			}
		}
	}
	if !sawReclaim {
		t.Error("--shutdown did not send OpReclaim to the holder")
	}
}

// TestMountDetachedDefaultLinksNotes proves the no-argument detached mount is
// served at the managed default and presented in the repo as a .notes symlink
// into it, kept out of git via .git/info/exclude (never a tracked .gitignore),
// with the .notes path — not the opaque managed path — printed to stdout.
func TestMountDetachedDefaultLinksNotes(t *testing.T) {
	mountDarwin(t)
	repo := initRepo(t)
	repoRoot := gittest.Git(t, repo, "rev-parse", "--show-toplevel")
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
	repoRoot := gittest.Git(t, repo, "rev-parse", "--show-toplevel")
	sock, requests := fakeHolder(t, okHolder)

	mp := filepath.Join(t.TempDir(), "mnt")
	if err := os.MkdirAll(mp, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(repoRoot, ".notes")
	if err := os.Symlink(mp, link); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runCLI(t, repo, "mount", "--socket", sock, "stop", ".notes"); err != nil {
		t.Fatalf("mount stop .notes: %v", err)
	}
	if _, err := os.Lstat(link); !os.IsNotExist(err) {
		t.Errorf(".notes symlink not removed: err=%v", err)
	}
	if got := requests(); len(got) != 0 {
		t.Errorf("--stop contacted the holder for an unmounted target: %v", got)
	}
}

// TestMountStopLiveUsesOwnedCrossTenantList pins that --stop of a LIVE mount
// lists with a valid owner (proto-2 refuses an empty owner with ClassInvalidOwner
// — the cc-pool lesson) AND cross-tenant (All:true), so refuseForeignStop can see
// a foreign row at the dir. An owner-empty List errored out before Teardown, so
// no live cc-notes mount could ever be stopped.
func TestMountStopLiveUsesOwnedCrossTenantList(t *testing.T) {
	repo := initRepo(t)
	restore := cli.SetMountpointLiveForTest(func(string) bool { return true })
	defer restore()
	sock, requests := fakeHolder(t, okHolder)
	mp := filepath.Join(t.TempDir(), "mnt")
	if err := os.MkdirAll(mp, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, _, err := runCLI(t, repo, "mount", "--socket", sock, "stop", mp); err != nil {
		t.Fatalf("mount --stop: %v", err)
	}
	var sawList bool
	for _, r := range requests() {
		if r.Op == mountd.OpList {
			sawList = true
			if r.Owner != "cc-notes" {
				t.Errorf("--stop OpList Owner = %q, want %q (empty owner is refused ClassInvalidOwner)", r.Owner, "cc-notes")
			}
			if !r.All {
				t.Error("--stop OpList All = false, want true (cross-tenant view so a foreign row at the dir is visible)")
			}
		}
	}
	if !sawList {
		t.Error("--stop of a live mount did not send OpList; refuseForeignStop never ran")
	}
}

// TestMountStopRefusesForeignTenant pins that a --stop aimed at a dir the holder
// registers to ANOTHER owner is refused (never torn out beneath that tenant).
func TestMountStopRefusesForeignTenant(t *testing.T) {
	repo := initRepo(t)
	restore := cli.SetMountpointLiveForTest(func(string) bool { return true })
	defer restore()
	mp := filepath.Join(t.TempDir(), "mnt")
	if err := os.MkdirAll(mp, 0o700); err != nil {
		t.Fatal(err)
	}
	sock, _ := fakeHolder(t, func(req mountd.Request) mountd.Response {
		if req.Op == mountd.OpList {
			return mountd.Response{OK: true, Mounts: []mountd.MountInfo{
				{Dir: mp, Base: "/other/repo", Owner: "cc-pool"},
			}}
		}
		return okHolder(req)
	})

	_, _, err := runCLI(t, repo, "mount", "--socket", sock, "stop", mp)
	if err == nil {
		t.Fatal("--stop of a dir registered to another tenant succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "another holder tenant") {
		t.Errorf("err = %v, want the foreign-tenant refusal", err)
	}
}

// TestMountStopRefusesLegacyIncumbent pins that --stop, like the mount path,
// refuses to tear down through the shared holder while a pre-cutover private
// holder still serves its socket — otherwise the shared holder would unmount the
// live legacy mount beneath its incumbent.
func TestMountStopRefusesLegacyIncumbent(t *testing.T) {
	repo := initRepo(t)
	shortHome(t)
	sock, requests := fakeHolder(t, okHolder)

	legacy := filepath.Join(os.Getenv("HOME"), ".cc-notes", "mounts.sock")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", legacy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	mp := filepath.Join(t.TempDir(), "mnt")
	if err := os.MkdirAll(mp, 0o700); err != nil {
		t.Fatal(err)
	}

	_, _, err = runCLI(t, repo, "mount", "--socket", sock, "stop", mp)
	if err == nil {
		t.Fatal("--stop succeeded while a legacy incumbent was serving, want a refusal")
	}
	if !strings.Contains(err.Error(), "--shutdown") {
		t.Errorf("err = %v, want the graceful displacement recipe", err)
	}
	for _, r := range requests() {
		if r.Op == mountd.OpUnmount {
			t.Error("refused --stop still sent OpUnmount to the shared holder")
		}
	}
}

// TestMountDetachedRequiresListAll pins O8: a holder advertising every tree
// capability EXCEPT FeatureListAll is refused up front. --stop of a live mount
// needs the cross-tenant ListAll view for its foreign-owner refusal, so the mount
// path negotiates it now rather than failing a teardown later.
func TestMountDetachedRequiresListAll(t *testing.T) {
	mountDarwin(t)
	repo := initRepo(t)
	sock, requests := fakeHolder(t, func(req mountd.Request) mountd.Response {
		if req.Op == mountd.OpHello {
			var feats []string
			for _, f := range mountd.HolderFeatures {
				if f != mountd.FeatureListAll {
					feats = append(feats, f)
				}
			}
			return mountd.Response{OK: true, Version: version.String(), Features: feats}
		}
		return okHolder(req)
	})
	mp := filepath.Join(t.TempDir(), "mnt")

	_, _, err := runCLI(t, repo, "mount", "--socket", sock, mp)
	if err == nil {
		t.Fatal("mount succeeded against a holder lacking FeatureListAll, want a refusal")
	}
	if !strings.Contains(err.Error(), "brew upgrade --cask fusekit-holder") {
		t.Errorf("err = %v, want the cask-upgrade remediation", err)
	}
	for _, r := range requests() {
		if r.Op == mountd.OpMount {
			t.Error("a feature-negotiation failure still sent OpMount; it must refuse before mounting")
		}
	}
}

// TestMountDetachedNoSpawnAfterHolderDiesPostHello pins O2: driving the full
// detached-mount path against a fake holder that answers Hello then dies must NOT
// trigger a real cask spawn. requireHolder's Hello succeeds; by the time
// AddMount's own EnsureRunning re-checks the socket the holder is gone, so a
// production ExecPath would `open -g` the installed cask. The suite-armed empty
// ExecPath (see mount_seams_test.go) makes canHost refuse instead — a clean
// ErrCannotHost (exit 1), the same on every machine regardless of cask state.
func TestMountDetachedNoSpawnAfterHolderDiesPostHello(t *testing.T) {
	mountDarwin(t)
	repo := initRepo(t)
	dir, err := os.MkdirTemp("/tmp", "ccn-die")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "m.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	helloSeen := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				var req mountd.Request
				if err := json.NewDecoder(bufio.NewReader(conn)).Decode(&req); err != nil {
					return // a bare Available() probe sends nothing
				}
				if req.Op == mountd.OpHello {
					// Answer Hello, then retire the listener so the NEXT dial —
					// AddMount's EnsureRunning Available check — sees the holder
					// gone: the post-Hello-death state.
					_ = json.NewEncoder(conn).Encode(mountd.Response{OK: true, Version: version.String(), Features: mountd.HolderFeatures, Proto: mountd.MountProtoVersion})
					once.Do(func() { _ = ln.Close(); close(helloSeen) })
					return
				}
				_ = json.NewEncoder(conn).Encode(mountd.Response{OK: true, Proto: mountd.MountProtoVersion})
			}(conn)
		}
	}()

	mp := filepath.Join(t.TempDir(), "mnt")
	_, _, err = runCLI(t, repo, "mount", "--socket", socket, mp)
	if err == nil {
		t.Fatal("mount succeeded after the holder died post-Hello, want ErrCannotHost (never a real cask spawn)")
	}
	if code := cli.ExitCode(err); code != 1 {
		t.Errorf("exit = %d, want 1 (ErrCannotHost, not a spawn); err = %v", code, err)
	}
	if !strings.Contains(err.Error(), "cannot host") {
		t.Errorf("err = %v, want the no-ExecPath cannot-host refusal — AddMount must not spawn the real holder", err)
	}
	// The holder must have answered Hello and retired before AddMount ran. A
	// timeout here means the detached path never dialed (a regression that would
	// otherwise hang the receive until the test binary's 10m limit).
	select {
	case <-helloSeen:
	case <-time.After(30 * time.Second):
		t.Fatal("holder never answered Hello within 30s; serveDetached did not dial as expected")
	}
}

// TestMountSubcommandArgsExit2 pins the parse-level arg wiring of the mount
// subcommands: a missing or extra positional is a usage error (exit 2), and the
// hidden --auto session-start mode still refuses --foreground. No holder is
// contacted — these fail during arg validation.
func TestMountSubcommandArgsExit2(t *testing.T) {
	repo := initRepo(t)
	for _, args := range [][]string{
		{"mount", "stop"},                   // stop needs a DIR
		{"mount", "stop", "/x", "/y"},       // stop takes exactly one DIR
		{"mount", "list", "somewhere"},      // list takes no args
		{"mount", "shutdown", "extra"},      // shutdown takes no args
		{"mount", "--foreground", "--auto"}, // --auto cannot combine with --foreground
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
