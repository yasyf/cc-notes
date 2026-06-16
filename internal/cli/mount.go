package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/store"
)

func newMountCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mount [MOUNTPOINT]",
		Short: "Mount notes and tasks as a filesystem (foreground; Ctrl-C unmounts)",
		Long: "Mount the repository's notes and tasks as an editable filesystem and serve it in\n" +
			"the foreground until Ctrl-C. MOUNTPOINT is created if it does not exist; omit it\n" +
			"to use a managed per-repo default under ~/.cc-notes/mnt.",
		Args: maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("working directory: %w", err)
			}
			mp, err := resolveMountpoint(cmd.Context(), dir, args)
			if err != nil {
				return err
			}
			if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: mounting at %s (Ctrl-C to unmount)\n", mp); err != nil {
				return err
			}
			return fusefs.Mount(cmd.Context(), dir, mp)
		},
	}
}

// resolveMountpoint picks the mount directory and ensures it exists. An
// explicit MOUNTPOINT is used verbatim and created when missing — a missing
// directory is the common first-run snag, not an error to refuse. With no
// argument it defaults to a managed per-repo path under ~/.cc-notes/mnt keyed
// by the worktree root. fusefs.Mount never creates the mountpoint itself
// (see internal/fusefs/mount.go), so the directory is materialized here.
func resolveMountpoint(ctx context.Context, repoDir string, args []string) (string, error) {
	if len(args) == 1 {
		mp := args[0]
		if err := os.MkdirAll(mp, 0o700); err != nil {
			return "", fmt.Errorf("create mountpoint %s: %w", mp, err)
		}
		return mp, nil
	}
	s, err := store.Open(repoDir)
	if err != nil {
		return "", err
	}
	root, err := s.Git.Root(ctx)
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	sum := sha256.Sum256([]byte(root))
	mp := filepath.Join(home, ".cc-notes", "mnt", filepath.Base(root)+"-"+hex.EncodeToString(sum[:])[:8])
	if err := os.MkdirAll(mp, 0o700); err != nil {
		return "", fmt.Errorf("create mountpoint %s: %w", mp, err)
	}
	return mp, nil
}
