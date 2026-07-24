// Package helperapp composes cc-notes' fixed signed FuseKit helper.
package helperapp

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/helperdeployment"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/daemonkit/service"
	"github.com/yasyf/daemonkit/trust"
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
)

// RuntimeTrustRequirements pins every protected lifecycle role to the sole fixed signed helper.
func RuntimeTrustRequirements() holder.RuntimeTrustRequirements {
	requirement := trust.Requirement{TeamID: TeamID, SigningIdentifier: BundleID}
	return holder.RuntimeTrustRequirements{
		StopController: requirement, ReceiptController: requirement, ReadinessController: requirement,
	}
}

// Application returns cc-notes' fixed signed helper identity.
func Application(appPath string) holder.SignedApplication {
	return helperdeployment.Application(appPath)
}

// RuntimeDirectory returns the sole v1 derived helper state root.
func RuntimeDirectory() (string, error) {
	return helperdeployment.RuntimeDirectory()
}

// PresentationRoot returns the sole native mount presentation root.
func PresentationRoot() (string, error) {
	return helperdeployment.PresentationRoot()
}

// RuntimePlanSpec returns cc-notes' concrete signed-side helper contract.
func RuntimePlanSpec(
	appPath, runtimeDirectory, presentationRoot, buildID string,
	verifier *holder.FUSEVerifier,
) holder.RuntimePlanSpec {
	return helperdeployment.RuntimePlanSpec(appPath, runtimeDirectory, presentationRoot, buildID, verifier)
}

// NewRuntimePlan verifies the installed application and derives its runtime plan.
func NewRuntimePlan(ctx context.Context) (holder.RuntimePlan, error) {
	executable, err := service.CanonicalExecutable()
	if err != nil {
		return holder.RuntimePlan{}, fmt.Errorf("FuseKit runtime: resolve canonical executable: %w", err)
	}
	macOS := filepath.Dir(executable)
	contents := filepath.Dir(macOS)
	appPath := filepath.Dir(contents)
	if filepath.Base(executable) != ExecutableName || filepath.Base(macOS) != "MacOS" ||
		filepath.Base(contents) != "Contents" || filepath.Base(appPath) != ExecutableName+".app" {
		return holder.RuntimePlan{}, errors.New("FuseKit runtime: executable is not the fixed CCNotesHelper app child")
	}
	installedPath, err := helperclient.InstalledPath()
	if err != nil {
		return holder.RuntimePlan{}, fmt.Errorf("FuseKit runtime: resolve fixed application path: %w", err)
	}
	if appPath != installedPath {
		return holder.RuntimePlan{}, errors.New("FuseKit runtime: application is not installed at the fixed user path")
	}
	plan, err := helperdeployment.NewRuntimePlan(ctx, appPath, version.String())
	if err != nil {
		return holder.RuntimePlan{}, fmt.Errorf("FuseKit runtime: derive runtime plan: %w", err)
	}
	return plan, nil
}

// PackageFUSE delegates the complete reviewed FUSE-T bundle transaction to FuseKit.
func PackageFUSE(ctx context.Context, pool *fuset.ToolPool, signingIdentity, appPath string) error {
	packager, err := holder.NewFUSEPackager(pool, signingIdentity)
	if err != nil {
		return fmt.Errorf("cc-notes helper: create FUSE packager: %w", err)
	}
	if _, err := packager.Package(ctx, Application(appPath), fuset.CaskDylib); err != nil {
		return fmt.Errorf("cc-notes helper: package FUSE bundle: %w", err)
	}
	return nil
}
