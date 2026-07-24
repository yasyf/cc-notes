//go:build darwin

// Package helperpackage resolves the fixed signed helper delivered with cc-notes.
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
	packagedPath func() (string, error)
	install      func(context.Context, string) error
	uninstall    func(context.Context) error
}

var defaultOperations = operations{
	packagedPath: PackagedPath,
	install:      helperdeployment.InstallCandidate,
	uninstall:    helperdeployment.UninstallCurrent,
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

// Install delegates the complete local-candidate transaction to daemonkit.
func Install(ctx context.Context) error {
	return install(ctx, defaultOperations)
}

func install(ctx context.Context, ops operations) error {
	source, err := ops.packagedPath()
	if err != nil {
		return err
	}
	if err := ops.install(ctx, source); err != nil {
		return fmt.Errorf("cc-notes package: install signed helper: %w", err)
	}
	return nil
}

// Uninstall delegates the complete sealed uninstall transaction to daemonkit.
func Uninstall(ctx context.Context) error {
	return uninstall(ctx, defaultOperations)
}

func uninstall(ctx context.Context, ops operations) error {
	if err := ops.uninstall(ctx); err != nil {
		return fmt.Errorf("cc-notes package: uninstall signed helper: %w", err)
	}
	return nil
}
