//go:build darwin

package helperdeployment

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/helperclient"
)

// writeSourceBundle builds the shape holder validates in a packaged candidate:
// a real, non-symlinked, executable runtime binary and a readable short version.
func writeSourceBundle(t *testing.T) string {
	t.Helper()
	// macOS hands out temp dirs under /var, which is a symlink holder refuses.
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	appPath := filepath.Join(root, helperclient.ExecutableName+".app")
	macOS := filepath.Join(appPath, "Contents", "MacOS")
	if err := os.MkdirAll(macOS, 0o755); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(macOS, helperclient.ExecutableName)
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleShortVersionString</key><string>0.0.1</string>
</dict></plist>
`
	if err := os.WriteFile(filepath.Join(appPath, "Contents", "Info.plist"), []byte(plist), 0o644); err != nil {
		t.Fatal(err)
	}
	return appPath
}

// absentTarget names a fixed user application this machine has not installed.
// helperclient.InstalledPath resolves through the account database rather than
// $HOME, so a guarded home cannot stand in for one.
func absentTarget(t *testing.T) string {
	t.Helper()
	installed, err := helperclient.InstalledPath()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(filepath.Dir(installed), helperclient.ExecutableName+"BootstrapProbe.app")
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Skipf("%s unexpectedly exists", target)
	}
	return target
}

// A first install has no target to plan against, and the strict constructor
// validates the installed runtime executable — so planning the very install
// that creates it has to run off the packaged source. Every agent must still
// name the installed path, or launchd would run the packaged copy forever.
func TestCandidateAgentsPlanAnInstallWhoseTargetDoesNotExistYet(t *testing.T) {
	target := absentTarget(t)
	if _, err := exactAgents(target); err == nil {
		t.Fatal("strict planning accepted a missing installed target; the bootstrap bug is back")
	}
	// deploy.Open resolves its Program inside that same missing bundle, which
	// is why the install path names the daemon by label alone.
	if _, err := helperDaemon(target); err == nil {
		t.Fatal("bundled program resolved against a missing bundle")
	}

	agents, err := candidateAgents(target, writeSourceBundle(t))
	if err != nil {
		t.Fatalf("candidate planning refused a missing installed target: %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(agents))
	}
	wantProgram := filepath.Join(target, "Contents", "MacOS", helperclient.ExecutableName)
	if agents[0].Program != wantProgram {
		t.Fatalf("agent program = %q, want the installed path %q", agents[0].Program, wantProgram)
	}
}
