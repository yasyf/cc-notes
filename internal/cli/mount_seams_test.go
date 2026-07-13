package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/yasyf/fusekit/mountd"
)

// The cli test binary MUST never reach the real launchctl bootstrap or the real
// `open -g` holder spawn — installing a test binary as a LaunchAgent or spawning
// a holder is this repo's fork-storm class. init stubs every spawn/bootstrap
// seam for EVERY test in the package (the internal `cli` and external `cli_test`
// files compile into one binary), so no test can touch the real thing even
// accidentally. Two spawn paths exist and BOTH are neutralized: spawnHolder (the
// requireHolder seam) and remoteHostExecPath (the ExecPath newRemoteHost hands
// its RemoteHost — RemoteHost.AddMount/Teardown drive fusekit's OWN EnsureRunning,
// which spawnHolder does not cover; blanking the ExecPath makes canHost refuse
// the spawn in the pure test build).
func init() {
	launchctlLoad = func(_, label string) error {
		panic("cli test reached the real launchctl bootstrap seam (" + label + "); the fork-storm guard requires it stay stubbed")
	}
	spawnHolder = func(socket string) error {
		// Never spawn: report only whether the socket is already up (a bound
		// fake holder), else refuse with ErrCannotHost — the down case's exact
		// outcome, minus the real `open -g`.
		if mountd.NewClient(socket).Available() {
			return nil
		}
		return fmt.Errorf("%w: %s", mountd.ErrCannotHost, cannotHostHint)
	}
	remoteHostExecPath = func() string { return "" }
}

// TestNewRemoteHostCarriesNoExecPathInTests pins the O2 guard structurally: the
// suite arms remoteHostExecPath to "", so every RemoteHost the cli builds carries
// no spawn target. In the pure test build that makes AddMount/Teardown's own
// EnsureRunning refuse (canHost → ErrCannotHost) instead of `open -g`-ing the
// installed cask — the spawn the spawnHolder seam does not reach.
func TestNewRemoteHostCarriesNoExecPathInTests(t *testing.T) {
	if got := newRemoteHost("/tmp/whatever.sock").ExecPath; got != "" {
		t.Fatalf("newRemoteHost ExecPath = %q, want empty in the test binary (else AddMount could spawn the real cask)", got)
	}
}

// TestServeDetachedUnsupportedOffDarwin pins the O7 fail-fast: a detached mount
// off macOS refuses with a crisp `--foreground` pointer rather than half-running
// the macOS-only shared-holder path.
func TestServeDetachedUnsupportedOffDarwin(t *testing.T) {
	old := hostGOOS
	hostGOOS = "linux"
	defer func() { hostGOOS = old }()

	err := serveDetached(&cobra.Command{}, "/tmp/x.sock", t.TempDir(), t.TempDir(), false)
	if err == nil {
		t.Fatal("serveDetached off darwin succeeded, want an unsupported-platform refusal")
	}
	if !strings.Contains(err.Error(), "--foreground") || !strings.Contains(err.Error(), "linux") {
		t.Errorf("err = %v, want the crisp --foreground pointer naming the platform", err)
	}
}

// TestInstallContentdAgentUnsupportedOffDarwin pins that the contentd LaunchAgent
// bootstrap is darwin-gated: off macOS it refuses BEFORE any os.Executable /
// launchctl / filesystem side effect (the check is first).
func TestInstallContentdAgentUnsupportedOffDarwin(t *testing.T) {
	old := hostGOOS
	hostGOOS = "linux"
	defer func() { hostGOOS = old }()
	t.Setenv("HOME", t.TempDir())

	err := installContentdAgent()
	if err == nil {
		t.Fatal("installContentdAgent off darwin succeeded, want a macOS-only refusal")
	}
	if !strings.Contains(err.Error(), "macOS") {
		t.Errorf("err = %v, want the macOS-only refusal", err)
	}
}

// TestInstallContentdAgentRefusesTestBinary pins the structural guard: the
// LaunchAgent installer refuses a `go test` binary (os.Executable() ends in
// `.test` / runs from a go-build tree) BEFORE any filesystem or launchctl side
// effect, so a developer running `go test -tags fuse ./internal/cli/...` cannot
// install the test binary as a KeepAlive agent (fork storm).
func TestInstallContentdAgentRefusesTestBinary(t *testing.T) {
	old := hostGOOS
	hostGOOS = "darwin"
	defer func() { hostGOOS = old }()
	// Belt-and-suspenders: were the guard to regress, the installer would
	// MkdirAll under $HOME — keep that off the real home.
	t.Setenv("HOME", t.TempDir())

	err := installContentdAgent()
	if err == nil {
		t.Fatal("installContentdAgent from a test binary succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "test binary") || !strings.Contains(err.Error(), "fork storm") {
		t.Errorf("err = %v, want the fork-storm test-binary refusal", err)
	}
}

// TestIsTestBinary pins the disqualifying signals (non-family basename, go-build
// cache, temp dir) and the allowed installed family (cc-notes, its ccn shorthand,
// and a local cc-notes_fuse build).
func TestIsTestBinary(t *testing.T) {
	for _, tc := range []struct {
		exe  string
		want bool
	}{
		{"/var/folders/xy/T/go-build123/b001/cli.test", true}, // compiled test binary
		{"/some/where/cc-notes.test", true},                   // <pkg>.test basename
		{"/tmp/go-build999/exe/main", true},                   // go-build cache
		{"/tmp/runner", true},                                 // go test -c -o /tmp/runner (pinned O1 hole)
		{"/usr/local/bin/cc-notes", false},                    // installed binary
		{"/Users/x/.cc-notes/bin/cc-notes", false},            // installed under state dir
		{"/opt/homebrew/bin/ccn", false},                      // ccn shorthand symlink (os.Executable is unresolved on macOS)
		{"/usr/local/bin/cc-notes_fuse", false},               // local fuse build
	} {
		if got := isTestBinary(tc.exe); got != tc.want {
			t.Errorf("isTestBinary(%q) = %v, want %v", tc.exe, got, tc.want)
		}
	}
	// A correctly-named binary built into the temp dir (go test -c -o "$TMPDIR/cc-notes")
	// is still refused — the family check alone would pass it.
	if tmpExe := filepath.Join(os.TempDir(), "cc-notes"); !isTestBinary(tmpExe) {
		t.Errorf("isTestBinary(%q) = false, want true (real name, but built under the temp dir)", tmpExe)
	}
}

// TestRequireHolderRoutesThroughSpawnSeam pins that requireHolder drives the
// spawn SEAM, not a hardcoded `open -g` — so a down holder refuses via the seam
// (no real cask bootstrap on a machine that has the cask installed).
func TestRequireHolderRoutesThroughSpawnSeam(t *testing.T) {
	const socket = "/tmp/cc-notes-never-bound.sock"
	var seenSocket string
	old := spawnHolder
	spawnHolder = func(s string) error {
		seenSocket = s
		return fmt.Errorf("%w: stub refusal", mountd.ErrCannotHost)
	}
	defer func() { spawnHolder = old }()

	err := requireHolder(socket)
	if !errors.Is(err, mountd.ErrCannotHost) {
		t.Fatalf("requireHolder err = %v, want the seam's ErrCannotHost (no real spawn)", err)
	}
	if seenSocket != socket {
		t.Errorf("spawn seam saw socket %q, want %q — requireHolder must route through spawnHolder", seenSocket, socket)
	}
}
