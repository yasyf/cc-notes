package helperclient

import (
	"context"
	"testing"

	"github.com/yasyf/cc-notes/internal/helpercontract"
)

func TestServiceRequestsUseOnlyCanonicalSignedHelper(t *testing.T) {
	originalPath, originalDeployment, originalProvision := serviceExecutablePath, serviceRunDeployment, serviceRunProvision
	t.Cleanup(func() {
		serviceExecutablePath, serviceRunDeployment, serviceRunProvision = originalPath, originalDeployment, originalProvision
	})
	const executable = "/Users/test/Applications/CCNotesHelper.app/Contents/MacOS/CCNotesHelper"
	serviceExecutablePath = func() (string, error) { return executable, nil }
	var actions []helpercontract.DeploymentAction
	serviceRunDeployment = func(
		_ context.Context,
		gotExecutable string,
		action helpercontract.DeploymentAction,
	) (helpercontract.DeploymentResult, error) {
		if gotExecutable != executable {
			t.Fatalf("executable = %q", gotExecutable)
		}
		actions = append(actions, action)
		state := helpercontract.DeploymentActive
		if action == helpercontract.DeploymentDeactivate {
			state = helpercontract.DeploymentInactive
		}
		return helpercontract.NewDeploymentResult(action, state)
	}
	if err := ActivateService(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := DeactivateService(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || actions[0] != helpercontract.DeploymentActivate || actions[1] != helpercontract.DeploymentDeactivate {
		t.Fatalf("actions = %q", actions)
	}
	const root = "/tmp/repository"
	serviceRunProvision = func(_ context.Context, gotExecutable, gotRoot string) error {
		if gotExecutable != executable || gotRoot != root {
			t.Fatalf("provision = (%q, %q)", gotExecutable, gotRoot)
		}
		return nil
	}
	if err := ProvisionRepository(t.Context(), root); err != nil {
		t.Fatal(err)
	}
}
