//go:build !darwin

package cli

import (
	"context"
	"errors"
)

var (
	installPackage   = unsupportedPackage
	uninstallPackage = unsupportedPackage
)

func unsupportedPackage(context.Context) error {
	return errors.New("cc-notes package: signed helper packaging requires macOS")
}
