package helperdeployment

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/cc-notes/internal/version"
)

func TestExecuteDeploymentRequiresFixedSignedHelperAndDispatchesExactAction(t *testing.T) {
	originalCanonical, originalInstalled := canonicalExecutable, installedPath
	originalController := newInstalledController
	originalActivate, originalDeactivate := executeActivation, executeDeactivation
	t.Cleanup(func() {
		canonicalExecutable, installedPath = originalCanonical, originalInstalled
		newInstalledController = originalController
		executeActivation, executeDeactivation = originalActivate, originalDeactivate
	})
	const app = "/Users/test/Applications/CCNotesHelper.app"
	const executable = app + "/Contents/MacOS/CCNotesHelper"
	installedPath = func() (string, error) { return app, nil }
	canonicalExecutable = func() (string, error) { return executable, nil }
	newInstalledController = func() installedController { return nil }
	var actions []helpercontract.DeploymentAction
	executeActivation = func(context.Context, installedController) (helpercontract.DeploymentResult, error) {
		actions = append(actions, helpercontract.DeploymentActivate)
		return helpercontract.NewDeploymentResult(helpercontract.DeploymentActivate, helpercontract.DeploymentActive)
	}
	executeDeactivation = func(context.Context, installedController) (helpercontract.DeploymentResult, error) {
		actions = append(actions, helpercontract.DeploymentDeactivate)
		return helpercontract.NewDeploymentResult(helpercontract.DeploymentDeactivate, helpercontract.DeploymentInactive)
	}
	for _, action := range []helpercontract.DeploymentAction{
		helpercontract.DeploymentActivate, helpercontract.DeploymentDeactivate,
	} {
		result, err := ExecuteDeployment(t.Context(), helpercontract.DeploymentRequest{Action: action})
		if err != nil || result.Action != action {
			t.Fatalf("ExecuteDeployment(%q) = (%#v, %v)", action, result, err)
		}
	}
	if len(actions) != 2 || actions[0] != helpercontract.DeploymentActivate || actions[1] != helpercontract.DeploymentDeactivate {
		t.Fatalf("actions = %q", actions)
	}
	canonicalExecutable = func() (string, error) { return "/tmp/copied-helper", nil }
	if _, err := ExecuteDeployment(t.Context(), helpercontract.DeploymentRequest{Action: helpercontract.DeploymentActivate}); err == nil {
		t.Fatal("ExecuteDeployment accepted a noncanonical running helper")
	}
}

func TestHelperMarketingVersionIsExactReleaseTag(t *testing.T) {
	original := version.Version
	t.Cleanup(func() { version.Version = original })
	for tag, want := range map[string]string{
		"v1.2.3":      "1.2.3",
		"v1.2.3-rc.4": "1.2.3-rc.4",
	} {
		version.Version = tag
		got, err := helperMarketingVersion()
		if err != nil || got != want {
			t.Fatalf("helperMarketingVersion(%q) = (%q, %v), want %q", tag, got, err, want)
		}
	}
	for _, tag := range []string{"dev", "1.2.3", "v01.2.3", "v1.2", "v1.2.3-rc..1"} {
		version.Version = tag
		if _, err := helperMarketingVersion(); err == nil {
			t.Fatalf("helperMarketingVersion accepted %q", tag)
		}
	}
}

func TestValidDeploymentOperationIDRequiresFullNonzeroSHA256(t *testing.T) {
	exact := strings.Repeat("ab", 32)
	if !validDeploymentOperationID(exact) {
		t.Fatal("valid operation ID was rejected")
	}
	for _, value := range []string{
		"", strings.Repeat("ab", 16), strings.Repeat("AB", 32), strings.Repeat("0", 64),
		strings.Repeat("gg", 32), exact + "00",
	} {
		if validDeploymentOperationID(value) {
			t.Fatalf("invalid operation ID %q was accepted", value)
		}
	}
}

func TestExecuteDeploymentPropagatesIdentityFailures(t *testing.T) {
	originalCanonical := canonicalExecutable
	t.Cleanup(func() { canonicalExecutable = originalCanonical })
	want := errors.New("identity unavailable")
	canonicalExecutable = func() (string, error) { return "", want }
	if _, err := ExecuteDeployment(t.Context(), helpercontract.DeploymentRequest{Action: helpercontract.DeploymentActivate}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
