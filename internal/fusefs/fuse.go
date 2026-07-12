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
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/yasyf/fusekit"

	"github.com/yasyf/cc-notes/internal/store"
)

// Hostable reports whether this binary can host fuse mounts in-process. It is
// true in the fuse build, so automatic mounts (init's auto-mount, the
// session-start ensure-mount) proceed.
const Hostable = true

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
// default serves the same tree through the shared fusekit holder in
// ContentModeTree, fed by the contentd content server (see internal/cli/mount.go
// and contentd.go). fusekit.Serve owns the whole lifecycle; see mount.go for the
// cross-build contract.
func Mount(ctx context.Context, repoRoot string, mountpoint string) error {
	return fusekit.Serve(ctx, buildConfig(repoRoot, mountpoint))
}

// mountWait bounds the wait for a freshly issued foreground mount to come live,
// handed to fusekit via Config.Wait. A mount stuck on the one-time macOS
// "Network Volumes" TCC grant must not hang; fusekit owns the proven/unproven
// deduction that decides whether a timeout reads as the TCC condition
// (ErrMountNotLive) or transient slowness (ErrMountTimeout).
const mountWait = 8 * time.Second

// buildConfig constructs the fusekit Config for the in-process foreground mount
// of base's notes and tasks at dir. base is the repo ROOT — the caller resolves
// it through the store first, so store.Open(base) opens an already-validated
// repository and a failure here is an unreachable invariant violation, loud by
// design. The cache-defeat callbacks route cc-notes' NFS data-cache defeats
// through fusekit: notesSeed feeds the per-version mtime nanosecond on Getattr,
// notesCommit commits on both Flush and Fsync.
func buildConfig(base, dir string) fusekit.Config {
	s, err := store.Open(base)
	if err != nil {
		panic(fmt.Sprintf("fusefs: open store at repo root %s: %v", base, err))
	}
	fs := newFS(context.Background(), s)

	// The darwin-only fuse-t `-o` flags: volname names the volume, noattrcache
	// is the NFS backend's only coherence lever (the store is written by the CLI
	// while editors read through the mount, so attribute caching would serve
	// stale sizes), and nobrowse keeps the mount out of Finder sidebars.
	// cc-notes' fuse build is cross-platform; on Linux libfuse3 understands none
	// of these, so emit no options there.
	var opts []string
	if runtime.GOOS == "darwin" {
		opts = fusekit.MountOptions{
			Volname:  "cc-notes-" + filepath.Base(base),
			NoBrowse: true,
		}.Build()
	}

	return fusekit.Config{
		Base:    base,
		Dir:     dir,
		FS:      fs,
		Options: opts,
		// Liveness is the synthesized tree showing through, NOT base's contents
		// (the default MountAlive) — cc-notes renders a synthetic tree, so the
		// repo root never appears under the mount.
		Ready: func() bool { return hasMountRoot(dir) },
		Wait:  mountWait,
		CacheDefeat: &fusekit.CacheDefeat{
			VersionSeed: fs.notesSeed,
			Commit:      fs.notesCommit,
		},
	}
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
