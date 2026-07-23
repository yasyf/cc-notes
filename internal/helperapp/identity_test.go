package helperapp

import (
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/daemonkit/codeidentity"
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
	presentation, err := PresentationRoot()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(presentation) != "mnt" || filepath.Dir(presentation) != filepath.Dir(directory) {
		t.Fatalf("presentation root = %q", presentation)
	}
}

func TestRuntimePlanSpecPinsNativeContract(t *testing.T) {
	verifier := new(holder.FUSEVerifier)
	spec := RuntimePlanSpec(filepath.Join("/Users/example", "Applications", "CCNotesHelper.app"), "/runtime", "/presentation", "v0.41.0", verifier)
	if spec.Native == nil || spec.Native.PresentationRoot != "/presentation" || spec.Native.FUSEVerifier != verifier {
		t.Fatalf("native runtime spec = %#v", spec.Native)
	}
	if spec.RuntimeDirectory != "/runtime" || spec.BuildID != "v0.41.0" ||
		spec.Readiness != holder.StandardReadinessContract() || !spec.SourceCapable {
		t.Fatalf("runtime plan spec = %#v", spec)
	}
	if spec.Application.Broker != (holder.SignedExecutable{}) || spec.BrokerPolicy.RequiredAppGroup != "" ||
		len(spec.BrokerPolicy.RequiredEntitlements) != 0 {
		t.Fatalf("mount-only runtime carried broker policy: %#v", spec)
	}
}

func TestInstalledApplicationUsesFixedUserApplicationsRoot(t *testing.T) {
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	path, err := helperclient.InstalledPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(account.HomeDir, "Applications", "CCNotesHelper.app")
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
		Native: &holder.NativeDeploymentSpec{
			PresentationRoot: filepath.Join(account.HomeDir, ".cc-notes", "plan-presentation"),
		},
		BuildID: "v0.41.0", Readiness: holder.StandardReadinessContract(),
		RuntimePolicyDigest: codeidentity.PolicyDigest{1},
	}
	_, err = holder.NewDeploymentPlan(spec)
	if err == nil || !strings.Contains(err.Error(), "not a fixed user application") {
		t.Fatalf("hidden helper plan error = %v", err)
	}
	if !strings.HasPrefix(err.Error(), "FuseKit runtime:") || strings.Contains(strings.ToLower(err.Error()), "holder") {
		t.Fatalf("hidden helper plan exposes retired runtime vocabulary: %v", err)
	}
}
