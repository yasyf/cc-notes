//go:build darwin && !ccnotes_test

package cli

import (
	"context"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/helperdeployment"
	"github.com/yasyf/cc-notes/internal/version"
)

func provisionRepositoryPlatform(ctx context.Context, root string) error {
	appPath, err := helperclient.InstalledPath()
	if err != nil {
		return err
	}
	plan, err := helperdeployment.NewRuntimePlan(ctx, appPath, version.String())
	if err != nil {
		return err
	}
	return fusefs.ProvisionRepository(ctx, plan, root)
}

func installServicePlatform(ctx context.Context) error {
	return helperdeployment.ActivateService(ctx)
}

func uninstallServicePlatform(ctx context.Context) error {
	return helperdeployment.DeactivateService(ctx)
}
