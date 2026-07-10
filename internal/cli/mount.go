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

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/fusekit"
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
	caskless   bool
	auto       bool
}

func newMountCmd() *cobra.Command {
	var opts mountOpts
	cmd := &cobra.Command{
		Use:   "mount [MOUNTPOINT]",
		Short: "Mount notes, docs, and tasks as a filesystem (detaches; a mount holder serves it)",
		Long: "Mount the repository's notes, docs, and tasks as an editable filesystem.\n\n" +
			"By default `mount` DETACHES: a background mount holder serves the mount, the\n" +
			"command prints the mountpoint and returns, and the mount persists after the\n" +
			"command exits. With no MOUNTPOINT the mount is served at a managed per-repo\n" +
			"default under ~/.cc-notes/mnt and presented in the repo as a `.notes` symlink\n" +
			"into it (kept out of git via .git/info/exclude); `cd .notes` to browse. Pass an\n" +
			"explicit MOUNTPOINT to serve there instead — it is created if missing and no\n" +
			"symlink is made. Unmount with `mount --stop DIR` (DIR may be `.notes`) or plain\n" +
			"`umount DIR` (the holder reconciles either); --stop and --shutdown remove the\n" +
			".notes symlink they created.\n\n" +
			"--foreground keeps the old in-process lifecycle: the command blocks serving the\n" +
			"mount and Ctrl-C unmounts it (bypassing the holder).",
		Args: maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMount(cmd, args, opts)
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&opts.foreground, "foreground", "f", false, "serve in the foreground and unmount on Ctrl-C (bypasses the mount holder)")
	f.BoolVar(&opts.list, "list", false, "list cc-notes' mounts on the holder, then exit")
	f.BoolVar(&opts.shutdown, "shutdown", false, "unmount cc-notes' own mounts (the shared holder keeps running), then exit")
	f.StringVar(&opts.stop, "stop", "", "unmount the mount at DIR, then exit")
	f.StringVar(&opts.socket, "socket", "", "mount-holder unix socket path (default: the shared fusekit-holder cask socket, or ~/.cc-notes/mounts.sock with --caskless)")
	_ = f.MarkHidden("socket")
	f.BoolVar(&opts.caskless, "caskless", false, "drive cc-notes' own self-exec mount holder instead of the shared fusekit-holder cask (also via CC_NOTES_CASKLESS_HOLDER)")
	_ = f.MarkHidden("caskless")
	f.BoolVar(&opts.auto, "auto", false, "session-start ensure-mount: mount only if this repo opted in (cc-notes.autoMount) and the binary can host fuse; self-gating, best-effort, quiet")
	_ = f.MarkHidden("auto")
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

	if opts.auto && (modes > 0 || opts.foreground || len(args) > 0) {
		return &UsageError{Err: errors.New("--auto takes no MOUNTPOINT and cannot be combined with --foreground, --list, --shutdown, or --stop")}
	}

	// Resolve the holder mode and its socket once: the shared fusekit-holder cask
	// (default) or cc-notes' own self-exec holder (--caskless / env). --caskless is
	// orthogonal to the modes above — it selects WHICH holder, not what to do.
	caskless := opts.caskless || casklessEnv()
	socket := holderSocket(opts.socket, caskless)

	switch {
	case opts.auto:
		return runMountAuto(cmd)
	case opts.list:
		return runMountList(cmd, socket)
	case opts.shutdown:
		return runMountShutdown(cmd, socket)
	case opts.stop != "":
		return runMountStop(cmd, socket, caskless, opts.stop)
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	repoRoot, mp, usedDefault, err := resolveRepoAndMountpoint(cmd.Context(), cwd, args)
	if err != nil {
		return err
	}
	// Reject a conflicting .notes before serving, so a real file or directory at
	// the path fails fast instead of after Setup has already brought a mount up.
	if usedDefault {
		if err := notesLinkBlocked(repoRoot); err != nil {
			return err
		}
	}

	if opts.foreground {
		// The managed default is presented at .notes before the blocking serve
		// and removed when it returns (Ctrl-C cancels ctx → Mount returns).
		shown := mp
		if usedDefault {
			link, err := presentNotes(cmd.Context(), gitcmd.Git{Dir: repoRoot}, repoRoot, mp)
			if err != nil {
				return err
			}
			shown = link
			defer func() { _ = unlinkNotes(repoRoot, mp) }()
		}
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: mounting at %s (foreground; Ctrl-C to unmount)\n", shown); err != nil {
			return err
		}
		return fusefs.Mount(cmd.Context(), repoRoot, mp)
	}

	// Detached default: hand the mount to the holder and present the managed
	// .notes symlink. serveDetached owns that whole path so `init`'s auto-mount
	// reuses it verbatim.
	return serveDetached(cmd, socket, caskless, repoRoot, mp, usedDefault)
}

// serveDetached hands repoRoot's mount to the holder on socket and returns —
// the mount persists. It ensures the mount via Setup, then — for the managed
// default (usedDefault) — presents it in the repo as a .notes symlink and prints
// that path; an explicit mountpoint prints itself. The state dir is created
// first: it homes the spawn log (and, in cask-less mode, the default socket), and
// fusekit treats both paths' parent dirs as the caller's to create. Shared by
// `mount` and `init`.
func serveDetached(cmd *cobra.Command, socket string, caskless bool, repoRoot, mp string, usedDefault bool) error {
	if err := os.MkdirAll(stateDir(), 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	host := newRemoteHost(socket, caskless)
	if err := host.Setup(repoRoot, mp); err != nil {
		return err
	}
	// Present the managed default at .notes only after the mount is live, so a
	// failed Setup leaves no dangling symlink; print .notes (the path to browse)
	// and note the managed target on stderr. An explicit MOUNTPOINT prints itself.
	out := mp
	if usedDefault {
		link, err := presentNotes(cmd.Context(), gitcmd.Git{Dir: repoRoot}, repoRoot, mp)
		if err != nil {
			return err
		}
		out = link
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: serving at %s\n", mp); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(cmd.OutOrStdout(), out); err != nil {
		return err
	}
	return nil
}

// runMountList prints the mounts cc-notes owns on the holder, one tab-separated
// dir/base/liveness line each. The listing is owner-scoped: on the shared cask
// holder it shows only cc-notes' mounts, never another tenant's. A holder that
// is not running surfaces mountd.ErrHolderUnavailable (exit 1).
func runMountList(cmd *cobra.Command, socket string) error {
	mounts, err := ownedClient(socket).List()
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

// runMountShutdown reclaims cc-notes' own mounts on the holder — a per-owner
// unmount of every mount tagged Owner "cc-notes" — and removes the .notes
// symlinks presenting them. It does NOT stop the holder: the shared
// fusekit-holder cask hosts other consumers' mounts and refuses a cross-owner
// Shutdown, so cc-notes only ever tears down its own mounts and leaves the
// holder running for its other tenants. Dirs that fail to come down surface
// ErrUnmountWedged (exit 1).
func runMountShutdown(cmd *cobra.Command, socket string) error {
	client := ownedClient(socket)
	// Snapshot cc-notes' mounts before teardown so the .notes symlinks presenting
	// them can be removed afterward — Reclaim drops them from the holder's
	// registry. Owner-scoped, so a foreign tenant's mount is never listed or
	// touched. Best-effort: a List failure just leaves the symlinks for the next
	// mount.
	mounts, _ := client.List()
	failed, err := client.Reclaim()
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
	// Every mount came down (failed is empty above), so remove each presenting
	// .notes symlink that still points at its now-unmounted dir.
	for _, m := range mounts {
		_ = unlinkNotes(m.Base, m.Dir)
	}
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "cc-notes: reclaimed cc-notes mounts"); err != nil {
		return err
	}
	return nil
}

// runMountStop unmounts the mount at dir via the holder. Nothing mounted at dir
// is an immediate no-op (no holder contact); a live mount or a carcass routes
// through the holder's bounded teardown — unless the holder registers dir to
// another owner, which is refused (the shared holder hosts other tenants).
func runMountStop(cmd *cobra.Command, socket string, caskless bool, dir string) error {
	mp, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("mountpoint: %w", err)
	}
	// --stop may name the in-repo .notes symlink rather than the managed
	// mountpoint the holder registered; resolve it so teardown matches, and
	// remember the symlink to remove once the mount is down. A --stop on the
	// managed path directly stays a pure teardown — its .notes symlink is cleaned
	// by `--stop .notes` or `--shutdown` — so nothing-mounted keeps contacting no
	// holder (Teardown short-circuits locally).
	namedLink := ""
	if target, rerr := os.Readlink(mp); rerr == nil {
		namedLink = mp
		mp = absSymlinkTarget(mp, target)
	}
	// The holder's unmount is path-keyed, so on the shared holder a --stop
	// aimed at another tenant's registered mount would tear it down. Refuse a
	// dir the holder registers to a foreign owner. An unreachable holder has no
	// registry — anything still mounted then is a carcass teardown may clear —
	// and an unmounted dir stays a local no-op that contacts no holder.
	if fusekit.Mounted(mp) {
		mounts, err := (&mountd.Client{Socket: socket}).List()
		if err != nil && !errors.Is(err, mountd.ErrHolderUnavailable) {
			return fmt.Errorf("unmount %s: %w", mp, err)
		}
		if err := refuseForeignStop(mounts, mp); err != nil {
			return err
		}
	}
	// Teardown needs a base only for its base != dir refusal: a registered mount
	// is torn down by the base the holder recorded, and an unregistered carcass
	// ignores base entirely (a forced kernel unmount). The mountpoint's parent
	// is a stable non-dir value that satisfies the refusal.
	if err := newRemoteHost(socket, caskless).Teardown(filepath.Dir(mp), mp); err != nil {
		return err
	}
	if namedLink != "" {
		if err := os.Remove(namedLink); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove %s: %w", namedLink, err)
		}
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: unmounted %s\n", mp); err != nil {
		return err
	}
	return nil
}

// refuseForeignStop returns the refusal for a --stop aimed at a mount the
// holder registers to another owner. mounts is the holder's unscoped listing;
// an mp it does not register — a carcass, or a dir the holder never served —
// is not refused, since the bounded teardown owns those.
func refuseForeignStop(mounts []mountd.MountInfo, mp string) error {
	for _, m := range mounts {
		if m.Dir == mp && m.Owner != holderOwner {
			return fmt.Errorf("refusing to unmount %s: the holder registers it to owner %q, not %q", mp, m.Owner, holderOwner)
		}
	}
	return nil
}

// holderOwner tags cc-notes' mounts on the shared cask holder. It scopes List
// and Reclaim to cc-notes' own mounts so a per-owner teardown never disturbs
// another tenant, and it is the identity a cross-owner Shutdown is refused by.
const holderOwner = "cc-notes"

// ownedClient is a bare holder client scoped to cc-notes' owner — for the
// no-spawn RPCs (List, Reclaim) that drive an already-running holder.
func ownedClient(socket string) *mountd.Client {
	return &mountd.Client{Socket: socket, Owner: holderOwner}
}

// newRemoteHost builds the holder-backed mount driver for socket, tagged with
// cc-notes' owner. SpawnTimeout is left zero (mountd.DefaultSpawnTimeout).
//
// The default (caskless=false) drives the shared fusekit-holder cask: ExecPath
// is the installed cask binary, which spawn launches via `open -g`; it is
// already stable-path, so no StableExecDir is needed and no Version is set
// (the holder owns its own upgrade lifecycle — cc-notes never converge-replaces
// it). Caskless mode drives cc-notes' own self-exec holder (the mount-holder
// subcommand): the holder is this binary respawned from a stable copy so the
// macOS TCC grant survives upgrades.
func newRemoteHost(socket string, caskless bool) *mountd.RemoteHost {
	h := &mountd.RemoteHost{
		Socket:         socket,
		LogPath:        mountHolderLogPath(),
		CannotHostHint: cannotHostHint,
		Owner:          holderOwner,
	}
	if caskless {
		h.Args = []string{"mount-holder", "--socket", socket}
		h.StableExecDir = stableExecDir()
	} else {
		h.ExecPath = mountd.HolderExe
	}
	return h
}

// resolveRepoAndMountpoint resolves the repository root the mount renders over
// and the mountpoint to serve it at, creating the mountpoint when missing. base
// is always the worktree root (the holder keys its registry on the mountpoint
// and records the root as base), so an explicit MOUNTPOINT and the managed
// default both resolve against it. An explicit MOUNTPOINT is made absolute and
// created — a missing directory is the common first-run snag, not an error to
// refuse; with no argument it defaults to ~/.cc-notes/mnt/<base>-<hash>.
// usedDefault is true only for that no-argument managed default — the case the
// caller presents in the repo as a .notes symlink.
func resolveRepoAndMountpoint(ctx context.Context, cwd string, args []string) (repoRoot, mountpoint string, usedDefault bool, err error) {
	s, err := store.OpenContext(ctx, cwd)
	if err != nil {
		return "", "", false, err
	}
	repoRoot, err = s.Git.Root(ctx)
	if err != nil {
		return "", "", false, err
	}
	if len(args) == 1 {
		mp, err := filepath.Abs(args[0])
		if err != nil {
			return "", "", false, fmt.Errorf("mountpoint: %w", err)
		}
		if err := os.MkdirAll(mp, 0o700); err != nil {
			return "", "", false, fmt.Errorf("create mountpoint %s: %w", mp, err)
		}
		return repoRoot, mp, false, nil
	}
	mp, err := defaultMountpoint(repoRoot)
	if err != nil {
		return "", "", false, err
	}
	return repoRoot, mp, true, nil
}

// defaultMountpoint is the managed per-repo mountpoint for repoRoot
// (~/.cc-notes/mnt/<base>-<hash>), created if missing. The hash keys the holder
// registry per repo; the basename keeps the path legible. Shared by the
// no-argument `mount` default and `init`'s auto-mount.
func defaultMountpoint(repoRoot string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	sum := sha256.Sum256([]byte(repoRoot))
	mp := filepath.Join(home, ".cc-notes", "mnt", filepath.Base(repoRoot)+"-"+hex.EncodeToString(sum[:])[:8])
	if err := os.MkdirAll(mp, 0o700); err != nil {
		return "", fmt.Errorf("create mountpoint %s: %w", mp, err)
	}
	return mp, nil
}
