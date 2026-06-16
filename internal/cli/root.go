// Package cli wires the cobra command tree for cc-notes: the note, task,
// skills, and hooks noun groups plus init, sync, and version. Output is
// agents-first — lean
// deterministic lines or compact JSON on stdout, one labeled error line on
// stderr, exit codes mapped from typed errors via ExitCode.
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
		Args:          noUnknownSubcommand,
		RunE:          runHelp,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.CompletionOptions.DisableDefaultCmd = true
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return &UsageError{Err: err}
	})
	root.AddCommand(newInitCmd(), newSyncCmd(), newReconcileCmd(), newMountCmd(), newVersionCmd(), newNoteCmd(), newTaskCmd(), newSkillsCmd(), newHooksCmd())
	return root
}
