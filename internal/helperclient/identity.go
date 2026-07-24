// Package helperclient invokes the fixed signed helper without linking its runtime.
package helperclient

import (
	"errors"
	"fmt"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/daemonkit/bundle"
	"github.com/yasyf/daemonkit/codeidentity"
)

var releaseVersionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// CodeIdentity returns the only accepted signed helper identity.
func CodeIdentity() codeidentity.CodeIdentity {
	return codeidentity.CodeIdentity{TeamID: TeamID, SigningIdentifier: BundleID}
}

// MarketingVersion returns the exact numeric helper bundle version for this release.
func MarketingVersion() (string, error) {
	tag := version.Version
	if !releaseVersionPattern.MatchString(tag) {
		return "", fmt.Errorf("cc-notes helper: build version %q is not an exact release tag", tag)
	}
	marketing := strings.TrimPrefix(tag, "v")
	if separator := strings.IndexByte(marketing, '-'); separator >= 0 {
		marketing = marketing[:separator]
	}
	return marketing, nil
}

const (
	// BundleID is the fixed helper application signing identifier.
	BundleID = "com.yasyf.cc-notes.helper"
	// TeamID is the fixed helper application signing team.
	TeamID = "SXKCTF23Q2"
	// ExecutableName is the fixed helper executable basename.
	ExecutableName = "CCNotesHelper"
)

// HomeStateDir returns ~/.cc-notes/<sub>, cc-notes' fixed per-user state root,
// rejecting a home directory that is not an exact absolute path.
func HomeStateDir(sub string) (string, error) {
	home, err := homeDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cc-notes", sub), nil
}

func homeDirectory() (string, error) {
	account, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("cc-notes helper: resolve current account: %w", err)
	}
	home := account.HomeDir
	if !filepath.IsAbs(home) || filepath.Clean(home) != home {
		return "", errors.New("cc-notes helper: home is not an exact absolute path")
	}
	return home, nil
}

// InstalledDir returns the fixed per-user application installation root.
func InstalledDir() (string, error) {
	home, err := homeDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "Applications"), nil
}

// InstalledPath returns the fixed signed helper application bundle path.
func InstalledPath() (string, error) {
	dir, err := InstalledDir()
	if err != nil {
		return "", err
	}
	return bundle.AppPath(dir, ExecutableName), nil
}

// PresentationRoot returns the sole user-visible native mount root.
func PresentationRoot() (string, error) {
	home, err := homeDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "CCNotes"), nil
}

// ExecutablePath returns the fixed signed helper executable path.
func ExecutablePath() (string, error) {
	appPath, err := InstalledPath()
	if err != nil {
		return "", err
	}
	return bundle.ExePath(appPath, ExecutableName), nil
}
