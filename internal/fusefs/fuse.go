//go:build fuse

// The fuse build of package fusefs hosts the mount in-process via cgofuse
// driving FUSE-T on macOS (kext-less, NFS-over-loopback, mounted as the
// user without root) or libfuse3 on Linux. Build with:
//
//	CGO_ENABLED=1 go build -tags fuse ./...
//
// The mount lifecycle is a port of claude-pool's overlay provider
// (internal/overlay/fuse.go): the same library pin, liveness probe, and
// bounded graceful-then-forced teardown.
package fusefs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/winfsp/cgofuse/fuse"
	"golang.org/x/sys/unix"

	"github.com/yasyf/cc-notes/internal/store"
)

// libfuseT is where the FUSE-T cask installs its dylib.
const libfuseT = "/usr/local/lib/libfuse-t.dylib"

const (
	// mountWait bounds the wait for a freshly issued mount to come live; a
	// mount stuck on the one-time macOS "Network Volumes" TCC grant must
	// not hang the process.
	mountWait = 8 * time.Second
	// unmountGrace lets cgofuse's graceful Unmount complete before
	// teardown escalates to a forced kernel unmount.
	unmountGrace = 3 * time.Second
	// forceGrace bounds the wait for the serving goroutine to exit after a
	// forced unmount, so a wedged FUSE-T fault can't hold shutdown open.
	forceGrace = 2 * time.Second
	// statProbeTimeout bounds wedge-prone kernel stats: FUSE-T's NFS
	// backend has no soft/timeout mount options, so a stat through a dead
	// mount can block indefinitely.
	statProbeTimeout = 2 * time.Second
)

func init() {
	// A dev Mac may have BOTH macFUSE's libfuse.2.dylib and FUSE-T's
	// libfuse-t.dylib. Without the override cgofuse dlopens macFUSE's
	// kext-backed lib first, so pin FUSE-T explicitly unless the user
	// already set the override. CGOFUSE_LIBFUSE_PATH is honored (and tried
	// FIRST) only by cgofuse newer than v1.6.0 — go.mod pins a post-v1.6.0
	// commit for exactly this; v1.6.0 ignored the variable entirely. The
	// dlopen is lazy (first fuse call), so setting it here is in time, and
	// os.Setenv updates the C environment under cgo.
	if runtime.GOOS == "darwin" && os.Getenv("CGOFUSE_LIBFUSE_PATH") == "" {
		_ = os.Setenv("CGOFUSE_LIBFUSE_PATH", libfuseT)
	}
}

// Mount serves repoDir's notes and tasks at mountpoint in the foreground;
// see mount.go for the cross-build contract.
func Mount(ctx context.Context, repoDir string, mountpoint string) error {
	mp, err := filepath.Abs(mountpoint)
	if err != nil {
		return fmt.Errorf("mountpoint: %w", err)
	}
	if err := clearCarcass(mp); err != nil {
		return err
	}
	fi, err := os.Stat(mp)
	if err != nil {
		return fmt.Errorf("mountpoint: %w", err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("mountpoint %s: not a directory", mp)
	}
	preDev, err := devOf(mp)
	if err != nil {
		return fmt.Errorf("mountpoint: %w", err)
	}
	s, err := store.Open(repoDir)
	if err != nil {
		return err
	}
	repoRoot, err := s.Git.Root(ctx)
	if err != nil {
		return err
	}

	host := fuse.NewFileSystemHost(newFS(ctx, s))
	host.SetCapReaddirPlus(true)
	var opts []string
	if runtime.GOOS == "darwin" {
		// FUSE-T mount options: its NFS backend's only coherence lever is
		// noattrcache — the store is written concurrently by the CLI while
		// editors read through this mount, so attribute caching would serve
		// stale sizes. nobrowse keeps the mount out of Finder sidebars.
		opts = append(opts,
			"-o", "volname=cc-notes-"+filepath.Base(repoRoot),
			"-o", "noattrcache",
			"-o", "nobrowse",
		)
	}

	done := make(chan struct{})
	panicked := make(chan string, 1)
	go func() {
		defer close(done)
		defer func() {
			// cgofuse panics when the fuse library cannot be loaded; turn
			// that into the install-matrix error instead of crashing.
			if r := recover(); r != nil {
				panicked <- fmt.Sprint(r)
			}
		}()
		// Mount blocks until unmounted. ok=false means the mount failed.
		_ = host.Mount(mp, opts)
	}()

	if !waitMounted(mp, preDev, done, mountWait) {
		// Capture the failure mode before Unmount: an already-closed done
		// means the serving goroutine exited on its own (the mount failed
		// outright), versus a true 8s timeout with the goroutine still
		// blocked in host.Mount (the slow "Network Volumes" TCC grant).
		exited := false
		select {
		case <-done:
			exited = true
		default:
		}
		host.Unmount()
		select {
		case <-done:
		case <-time.After(unmountGrace):
		}
		select {
		case msg := <-panicked:
			return fmt.Errorf("%w: %s (macOS: brew install fuse-t; Linux: apt install fuse3)", ErrFuseUnavailable, msg)
		default:
		}
		if exited {
			return fmt.Errorf("%w: %s (mount failed; check fuse-t is installed and `mount | grep cc-notes`)", ErrMountNotLive, mp)
		}
		return fmt.Errorf("%w: %s (macOS: grant the one-time \"Network Volumes\" access in System Settings > Privacy & Security, then retry)", ErrMountNotLive, mp)
	}

	select {
	case <-ctx.Done():
		return teardown(host, mp, preDev, done)
	case <-done:
		return nil // unmounted externally (umount(8)) — clean exit
	}
}

// clearCarcass force-unmounts the dead mount a killed daemon leaves at
// mountpoint: a stat that answers ENOTCONN/EIO — or does not answer at all
// (FUSE-T's NFS backend has no soft/timeout knobs, so a dead server can
// hang the stat) — marks a carcass; umount -f (darwin) / fusermount3 -uz
// (linux) clears it, verified by one retried stat.
func clearCarcass(mountpoint string) error {
	if statAnswers(mountpoint) {
		return nil
	}
	forceUnmount(mountpoint)
	if statAnswers(mountpoint) {
		return nil
	}
	return fmt.Errorf("%w: dead mount at %s did not clear", ErrUnmountWedged, mountpoint)
}

// statAnswers reports a healthy, bounded stat of p. ENOENT is healthy —
// the path simply does not exist, which the caller rejects on its own
// terms; ENOTCONN/EIO and a stat that never answers are carcass signs.
func statAnswers(p string) bool {
	ch := make(chan error, 1)
	go func() {
		_, err := os.Stat(p)
		ch <- err
	}()
	select {
	case err := <-ch:
		return !errors.Is(err, unix.ENOTCONN) && !errors.Is(err, unix.EIO)
	case <-time.After(statProbeTimeout):
		return false
	}
}

func forceUnmount(mountpoint string) {
	switch runtime.GOOS {
	case "darwin":
		_ = exec.Command("umount", "-f", mountpoint).Run()
	default:
		_ = exec.Command("fusermount3", "-uz", mountpoint).Run()
	}
}

// waitMounted polls until the mount is live: the mountpoint's device id
// has changed from its pre-mount value AND the root readdir serves the
// synthesized notes/tasks tree. done closing early means the serving
// goroutine already exited — the mount failed outright.
func waitMounted(mountpoint string, preDev uint64, done <-chan struct{}, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			return false
		default:
		}
		if dev, err := devOf(mountpoint); err == nil && dev != preDev && hasMountRoot(mountpoint) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

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

// teardown unmounts bounded: cgofuse's host.Unmount is a blocking call
// that can wedge on a FUSE-T fault, so it runs behind a grace timer and
// escalates to a forced kernel unmount. Honest teardown: confirm the path
// is no longer a mountpoint, and report ErrUnmountWedged when it still is
// — a probe that does not answer reads still-mounted, never torn down.
func teardown(host *fuse.FileSystemHost, mountpoint string, preDev uint64, done <-chan struct{}) error {
	go host.Unmount()
	select {
	case <-done:
	case <-time.After(unmountGrace):
		_ = unix.Unmount(mountpoint, unix.MNT_FORCE)
		select {
		case <-done:
		case <-time.After(forceGrace):
		}
	}
	if mounted, answered := stillMounted(mountpoint, preDev); !answered || mounted {
		return fmt.Errorf("%w: %s", ErrUnmountWedged, mountpoint)
	}
	return nil
}

// stillMounted reports whether mountpoint's device id still differs from
// its pre-mount value, bounded by statProbeTimeout. A stat error reads
// still-mounted: an erroring mountpoint is occupied either way.
func stillMounted(mountpoint string, preDev uint64) (mounted, answered bool) {
	ch := make(chan bool, 1)
	go func() {
		dev, err := devOf(mountpoint)
		ch <- err != nil || dev != preDev
	}()
	select {
	case m := <-ch:
		return m, true
	case <-time.After(statProbeTimeout):
		return true, false
	}
}

func devOf(p string) (uint64, error) {
	var st unix.Stat_t
	if err := unix.Stat(p, &st); err != nil {
		return 0, err
	}
	return uint64(st.Dev), nil
}
