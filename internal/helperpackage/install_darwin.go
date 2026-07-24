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
	"github.com/yasyf/cc-notes/internal/helperdeployment"
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
	apply:         helperdeployment.ApplyPackage,
	uninstall:     helperdeployment.UninstallPackage,
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
	if err := ensureRealDirectory(filepath.Dir(target)); err != nil {
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

func ensureRealDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("cc-notes package: application directory is not an exact absolute path")
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return fmt.Errorf("cc-notes package: create application directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("cc-notes package: inspect application directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("cc-notes package: application directory is not a real directory")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("cc-notes package: resolve application directory: %w", err)
	}
	if resolved != path {
		return errors.New("cc-notes package: application directory is not a canonical real path")
	}
	return nil
}
