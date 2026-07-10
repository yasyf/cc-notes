package cli

import (
	"reflect"
	"testing"

	"github.com/yasyf/fusekit/mountd"
)

// TestNewRemoteHostCaskMode pins the default (shared cask holder) wiring: the
// driver targets the installed fusekit-holder cask binary (ExecPath), tags every
// mount with cc-notes' owner, and sets NO Version and NO StableExecDir — the cask
// holder is already stable-path and owns its own upgrade lifecycle, so cc-notes
// never converge-replaces it and never self-execs a private holder.
func TestNewRemoteHostCaskMode(t *testing.T) {
	h := newRemoteHost("/tmp/holder.sock", false)
	if h.ExecPath != mountd.HolderExe {
		t.Errorf("ExecPath = %q, want the cask holder %q", h.ExecPath, mountd.HolderExe)
	}
	if h.Owner != "cc-notes" {
		t.Errorf("Owner = %q, want %q", h.Owner, "cc-notes")
	}
	if h.Version != "" {
		t.Errorf("Version = %q, want empty (no converge against the shared holder)", h.Version)
	}
	if h.StableExecDir != "" {
		t.Errorf("StableExecDir = %q, want empty (the cask holder is already stable-path)", h.StableExecDir)
	}
	if h.Args != nil {
		t.Errorf("Args = %v, want nil (the cask holder launches via `open -g`, not self-exec)", h.Args)
	}
}

// TestNewRemoteHostCasklessMode pins the private self-exec holder wiring: no
// cask ExecPath, the mount-holder self-exec argv, and a StableExecDir so the
// macOS TCC grant survives upgrades — still tagged with cc-notes' owner and no
// Version.
func TestNewRemoteHostCasklessMode(t *testing.T) {
	const sock = "/tmp/mounts.sock"
	h := newRemoteHost(sock, true)
	if h.ExecPath != "" {
		t.Errorf("ExecPath = %q, want empty (self-exec, not the cask)", h.ExecPath)
	}
	wantArgs := []string{"mount-holder", "--socket", sock}
	if !reflect.DeepEqual(h.Args, wantArgs) {
		t.Errorf("Args = %v, want %v", h.Args, wantArgs)
	}
	if h.StableExecDir != stableExecDir() {
		t.Errorf("StableExecDir = %q, want %q", h.StableExecDir, stableExecDir())
	}
	if h.Owner != "cc-notes" {
		t.Errorf("Owner = %q, want %q", h.Owner, "cc-notes")
	}
	if h.Version != "" {
		t.Errorf("Version = %q, want empty", h.Version)
	}
}

// TestHolderSocket pins socket resolution across modes: an explicit --socket
// always wins; otherwise cask mode targets the shared holder's default socket
// and cask-less mode targets cc-notes' private socket.
func TestHolderSocket(t *testing.T) {
	for _, tc := range []struct {
		name     string
		chosen   string
		caskless bool
		want     string
	}{
		{"explicit wins over cask", "/x/y.sock", false, "/x/y.sock"},
		{"explicit wins over caskless", "/x/y.sock", true, "/x/y.sock"},
		{"cask default", "", false, mountd.DefaultHolderSocket()},
		{"caskless default", "", true, mountsSocketPath()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := holderSocket(tc.chosen, tc.caskless); got != tc.want {
				t.Errorf("holderSocket(%q, %v) = %q, want %q", tc.chosen, tc.caskless, got, tc.want)
			}
		})
	}
}

// TestRefuseForeignStop pins the --stop cross-tenant guard: a dir the shared
// holder registers to another owner is refused, while cc-notes' own mounts and
// unregistered dirs (carcasses, or dirs the holder never served) pass through
// to teardown.
func TestRefuseForeignStop(t *testing.T) {
	mounts := []mountd.MountInfo{
		{Dir: "/m/ours", Base: "/r/ours", Owner: holderOwner},
		{Dir: "/m/theirs", Base: "/r/theirs", Owner: "cc-pool"},
	}
	for _, tc := range []struct {
		name    string
		mp      string
		refused bool
	}{
		{"own mount passes", "/m/ours", false},
		{"foreign mount refused", "/m/theirs", true},
		{"unregistered dir passes", "/m/carcass", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseForeignStop(mounts, tc.mp)
			if tc.refused && err == nil {
				t.Fatalf("refuseForeignStop(%q) = nil, want a foreign-owner refusal", tc.mp)
			}
			if !tc.refused && err != nil {
				t.Fatalf("refuseForeignStop(%q) = %v, want nil", tc.mp, err)
			}
		})
	}
	if err := refuseForeignStop(nil, "/m/anything"); err != nil {
		t.Fatalf("refuseForeignStop(nil listing) = %v, want nil (no registry, carcass semantics)", err)
	}
}

// TestCasklessEnv proves the CC_NOTES_CASKLESS_HOLDER env var selects the private
// holder: any non-empty value is true, unset is false.
func TestCasklessEnv(t *testing.T) {
	t.Setenv(casklessEnvVar, "")
	if casklessEnv() {
		t.Error("empty env should not select caskless")
	}
	t.Setenv(casklessEnvVar, "1")
	if !casklessEnv() {
		t.Error("non-empty env should select caskless")
	}
}
