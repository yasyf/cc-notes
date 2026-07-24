package helperclient

import (
	"context"
	"fmt"

	"github.com/yasyf/cc-notes/internal/helpercontract"
)

var (
	serviceExecutablePath = ExecutablePath
	serviceRunDeployment  = RunDeployment
	serviceRunProvision   = RunProvision
)

// ProvisionRepository invokes the fixed signed helper for one repository.
func ProvisionRepository(ctx context.Context, repoRoot string) error {
	executable, err := serviceExecutablePath()
	if err != nil {
		return err
	}
	return serviceRunProvision(ctx, executable, repoRoot)
}

// ActivateService asks the canonical signed helper to activate its exact installed generation.
func ActivateService(ctx context.Context) error {
	return runServiceAction(ctx, helpercontract.DeploymentActivate)
}

// DeactivateService asks the canonical signed helper to deactivate its exact installed generation.
func DeactivateService(ctx context.Context) error {
	return runServiceAction(ctx, helpercontract.DeploymentDeactivate)
}

func runServiceAction(ctx context.Context, action helpercontract.DeploymentAction) error {
	executable, err := serviceExecutablePath()
	if err != nil {
		return err
	}
	result, err := serviceRunDeployment(ctx, executable, action)
	if err != nil {
		return err
	}
	if result.Action != action {
		return fmt.Errorf("cc-notes helper: deployment returned action %q for %q", result.Action, action)
	}
	return nil
}
