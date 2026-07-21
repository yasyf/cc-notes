// Package holderclient invokes the fixed signed holder without linking its runtime.
package holderclient

import "path/filepath"

const (
	// BundleID is the fixed holder application signing identifier.
	BundleID = "com.yasyf.cc-notes.holder"
	// TeamID is the fixed holder application signing team.
	TeamID = "SXKCTF23Q2"
	// ExecutableName is the fixed holder executable basename.
	ExecutableName = "CCNotesHolder"
	// InstalledPath is the fixed holder application path.
	InstalledPath = "/Applications/CCNotesHolder.app"
)

// ExecutablePath returns the fixed signed holder executable path.
func ExecutablePath() string {
	return filepath.Join(InstalledPath, "Contents", "MacOS", ExecutableName)
}
