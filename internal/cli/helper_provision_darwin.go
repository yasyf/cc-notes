//go:build darwin && !ccnotes_test

package cli

import (
	"context"

	"github.com/yasyf/cc-notes/internal/helperclient"
	"github.com/yasyf/cc-notes/internal/helperpackage"
)

func provisionRepositoryPlatform(ctx context.Context, root string) error {
	return helperpackage.Invoke(ctx, helperclient.VerbProvisionRepo, root)
}

func installServicePlatform(ctx context.Context) error {
	return helperpackage.Invoke(ctx, helperclient.VerbServiceInstall)
}

func uninstallServicePlatform(ctx context.Context) error {
	return helperpackage.Invoke(ctx, helperclient.VerbServiceUninstall)
}
