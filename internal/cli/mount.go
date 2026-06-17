package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/fusekit/mountd"
)

// cannotHostHint is the install guidance appended to mountd.ErrCannotHost when a
// pure (non-fuse) build is asked to spawn a mount holder. It is cc-notes' brew
// text, lifted from the pre-fusekit fuse-unavailable message.
const cannotHostHint = "rebuild with -tags fuse, or install the _fuse release binary (macOS: brew install fuse-t; Linux: apt install fuse3)"

// mountOpts collects the mount command's flags.
type mountOpts struct {
	foreground bool
	list       bool
	shutdown   bool
	stop       string
	socket     string
}

func newMountCmd() *cobra.Command {
	var opts mountOpts
	cmd := &cobra.Command{
		Use:   "mount [MOUNTPOINT]",
		Short: "Mount notes and tasks as a filesystem (detaches; a mount holder serves it)",
		Long: "Mount the repository's notes and tasks as an editable filesystem.\n\n" +
			"By default `mount` DETACHES: a background mount holder serves the mount, the\n" +
			"command prints the mountpoint and returns, and the mount persists after the\n" +
			"command exits. MOUNTPOINT is created if it does not exist; omit it to use a\n" +
			"managed per-repo default under ~/.cc-notes/mnt. Unmount with `mount --stop DIR`\n" +
			"or plain `umount DIR` (the holder reconciles either).\n\n" +
			"--foreground keeps the old in-process lifecycle: the command blocks serving the\n" +
			"mount and Ctrl-C unmounts it (bypassing the holder).",
		Args: maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMount(cmd, args, opts)
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&opts.foreground, "foreground", "f", false, "serve in the foreground and unmount on Ctrl-C (bypasses the mount holder)")
	f.BoolVar(&opts.list, "list", false, "list the mounts the holder serves, then exit")
	f.BoolVar(&opts.shutdown, "shutdown", false, "unmount everything and stop the mount holder, then exit")
	f.StringVar(&opts.stop, "stop", "", "unmount the mount at DIR, then exit")
	f.StringVar(&opts.socket, "socket", mountsSocketPath(), "mount-holder unix socket path")
	_ = f.MarkHidden("socket")
	return cmd
}

// runMount dispatches the mount command: the holder-management modes
// (--list/--shutdown/--stop) are mutually exclusive with each other, with a
// MOUNTPOINT, and with --foreground; otherwise the command mounts, detaching by
// default or blocking under --foreground.
func runMount(cmd *cobra.Command, args []string, opts mountOpts) error {
	modes := 0
	if opts.list {
		modes++
	}
	if opts.shutdown {
		modes++
	}
	if opts.stop != "" {
		modes++
	}
	if modes > 1 {
		return &UsageError{Err: errors.New("--list, --shutdown, and --stop are mutually exclusive")}
	}
	if modes == 1 && (opts.foreground || len(args) > 0) {
		return &UsageError{Err: errors.New("--list, --shutdown, and --stop take no MOUNTPOINT and cannot be combined with --foreground")}
	}

	switch {
	case opts.list:
		return runMountList(cmd, opts.socket)
	case opts.shutdown:
		return runMountShutdown(cmd, opts.socket)
	case opts.stop != "":
		return runMountStop(cmd, opts.socket, opts.stop)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	repoRoot, mp, err := resolveRepoAndMountpoint(cmd.Context(), cwd, args)
	if err != nil {
		return err
	}

	if opts.foreground {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: mounting at %s (foreground; Ctrl-C to unmount)\n", mp); err != nil {
			return err
		}
		return fusefs.Mount(cmd.Context(), repoRoot, mp)
	}

	// Detached default: hand the mount to the holder (spawning it if needed),
	// print the mountpoint, and return — the mount persists.
	if err := newRemoteHost(opts.socket).Setup(repoRoot, mp); err != nil {
		return err
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), mp); err != nil {
		return err
	}
	return nil
}

// runMountList prints the mounts the holder serves, one tab-separated
// dir/base/liveness line each. A holder that is not running surfaces
// mountd.ErrHolderUnavailable (exit 1).
func runMountList(cmd *cobra.Command, socket string) error {
	mounts, err := mountd.NewClient(socket).List()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	for _, m := range mounts {
		live := "dead"
		if m.Live {
			live = "live"
		}
		if _, err := fmt.Fprintf(out, "%s\t%s\t%s\n", m.Dir, m.Base, live); err != nil {
			return err
		}
	}
	return nil
}

// runMountShutdown unmounts everything the holder owns and stops it. Dirs that
// fail to come down surface ErrUnmountWedged (exit 1).
func runMountShutdown(cmd *cobra.Command, socket string) error {
	client := mountd.NewClient(socket)
	failed, err := client.Shutdown()
	if err != nil {
		return err
	}
	if len(failed) > 0 {
		dirs := make([]string, len(failed))
		for i, m := range failed {
			dirs[i] = m.Dir
		}
		return fmt.Errorf("%w: %s", fusefs.ErrUnmountWedged, strings.Join(dirs, ", "))
	}
	client.WaitGone(5 * time.Second)
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "cc-notes: mount holder stopped"); err != nil {
		return err
	}
	return nil
}

// runMountStop unmounts the mount at dir via the holder. Nothing mounted at dir
// is an immediate no-op (no holder contact); a live mount or a carcass routes
// through the holder's bounded teardown.
func runMountStop(cmd *cobra.Command, socket, dir string) error {
	mp, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("mountpoint: %w", err)
	}
	// Teardown needs a base only for its base != dir refusal: a registered mount
	// is torn down by the base the holder recorded, and an unregistered carcass
	// ignores base entirely (a forced kernel unmount). The mountpoint's parent
	// is a stable non-dir value that satisfies the refusal.
	base := filepath.Dir(mp)
	if err := newRemoteHost(socket).Teardown(base, mp); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: unmounted %s\n", mp); err != nil {
		return err
	}
	return nil
}

// newRemoteHost builds the holder-backed mount driver for socket, carrying
// cc-notes' holder argv and pure-build install hint. SpawnTimeout is left zero
// (mountd.DefaultSpawnTimeout).
func newRemoteHost(socket string) *mountd.RemoteHost {
	return &mountd.RemoteHost{
		Socket:         socket,
		LogPath:        mountHolderLogPath(),
		Args:           []string{"mount-holder", "--socket", socket},
		CannotHostHint: cannotHostHint,
	}
}

// resolveRepoAndMountpoint resolves the repository root the mount renders over
// and the mountpoint to serve it at, creating the mountpoint when missing. base
// is always the worktree root (the holder keys its registry on the mountpoint
// and records the root as base), so an explicit MOUNTPOINT and the managed
// default both resolve against it. An explicit MOUNTPOINT is made absolute and
// created — a missing directory is the common first-run snag, not an error to
// refuse; with no argument it defaults to ~/.cc-notes/mnt/<base>-<hash>.
func resolveRepoAndMountpoint(ctx context.Context, cwd string, args []string) (repoRoot, mountpoint string, err error) {
	s, err := store.Open(cwd)
	if err != nil {
		return "", "", err
	}
	repoRoot, err = s.Git.Root(ctx)
	if err != nil {
		return "", "", err
	}
	if len(args) == 1 {
		mp, err := filepath.Abs(args[0])
		if err != nil {
			return "", "", fmt.Errorf("mountpoint: %w", err)
		}
		if err := os.MkdirAll(mp, 0o700); err != nil {
			return "", "", fmt.Errorf("create mountpoint %s: %w", mp, err)
		}
		return repoRoot, mp, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", fmt.Errorf("home directory: %w", err)
	}
	sum := sha256.Sum256([]byte(repoRoot))
	mp := filepath.Join(home, ".cc-notes", "mnt", filepath.Base(repoRoot)+"-"+hex.EncodeToString(sum[:])[:8])
	if err := os.MkdirAll(mp, 0o700); err != nil {
		return "", "", fmt.Errorf("create mountpoint %s: %w", mp, err)
	}
	return repoRoot, mp, nil
}
