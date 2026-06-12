package fusefs

import "errors"

// Mount — declared in fuse.go behind the fuse build tag, with a stub in
// fuse_stub.go for every other build — has the signature
//
//	Mount(ctx context.Context, repoDir string, mountpoint string) error
//
// It mounts repoDir's notes and tasks as a filesystem at mountpoint and
// serves it in the foreground: the call blocks until ctx is canceled (the
// CLI wires SIGINT/SIGTERM via signal.NotifyContext) or the mount is
// removed externally with umount(8), then tears the mount down. The
// mountpoint must already exist and be a directory; Mount never creates
// it. The sentinels below compile in every build variant so a non-fuse
// binary can still classify mount failures with errors.Is.
var (
	// ErrFuseUnavailable means the binary cannot host a fuse mount: it was
	// built without the fuse tag, or the fuse library failed to load at
	// mount time.
	ErrFuseUnavailable = errors.New("fuse support unavailable")

	// ErrMountNotLive means a fuse mount was issued but never came live —
	// on macOS almost always the one-time "Network Volumes" TCC grant.
	ErrMountNotLive = errors.New("fuse mount did not come up")

	// ErrUnmountWedged means an unmount did not take: the mountpoint is
	// still a live mount and must not be treated as torn down.
	ErrUnmountWedged = errors.New("unmount did not take")
)
