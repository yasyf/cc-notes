package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/cc-notes/notes"
)

// postMergeHook is the body cc-notes init --hook writes to the repository's
// post-merge hook: reconcile the merged branch's open tasks into the current
// branch after every git merge that updates the worktree.
const postMergeHook = "#!/bin/sh\nexec cc-notes reconcile\n"

func newInitCmd() *cobra.Command {
	var remote string
	var hook bool
	var ci bool
	var noCI bool
	var noMount bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up cc-notes in this repository",
		Long: "Set up cc-notes in this repository. Installs the refs/cc-notes/* fetch and\n" +
			"push refspecs, then does everything the repo is ready for:\n\n" +
			"  - When a .claude/ directory exists (the repo uses Claude Code), registers\n" +
			"    the cc-notes plugin in .claude/settings.json and enables the cc-notes\n" +
			"    capt-hook pack via `capt-hook pack add`.\n" +
			"  - When a .github/ directory exists, installs a GitHub Actions workflow that\n" +
			"    reconciles merged tasks onto the default branch on every push. Pass\n" +
			"    --no-ci to skip it, or --ci to force it without a .github/ directory.\n" +
			"  - Mounts the repository's notes, docs, and tasks as a `.notes` filesystem\n" +
			"    (a background holder serves it) and records the preference so new\n" +
			"    sessions re-mount it. Pass --no-mount to skip and disable that.\n\n" +
			"--hook also installs a git post-merge hook that runs `cc-notes reconcile`\n" +
			"after every merge. The hook is git-only: it does NOT fire under jj, a\n" +
			"rebase, or a server-side squash merge. Treat it as a git-only convenience —\n" +
			"prefer the CI workflow.",
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if ci && noCI {
				return &UsageError{Err: errors.New("--ci and --no-ci are mutually exclusive")}
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if _, err := ccsync.Install(ctx, s.Git, remote); err != nil {
				return err
			}
			root, err := s.Git.Root(ctx)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if _, err := fmt.Fprintf(out, "initialized: refs/cc-notes/* refspecs installed for %s\n", remote); err != nil {
				return err
			}
			claudeExists := dirExists(filepath.Join(root, ".claude"))
			if claudeExists {
				if err := registerPlugin(repoSettingsPath(root)); err != nil {
					return err
				}
				if _, err := fmt.Fprintln(out, "registered: cc-notes plugin in .claude/settings.json"); err != nil {
					return err
				}
			}
			if ci || (!noCI && dirExists(filepath.Join(root, ".github"))) {
				if err := installWorkflows(cmd, filepath.Join(root, ".github", "workflows")); err != nil {
					return err
				}
			}
			if hook {
				path, err := installPostMergeHook(cmd, s.Git)
				if err != nil {
					return err
				}
				if _, err := fmt.Fprintf(out, "installed: post-merge hook at %s\n", path); err != nil {
					return err
				}
			}
			// Auto-mount the repo's .notes by default so the notes/tasks tree is
			// there without a manual `cc-notes mount`; --no-mount opts out. The
			// preference is persisted in git config (read by the session-start
			// ensure-mount nudge) regardless, and the mount itself is best-effort —
			// a build that cannot host fuse just warns. Recorded before the network
			// capt-hook call below so a uvx blip never loses the preference.
			if err := setAutoMount(ctx, s.Git, !noMount); err != nil {
				return fmt.Errorf("record auto-mount preference: %w", err)
			}
			if !noMount {
				autoMount(cmd, root)
			}
			// The capt-hook calls shell out to uvx over the network, so they run
			// last: a failure here never blocks the local-only refspecs, plugin
			// registration, CI workflow, post-merge hook, or auto-mount above.
			// `skills install` enables captain-hook@captain-hook (the dispatcher);
			// `pack add` registers the cc-notes pack it dispatches — without the
			// former the pack is installed but dormant. Each step is independently
			// best-effort, so one uvx failure never suppresses the other.
			if claudeExists {
				if err := enableCaptHook(cmd, root); err != nil {
					return err
				}
			}
			return nil
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&remote, "remote", defaultRemote, "remote to wire")
	flags.BoolVar(&ci, "ci", false, "force-install the reconcile GitHub Actions workflow even without a .github/ directory")
	flags.BoolVar(&noCI, "no-ci", false, "skip the reconcile GitHub Actions workflow even when a .github/ directory exists")
	flags.BoolVar(&hook, "hook", false, "also install a git post-merge hook running `cc-notes reconcile` (git-only; skipped by jj/rebase/server-side squash)")
	flags.BoolVar(&noMount, "no-mount", false, "skip auto-mounting the `.notes` filesystem and disable the session-start ensure-mount")
	return cmd
}

// installPostMergeHook writes an executable post-merge hook invoking
// cc-notes reconcile and returns its path. An existing post-merge hook is a
// UsageError rather than a clobber.
func installPostMergeHook(cmd *cobra.Command, g gitcmd.Git) (string, error) {
	dir, err := g.HooksDir(cmd.Context())
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "post-merge")
	if _, err := os.Stat(path); err == nil {
		return "", &UsageError{Err: fmt.Errorf("post-merge hook already exists at %s", path)}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat post-merge hook: %w", err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create hooks dir: %w", err)
	}
	//nolint:gosec // G306: a git post-merge hook must be executable (0o755) to run.
	if err := os.WriteFile(path, []byte(postMergeHook), 0o755); err != nil {
		return "", fmt.Errorf("write post-merge hook: %w", err)
	}
	return path, nil
}

func newSyncCmd() *cobra.Command {
	var remote string
	var jsonOut bool
	var full bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Converge refs/cc-notes/* with a remote and push",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := openClient()
			if err != nil {
				return err
			}
			report, syncErr := c.Sync(ctx, notes.SyncOptions{Remote: remote, Full: full})
			// A failed sync can still have done real work — e.g. a download
			// failure after the push loop converged — so the report prints
			// before the error propagates to a non-zero exit. A run that
			// never completed a round has nothing to report.
			if syncErr != nil && report.Rounds == 0 {
				return syncErr
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				if perr := printJSON(out, syncDTO{
					Created:       report.Created,
					FastForwarded: report.FastForwarded,
					Merged:        report.Merged,
					Pushed:        report.Pushed,
					Uploaded:      report.Uploaded,
					Downloaded:    report.Downloaded,
					Rounds:        report.Rounds,
				}); perr != nil {
					return perr
				}
				return syncErr
			}
			for _, line := range []struct {
				verb  string
				count int
			}{
				{"created", report.Created},
				{"fast-forwarded", report.FastForwarded},
				{"merged", report.Merged},
				{"pushed", report.Pushed},
				{"uploaded", report.Uploaded},
				{"downloaded", report.Downloaded},
			} {
				if line.count == 0 {
					continue
				}
				if _, perr := fmt.Fprintf(out, "%s: %d\n", line.verb, line.count); perr != nil {
					return perr
				}
			}
			if _, perr := fmt.Fprintf(out, "rounds: %d\n", report.Rounds); perr != nil {
				return perr
			}
			return syncErr
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&remote, "remote", "", "remote to sync with (default: every cc-notes-wired remote, else origin)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	flags.BoolVar(&full, "full", false, "force a whole-namespace reconcile scan")
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the cc-notes version",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version.String())
			return err
		},
	}
}

// syncDTO fixes the JSON field order for a sync report.
type syncDTO struct {
	Created       int `json:"created"`
	FastForwarded int `json:"fast_forwarded"`
	Merged        int `json:"merged"`
	Pushed        int `json:"pushed"`
	Uploaded      int `json:"uploaded"`
	Downloaded    int `json:"downloaded"`
	Rounds        int `json:"rounds"`
}
