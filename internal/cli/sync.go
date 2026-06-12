package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/internal/version"
)

func newInitCmd() *cobra.Command {
	var remote string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Install refs/cc-notes/* refspecs on a remote",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := ccsync.Install(cmd.Context(), s.Git, remote); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "initialized: refs/cc-notes/* refspecs installed for %s\n", remote)
			return err
		},
	}
	cmd.Flags().StringVar(&remote, "remote", defaultRemote, "remote to wire")
	return cmd
}

func newSyncCmd() *cobra.Command {
	var remote string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Converge refs/cc-notes/* with a remote and push",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("working directory: %w", err)
			}
			report, err := ccsync.Sync(cmd.Context(), dir, remote)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(out, syncDTO{
					Created:       report.Created,
					FastForwarded: report.FastForwarded,
					Merged:        report.Merged,
					Pushed:        report.Pushed,
					Rounds:        report.Rounds,
				})
			}
			for _, line := range []struct {
				verb  string
				count int
			}{
				{"created", report.Created},
				{"fast-forwarded", report.FastForwarded},
				{"merged", report.Merged},
				{"pushed", report.Pushed},
			} {
				if line.count == 0 {
					continue
				}
				if _, err := fmt.Fprintf(out, "%s: %d\n", line.verb, line.count); err != nil {
					return err
				}
			}
			_, err = fmt.Fprintf(out, "rounds: %d\n", report.Rounds)
			return err
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&remote, "remote", defaultRemote, "remote to sync with")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the cc-notes version",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			_, err := fmt.Fprintln(cmd.OutOrStdout(), version.String())
			return err
		},
	}
}
