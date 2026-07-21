//go:build darwin && !ccnotes_test

package cli

import (
	"context"

	"github.com/yasyf/cc-notes/internal/holderclient"
)

func provisionRepositoryPlatform(ctx context.Context, root string) error {
	return holderclient.ProvisionRepository(ctx, root)
}
