//go:build darwin && !ccnotes_test

package cli

import (
	"context"

	"github.com/yasyf/cc-notes/internal/helperclient"
)

func provisionRepositoryPlatform(ctx context.Context, root string) error {
	return helperclient.ProvisionRepository(ctx, root)
}
