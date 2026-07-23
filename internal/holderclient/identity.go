// Package holderclient invokes the fixed signed holder without linking its runtime.
package holderclient

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/yasyf/daemonkit/bundle"
)

const (
	// BundleID is the fixed holder application signing identifier.
	BundleID = "com.yasyf.cc-notes.holder"
	// TeamID is the fixed holder application signing team.
	TeamID = "SXKCTF23Q2"
	// ExecutableName is the fixed holder executable basename.
	ExecutableName = "CCNotesHolder"
)

// HomeStateDir returns ~/.cc-notes/<sub>, cc-notes' fixed per-user state root,
// rejecting a home directory that is not an exact absolute path.
func HomeStateDir(sub string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cc-notes holder: resolve home: %w", err)
	}
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return "", errors.New("cc-notes holder: home is not an exact absolute path")
	}
	return filepath.Join(home, ".cc-notes", sub), nil
}

// InstalledDir returns the caller-managed directory the fixed signed app installs into.
func InstalledDir() (string, error) {
	return HomeStateDir("holder")
}

// InstalledPath returns the fixed signed holder application bundle path.
func InstalledPath() (string, error) {
	dir, err := InstalledDir()
	if err != nil {
		return "", err
	}
	return bundle.AppPath(dir, ExecutableName), nil
}

// ExecutablePath returns the fixed signed holder executable path.
func ExecutablePath() (string, error) {
	appPath, err := InstalledPath()
	if err != nil {
		return "", err
	}
	return bundle.ExePath(appPath, ExecutableName), nil
}
