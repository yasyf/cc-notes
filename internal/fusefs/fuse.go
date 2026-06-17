//go:build fuse

// The fuse build of package fusefs hosts the mount in-process via cgofuse
// driving FUSE-T on macOS (kext-less, NFS-over-loopback, mounted as the user
// without root) or libfuse3 on Linux. Build with:
//
//	CGO_ENABLED=1 go build -tags fuse ./...
//
// The mount lifecycle is fusekit's: carcass cleanup, panic-recovery, the
// bounded mount-up wait, and the graceful-then-forced teardown all live in
// github.com/yasyf/fusekit now. This file keeps only what is cc-notes-specific:
// the FUSE-T library pin, the synthesized-tree readiness check (hasMountRoot),
// and the foreground Mount entry point. The Config and the cache-defeat
// callbacks are built in holder.go; the synthesized FS in fs.go.
package fusefs

import (
	"context"
	"os"
	"runtime"

	"github.com/yasyf/fusekit"
)

// libfuseT is where the FUSE-T cask installs its dylib.
const libfuseT = "/usr/local/lib/libfuse-t.dylib"

func init() {
	// A dev Mac may have BOTH macFUSE's libfuse.2.dylib and FUSE-T's
	// libfuse-t.dylib. Without the override cgofuse dlopens macFUSE's
	// kext-backed lib first, so pin FUSE-T explicitly unless the user already
	// set the override. CGOFUSE_LIBFUSE_PATH is honored (and tried FIRST) only
	// by cgofuse newer than v1.6.0 — go.mod pins a post-v1.6.0 commit for
	// exactly this. The dlopen is lazy (first fuse call), so setting it here is
	// in time, and os.Setenv updates the C environment under cgo. The pin is
	// app-side per fusekit's platform decision: each consumer pins its own
	// platform's library.
	if runtime.GOOS == "darwin" && os.Getenv("CGOFUSE_LIBFUSE_PATH") == "" {
		_ = os.Setenv("CGOFUSE_LIBFUSE_PATH", libfuseT)
	}
}

// Mount serves repoRoot's notes and tasks at mountpoint in the foreground,
// blocking until ctx is canceled or the mount is removed externally with
// umount(8), then tearing it down — the `mount --foreground` path. The detached
// default goes through the mount holder instead (see internal/cli/mount.go).
// fusekit.Serve owns the whole lifecycle; see mount.go for the cross-build
// contract.
func Mount(ctx context.Context, repoRoot string, mountpoint string) error {
	return fusekit.Serve(ctx, buildConfig(repoRoot, mountpoint))
}

// hasMountRoot reports whether mountpoint serves the synthesized notes/tasks
// tree. It is cc-notes' readiness and liveness signal — used instead of
// fusekit.MountAlive because the mount renders a synthetic tree and the repo
// root's contents never show through it.
func hasMountRoot(mountpoint string) bool {
	entries, err := os.ReadDir(mountpoint)
	if err != nil {
		return false
	}
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Name()] = true
	}
	return seen["notes"] && seen["tasks"]
}
