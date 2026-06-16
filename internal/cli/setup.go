package cli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/plugin"
)

// captHookEvents are the Claude Code events wired to the capt-hook dispatcher.
// PostToolUse carries the cc-notes nudges; the rest keep sibling hook modules
// in the same directory live.
var captHookEvents = []string{
	"UserPromptSubmit",
	"PreToolUse",
	"PostToolUse",
	"PostToolUseFailure",
	"Stop",
}

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
	var dir string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the using-cc-notes skill into the repository",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := repoRoot(cmd)
			if err != nil {
				return err
			}
			return installTree(cmd, plugin.Files, "skills", filepath.Join(root, dir))
		},
	}
	cmd.Flags().StringVar(&dir, "dir", filepath.Join(".claude", "skills"), "destination directory, relative to the repo root")
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
	var dir string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install the capt-hook hook modules and wire .claude/settings.json",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := repoRoot(cmd)
			if err != nil {
				return err
			}
			if err := installHookModules(cmd, filepath.Join(root, dir)); err != nil {
				return err
			}
			settings := filepath.Join(root, ".claude", "settings.json")
			if err := wireCaptHook(cmd, settings); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "capt-hook runs via uvx; see plugin/hooks/README.md for what each nudge does")
			return err
		},
	}
	cmd.Flags().StringVar(&dir, "dir", filepath.Join(".claude", "hooks"), "destination directory, relative to the repo root")
	return cmd
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

// installHookModules copies the embedded *.py hook modules into dst.
func installHookModules(cmd *cobra.Command, dst string) error {
	entries, err := fs.ReadDir(plugin.Files, "hooks")
	if err != nil {
		return fmt.Errorf("read embedded hooks: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".py" {
			continue
		}
		if err := writeEmbedded(cmd, plugin.Files, "hooks/"+e.Name(), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
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

// wireCaptHook merges the capt-hook event dispatchers into the settings file
// at path, creating it when absent. Events already routed to capt-hook are
// left untouched; only missing ones are appended, so existing hooks survive.
func wireCaptHook(cmd *cobra.Command, path string) error {
	settings := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := json.Unmarshal(data, &settings); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("read %s: %w", path, err)
	}

	hooks, _ := settings["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
		settings["hooks"] = hooks
	}
	added := 0
	for _, event := range captHookEvents {
		command := "uvx capt-hook run " + event
		groups, _ := hooks[event].([]any)
		if hasHookCommand(groups, command) {
			continue
		}
		hooks[event] = append(groups, map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": command},
			},
		})
		added++
	}

	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "wired %d capt-hook event(s) into %s\n", added, path)
	return err
}

// hasHookCommand reports whether any hook in any matcher group already runs
// command.
func hasHookCommand(groups []any, command string) bool {
	for _, group := range groups {
		g, ok := group.(map[string]any)
		if !ok {
			continue
		}
		entries, _ := g["hooks"].([]any)
		for _, entry := range entries {
			h, ok := entry.(map[string]any)
			if !ok {
				continue
			}
			if cmdStr, _ := h["command"].(string); cmdStr == command {
				return true
			}
		}
	}
	return false
}
