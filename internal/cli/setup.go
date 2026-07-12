package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/plugin"
)

func newSkillsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skills",
		Short: "Manage the cc-notes Claude Code skill",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(newSkillsInstallCmd())
	return cmd
}

func newSkillsInstallCmd() *cobra.Command {
	var global bool
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Register the cc-notes plugin in .claude/settings.json",
		Long: "Register the cc-notes plugin in the repo's .claude/settings.json, or in\n" +
			"the user-global ~/.claude/settings.json with --global.",
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := pluginSettingsTarget(cmd, global)
			if err != nil {
				return err
			}
			if err := registerPlugin(path); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "registered: cc-notes plugin in %s\n", path)
			return err
		},
	}
	cmd.Flags().BoolVar(&global, "global", false, "enable the plugin in the user-global ~/.claude/settings.json instead of the repo")
	return cmd
}

// pluginSettingsTarget resolves where `skills install` writes the plugin
// enablement: the user-global ~/.claude/settings.json when global is set, else
// the repo's .claude/settings.json. Only the repo target needs the repo root.
func pluginSettingsTarget(cmd *cobra.Command, global bool) (string, error) {
	if global {
		return userSettingsPath(), nil
	}
	root, err := repoRoot(cmd)
	if err != nil {
		return "", err
	}
	return repoSettingsPath(root), nil
}

func newWorkflowsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "workflows",
		Short: "Manage the cc-notes CI workflow",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(newWorkflowsInstallCmd())
	return cmd
}

func newWorkflowsInstallCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the cc-notes CI workflow into the repository",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := repoRoot(cmd)
			if err != nil {
				return err
			}
			return installWorkflows(cmd, filepath.Join(root, dir))
		},
	}
	cmd.Flags().StringVar(&dir, "dir", filepath.Join(".github", "workflows"), "destination directory, relative to the repo root")
	return cmd
}

func newHooksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "hooks",
		Short: "Manage the cc-notes capt-hook enforcement hooks",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(newHooksInstallCmd())
	return cmd
}

func newHooksInstallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Enable the cc-notes capt-hook pack and its dispatcher plugin",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := repoRoot(cmd)
			if err != nil {
				return err
			}
			return enableCaptHook(cmd, root)
		},
	}
	return cmd
}

// packAddArgs builds the argv after `uvx` that enables the cc-notes pack,
// always tracking the latest release rather than pinning to the running binary's
// version. The nudge pack and the binary version independently, so an unpinned
// source lets `uvx capt-hook pack update` carry pack fixes to every install
// without re-running `cc-notes hooks install` against a bumped binary.
func packAddArgs() []string {
	return []string{"capt-hook", "pack", "add", "github:yasyf/cc-notes@latest"}
}

// skillsInstallArgs builds the argv after `uvx` that enables the captain-hook
// plugin (`captain-hook@captain-hook`), the dispatcher that runs every pack's
// hooks. `pack add` and `registerPlugin` never turn it on, so init runs this too.
func skillsInstallArgs() []string {
	return []string{"capt-hook", "skills", "install"}
}

// runCaptHook shells out to `uvx --isolated <captHookArgs...>` from the repo
// root, streaming the subcommand's stdio. Both `pack add` and `skills install`
// route through here. `--isolated` ignores any machine-wide `uv tool install
// capt-hook`, which would otherwise silently short-circuit `uvx` to a stale
// pinned env.
func runCaptHook(cmd *cobra.Command, root string, captHookArgs []string) error {
	args := append([]string{"uvx", "--isolated"}, captHookArgs...)
	//nolint:gosec // G204: args[0] is the literal "uvx"; the rest is this command's own fixed capt-hook invocation, by design.
	c := exec.CommandContext(cmd.Context(), args[0], args[1:]...)
	c.Dir = root
	c.Stdout = cmd.OutOrStdout()
	c.Stderr = cmd.ErrOrStderr()
	if err := c.Run(); err != nil {
		return fmt.Errorf("uvx %s: %w", strings.Join(captHookArgs, " "), err)
	}
	return nil
}

// runCaptHookPackAdd enables the cc-notes capt-hook pack via `uvx capt-hook pack add`.
func runCaptHookPackAdd(cmd *cobra.Command, root string) error {
	return runCaptHook(cmd, root, packAddArgs())
}

// enableCaptHook enables the captain-hook dispatcher plugin (`skills install`) and registers the
// cc-notes pack (`pack add`). The two are independent installs, so each runs regardless of the
// other's outcome — a network blip on one never suppresses the other — and both errors are joined.
func enableCaptHook(cmd *cobra.Command, root string) error {
	return errors.Join(
		runCaptHook(cmd, root, skillsInstallArgs()),
		runCaptHookPackAdd(cmd, root),
	)
}

// repoRoot returns the absolute worktree root of the repository containing the
// working directory.
func repoRoot(cmd *cobra.Command) (string, error) {
	s, err := openStore()
	if err != nil {
		return "", err
	}
	return s.Git.Root(cmd.Context())
}

// installTree copies every file under src in fsys into dst, recreating the
// tree below src. Existing files are overwritten and each write is announced.
func installTree(cmd *cobra.Command, fsys fs.FS, src, dst string) error {
	return fs.WalkDir(fsys, src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel := strings.TrimPrefix(path, src+"/")
		return writeEmbedded(cmd, fsys, path, filepath.Join(dst, rel))
	})
}

// installWorkflows writes the embedded CI workflow template into dst. Both
// `workflows install` and `init --ci` route through here so they install the
// same tree.
func installWorkflows(cmd *cobra.Command, dst string) error {
	return installTree(cmd, plugin.Files, "workflows", dst)
}

// writeEmbedded copies src from fsys to dst, creating parent directories, and
// announces the write on stdout.
func writeEmbedded(cmd *cobra.Command, fsys fs.FS, src, dst string) error {
	data, err := fs.ReadFile(fsys, src)
	if err != nil {
		return fmt.Errorf("read embedded %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
	}
	//nolint:gosec // G306: dst is a workflow/config file installed into the user's repo and meant to be world-readable (0o644).
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", dst)
	return err
}
