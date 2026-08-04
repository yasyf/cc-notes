//go:build darwin

package helperdeployment

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/daemonkit"
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

func TestHelperDaemonFrameCarriesTheProvisionPayload(t *testing.T) {
	if got := daemonkit.MaxDetail(stopDaemon().MaxFrame); got < helpercontract.MaxProvisionPayload {
		t.Fatalf("max detail = %d, want at least %d", got, helpercontract.MaxProvisionPayload)
	}
}
