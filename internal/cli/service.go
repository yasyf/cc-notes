package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

func newServiceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Manage the signed cc-notes service",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Install or reconcile the signed cc-notes service",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := rejectServiceRepository(cmd); err != nil {
				return err
			}
			if err := installService(cmd.Context()); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "installed: CCNotesHelper service")
			return err
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Deactivate the signed cc-notes service",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := rejectServiceRepository(cmd); err != nil {
				return err
			}
			if err := uninstallService(cmd.Context()); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "uninstalled: CCNotesHelper service")
			return err
		},
	})
	return cmd
}

func rejectServiceRepository(cmd *cobra.Command) error {
	if cmd.Flags().Changed("repo") {
		return &UsageError{Err: errors.New("cc-notes service commands do not accept --repo")}
	}
	return nil
}
