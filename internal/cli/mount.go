package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/fusekit"
	"github.com/yasyf/fusekit/mountd"
)

// cannotHostHint is the install guidance appended to mountd.ErrCannotHost when a
// pure (non-fuse) build cannot bring up the shared holder. It is cc-notes' brew
// text.
const cannotHostHint = "install the shared holder with `brew install --cask " + mountd.HolderCask + "`, or rebuild cc-notes with -tags fuse for the foreground mount"

// holderOwner tags cc-notes' mounts on the shared fusekit holder. It scopes List
// and Reclaim to cc-notes' own mounts so a per-owner teardown never disturbs
// another tenant, and it is the identity a cross-owner --stop is refused by. It
// also names cc-notes' on-disk bridge spool dir, so it must stay a safe single
// path segment.
const holderOwner = "cc-notes"

// treeProbePath is the mount-relative path the holder's mount-ready gate lstats
// to confirm the synthetic tree is serving before declaring the mount live.
// "/notes" is a root directory every cc-notes tree serves (even an empty repo),
// so it resolves the moment contentd answers for this domain — the tree needs a
// probe because MountAlive (Base's first entry) is meaningless for a mount with
// no local backing.
const treeProbePath = "/notes"

// requiredHolderFeatures are the shared-holder capabilities cc-notes depends on,
// negotiated via OpHello before the first mount: ContentModeTree serving
// (FeatureTree), the lease-ladder teardown its --shutdown/--stop ride
// (FeatureLeaseGate), the journal persist-warning cc-notes threads out of every
// teardown (FeatureWarning), and — load-bearing for across-reboot survival —
// deferred content-dial replay (FeatureContentDeferred), and the cross-tenant
// read-only mount view (FeatureListAll) --stop's foreign-owner refusal rides. On
// content-deferred: a holder replaying a journaled tree mount whose content
// socket (contentd) is not yet answering DEFERS the row (retry with backoff,
// surfaced as ContentDeferred) instead of striking it. Without it, a login-time
// race where the holder replays before contentd's LaunchAgent binds the socket
// permanently drops cc-notes' mount row. On list-all: --stop of a live mount
// lists cross-tenant (ListAll) so a foreign row at the target dir is visible and
// refuseForeignStop can fire; without it that call errors and no live mount could
// be stopped. A miss — or a proto-1 holder — is a crisp "brew upgrade --cask
// fusekit-holder", never a silent degrade. cc-notes drives NO content bridge (it
// dials the tree mount's ContentSocket directly), so FeatureBridge is
// deliberately absent.
var requiredHolderFeatures = []string{mountd.FeatureTree, mountd.FeatureLeaseGate, mountd.FeatureWarning, mountd.FeatureContentDeferred, mountd.FeatureListAll}

// mountOpts collects the mount command's flags.
type mountOpts struct {
	foreground bool
	list       bool
	shutdown   bool
	stop       string
	socket     string
	auto       bool
}

func newMountCmd() *cobra.Command {
	var opts mountOpts
	cmd := &cobra.Command{
		Use:   "mount [MOUNTPOINT]",
		Short: "Mount notes, docs, and tasks as a filesystem (detaches; the shared fusekit holder serves it)",
		Long: "Mount the repository's notes, docs, and tasks as an editable filesystem.\n\n" +
			"By default `mount` DETACHES: the shared fusekit-holder cask serves the mount in\n" +
			"ContentModeTree over cc-notes' content server (contentd), the command prints the\n" +
			"mountpoint and returns, and the mount persists after the command exits. With no\n" +
			"MOUNTPOINT the mount is served at a managed per-repo default under ~/.cc-notes/mnt\n" +
			"and presented in the repo as a `.notes` symlink into it (kept out of git via\n" +
			".git/info/exclude); `cd .notes` to browse. Pass an explicit MOUNTPOINT to serve\n" +
			"there instead — it is created if missing and no symlink is made. Unmount with\n" +
			"`mount --stop DIR` (DIR may be `.notes`) or plain `umount DIR` (the holder\n" +
			"reconciles either); --stop and --shutdown remove the .notes symlink they created.\n\n" +
			"--foreground keeps the in-process lifecycle: the command blocks serving the mount\n" +
			"and Ctrl-C unmounts it (bypassing the shared holder and contentd). It no longer\n" +
			"force-clears a leftover mount carcass (Holder v2 deleted every consumer force\n" +
			"primitive): a stale carcass at the mountpoint fails the mount loudly — clear it\n" +
			"with `umount` (or let the shared holder's own carcass handling reap it) and retry.",
		Args: maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMount(cmd, args, opts)
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&opts.foreground, "foreground", "f", false, "serve in the foreground and unmount on Ctrl-C (bypasses the shared holder)")
	f.BoolVar(&opts.list, "list", false, "list cc-notes' mounts on the shared holder, then exit")
	f.BoolVar(&opts.shutdown, "shutdown", false, "unmount cc-notes' own mounts (the shared holder keeps running for other tenants), then exit")
	f.StringVar(&opts.stop, "stop", "", "unmount the mount at DIR, then exit")
	f.StringVar(&opts.socket, "socket", "", "mount-holder unix socket path (default: the shared fusekit-holder cask socket)")
	_ = f.MarkHidden("socket")
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

	socket := holderSocket(opts.socket)

	switch {
	case opts.auto:
		return runMountAuto(cmd)
	case opts.list:
		return runMountList(cmd, socket)
	case opts.shutdown:
		return runMountShutdown(cmd, socket)
	case opts.stop != "":
		return runMountStop(cmd, socket, opts.stop)
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
	// the path fails fast instead of after a mount has already come up.
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

	return serveDetached(cmd, socket, repoRoot, mp, usedDefault)
}

// serveDetached hands repoRoot's mount to the shared fusekit holder in
// ContentModeTree and returns — the mount persists. The holder serves every
// entry over the bridge by dialing contentd's content socket; cc-notes supplies
// Base as a nominal identity key (the holder never reads it) and Domain as the
// repo root contentd keys its renderer on. It first refuses a live legacy
// incumbent, ensures contentd is up, negotiates holder capabilities, then adds
// the mount; for the managed default (usedDefault) it presents the mount as a
// .notes symlink and prints that path, else it prints the explicit mountpoint.
//
// Durability lives in cc-notes' git-object store, NOT a write spool: tree mode's
// HandleTree commits on Flush (parse+diff+append), and a path-wise write-through
// spool would commit per-write — semantically wrong for cc-notes and unable to
// carry the parse verdict a save needs. A flush racing a contentd restart fails
// the save loudly (ErrContentUnavailable) and the editor retries; there is no
// silent spool to reconcile. Shared by `mount` and `init`'s auto-mount.
//
// The detached tree mount drops the foreground mount's cosmetic fuse-t options —
// `volname` (the Finder volume label) and `nobrowse` (hidden from Finder
// sidebars): the holder owns its mounts' options and cc-notes does not reassert
// them. Accepted losses; the correctness lever (noattrcache) is carried
// explicitly via MountSpec.AttrCache=false.
func serveDetached(cmd *cobra.Command, socket, repoRoot, mp string, usedDefault bool) error {
	if hostGOOS != "darwin" {
		return fmt.Errorf("detached mounts need the macOS fusekit-holder cask (contentd LaunchAgent, launchctl, `open -g`) and are unsupported on %s; run `cc-notes mount --foreground %s` to serve the mount in-process instead", hostGOOS, mp)
	}
	if err := refuseIncumbentHolder(); err != nil {
		return err
	}
	if err := ensureContentd(cmd); err != nil {
		return err
	}
	if err := requireHolder(socket); err != nil {
		return err
	}
	host := newRemoteHost(socket)
	if err := host.AddMount(fusekit.MountSpec{
		Base:          repoRoot,
		Dir:           mp,
		Owner:         holderOwner,
		ContentMode:   fusekit.ContentModeTree,
		ContentSocket: contentSocketPath(),
		Domain:        repoRoot,
		// ProbePath makes the holder's mount-ready gate exercise the synthetic
		// tree before reporting the mount live, so a mount is never announced up
		// until contentd answers for this domain.
		ProbePath: treeProbePath,
		// AttrCache stays OFF explicitly: cc-notes' store is written by the CLI
		// while editors read through the mount, so the go-nfsv4 attribute cache
		// would serve stale sizes and listings. The tree has no attribute
		// stability guarantee to make caching sound; the holder defaults it off
		// for tree rows, and naming it documents that coherence dependency.
		AttrCache: false,
	}); err != nil {
		return err
	}
	// Present the managed default at .notes only after the mount is live, so a
	// failed AddMount leaves no dangling symlink; print .notes (the path to
	// browse) and note the managed target on stderr. An explicit MOUNTPOINT
	// prints itself.
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

// requireHolder brings the shared holder up (cask bootstrap: `open -g` the
// installed fusekit-holder.app, else a crisp brew-install refusal) and
// negotiates its capabilities. A proto-1 holder or a missing feature is a
// "brew upgrade --cask fusekit-holder", never a silent degrade. It never stops
// or retires the shared holder — the ONE sanctioned stop is cc-notes' own
// owner-scoped, lease-gated --shutdown of its own mounts.
func requireHolder(socket string) error {
	if err := spawnHolder(socket); err != nil {
		return err
	}
	hello, err := ownedClient(socket).Hello()
	if err != nil {
		return err
	}
	return hello.Require(requiredHolderFeatures...)
}

// spawnHolder ensures the shared fusekit-holder cask is serving socket, spawning
// it (`open -g` the installed holder app, else a crisp ErrCannotHost refusal)
// when it is not. It is a package seam so the whole test suite can neutralize the
// real cask bootstrap — no test may run `open -g` (the fork-storm class this repo
// guards against) — while production always drives the real Spawn.
var spawnHolder = func(socket string) error {
	return (mountd.Spawn{
		Socket:         socket,
		ExecPath:       mountd.HolderExe,
		CannotHostHint: cannotHostHint,
	}).EnsureRunning()
}

// refuseIncumbentHolder refuses to mount through the shared holder while a
// pre-cutover cc-notes private mount holder is still serving its own socket —
// the two would fight over the same mountpoints. It never displaces the
// incumbent (that is the retired holder's own graceful teardown); it prints the
// recipe and stops. A silent socket is the common case (a no-op).
func refuseIncumbentHolder() error {
	legacy := legacyPrivateHolderSocket()
	if !mountd.NewClient(legacy).Available() {
		return nil
	}
	return fmt.Errorf("a legacy cc-notes private mount holder is still serving %s; retire it first with the pre-cutover binary's `cc-notes mount --shutdown`, then retry (the shared holder will not stack on its mounts)", legacy)
}

// runMountList prints cc-notes' mounts on the shared holder, one tab-separated
// dir/base/liveness line each. The listing is owner-scoped: it shows only
// cc-notes' mounts, never another tenant's. A holder that is not running
// surfaces mountd.ErrHolderUnavailable (exit 1).
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

// runMountShutdown reclaims cc-notes' own mounts on the shared holder — a
// per-owner unmount of every mount tagged Owner "cc-notes" — and removes the
// .notes symlinks presenting them. It does NOT stop the holder: the shared cask
// holder hosts other tenants, so cc-notes only ever tears down its own mounts
// and leaves the holder running. Dirs that fail to come down surface
// ErrUnmountWedged (exit 1); a journal persist-warning is surfaced but not
// fatal.
func runMountShutdown(cmd *cobra.Command, socket string) error {
	client := ownedClient(socket)
	// Snapshot cc-notes' mounts before teardown so the .notes symlinks presenting
	// them can be removed afterward — Reclaim drops them from the registry.
	// Best-effort: a List failure just leaves the symlinks for the next mount.
	mounts, _ := client.List()
	failed, warning, err := client.Reclaim()
	if err != nil {
		return err
	}
	if warning != "" {
		if _, werr := fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: holder warning: %s\n", warning); werr != nil {
			return werr
		}
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
	if _, err := fmt.Fprintln(cmd.ErrOrStderr(), "cc-notes: mounts reclaimed"); err != nil {
		return err
	}
	return nil
}

// mountpointLive reports whether mp is a live kernel mountpoint. It is a package
// seam so --stop tests can drive the holder-contacting teardown path without a
// real kernel mount (forbidden in tests per the fork-storm rules).
var mountpointLive = fusekit.Mounted

// runMountStop unmounts the mount at dir via the shared holder. Nothing mounted
// at dir is an immediate no-op (no holder contact); a live mount or a carcass
// routes through the holder's lease-gated teardown — unless the holder registers
// dir to another owner, which is refused (the shared holder hosts other
// tenants). A journal persist-warning on a successful teardown is surfaced.
func runMountStop(cmd *cobra.Command, socket, dir string) error {
	mp, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("mountpoint: %w", err)
	}
	// --stop may name the in-repo .notes symlink rather than the managed
	// mountpoint the holder registered; resolve it so teardown matches, and
	// remember the symlink to remove once the mount is down.
	namedLink := ""
	if target, rerr := os.Readlink(mp); rerr == nil {
		namedLink = mp
		mp = absSymlinkTarget(mp, target)
	}
	// Refuse a teardown through the shared holder while a pre-cutover cc-notes
	// private holder still serves its own socket — it, not the shared holder,
	// owns the target, so a shared-holder Teardown would unmount it beneath the
	// incumbent. Same refusal (and recipe) serveDetached runs before mounting.
	if err := refuseIncumbentHolder(); err != nil {
		return err
	}
	// The holder's unmount is path-keyed, so on the shared holder a --stop aimed
	// at another tenant's registered mount would tear it down. Refuse a dir the
	// holder registers to a foreign owner. The listing MUST be owner-qualified
	// (proto-2 refuses an empty owner) AND cross-tenant (ListAll) so a foreign
	// row at mp is visible — an owner-scoped List would hide it, and the refusal
	// could never fire. An unreachable holder has no registry — anything still
	// mounted then is a carcass teardown may clear — and an unmounted dir stays a
	// local no-op that contacts no holder.
	if mountpointLive(mp) {
		mounts, err := ownedClient(socket).ListAll()
		if err != nil && !errors.Is(err, mountd.ErrHolderUnavailable) {
			return err
		}
		if err := refuseForeignStop(mounts, mp); err != nil {
			return err
		}
	}
	// Teardown needs a base only for its base != dir refusal: a registered mount
	// is torn down by the base the holder recorded, and an unregistered carcass
	// ignores base entirely. The mountpoint's parent is a stable non-dir value
	// that satisfies the refusal.
	warning, err := newRemoteHost(socket).Teardown(filepath.Dir(mp), mp)
	if err != nil {
		return err
	}
	if warning != "" {
		if _, werr := fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: holder warning: %s\n", warning); werr != nil {
			return werr
		}
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

// refuseForeignStop returns the refusal for a --stop aimed at a mount the holder
// registers to another owner. mounts is the holder's unscoped listing; an mp it
// does not register — a carcass, or a dir the holder never served — is not
// refused, since the bounded teardown owns those.
func refuseForeignStop(mounts []mountd.MountInfo, mp string) error {
	for _, m := range mounts {
		if m.Dir == mp && m.Owner != holderOwner {
			return fmt.Errorf("%s is registered to another holder tenant (%q); refusing to unmount it", mp, m.Owner)
		}
	}
	return nil
}

// ownedClient is a bare holder client scoped to cc-notes' owner — for the
// non-spawning ops (List, Reclaim, Hello). The shared holder scopes List and
// Reclaim to this owner so cc-notes never sees or tears down another tenant's
// mounts.
func ownedClient(socket string) *mountd.Client {
	return &mountd.Client{Socket: socket, Owner: holderOwner}
}

// hostGOOS is the platform the detached-mount and contentd-bootstrap paths gate
// on. A package seam so a test can drive the !darwin fail-fast without a cross
// build; production is the compile-time GOOS. The whole shared-holder model
// (cask, launchctl, `open -g`) is macOS-only, so off darwin those paths refuse
// with a crisp `--foreground` pointer rather than half-running.
var hostGOOS = runtime.GOOS

// remoteHostExecPath is the cask-holder binary newRemoteHost hands its RemoteHost
// as the spawn target. It is a package seam: the cli test binary blanks it (see
// mount_seams_test.go), and with no ExecPath in the pure test build canHost
// refuses the spawn (ErrCannotHost) instead of `open -g`-ing the real cask. This
// closes the path spawnHolder does not cover — RemoteHost.AddMount and Teardown
// drive fusekit's OWN EnsureRunning, not spawnHolder — so no test can bootstrap
// the real holder even after a post-Hello holder death makes the socket
// unavailable mid-mount.
var remoteHostExecPath = func() string { return mountd.HolderExe }

// newRemoteHost builds the mount driver for the shared fusekit-holder cask,
// tagged with cc-notes' owner. ExecPath is the installed cask binary, which
// spawn launches via `open -g`; it is already stable-path, so no StableExecDir
// and no Version — the holder owns its own upgrade lifecycle and cc-notes never
// converge-replaces or retires it. SpawnTimeout is left zero
// (mountd.DefaultSpawnTimeout).
func newRemoteHost(socket string) *mountd.RemoteHost {
	return &mountd.RemoteHost{
		Socket:         socket,
		CannotHostHint: cannotHostHint,
		Owner:          holderOwner,
		ExecPath:       remoteHostExecPath(),
	}
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
	sum := sha256.Sum256([]byte(repoRoot))
	mp := filepath.Join(stateDir(), "mnt", filepath.Base(repoRoot)+"-"+hex.EncodeToString(sum[:])[:8])
	if err := os.MkdirAll(mp, 0o700); err != nil {
		return "", fmt.Errorf("create mountpoint %s: %w", mp, err)
	}
	return mp, nil
}
