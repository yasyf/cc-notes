package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"
)

func newPackageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "package",
		Short: "Install or remove the delivered signed helper",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Verify, install, and activate the delivered signed helper",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := rejectPackageRepository(cmd); err != nil {
				return err
			}
			if err := installPackage(cmd.Context()); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "installed: CCNotesHelper package")
			return err
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Deactivate and remove the installed signed helper",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := rejectPackageRepository(cmd); err != nil {
				return err
			}
			if err := uninstallPackage(cmd.Context()); err != nil {
				return err
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), "uninstalled: CCNotesHelper package")
			return err
		},
	})
	return cmd
}

func rejectPackageRepository(cmd *cobra.Command) error {
	if cmd.Flags().Changed("repo") {
		return &UsageError{Err: errors.New("cc-notes package commands do not accept --repo")}
	}
	return nil
}
