package helperdeployment

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/daemonkit/bundle"
	"github.com/yasyf/daemonkit/codeidentity"
	"github.com/yasyf/daemonkit/daemon"
	"github.com/yasyf/daemonkit/deployment"
	"github.com/yasyf/daemonkit/service"
)

type deployer interface {
	Deploy(context.Context, deployment.Config) (serviceDeployment, error)
	Deactivate(context.Context, deployment.DeactivateConfig) (deactivationResult, error)
}

type daemonkitDeploymentController interface {
	Deploy(context.Context, deployment.Config) (deployment.DeploymentReceipt, error)
	Deactivate(context.Context, deployment.DeactivateConfig) (deployment.DeactivationResult, error)
}

type daemonkitDeployer struct {
	controller daemonkitDeploymentController
}

type serviceDeployment struct {
	operationID    string
	current        deployment.CanonicalGeneration
	plan           service.Plan
	activationPlan service.Plan
}

type deactivationState uint8

const (
	deactivationAbsent deactivationState = iota + 1
	deactivationInactive
)

type deactivationResult struct {
	state    deactivationState
	inactive serviceDeployment
}

var (
	newDeployer      = func() deployer { return daemonkitDeployer{controller: deployment.New()} }
	installedDir     = helperclient.InstalledDir
	makeProductHooks = newProductHooks
)

var releaseTagPattern = regexp.MustCompile(`^v((?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*))(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

func helperCodeIdentity() codeidentity.CodeIdentity {
	return codeidentity.CodeIdentity{
		TeamID: helperclient.TeamID, SigningIdentifier: helperclient.BundleID,
	}
}

func (d daemonkitDeployer) Deploy(
	ctx context.Context,
	config deployment.Config,
) (serviceDeployment, error) {
	receipt, err := d.controller.Deploy(ctx, config)
	if err != nil {
		return serviceDeployment{}, err
	}
	return mapDaemonkitReceipt(receipt, deployment.DeploymentActive)
}

func (d daemonkitDeployer) Deactivate(
	ctx context.Context,
	config deployment.DeactivateConfig,
) (deactivationResult, error) {
	result, err := d.controller.Deactivate(ctx, config)
	if err != nil {
		return deactivationResult{}, err
	}
	receipt, hasReceipt := result.Receipt()
	switch result.State() {
	case deployment.DeactivationAbsent:
		if hasReceipt {
			return deactivationResult{}, errors.New("cc-notes helper: daemonkit returned absence with a receipt")
		}
		return deactivationResult{state: deactivationAbsent}, nil
	case deployment.DeactivationInactive:
		if !hasReceipt {
			return deactivationResult{}, errors.New("cc-notes helper: daemonkit returned inactivity without a receipt")
		}
		inactive, err := mapDaemonkitReceipt(receipt, deployment.DeploymentInactive)
		if err != nil {
			return deactivationResult{}, err
		}
		return deactivationResult{state: deactivationInactive, inactive: inactive}, nil
	default:
		return deactivationResult{}, errors.New("cc-notes helper: daemonkit returned an unknown deactivation state")
	}
}

func mapDaemonkitReceipt(
	receipt deployment.DeploymentReceipt,
	wantState deployment.DeploymentState,
) (serviceDeployment, error) {
	current, hasCurrent := receipt.Current()
	if receipt.State() != wantState || !hasCurrent {
		return serviceDeployment{}, errors.New("cc-notes helper: daemonkit returned an inexact deployment receipt")
	}
	return serviceDeployment{
		operationID: receipt.OperationID(), current: current,
		plan: receipt.Plan(), activationPlan: receipt.ActivationPlan(),
	}, nil
}

// InstallService deploys the exact signed helper and reconciles its service plan.
func InstallService(ctx context.Context) error {
	_, err := installService(ctx, newDeployer())
	return err
}

// UninstallService durably deactivates the exact service while retaining its signed app.
func UninstallService(ctx context.Context) error {
	_, err := uninstallService(ctx, newDeployer())
	return err
}

func uninstallService(ctx context.Context, controller deployer) (deactivationResult, error) {
	dir, err := installedDir()
	if err != nil {
		return deactivationResult{}, err
	}
	consumerBuild, policyDigest, err := helperclient.DeploymentIdentity()
	if err != nil {
		return deactivationResult{}, err
	}
	hooks := makeProductHooks(version.String(), policyDigest)
	result, err := controller.Deactivate(ctx, deployment.DeactivateConfig{
		Dir: dir, AppName: helperclient.ExecutableName,
		Identity:      helperCodeIdentity(),
		ConsumerBuild: consumerBuild, PolicyDigest: policyDigest,
		RuntimeQuiesce: hooks.runtimeQuiesce,
		Readiness:      hooks.readiness,
	})
	if err != nil {
		return deactivationResult{}, fmt.Errorf("cc-notes helper: deactivate signed service: %w", err)
	}
	switch result.state {
	case deactivationAbsent:
	case deactivationInactive:
		if err := validateInactiveResult(dir, hooks, result.inactive); err != nil {
			return deactivationResult{}, err
		}
	default:
		return deactivationResult{}, errors.New("cc-notes helper: deactivation returned an unknown state")
	}
	return result, nil
}

func validateInactiveResult(
	dir string,
	hooks productHooks,
	receipt serviceDeployment,
) error {
	if receipt.operationID == "" || len(receipt.plan.Agents()) != 0 {
		return errors.New("cc-notes helper: deactivation did not return one exact inactive generation")
	}
	wantRequirement, err := helperCodeIdentity().DRString()
	if err != nil {
		return fmt.Errorf("cc-notes helper: derive designated requirement: %w", err)
	}
	wantApp := bundle.AppPath(dir, helperclient.ExecutableName)
	if receipt.current.Path != wantApp || receipt.current.DesignatedRequirement != wantRequirement ||
		receipt.current.CDHash == "" || receipt.current.BundleDigest == (deployment.SHA256{}) ||
		receipt.current.Device == "" || receipt.current.Inode == "" {
		return errors.New("cc-notes helper: deactivation did not retain the exact signed app generation")
	}
	operation := deployment.Operation{ID: receipt.operationID, Generation: receipt.current}
	activationPlan := receipt.activationPlan
	buildID, err := exactPlanBuildID(operation, activationPlan)
	if err != nil {
		return err
	}
	wantPlan, err := hooks.servicePlanBuild(operation, buildID)
	if err != nil {
		return fmt.Errorf("cc-notes helper: derive retained activation plan: %w", err)
	}
	if !samePlan(activationPlan, wantPlan) {
		return errors.New("cc-notes helper: deactivation returned the wrong retained activation plan")
	}
	return nil
}

func installService(ctx context.Context, controller deployer) (serviceDeployment, error) {
	dir, err := installedDir()
	if err != nil {
		return serviceDeployment{}, err
	}
	release, err := helperRelease()
	if err != nil {
		return serviceDeployment{}, err
	}
	consumerBuild, policyDigest, err := helperclient.DeploymentIdentity()
	if err != nil {
		return serviceDeployment{}, err
	}
	if err := ensureInstallDirectory(dir); err != nil {
		return serviceDeployment{}, err
	}
	hooks := makeProductHooks(version.String(), policyDigest)
	receipt, err := controller.Deploy(ctx, deployment.Config{
		Dir: dir, AppName: helperclient.ExecutableName, Release: release,
		Identity:      helperCodeIdentity(),
		ConsumerBuild: consumerBuild, PolicyDigest: policyDigest,
		RuntimeQuiesce: hooks.runtimeQuiesce, PostInstallProof: hooks.postInstallProof,
		PriorAppRestoreProof: hooks.priorAppRestoreProof, BuildPlan: hooks.buildPlan,
		Readiness: hooks.readiness,
	})
	if err != nil {
		return serviceDeployment{}, fmt.Errorf("cc-notes helper: deploy signed app %s: %w", version.Version, err)
	}
	wantRequirement, err := helperCodeIdentity().DRString()
	if err != nil {
		return serviceDeployment{}, fmt.Errorf("cc-notes helper: derive designated requirement: %w", err)
	}
	wantApp := bundle.AppPath(dir, helperclient.ExecutableName)
	if receipt.current.Path != wantApp || receipt.current.Release != release ||
		receipt.current.DesignatedRequirement != wantRequirement ||
		receipt.current.CDHash == "" || receipt.current.BundleDigest == (deployment.SHA256{}) ||
		receipt.current.Device == "" || receipt.current.Inode == "" {
		return serviceDeployment{}, errors.New("cc-notes helper: deployment returned the wrong current generation")
	}
	if receipt.operationID == "" {
		return serviceDeployment{}, errors.New("cc-notes helper: deployment returned no operation identity")
	}
	wantPlan, err := hooks.buildPlan(ctx, deployment.Operation{
		ID: receipt.operationID, Generation: receipt.current,
	})
	if err != nil {
		return serviceDeployment{}, fmt.Errorf("cc-notes helper: derive deployed service plan: %w", err)
	}
	if !samePlan(receipt.plan, wantPlan) || !samePlan(receipt.activationPlan, wantPlan) {
		return serviceDeployment{}, errors.New("cc-notes helper: deployment returned the wrong active service plan")
	}
	return receipt, nil
}

func samePlan(left, right service.Plan) bool {
	return left.Digest() == right.Digest() && reflect.DeepEqual(left.Agents(), right.Agents())
}

// ProvisionRepository invokes the already-installed signed helper for one repository.
func ProvisionRepository(ctx context.Context, repoRoot string) error {
	return provisionRepository(ctx, repoRoot, helperclient.ExecutablePath, helperclient.RunProvision)
}

func provisionRepository(
	ctx context.Context,
	repoRoot string,
	executablePath func() (string, error),
	run func(context.Context, string, string) error,
) error {
	executable, err := executablePath()
	if err != nil {
		return err
	}
	return run(ctx, executable, repoRoot)
}

func helperRelease() (deployment.Release, error) {
	if version.HelperVersion == "" || strings.TrimSpace(version.HelperVersion) != version.HelperVersion {
		return deployment.Release{}, errors.New("cc-notes helper: release bundle version is not exact")
	}
	match := releaseTagPattern.FindStringSubmatch(version.Version)
	if len(match) != 2 || version.HelperVersion != match[1] {
		return deployment.Release{}, errors.New("cc-notes helper: release tag and bundle version are not the same release")
	}
	digest, err := deployment.ParseSHA256(version.HelperSHA256)
	if err != nil {
		return deployment.Release{}, fmt.Errorf("cc-notes helper: parse release digest: %w", err)
	}
	return deployment.Release{
		Version: version.HelperVersion,
		URL: fmt.Sprintf(
			"https://github.com/yasyf/cc-notes/releases/download/%s/cc-notes-helper-%s-darwin.zip",
			version.Version, version.Version,
		),
		SHA256: digest,
	}, nil
}

func ensureInstallDirectory(path string) error {
	parent := filepath.Dir(path)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("cc-notes helper: inspect install parent %q: %w", parent, err)
	}
	if !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cc-notes helper: install parent %q is not a real directory", parent)
	}
	created := false
	if err := os.Mkdir(path, 0o700); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("cc-notes helper: create install directory %q: %w", path, err)
		}
	} else {
		created = true
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("cc-notes helper: inspect install directory %q: %w", path, err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("cc-notes helper: install path %q is not a real directory", path)
	}
	if created && info.Mode().Perm() != 0o700 {
		if err := os.Chmod(path, 0o700); err != nil {
			return fmt.Errorf("cc-notes helper: protect install directory %q: %w", path, err)
		}
		if err := daemon.SyncDir(path); err != nil {
			return fmt.Errorf("cc-notes helper: persist install directory permissions: %w", err)
		}
	}
	if created {
		if err := daemon.SyncDir(parent); err != nil {
			return fmt.Errorf("cc-notes helper: persist install directory: %w", err)
		}
	}
	return nil
}
