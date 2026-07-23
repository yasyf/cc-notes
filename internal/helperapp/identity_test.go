package helperapp

import (
	"encoding/binary"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/daemonkit/codeidentity"
	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/wire"
	"github.com/yasyf/fusekit/holder"
)

func TestApplicationPinsFixedSignedIdentity(t *testing.T) {
	installedPath, err := helperclient.InstalledPath()
	if err != nil {
		t.Fatal(err)
	}
	application := Application(installedPath)
	if application.AppPath != installedPath || application.BundleID != BundleID || application.TeamID != TeamID {
		t.Fatalf("application = %+v", application)
	}
	if application.Broker.ExecutableName != "" || application.Broker.SigningIdentifier != "" {
		t.Fatalf("mount-only application unexpectedly configured a broker: %+v", application.Broker)
	}
	if application.Runtime.ExecutableName != ExecutableName || application.Runtime.SigningIdentifier != BundleID {
		t.Fatalf("runtime identity = %+v", application.Runtime)
	}
}

func TestRuntimeDirectoryUsesOnlyV1DerivedState(t *testing.T) {
	directory, err := RuntimeDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(directory) != "fusekit-v1" || filepath.Base(filepath.Dir(directory)) != ".cc-notes" {
		t.Fatalf("runtime directory = %q", directory)
	}
}

func TestStopControlStoreConsumesOnlyExactRoleAndProcessGeneration(t *testing.T) {
	runtimeDirectory := t.TempDir()
	store := stopControlStore(runtimeDirectory)
	var audit proc.AuditToken
	binary.NativeEndian.PutUint32(audit[20:24], 42)
	binary.NativeEndian.PutUint32(audit[28:32], 1)
	expires := time.Now().Add(time.Minute).UnixMilli()
	identity := proc.Identity{
		PID: 42, StartTime: "start", Boot: "boot", Comm: ExecutableName,
		Executable: filepath.Join(runtimeDirectory, ExecutableName), AuditToken: audit,
	}
	record := proc.Record{
		RecoveryClass: proc.RecoveryStopControl,
		PID:           identity.PID, StartTime: identity.StartTime, Boot: identity.Boot, Comm: identity.Comm,
		Executable: identity.Executable, AuditToken: identity.AuditToken, Generation: "controller-generation",
		Role: StopControlRole, RuntimeBuild: "v0.40.0", RuntimeProtocol: 1,
		TargetProcessGeneration: "runtime-generation", Intent: string(wire.StopIntentRestart),
		StopAuthorityState: proc.StopAuthorityArmed, ExpiresUnixMilli: expires,
	}
	if err := store.Add(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	if _, consumed, err := store.ConsumeStopControl(
		t.Context(), identity, BundleID+".daemon", record.TargetProcessGeneration, time.Now(),
	); err != nil || consumed {
		t.Fatalf("consume wrong role = (%t, %v)", consumed, err)
	}
	if _, consumed, err := store.ConsumeStopControl(
		t.Context(), identity, record.Role, "other-generation", time.Now(),
	); err != nil || consumed {
		t.Fatalf("consume wrong process generation = (%t, %v)", consumed, err)
	}
	consumedRecord, consumed, err := store.ConsumeStopControl(
		t.Context(), identity, record.Role, record.TargetProcessGeneration, time.Now(),
	)
	if err != nil || !consumed || consumedRecord != record {
		t.Fatalf("consume exact stop authority = (%+v, %t, %v)", consumedRecord, consumed, err)
	}
	if _, consumed, err := store.ConsumeStopControl(
		t.Context(), identity, record.Role, record.TargetProcessGeneration, time.Now(),
	); err != nil || consumed {
		t.Fatalf("replay consumed stop authority = (%t, %v)", consumed, err)
	}
}

func TestInstalledApplicationUsesFixedUserApplicationsRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := helperclient.InstalledPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "Applications", "CCNotesHelper.app")
	if path != want {
		t.Fatalf("installed path = %q, want %q", path, want)
	}
}

func TestDeploymentPlanRejectsHiddenHelper(t *testing.T) {
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	spec := holder.DeploymentPlanSpec{
		Application:      Application(filepath.Join(account.HomeDir, ".cc-notes", "helper", "CCNotesHelper.app")),
		RuntimeDirectory: filepath.Join(account.HomeDir, ".cc-notes", "plan-runtime"),
		PresentationRoot: filepath.Join(account.HomeDir, ".cc-notes", "plan-presentation"),
		BuildID:          "v0.40.0", RuntimePolicyDigest: codeidentity.PolicyDigest{1},
	}
	if _, err := holder.NewDeploymentPlan(spec); err == nil || !strings.Contains(err.Error(), "not a fixed installed application") {
		t.Fatalf("hidden helper plan error = %v", err)
	}
}
