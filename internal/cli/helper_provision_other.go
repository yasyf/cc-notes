//go:build !darwin && !ccnotes_test

package cli

import (
	"context"
	"errors"
)

func provisionRepositoryPlatform(context.Context, string) error { return nil }

func installServicePlatform(context.Context) error {
	return errors.New("cc-notes service install is only supported on macOS")
}

func uninstallServicePlatform(context.Context) error {
	return errors.New("cc-notes service uninstall is only supported on macOS")
}
