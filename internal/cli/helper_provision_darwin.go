//go:build darwin && !ccnotes_test

package cli

import (
	"context"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/helperdeployment"
)

func provisionRepositoryPlatform(ctx context.Context, root string) error {
	return helperclient.ProvisionRepository(ctx, root)
}

func installServicePlatform(ctx context.Context) error {
	return helperdeployment.ActivateService(ctx)
}

func uninstallServicePlatform(ctx context.Context) error {
	return helperdeployment.DeactivateService(ctx)
}
