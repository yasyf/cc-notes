//go:build darwin

package helperclient

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The deployment verbs the signed helper answers, and the ordinary CLI
// forwards to it because daemonkit admits it on neither lane.
const (
	VerbVersion          = "version"
	VerbPackageInstall   = "package-install"
	VerbPackageUninstall = "package-uninstall"
	VerbServiceInstall   = "service-install"
	VerbServiceUninstall = "service-uninstall"
	VerbProvisionRepo    = "provision-repository"
)

// EnsureApplicationDirectory creates the fixed user application directory and
// proves it is a real one. A symlinked ~/Applications would land the signed
// helper somewhere the deployment never named, so an inexact directory is
// refused rather than followed.
func EnsureApplicationDirectory(path string) error {
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
