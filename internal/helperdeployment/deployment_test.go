//go:build darwin

package helperdeployment

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/daemonkit"
	"github.com/yasyf/daemonkit/deploy"
)

func TestStopDaemonNamesNoProgram(t *testing.T) {
	daemon := stopDaemon()
	if daemon.Program != (daemonkit.Program{}) {
		t.Fatal("stop daemon names a program; Stop would refuse a live pre-v0.21 runtime instead of removing it")
	}
	if daemon.Label != DeploymentServiceLabel {
		t.Fatalf("stop daemon label = %q, want %q", daemon.Label, DeploymentServiceLabel)
	}
	if _, err := daemonkit.Open(daemon); err != nil {
		t.Fatalf("stop daemon is not openable as a client: %v", err)
	}
}

func TestHelperDaemonRestartsAlways(t *testing.T) {
	daemon := stopDaemon()
	if daemon.Restart != daemonkit.RestartAlways {
		t.Fatalf("restart = %v, want RestartAlways", daemon.Restart)
	}
}

func TestBudgetedAlwaysStatesADeadlineAndKeepsAStatedOne(t *testing.T) {
	ctx, cancel := budgeted(context.Background(), applyPackageBudget)
	defer cancel()
	deadline, stated := ctx.Deadline()
	if !stated {
		t.Fatal("budgeted returned a deadline-less context; every daemonkit verb refuses one")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > applyPackageBudget {
		t.Fatalf("budget = %v, want (0, %v]", remaining, applyPackageBudget)
	}

	stricter := 5 * time.Second
	outer, cancelOuter := context.WithTimeout(context.Background(), stricter)
	defer cancelOuter()
	inner, cancelInner := budgeted(outer, applyPackageBudget)
	defer cancelInner()
	got, _ := inner.Deadline()
	want, _ := outer.Deadline()
	if !got.Equal(want) {
		t.Fatalf("budgeted widened a stated deadline to %v, want %v", got, want)
	}
}

func TestHelperDaemonFrameCarriesTheProvisionPayload(t *testing.T) {
	if got := daemonkit.MaxDetail(stopDaemon().MaxFrame); got < helpercontract.MaxProvisionPayload {
		t.Fatalf("max detail = %d, want at least %d", got, helpercontract.MaxProvisionPayload)
	}
}

func TestReadinessBuildIsTheInstalledBundleVersion(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), helperclient.ExecutableName+".app")
	contents := filepath.Join(appPath, "Contents")
	if err := os.MkdirAll(contents, 0o755); err != nil {
		t.Fatal(err)
	}
	const build = "0.73.4"
	if err := os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(`<plist><dict><key>CFBundleShortVersionString</key><string>`+build+`</string></dict></plist>`), 0o644); err != nil {
		t.Fatal(err)
	}
	digest := deploy.SHA256{1}

	if err := validateReadiness(build, 1, digest, appPath); err != nil {
		t.Fatalf("readiness naming the installed bundle version was refused: %v", err)
	}
	if err := validateReadiness("0.73.3", 1, digest, appPath); err == nil {
		t.Fatal("readiness naming a different bundle version was accepted")
	}
	if err := validateReadiness(build, 0, digest, appPath); err == nil {
		t.Fatal("readiness with no generation was accepted")
	}
	if err := validateReadiness(build, 1, deploy.SHA256{}, appPath); err == nil {
		t.Fatal("readiness with no evidence digest was accepted")
	}
}
