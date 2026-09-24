//go:build darwin

package helperdeployment

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/daemonkit"
	"github.com/yasyf/daemonkit/bundle"
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

func TestHelperDaemonRestartsAlwaysAndTrustsOnlyTheSignedHelper(t *testing.T) {
	daemon := stopDaemon()
	if daemon.Restart != daemonkit.RestartAlways {
		t.Fatalf("restart = %v, want RestartAlways", daemon.Restart)
	}
	want := daemonkit.Requirement{
		TeamID: helperclient.TeamID, SigningIdentifier: helperclient.BundleID,
	}.Digest()
	if daemon.Trust.Control == nil || daemon.Trust.Control.Digest() != want ||
		len(daemon.Trust.Business) != 1 || daemon.Trust.Business[0].Digest() != want {
		t.Fatalf("trust = %#v, want control and business pinned to the signed helper", daemon.Trust)
	}
	if daemon.Trust.Serving == (daemonkit.Serving{}) {
		t.Fatal("serving posture is unstated; Open would refuse the daemon")
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

func TestReadinessBuildIsTheInstalledExecutableDigest(t *testing.T) {
	appPath := filepath.Join(t.TempDir(), helperclient.ExecutableName+".app")
	executable := bundle.ExePath(appPath, helperclient.ExecutableName)
	if err := os.MkdirAll(filepath.Dir(executable), 0o755); err != nil {
		t.Fatal(err)
	}
	contents := []byte("signed helper bytes")
	if err := os.WriteFile(executable, contents, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(contents)
	build := hex.EncodeToString(sum[:])
	digest := deploy.SHA256{1}

	if err := validateReadiness(build, 1, digest, appPath); err != nil {
		t.Fatalf("readiness proving the installed executable's digest was refused: %v", err)
	}
	if err := validateReadiness(version.String(), 1, digest, appPath); err == nil {
		t.Fatal("readiness naming the product version string was accepted; daemonkit reports the executable digest")
	}
	if err := validateReadiness(build, 0, digest, appPath); err == nil {
		t.Fatal("readiness with no generation was accepted")
	}
	if err := validateReadiness(build, 1, deploy.SHA256{}, appPath); err == nil {
		t.Fatal("readiness with no evidence digest was accepted")
	}
}
