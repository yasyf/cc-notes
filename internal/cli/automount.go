package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/store"
)

// autoMountConfig is the git-config key recording whether this repo's `.notes`
// mount should be brought up automatically — set by `init` (cleared by
// `init --no-mount`) and read by the session-start ensure-mount nudge. Stored in
// .git/config, so it is per-clone and never travels with a push.
const autoMountConfig = "cc-notes.autoMount"

// autoMountEnabled reports whether cc-notes.autoMount is set true for this repo.
// The last configured value wins, matching git's own last-wins semantics; an
// unset key is false (the default — no auto-mount).
func autoMountEnabled(ctx context.Context, g gitcmd.Git) (bool, error) {
	values, err := g.ConfigGetAll(ctx, autoMountConfig)
	if err != nil {
		return false, err
	}
	return len(values) > 0 && values[len(values)-1] == "true", nil
}

// setAutoMount persists the auto-mount preference in git config. `init` sets it
// true (the default) and `init --no-mount` sets it false, so re-running init can
// flip a prior choice either way.
func setAutoMount(ctx context.Context, g gitcmd.Git, on bool) error {
	value := "false"
	if on {
		value = "true"
	}
	return g.ConfigSet(ctx, autoMountConfig, value)
}

// autoMount serves repoRoot's managed .notes mount best-effort. It is purely a
// convenience: a build that cannot host fuse (the pure binary, or a CI runner)
// — or any other mount failure — is surfaced as a warning and swallowed, so it
// never fails the caller. The durable preference (cc-notes.autoMount) is
// persisted separately by the caller, so a later session-start ensure-mount in a
// fuse-capable build still brings the mount up. It reuses serveDetached, so an
// auto-mount is identical to a manual `cc-notes mount` (ensure contentd,
// negotiate the shared holder, add the tree mount, then present .notes).
func autoMount(cmd *cobra.Command, repoRoot string) {
	if !fusefs.Hostable {
		// A build that cannot host fuse (the pure binary, dev, CI) never
		// auto-mounts: it can serve no content, no LaunchAgent is installed, and
		// the shared holder is never contacted. The durable preference is still
		// recorded by the caller for a fuse binary to honor.
		return
	}
	mp, err := defaultMountpoint(repoRoot)
	if err != nil {
		warnAutoMount(cmd, err)
		return
	}
	// A real file or directory already at .notes is the user's; never serve over
	// it. notesLinkBlocked reports that conflict.
	if err := notesLinkBlocked(repoRoot); err != nil {
		warnAutoMount(cmd, err)
		return
	}
	if err := serveDetached(cmd, holderSocket(""), repoRoot, mp, true); err != nil {
		warnAutoMount(cmd, err)
	}
}

// warnAutoMount reports a best-effort auto-mount failure to stderr without
// failing init; the write error is itself best-effort.
func warnAutoMount(cmd *cobra.Command, err error) {
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: auto-mount skipped (run `cc-notes mount` when ready): %v\n", err)
}

// runMountAuto is the session-start ensure-mount behind the hidden `mount
// --auto`: a self-gating, best-effort, quiet mount honored only when this binary
// can host fuse and the repo opted in (cc-notes.autoMount=true). Every failure to
// determine state — not inside a repo, an unreadable config — is swallowed to a
// silent no-op, and the mount itself is best-effort (autoMount warns at most), so
// the session-start hook that invokes it can never be made to fail. An
// already-live mount is adopted with zero RPC. The default socket is always used.
func runMountAuto(cmd *cobra.Command) error {
	if !fusefs.Hostable {
		return nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	s, err := store.OpenContext(cmd.Context(), cwd)
	if err != nil {
		return nil
	}
	repoRoot, err := s.Git.Root(cmd.Context())
	if err != nil {
		return nil
	}
	if on, err := autoMountEnabled(cmd.Context(), s.Git); err != nil || !on {
		return nil
	}
	autoMount(cmd, repoRoot)
	return nil
}
