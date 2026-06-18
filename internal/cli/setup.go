package cli

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/version"
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
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Register the cc-notes plugin in .claude/settings.json",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := repoRoot(cmd)
			if err != nil {
				return err
			}
			if err := registerPlugin(root); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "registered: cc-notes plugin in .claude/settings.json")
			return err
		},
	}
	return cmd
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
		Short: "Enable the cc-notes capt-hook pack via `capt-hook pack add`",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := repoRoot(cmd)
			if err != nil {
				return err
			}
			return runCaptHookPackAdd(cmd, root, version.Version)
		},
	}
	return cmd
}

// packAddArgs builds the argv after `uvx` that enables the cc-notes pack,
// pinned to the running binary's release tag. A dev build has no matching
// tag, so it tracks the default branch.
func packAddArgs(ver string) []string {
	source := "github:yasyf/cc-notes"
	if ver != "" && ver != "dev" {
		source += "@" + ver
	}
	return []string{"capt-hook", "pack", "add", source}
}

// runCaptHookPackAdd shells out to `uvx capt-hook pack add` from the repo root,
// streaming the subcommand's stdio so its progress reaches the operator.
func runCaptHookPackAdd(cmd *cobra.Command, root, ver string) error {
	args := append([]string{"uvx"}, packAddArgs(ver)...)
	c := exec.CommandContext(cmd.Context(), args[0], args[1:]...)
	c.Dir = root
	c.Stdout = cmd.OutOrStdout()
	c.Stderr = cmd.ErrOrStderr()
	if err := c.Run(); err != nil {
		return fmt.Errorf("uvx capt-hook pack add: %w", err)
	}
	return nil
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
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", dst)
	return err
}
