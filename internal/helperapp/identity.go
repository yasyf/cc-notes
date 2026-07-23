// Package helperapp composes cc-notes' fixed signed FuseKit helper.
package helperapp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/supervise"
	"github.com/yasyf/fusekit/fuset"
	"github.com/yasyf/fusekit/holder"
)

const (
	// BundleID is the fixed helper application signing identifier.
	BundleID = helperclient.BundleID
	// TeamID is the fixed helper application signing team.
	TeamID = helperclient.TeamID
	// ExecutableName is the fixed helper executable basename.
	ExecutableName = helperclient.ExecutableName
	// StopControlRole is the sole tracked helper process role allowed to settle the runtime.
	StopControlRole = BundleID + ".stop-control"
)

// Application returns cc-notes' fixed signed helper identity.
func Application(appPath string) holder.SignedApplication {
	return holder.SignedApplication{
		AppPath: appPath, BundleID: BundleID, TeamID: TeamID,
		Runtime: holder.SignedExecutable{
			ExecutableName: ExecutableName, SigningIdentifier: BundleID,
		},
	}
}

// RuntimeDirectory returns the sole v1 derived helper state root.
func RuntimeDirectory() (string, error) {
	return helperclient.HomeStateDir("fusekit-v1")
}

// StopControlStore returns the controller-owned one-shot stop authority store.
func StopControlStore(plan holder.RuntimePlan) proc.StopControlStore {
	return stopControlStore(plan.Paths().Directory)
}

func stopControlStore(runtimeDirectory string) *proc.FileStore {
	return &proc.FileStore{Path: serviceProcessPath(runtimeDirectory)}
}

func serviceProcessPath(runtimeDirectory string) string {
	return filepath.Join(runtimeDirectory, "service-processes.db")
}

// RuntimePlanSpec returns cc-notes' concrete signed-side helper contract.
func RuntimePlanSpec(appPath, runtimeDirectory, buildID string, verifier *holder.FUSEVerifier) holder.RuntimePlanSpec {
	return holder.RuntimePlanSpec{
		Application: Application(appPath), RuntimeDirectory: runtimeDirectory,
		BuildID: buildID, SourceCapable: true, FUSEVerifier: verifier,
	}
}

// NewRuntimePlan verifies the installed application and derives its runtime plan.
func NewRuntimePlan(ctx context.Context) (holder.RuntimePlan, error) {
	runner, err := helperclient.NewToolRunner(ctx)
	if err != nil {
		return holder.RuntimePlan{}, err
	}
	verifier, verifierErr := holder.NewFUSEVerifier(runner)
	if verifierErr != nil {
		return holder.RuntimePlan{}, errors.Join(verifierErr, runner.Close(ctx))
	}
	runtimeDirectory, pathErr := RuntimeDirectory()
	if pathErr != nil {
		return holder.RuntimePlan{}, errors.Join(pathErr, runner.Close(ctx))
	}
	installedPath, installedErr := helperclient.InstalledPath()
	if installedErr != nil {
		return holder.RuntimePlan{}, errors.Join(installedErr, runner.Close(ctx))
	}
	plan, planErr := holder.NewRuntimePlan(RuntimePlanSpec(
		installedPath, runtimeDirectory, version.String(), verifier,
	))
	if err := errors.Join(planErr, runner.Close(ctx)); err != nil {
		return holder.RuntimePlan{}, fmt.Errorf("cc-notes helper: derive runtime plan: %w", err)
	}
	return plan, nil
}

// PackageFUSE delegates the complete reviewed FUSE-T bundle transaction to FuseKit.
func PackageFUSE(ctx context.Context, runner supervise.TaskRunner, signingIdentity, appPath string) error {
	packager, err := holder.NewFUSEPackager(runner, signingIdentity)
	if err != nil {
		return fmt.Errorf("cc-notes helper: create FUSE packager: %w", err)
	}
	if _, err := packager.Package(ctx, Application(appPath), fuset.CaskDylib); err != nil {
		return fmt.Errorf("cc-notes helper: package FUSE bundle: %w", err)
	}
	return nil
}
