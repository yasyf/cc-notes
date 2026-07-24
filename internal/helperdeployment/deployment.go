package helperdeployment

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/daemonkit/deployment"
	"github.com/yasyf/daemonkit/service"
)

type installedController interface {
	AttestInstalled(context.Context, deployment.InstalledSpec) (deployment.InstalledAttestation, error)
	ActivateInstalled(context.Context, deployment.ActivateInstalledConfig) (deployment.ActivationReceipt, error)
	ApplyInstalledCandidate(context.Context, deployment.ApplyInstalledCandidateConfig) (deployment.ApplyInstalledCandidateReceipt, error)
	DeactivateCurrentInstalled(context.Context, deployment.DeactivateCurrentInstalledConfig) (deployment.DeactivationReceipt, error)
	UninstallCurrentInstalled(context.Context, deployment.UninstallCurrentInstalledConfig) (deployment.UninstallReceipt, error)
}

var (
	newInstalledController = func() installedController { return deployment.New() }
	installedPath          = helperclient.InstalledPath
	helperMarketingVersion = helperclient.MarketingVersion
)

// InstallCandidate installs and activates one locally packaged signed helper.
func InstallCandidate(ctx context.Context, candidateSourcePath string) error {
	controller := newInstalledController()
	target, err := installedPath()
	if err != nil {
		return err
	}
	marketingVersion, err := helperMarketingVersion()
	if err != nil {
		return err
	}
	candidate, err := controller.AttestInstalled(ctx, deployment.InstalledSpec{
		AppPath: candidateSourcePath, Version: marketingVersion, Identity: helperclient.CodeIdentity(),
	})
	if err != nil {
		return fmt.Errorf("cc-notes helper: attest packaged candidate: %w", err)
	}
	plan, hooks, err := activationInputs(ctx, candidate, target)
	if err != nil {
		return err
	}
	receipt, err := controller.ApplyInstalledCandidate(ctx, deployment.ApplyInstalledCandidateConfig{
		Target: deployment.CurrentInstalledSpec{
			AppPath: target, Identity: helperclient.CodeIdentity(),
		},
		CandidateSourcePath:   candidateSourcePath,
		CandidateVersion:      marketingVersion,
		CandidateBundleDigest: candidate.BundleDigest(),
		ConsumerBuild:         version.String(),
		PolicyDigest:          hooks.policyDigest,
		Plan:                  plan,
		RuntimeQuiesce:        quiesceInstalled,
		Readiness:             hooks.readiness,
	})
	if err != nil {
		return fmt.Errorf("cc-notes helper: apply packaged candidate: %w", err)
	}
	activation := receipt.Activation()
	if receipt.OperationID() == "" || !activation.Active() {
		return errors.New("cc-notes helper: daemonkit returned an incomplete candidate receipt")
	}
	return validateActivationReceipt(activation, activation.Generation(), plan, version.String())
}

// ActivateCurrent activates or exactly reconciles the installed helper.
func ActivateCurrent(ctx context.Context) error {
	controller := newInstalledController()
	spec, err := installedSpec()
	if err != nil {
		return err
	}
	attestation, err := controller.AttestInstalled(ctx, spec)
	if err != nil {
		return fmt.Errorf("cc-notes helper: attest installed app: %w", err)
	}
	plan, hooks, err := activationInputs(ctx, attestation, attestation.Path())
	if err != nil {
		return err
	}
	receipt, err := controller.ActivateInstalled(ctx, deployment.ActivateInstalledConfig{
		Expected: attestation, ConsumerBuild: version.String(), PolicyDigest: hooks.policyDigest,
		Plan: plan, Readiness: hooks.readiness,
	})
	if err != nil {
		return fmt.Errorf("cc-notes helper: activate installed app: %w", err)
	}
	return validateActivationReceipt(receipt, attestation, plan, version.String())
}

// DeactivateCurrent deactivates the controller-sealed installed helper.
func DeactivateCurrent(ctx context.Context) error {
	target, err := installedPath()
	if err != nil {
		return err
	}
	receipt, err := newInstalledController().DeactivateCurrentInstalled(ctx, deployment.DeactivateCurrentInstalledConfig{
		Current: deployment.CurrentInstalledSpec{
			AppPath: target, Identity: helperclient.CodeIdentity(),
		},
		RuntimeQuiesce: quiesceInstalled,
		Readiness:      readinessInstalled,
	})
	if err != nil {
		return fmt.Errorf("cc-notes helper: deactivate installed app: %w", err)
	}
	proof := receipt.RuntimeProof()
	if receipt.OperationID() == "" || !proof.Absent() || proof.Digest() == (deployment.SHA256{}) {
		return errors.New("cc-notes helper: daemonkit returned an incomplete deactivation receipt")
	}
	return nil
}

// UninstallCurrent deactivates and removes the controller-sealed installed helper.
func UninstallCurrent(ctx context.Context) error {
	controller := newInstalledController()
	target, err := installedPath()
	if err != nil {
		return err
	}
	receipt, err := controller.UninstallCurrentInstalled(ctx, deployment.UninstallCurrentInstalledConfig{
		Current: deployment.CurrentInstalledSpec{
			AppPath: target, Identity: helperclient.CodeIdentity(),
		},
		RuntimeQuiesce: quiesceInstalled,
		Readiness:      readinessInstalled,
	})
	if err != nil {
		return fmt.Errorf("cc-notes helper: uninstall installed app: %w", err)
	}
	proof := receipt.RuntimeProof()
	if receipt.OperationID() == "" || receipt.DeactivationOperationID() == "" ||
		validateInstalledReceiptGeneration(receipt.Generation(), target) != nil || !proof.Absent() ||
		proof.Digest() == (deployment.SHA256{}) {
		return errors.New("cc-notes helper: daemonkit returned an incomplete uninstall receipt")
	}
	return nil
}

func readinessInstalled(
	ctx context.Context,
	operation deployment.InstalledOperation,
) (deployment.ReadinessProof, error) {
	generation := operation.Generation()
	buildID, err := exactPlanBuildID(generation.Path(), operation.Plan())
	if err != nil {
		return deployment.ReadinessProof{}, err
	}
	runtimePlan, err := NewRuntimePlan(ctx, generation.Path(), buildID)
	if err != nil {
		return deployment.ReadinessProof{}, err
	}
	hooks := newProductHooks(buildID, deployment.SHA256(runtimePlan.Deployment().RuntimePolicyDigest()))
	return hooks.readiness(ctx, operation)
}

func validateInstalledReceiptGeneration(generation deployment.InstalledAttestation, target string) error {
	identity := helperclient.CodeIdentity()
	if generation.Path() != target || generation.Version() == "" ||
		generation.TeamID() != identity.TeamID || generation.SigningIdentifier() != identity.SigningIdentifier ||
		generation.BundleDigest() == (deployment.SHA256{}) || generation.EntitlementsDigest() == (deployment.SHA256{}) {
		return errors.New("cc-notes helper: deployment receipt generation is inexact")
	}
	return nil
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

func activationInputs(
	ctx context.Context,
	attestation deployment.InstalledAttestation,
	activationPath string,
) (service.Plan, productHooks, error) {
	runtimePlan, err := NewRuntimePlan(ctx, attestation.Path(), version.String())
	if err != nil {
		return service.Plan{}, productHooks{}, err
	}
	fuseAttestation, ok := runtimePlan.FUSEAttestation()
	if !ok {
		return service.Plan{}, productHooks{}, errors.New("cc-notes helper: signed app has no exact FUSE attestation")
	}
	if deployment.SHA256(fuseAttestation.OuterEntitlementsSHA256) != attestation.EntitlementsDigest() {
		return service.Plan{}, productHooks{}, errors.New("cc-notes helper: FuseKit outer entitlements differ from daemonkit attestation")
	}
	hooks := newProductHooks(version.String(), deployment.SHA256(runtimePlan.Deployment().RuntimePolicyDigest()))
	plan, err := hooks.servicePlanForBuild(activationPath, version.String())
	if err != nil {
		return service.Plan{}, productHooks{}, err
	}
	return plan, hooks, nil
}

func quiesceInstalled(
	ctx context.Context,
	stopper deployment.RuntimeStopper,
	operation deployment.DeactivateInstalledOperation,
) (deployment.RuntimeProof, error) {
	activation := operation.Activation()
	buildID, err := exactPlanBuildID(activation.Generation().Path(), activation.Plan())
	if err != nil {
		return deployment.RuntimeProof{}, err
	}
	runtimePlan, err := NewRuntimePlan(ctx, activation.Generation().Path(), buildID)
	if err != nil {
		return deployment.RuntimeProof{}, err
	}
	hooks := newProductHooks(buildID, deployment.SHA256(runtimePlan.Deployment().RuntimePolicyDigest()))
	return hooks.runtimeQuiesce(ctx, stopper, operation)
}

func validateActivationReceipt(
	receipt deployment.ActivationReceipt,
	want deployment.InstalledAttestation,
	plan service.Plan,
	buildID string,
) error {
	if receipt.OperationID() == "" || !receipt.Active() || !sameAttestation(receipt.Generation(), want) ||
		receipt.Plan().Digest() != plan.Digest() || !reflect.DeepEqual(receipt.Plan().Agents(), plan.Agents()) {
		return errors.New("cc-notes helper: daemonkit returned an inexact activation receipt")
	}
	readiness, ok := receipt.Readiness()
	if !ok || readiness.RuntimeBuild() != buildID || readiness.ProcessGeneration().String() == "" ||
		readiness.ResourceDigest() == (deployment.SHA256{}) {
		return errors.New("cc-notes helper: daemonkit activation lacks exact readiness")
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
