//go:build darwin && !ccnotes_test

package cli

import (
	"context"

	"github.com/yasyf/cc-notes/internal/helperdeployment"
)

func provisionRepositoryPlatform(ctx context.Context, root string) error {
	return helperdeployment.ProvisionRepository(ctx, root)
}

func installServicePlatform(ctx context.Context) error {
	return helperdeployment.InstallService(ctx)
}

func uninstallServicePlatform(ctx context.Context) error {
	return helperdeployment.UninstallService(ctx)
}
