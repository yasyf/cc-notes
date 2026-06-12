// Package cli wires the cobra command tree for cc-notes: the note and task
// noun groups plus init, sync, and mount.
package cli

import (
	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/version"
)

// NewRootCmd builds the root command with every subcommand attached.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "cc-notes",
		Short:         "Git-native notes and tasks for agents",
		Version:       version.String(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	return root
}
