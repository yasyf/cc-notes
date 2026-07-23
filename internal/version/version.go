// Package version exposes build metadata, injected at link time via -ldflags.
package version

import "runtime/debug"

var (
	// Version is the semantic version, set by -ldflags at release time.
	Version = "dev"
	// Commit is the exact git commit, set by -ldflags at release time.
	Commit = ""
	// HelperVersion is the exact signed-helper bundle version, set by -ldflags.
	HelperVersion = ""
	// HelperSHA256 is the exact signed-helper release digest, set by -ldflags.
	HelperSHA256 = ""
)

// String renders a human-readable version line.
func String() string {
	v := Version
	if v == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = bi.Main.Version
		}
	}
	if Commit != "" {
		v += " (" + Commit + ")"
	}
	return v
}
