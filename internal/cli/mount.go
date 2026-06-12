package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/fusefs"
)

func newMountCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mount MOUNTPOINT",
		Short: "Mount notes and tasks as a filesystem (foreground; Ctrl-C unmounts)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("working directory: %w", err)
			}
			return fusefs.Mount(cmd.Context(), dir, args[0])
		},
	}
}
