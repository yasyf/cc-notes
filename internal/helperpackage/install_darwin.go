//go:build darwin

// Package helperpackage installs the fixed signed helper already delivered with cc-notes.
package helperpackage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yasyf/cc-notes/internal/helperclient"
)

const packagedDirectory = "libexec"

type operations struct {
	packagedPath  func() (string, error)
	installedPath func() (string, error)
	apply         func(context.Context, string) error
	uninstall     func(context.Context) error
}

var defaultOperations = operations{
	packagedPath:  PackagedPath,
	installedPath: helperclient.InstalledPath,
	apply: func(ctx context.Context, source string) error {
		return invokeAt(ctx, source, helperclient.VerbPackageInstall)
	},
	uninstall: func(ctx context.Context) error {
		return Invoke(ctx, helperclient.VerbPackageUninstall)
	},
}

// PackagedPath returns the helper bundled beside the resolved cc-notes executable.
func PackagedPath() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("cc-notes package: resolve executable: %w", err)
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", fmt.Errorf("cc-notes package: resolve executable links: %w", err)
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return "", errors.New("cc-notes package: executable is not an exact absolute path")
	}
	prefix := filepath.Dir(filepath.Dir(executable))
	return filepath.Join(prefix, packagedDirectory, helperclient.ExecutableName+".app"), nil
}

// Install applies the exact delivered helper candidate through daemonkit.
func Install(ctx context.Context) error {
	return install(ctx, defaultOperations)
}

func install(ctx context.Context, ops operations) error {
	source, err := ops.packagedPath()
	if err != nil {
		return err
	}
	target, err := ops.installedPath()
	if err != nil {
		return err
	}
	if source == target {
		return errors.New("cc-notes package: packaged and installed helper paths are identical")
	}
	if err := helperclient.EnsureApplicationDirectory(filepath.Dir(target)); err != nil {
		return err
	}
	return ops.apply(ctx, source)
}

// Uninstall removes the controller-sealed installed helper through daemonkit.
func Uninstall(ctx context.Context) error {
	return uninstall(ctx, defaultOperations)
}

func uninstall(ctx context.Context, ops operations) error {
	return ops.uninstall(ctx)
}
