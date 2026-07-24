package helperdeployment

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/helpercontract"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/daemonkit/deployment"
	"github.com/yasyf/daemonkit/service"
)

type installedController interface {
	AttestInstalled(context.Context, deployment.InstalledSpec) (deployment.InstalledAttestation, error)
	StatusInstalled(context.Context, deployment.InstalledSpec) (deployment.InstalledStatus, error)
	ActivateInstalled(context.Context, deployment.ActivateInstalledConfig) (deployment.ActivationReceipt, error)
	DeactivateInstalled(context.Context, deployment.DeactivateInstalledConfig) (deployment.DeactivationReceipt, error)
}

var (
	newInstalledController = func() installedController { return deployment.New() }
	canonicalExecutable    = service.CanonicalExecutable
	installedPath          = helperclient.InstalledPath
	executeActivation      = activateInstalled
	executeDeactivation    = deactivateInstalled
)

// ExecuteDeployment runs one complete activation operation inside the fixed signed helper.
func ExecuteDeployment(
	ctx context.Context,
	request helpercontract.DeploymentRequest,
) (helpercontract.DeploymentResult, error) {
	running, err := canonicalExecutable()
	if err != nil {
		return helpercontract.DeploymentResult{}, fmt.Errorf("cc-notes helper: resolve running signed helper: %w", err)
	}
	appPath, err := installedPath()
	if err != nil {
		return helpercontract.DeploymentResult{}, err
	}
	wantExecutable := helperExecutablePath(appPath)
	if running != wantExecutable {
		return helpercontract.DeploymentResult{}, errors.New("cc-notes helper: deployment request did not reach the fixed signed app")
	}
	switch request.Action {
	case helpercontract.DeploymentActivate:
		return executeActivation(ctx, newInstalledController())
	case helpercontract.DeploymentDeactivate:
		return executeDeactivation(ctx, newInstalledController())
	default:
		return helpercontract.DeploymentResult{}, errors.New("cc-notes helper: deployment request action is invalid")
	}
}

func installedSpec() (deployment.InstalledSpec, error) {
	appPath, err := installedPath()
	if err != nil {
		return deployment.InstalledSpec{}, err
	}
	marketingVersion, err := helperMarketingVersion()
	if err != nil {
		return deployment.InstalledSpec{}, err
	}
	return deployment.InstalledSpec{
		AppPath: appPath, Version: marketingVersion, Identity: helperclient.CodeIdentity(),
	}, nil
}

func helperMarketingVersion() (string, error) {
	return helperclient.MarketingVersion()
}

func activationInputs(
	ctx context.Context,
	attestation deployment.InstalledAttestation,
) (service.Plan, productHooks, error) {
	consumerBuild, policyDigest, err := DeploymentIdentity()
	if err != nil {
		return service.Plan{}, productHooks{}, err
	}
	hooks := newProductHooks(version.String(), policyDigest)
	runtimePlan, err := NewRuntimePlan(ctx, attestation.Path(), hooks.buildID)
	if err != nil {
		return service.Plan{}, productHooks{}, err
	}
	fuseAttestation, ok := runtimePlan.FUSEAttestation()
	if !ok {
		return service.Plan{}, productHooks{}, errors.New("cc-notes helper: installed app has no exact FUSE attestation")
	}
	if deployment.SHA256(fuseAttestation.OuterEntitlementsSHA256) != attestation.EntitlementsDigest() {
		return service.Plan{}, productHooks{}, errors.New("cc-notes helper: FuseKit outer entitlements differ from daemonkit attestation")
	}
	plan, err := service.NewPlan([]service.Agent{runtimePlan.Deployment().Agent()})
	if err != nil {
		return service.Plan{}, productHooks{}, err
	}
	_ = consumerBuild
	return plan, hooks, nil
}

func activateInstalled(
	ctx context.Context,
	controller installedController,
) (helpercontract.DeploymentResult, error) {
	spec, err := installedSpec()
	if err != nil {
		return helpercontract.DeploymentResult{}, err
	}
	attestation, err := controller.AttestInstalled(ctx, spec)
	if err != nil {
		return helpercontract.DeploymentResult{}, fmt.Errorf("cc-notes helper: attest packaged app: %w", err)
	}
	plan, hooks, err := activationInputs(ctx, attestation)
	if err != nil {
		return helpercontract.DeploymentResult{}, err
	}
	consumerBuild, policyDigest, err := DeploymentIdentity()
	if err != nil {
		return helpercontract.DeploymentResult{}, err
	}
	receipt, err := controller.ActivateInstalled(ctx, deployment.ActivateInstalledConfig{
		Expected: attestation, ConsumerBuild: consumerBuild, PolicyDigest: policyDigest,
		Plan: plan, Readiness: hooks.readiness,
	})
	if err != nil {
		return helpercontract.DeploymentResult{}, fmt.Errorf("cc-notes helper: activate packaged app: %w", err)
	}
	if err := validateActivationReceipt(receipt, attestation, plan, hooks.buildID); err != nil {
		return helpercontract.DeploymentResult{}, err
	}
	return helpercontract.NewDeploymentResult(helpercontract.DeploymentActivate, helpercontract.DeploymentActive)
}

func deactivateInstalled(
	ctx context.Context,
	controller installedController,
) (helpercontract.DeploymentResult, error) {
	spec, err := installedSpec()
	if err != nil {
		return helpercontract.DeploymentResult{}, err
	}
	status, err := controller.StatusInstalled(ctx, spec)
	if err != nil {
		return helpercontract.DeploymentResult{}, fmt.Errorf("cc-notes helper: inspect packaged activation: %w", err)
	}
	if status.State() == deployment.InstalledVerifiedUnactivated {
		return helpercontract.NewDeploymentResult(helpercontract.DeploymentDeactivate, helpercontract.DeploymentInactive)
	}
	activation, ok := status.Receipt()
	if !ok {
		return helpercontract.DeploymentResult{}, errors.New("cc-notes helper: activation state has no exact receipt")
	}
	consumerBuild, policyDigest, err := DeploymentIdentity()
	if err != nil {
		return helpercontract.DeploymentResult{}, err
	}
	hooks := newProductHooks(version.String(), policyDigest)
	if status.State() == deployment.InstalledPrepared {
		plan, preparedHooks, err := activationInputs(ctx, status.Attestation())
		if err != nil {
			return helpercontract.DeploymentResult{}, err
		}
		activation, err = controller.ActivateInstalled(ctx, deployment.ActivateInstalledConfig{
			Expected: status.Attestation(), ConsumerBuild: consumerBuild, PolicyDigest: policyDigest,
			Plan: plan, Readiness: preparedHooks.readiness,
		})
		if err != nil {
			return helpercontract.DeploymentResult{}, fmt.Errorf("cc-notes helper: recover prepared activation: %w", err)
		}
		hooks = preparedHooks
	} else if status.State() != deployment.InstalledActive {
		return helpercontract.DeploymentResult{}, errors.New("cc-notes helper: installed activation has an unknown state")
	}
	receipt, err := controller.DeactivateInstalled(ctx, deployment.DeactivateInstalledConfig{
		Expected: activation, ConsumerBuild: consumerBuild, PolicyDigest: policyDigest,
		RuntimeQuiesce: hooks.runtimeQuiesce,
	})
	if err != nil {
		return helpercontract.DeploymentResult{}, fmt.Errorf("cc-notes helper: deactivate packaged app: %w", err)
	}
	proof := receipt.RuntimeProof()
	if !validDeploymentOperationID(receipt.OperationID()) || !proof.Absent() || proof.Digest() == (deployment.SHA256{}) {
		return helpercontract.DeploymentResult{}, errors.New("cc-notes helper: daemonkit returned an inexact deactivation receipt")
	}
	after, err := controller.StatusInstalled(ctx, spec)
	if err != nil {
		return helpercontract.DeploymentResult{}, fmt.Errorf("cc-notes helper: verify deactivated app: %w", err)
	}
	if after.State() != deployment.InstalledVerifiedUnactivated {
		return helpercontract.DeploymentResult{}, errors.New("cc-notes helper: deactivation retained activation ownership")
	}
	if _, hasReceipt := after.Receipt(); hasReceipt {
		return helpercontract.DeploymentResult{}, errors.New("cc-notes helper: deactivation retained an activation receipt")
	}
	return helpercontract.NewDeploymentResult(helpercontract.DeploymentDeactivate, helpercontract.DeploymentInactive)
}

func validateActivationReceipt(
	receipt deployment.ActivationReceipt,
	want deployment.InstalledAttestation,
	plan service.Plan,
	buildID string,
) error {
	readiness, ready := receipt.Readiness()
	if !receipt.Active() || !ready || !validDeploymentOperationID(receipt.OperationID()) ||
		!sameAttestation(receipt.Generation(), want) ||
		receipt.Plan().Digest() != plan.Digest() || !reflect.DeepEqual(receipt.Plan().Agents(), plan.Agents()) ||
		readiness.RuntimeBuild() != buildID || readiness.ProcessGeneration().String() == strings.Repeat("0", 32) ||
		readiness.ResourceDigest() == (deployment.SHA256{}) {
		return errors.New("cc-notes helper: daemonkit returned an inexact activation receipt")
	}
	return nil
}

func sameAttestation(left, right deployment.InstalledAttestation) bool {
	return left.Path() == right.Path() && left.Version() == right.Version() &&
		left.TeamID() == right.TeamID() && left.SigningIdentifier() == right.SigningIdentifier() &&
		left.DesignatedRequirement() == right.DesignatedRequirement() && left.CDHash() == right.CDHash() &&
		left.BundleDigest() == right.BundleDigest() && left.EntitlementsDigest() == right.EntitlementsDigest() &&
		left.Device() == right.Device() && left.Inode() == right.Inode()
}

func validDeploymentOperationID(value string) bool {
	if len(value) != 64 || value != strings.ToLower(value) {
		return false
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return false
	}
	for _, octet := range decoded {
		if octet != 0 {
			return true
		}
	}
	return false
}
