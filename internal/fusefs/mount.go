package fusefs

import "github.com/yasyf/fusekit"

// Mount — declared in fuse.go behind the fuse build tag, with a stub in
// fuse_stub.go for every other build — has the signature
//
//	Mount(ctx context.Context, repoRoot string, mountpoint string) error
//
// It serves repoRoot's notes, docs, and tasks as a filesystem at mountpoint in
// the FOREGROUND: the call blocks until ctx is canceled (the CLI wires
// SIGINT/SIGTERM via signal.NotifyContext) or the mount is removed externally
// with umount(8), then tears the mount down. It is the `mount --foreground`
// path and bypasses the mount holder; the default `mount` detaches and drives a
// holder instead. The mountpoint must already exist and be a directory; Mount
// never creates it.
//
// The sentinels below are ALIASES of fusekit's, never re-declared: errors.Is
// identity must hold across the process boundary so the CLI classifies a
// holder's wire-reported fuse failure identically to a local one. The pure-
// build refusal uses mountd.ErrCannotHost (a distinct sentinel that must never
// errors.Is-match a holder-availability condition); see fuse_stub.go.
var (
	// ErrFuseUnavailable means the binary cannot bring up the fuse runtime: it
	// was built with the fuse tag but the fuse library failed to load at mount
	// time (cgofuse could not dlopen libfuse-t/libfuse3).
	ErrFuseUnavailable = fusekit.ErrFuseUnavailable

	// ErrMountNotLive means a fuse mount was issued but never came live — on
	// macOS almost always the one-time "Network Volumes" TCC grant.
	ErrMountNotLive = fusekit.ErrMountNotLive

	// ErrUnmountWedged means an unmount did not take: the mountpoint is still a
	// live mount and must not be treated as torn down.
	ErrUnmountWedged = fusekit.ErrUnmountWedged
)
