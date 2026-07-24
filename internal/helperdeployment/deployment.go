package helperdeployment

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/daemonkit/deployment"
	"github.com/yasyf/daemonkit/service"
)

type controller interface {
	AttestInstalled(context.Context, deployment.InstalledSpec) (deployment.InstalledAttestation, error)
	ActivateInstalled(context.Context, deployment.ActivateInstalledConfig) (deployment.ActivationReceipt, error)
	DeactivateCurrentInstalled(context.Context, deployment.DeactivateCurrentInstalledConfig) (deployment.DeactivationReceipt, error)
	ApplyInstalledCandidate(context.Context, deployment.ApplyInstalledCandidateConfig) (deployment.ApplyInstalledCandidateReceipt, error)
	UninstallCurrentInstalled(context.Context, deployment.UninstallCurrentInstalledConfig) (deployment.UninstallReceipt, error)
}

var newController = func() controller { return deployment.New() }

func installedSpec(appPath string) (deployment.InstalledSpec, error) {
	marketingVersion, err := helperMarketingVersion()
	if err != nil {
		return deployment.InstalledSpec{}, err
	}
	return deployment.InstalledSpec{
		AppPath: appPath, Version: marketingVersion, Identity: helperclient.CodeIdentity(),
	}, nil
}

func currentSpec() (deployment.CurrentInstalledSpec, error) {
	appPath, err := helperclient.InstalledPath()
	if err != nil {
		return deployment.CurrentInstalledSpec{}, err
	}
	return deployment.CurrentInstalledSpec{AppPath: appPath, Identity: helperclient.CodeIdentity()}, nil
}

func helperMarketingVersion() (string, error) {
	return helperclient.MarketingVersion()
}

func activationInputs(
	ctx context.Context,
	attestation deployment.InstalledAttestation,
) (service.Plan, productHooks, error) {
	_, policyDigest, err := DeploymentIdentity()
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
	return plan, hooks, nil
}

// ApplyPackage installs and activates one exact delivered helper candidate.
func ApplyPackage(ctx context.Context, source string) error {
	manager := newController()
	candidateSpec, err := installedSpec(source)
	if err != nil {
		return err
	}
	candidate, err := manager.AttestInstalled(ctx, candidateSpec)
	if err != nil {
		return fmt.Errorf("cc-notes package: attest delivered app: %w", err)
	}
	candidateServicePlan, hooks, err := activationInputs(ctx, candidate)
	if err != nil {
		return err
	}
	candidatePlan, err := deployment.NewCandidatePlan(source, candidateServicePlan.Agents())
	if err != nil {
		return fmt.Errorf("cc-notes package: bind delivered service plan: %w", err)
	}
	consumerBuild, policyDigest, err := DeploymentIdentity()
	if err != nil {
		return err
	}
	target, err := currentSpec()
	if err != nil {
		return err
	}
	receipt, err := manager.ApplyInstalledCandidate(ctx, deployment.ApplyInstalledCandidateConfig{
		Target: target, CandidateSourcePath: source, CandidateVersion: candidate.Version(),
		CandidateBundleDigest: candidate.BundleDigest(), ConsumerBuild: consumerBuild,
		PolicyDigest: policyDigest, Plan: candidatePlan,
		RuntimeQuiesce: hooks.runtimeQuiesce, Readiness: hooks.readiness,
	})
	if err != nil {
		return fmt.Errorf("cc-notes package: apply delivered app: %w", err)
	}
	if !validDeploymentOperationID(receipt.OperationID()) {
		return errors.New("cc-notes package: daemonkit returned an inexact apply receipt")
	}
	installed, err := manager.AttestInstalled(ctx, deployment.InstalledSpec{
		AppPath: target.AppPath, Version: candidate.Version(), Identity: target.Identity,
	})
	if err != nil {
		return fmt.Errorf("cc-notes package: attest installed app: %w", err)
	}
	installedPlan, installedHooks, err := activationInputs(ctx, installed)
	if err != nil {
		return err
	}
	return validateActivationReceipt(receipt.Activation(), installed, installedPlan, installedHooks.buildID)
}

// ActivateService activates the exact installed helper generation.
func ActivateService(ctx context.Context) error {
	manager := newController()
	target, err := currentSpec()
	if err != nil {
		return err
	}
	spec, err := installedSpec(target.AppPath)
	if err != nil {
		return err
	}
	attestation, err := manager.AttestInstalled(ctx, spec)
	if err != nil {
		return fmt.Errorf("cc-notes helper: attest installed app: %w", err)
	}
	plan, hooks, err := activationInputs(ctx, attestation)
	if err != nil {
		return err
	}
	consumerBuild, policyDigest, err := DeploymentIdentity()
	if err != nil {
		return err
	}
	receipt, err := manager.ActivateInstalled(ctx, deployment.ActivateInstalledConfig{
		Expected: attestation, ConsumerBuild: consumerBuild, PolicyDigest: policyDigest,
		Plan: plan, Readiness: hooks.readiness,
	})
	if err != nil {
		return fmt.Errorf("cc-notes helper: activate installed app: %w", err)
	}
	return validateActivationReceipt(receipt, attestation, plan, hooks.buildID)
}

// DeactivateService durably retires the exact installed helper runtime.
func DeactivateService(ctx context.Context) error {
	manager := newController()
	target, hooks, err := currentHooks()
	if err != nil {
		return err
	}
	receipt, err := manager.DeactivateCurrentInstalled(ctx, deployment.DeactivateCurrentInstalledConfig{
		Current: target, RuntimeQuiesce: hooks.runtimeQuiesce, Readiness: hooks.readiness,
	})
	if err != nil {
		return fmt.Errorf("cc-notes helper: deactivate installed app: %w", err)
	}
	return validateRuntimeProof(receipt.OperationID(), receipt.RuntimeProof())
}

// UninstallPackage deactivates and removes the controller-sealed helper generation.
func UninstallPackage(ctx context.Context) error {
	manager := newController()
	target, hooks, err := currentHooks()
	if err != nil {
		return err
	}
	receipt, err := manager.UninstallCurrentInstalled(ctx, deployment.UninstallCurrentInstalledConfig{
		Current: target, RuntimeQuiesce: hooks.runtimeQuiesce, Readiness: hooks.readiness,
	})
	if err != nil {
		return fmt.Errorf("cc-notes package: uninstall installed app: %w", err)
	}
	if !validDeploymentOperationID(receipt.OperationID()) ||
		!validDeploymentOperationID(receipt.DeactivationOperationID()) {
		return errors.New("cc-notes package: daemonkit returned an inexact uninstall receipt")
	}
	generation := receipt.Generation()
	marketingVersion, err := helperMarketingVersion()
	if err != nil {
		return err
	}
	if generation.Path() != target.AppPath || generation.Version() != marketingVersion ||
		generation.TeamID() != target.Identity.TeamID ||
		generation.SigningIdentifier() != target.Identity.SigningIdentifier ||
		generation.BundleDigest() == (deployment.SHA256{}) ||
		generation.EntitlementsDigest() == (deployment.SHA256{}) {
		return errors.New("cc-notes package: uninstall receipt names a different helper generation")
	}
	return validateRuntimeProof(receipt.DeactivationOperationID(), receipt.RuntimeProof())
}

func currentHooks() (deployment.CurrentInstalledSpec, productHooks, error) {
	target, err := currentSpec()
	if err != nil {
		return deployment.CurrentInstalledSpec{}, productHooks{}, err
	}
	_, policyDigest, err := DeploymentIdentity()
	if err != nil {
		return deployment.CurrentInstalledSpec{}, productHooks{}, err
	}
	return target, newProductHooks(version.String(), policyDigest), nil
}

func validateRuntimeProof(operationID string, proof deployment.RuntimeProof) error {
	if !validDeploymentOperationID(operationID) || !proof.Absent() || proof.Digest() == (deployment.SHA256{}) {
		return errors.New("cc-notes helper: daemonkit returned an inexact deactivation receipt")
	}
	return nil
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
