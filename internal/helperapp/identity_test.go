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
		BuildID:          "v0.39.3", RuntimePolicyDigest: codeidentity.PolicyDigest{1},
	}
	if _, err := holder.NewDeploymentPlan(spec); err == nil || !strings.Contains(err.Error(), "not a fixed installed application") {
		t.Fatalf("hidden helper plan error = %v", err)
	}
}
