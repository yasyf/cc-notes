package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
)

// newStorageCmd builds "cc-notes storage": the operator step that points a
// checkout — a thin clone, say — at the cc-notes records of another local
// repository. It never opens a store, installs refspecs, or syncs.
func newStorageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "storage",
		Short: "Bind this checkout to a shared records repository",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(newStorageBindCmd())
	return cmd
}

func newStorageBindCmd() *cobra.Command {
	var source string
	cmd := &cobra.Command{
		Use:   "bind",
		Short: "Use the cc-notes records of the repository at --source",
		Long: "Publish one cc-notes.storage value in this checkout's git config naming the\n" +
			"repository at --source as its records backend. Every note, task, and other\n" +
			"record then reads from and writes to that repository, while HEAD, branches,\n" +
			"files, and author identity stay this checkout's own. Binding is idempotent,\n" +
			"refuses a checkout that already holds records or a different binding, and\n" +
			"writes nothing into the source.",
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !dirExists(source) {
				return &UsageError{Err: fmt.Errorf("--source %s: not a directory", source)}
			}
			dir, err := repoDir(cmd)
			if err != nil {
				return err
			}
			result, err := store.Bind(cmd.Context(), dir, source)
			if err != nil {
				return err
			}
			verb := "already bound"
			if result.Changed {
				verb = "bound"
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s to records at %s\n", verb, result.Context, result.Binding.CommonDir)
			return err
		},
	}
	cmd.Flags().StringVar(&source, "source", "", "any path inside the repository whose cc-notes records this checkout shares")
	cmd.MarkFlagsOneRequired("source")
	return cmd
}
