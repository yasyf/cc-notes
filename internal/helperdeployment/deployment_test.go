//go:build darwin

package helperdeployment

import (
	"context"
	"errors"
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

func TestReadinessBuildNamesTheInstalledBundleArtifact(t *testing.T) {
	appPath := writeSourceBundle(t)
	executable := filepath.Join(appPath, "Contents", "MacOS", helperclient.ExecutableName)
	stamp := time.Unix(1234567890, 987654321)
	if err := os.Chtimes(executable, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(executable)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(stamp) {
		t.Fatalf("executable mtime = %v, want %v", info.ModTime(), stamp)
	}
	const build = "0.0.1+1234567890987654321"
	digest := deploy.SHA256{1}
	for _, tt := range []struct {
		name       string
		build      string
		generation uint64
		digest     deploy.SHA256
		wantErr    bool
	}{
		{"exact artifact", build, 1, digest, false},
		{"marketing version only", "0.0.1", 1, digest, true},
		{"different version", "0.0.2+1234567890987654321", 1, digest, true},
		{"different executable", "0.0.1+1234567890987654322", 1, digest, true},
		{"extra suffix", build + "+other", 1, digest, true},
		{"no generation", build, 0, digest, true},
		{"no digest", build, 1, deploy.SHA256{}, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateReadiness(tt.build, tt.generation, tt.digest, appPath)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateReadiness(%q, %d, %x) = %v, wantErr %t", tt.build, tt.generation, tt.digest, err, tt.wantErr)
			}
		})
	}
	if err := os.Remove(executable); err != nil {
		t.Fatal(err)
	}
	if err := validateReadiness(build, 1, digest, appPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing executable = %v, want os.ErrNotExist", err)
	}
}
