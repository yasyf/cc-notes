// Package helperdeployment owns cc-notes' signed fixed-helper deployment policy.
package helperdeployment

import (
	"context"
	"errors"
	"fmt"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/daemonkit/codeidentity"
	"github.com/yasyf/daemonkit/trust"
	"github.com/yasyf/fusekit/holder"
)

// Application returns cc-notes' fixed signed helper identity at appPath.
func Application(appPath string) holder.SignedApplication {
	return holder.SignedApplication{
		AppPath: appPath, BundleID: helperclient.BundleID, TeamID: helperclient.TeamID,
		Runtime: holder.SignedExecutable{
			ExecutableName: helperclient.ExecutableName, SigningIdentifier: helperclient.BundleID,
		},
	}
}

// RuntimeDirectory returns the sole v1 derived helper state root.
func RuntimeDirectory() (string, error) {
	return helperclient.HomeStateDir("fusekit-v1")
}

// PresentationRoot returns the sole native mount presentation root.
func PresentationRoot() (string, error) {
	return helperclient.PresentationRoot()
}

// RuntimePlanSpec returns the signed-side native runtime contract.
func RuntimePlanSpec(
	appPath, runtimeDirectory, presentationRoot, buildID string,
	verifier *holder.FUSEVerifier,
) holder.RuntimePlanSpec {
	return holder.RuntimePlanSpec{
		Application: Application(appPath), RuntimeDirectory: runtimeDirectory,
		Native:  &holder.NativeRuntimeSpec{PresentationRoot: presentationRoot, FUSEVerifier: verifier},
		BuildID: buildID, Readiness: holder.StandardReadinessContract(), SourceCapable: true,
	}
}

// DeploymentPlanSpec returns the daemon-facing contract for one exact app generation.
func DeploymentPlanSpec(
	appPath, runtimeDirectory, presentationRoot, buildID string,
	runtimePolicyDigest codeidentity.PolicyDigest,
) holder.DeploymentPlanSpec {
	return holder.DeploymentPlanSpec{
		Application: Application(appPath), RuntimeDirectory: runtimeDirectory,
		Native:  &holder.NativeDeploymentSpec{PresentationRoot: presentationRoot},
		BuildID: buildID, Readiness: holder.StandardReadinessContract(), SourceCapable: true,
		RuntimePolicyDigest: runtimePolicyDigest,
	}
}

// NewRuntimePlan verifies one exact installed app generation and derives its runtime plan.
func NewRuntimePlan(ctx context.Context, appPath, buildID string) (plan holder.RuntimePlan, resultErr error) {
	runner, err := helperclient.NewFUSEToolPool(ctx)
	if err != nil {
		return holder.RuntimePlan{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, runner.Close(ctx)) }()
	verifier, err := holder.NewFUSEVerifier(runner.Pool())
	if err != nil {
		return holder.RuntimePlan{}, err
	}
	runtimeDirectory, err := RuntimeDirectory()
	if err != nil {
		return holder.RuntimePlan{}, err
	}
	presentationRoot, err := PresentationRoot()
	if err != nil {
		return holder.RuntimePlan{}, err
	}
	plan, err = holder.NewRuntimePlan(RuntimePlanSpec(
		appPath, runtimeDirectory, presentationRoot, buildID, verifier,
	))
	if err != nil {
		return holder.RuntimePlan{}, fmt.Errorf("cc-notes helper: derive runtime plan: %w", err)
	}
	return plan, nil
}

func runtimePolicyDigest() (codeidentity.PolicyDigest, error) {
	return (trust.Requirement{
		TeamID: helperclient.TeamID, SigningIdentifier: helperclient.BundleID,
	}).ValidationDigest()
}
