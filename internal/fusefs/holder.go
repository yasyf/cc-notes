//go:build fuse

package fusefs

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"time"

	"github.com/yasyf/fusekit"
	"github.com/yasyf/fusekit/mountd"

	"github.com/yasyf/cc-notes/internal/store"
)

// mountWait bounds the wait for a freshly issued mount to come live, handed to
// fusekit via Config.Wait. A mount stuck on the one-time macOS "Network
// Volumes" TCC grant must not hang the holder; fusekit owns the proven/unproven
// deduction that decides whether a timeout reads as the TCC condition
// (ErrMountNotLive) or transient slowness (ErrMountTimeout). FirstWait is left
// zero (fusekit falls back to Wait), preserving cc-notes' single 8s bound.
const mountWait = 8 * time.Second

// HolderHost returns the in-process fuse host that satisfies the mount holder's
// narrow seam (mountd.Host). One MountSet serves N repos: Build opens the store
// at the repo root (base) and renders the notes/tasks tree for the mountpoint
// (dir); StateFn reports the (mounted, alive) liveness pair. It is a *MountSet
// (the registry mutex/map cannot be copied), and the liveness func is the
// StateFn field — a struct field and the State method that satisfies mountd.Host
// cannot share a name.
func HolderHost() mountd.Host {
	return &fusekit.MountSet{Build: buildConfig, StateFn: probeState}
}

// buildConfig constructs the fusekit Config for serving base's notes and tasks
// at dir. base is the repo ROOT — the caller (the CLI, before it ever reaches
// the holder) resolves it through the store and passes it over the wire, so
// store.Open(base) is opening an already-validated repository; a failure here
// is an unreachable invariant violation (the root vanished mid-flight), loud by
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
	// is the NFS backend's only coherence lever (the store is written by the
	// CLI while editors read through the mount, so attribute caching would
	// serve stale sizes — MountOptions.Build forces it on darwin regardless),
	// and nobrowse keeps the mount out of Finder sidebars. cc-notes' fuse build
	// is cross-platform; on Linux libfuse3 understands none of these, so emit no
	// options there (matching the pre-fusekit darwin guard verbatim — no
	// namedattr, no rwsize).
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
		// ClearCarcass: force-unmount a dead-mount carcass a killed holder left
		// at dir before mounting over it — the lazy carcass cleanup that
		// substitutes for cc-notes' missing supervisor.
		ClearCarcass: true,
		CacheDefeat: &fusekit.CacheDefeat{
			VersionSeed: fs.notesSeed,
			Commit:      fs.notesCommit,
		},
	}
}

// probeState reports the (mounted, alive) liveness pair for dir. Liveness is
// hasMountRoot — the synthesized notes/tasks tree showing through — NOT
// fusekit.MountAlive, because cc-notes renders a synthetic tree and base's
// contents never appear under the mount. mounted is the non-blocking
// mountpoint check.
func probeState(base, dir string) (mounted, alive bool) {
	return fusekit.Mounted(dir), hasMountRoot(dir)
}
