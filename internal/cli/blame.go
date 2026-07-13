package cli

import (
	"github.com/spf13/cobra"
)

func newBlameCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "blame SHA",
		Short: "List the task(s) a commit implemented",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := openClient()
			if err != nil {
				return err
			}
			_, tasks, err := c.Blame(ctx, args[0])
			if err != nil {
				return err
			}
			return printTaskList(cmd, c, tasks, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}
