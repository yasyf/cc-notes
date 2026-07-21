// Package holderapp composes cc-notes' fixed signed FuseKit holder.
package holderapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yasyf/cc-notes/internal/holderclient"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/daemonkit/supervise"
	"github.com/yasyf/fusekit/fuset"
	"github.com/yasyf/fusekit/holder"
)

const (
	// BundleID is the fixed holder application signing identifier.
	BundleID = holderclient.BundleID
	// TeamID is the fixed holder application signing team.
	TeamID = holderclient.TeamID
	// ExecutableName is the fixed holder executable basename.
	ExecutableName = holderclient.ExecutableName
	// InstalledPath is the fixed holder application path.
	InstalledPath = holderclient.InstalledPath
)

// Application returns cc-notes' fixed signed holder identity.
func Application(appPath string) holder.SignedApplication {
	return holder.SignedApplication{
		AppPath: appPath, BundleID: BundleID, TeamID: TeamID,
		Runtime: holder.SignedExecutable{
			ExecutableName: ExecutableName, SigningIdentifier: BundleID,
		},
	}
}

// RuntimeDirectory returns the sole v1 derived holder state root.
func RuntimeDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cc-notes holder: resolve home: %w", err)
	}
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return "", errors.New("cc-notes holder: home is not an exact absolute path")
	}
	return filepath.Join(home, ".cc-notes", "fusekit-v1"), nil
}

// RuntimePlanSpec returns cc-notes' concrete signed-side holder contract.
func RuntimePlanSpec(appPath, runtimeDirectory, buildID string, verifier *holder.FUSEVerifier) holder.RuntimePlanSpec {
	return holder.RuntimePlanSpec{
		Application: Application(appPath), RuntimeDirectory: runtimeDirectory,
		BuildID: buildID, SourceCapable: true, FUSEVerifier: verifier,
	}
}

// NewRuntimePlan verifies the installed application and derives its runtime plan.
func NewRuntimePlan(ctx context.Context) (holder.RuntimePlan, error) {
	runner, err := holderclient.NewToolRunner(ctx)
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
	plan, planErr := holder.NewRuntimePlan(RuntimePlanSpec(
		InstalledPath, runtimeDirectory, version.String(), verifier,
	))
	if err := errors.Join(planErr, runner.Close(ctx)); err != nil {
		return holder.RuntimePlan{}, fmt.Errorf("cc-notes holder: derive runtime plan: %w", err)
	}
	return plan, nil
}

// PackageFUSE delegates the complete reviewed FUSE-T bundle transaction to FuseKit.
func PackageFUSE(ctx context.Context, runner supervise.TaskRunner, signingIdentity, appPath string) error {
	packager, err := holder.NewFUSEPackager(runner, signingIdentity)
	if err != nil {
		return fmt.Errorf("cc-notes holder: create FUSE packager: %w", err)
	}
	if _, err := packager.Package(ctx, Application(appPath), fuset.CaskDylib); err != nil {
		return fmt.Errorf("cc-notes holder: package FUSE bundle: %w", err)
	}
	return nil
}
