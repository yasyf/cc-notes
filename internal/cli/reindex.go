package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newReindexCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "reindex",
		Short: "Import Git entity refs into the managed authority",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStore(cmd)
			if err != nil {
				return err
			}
			if err := s.Reindex(cmd.Context()); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "reindexed: managed entity authority")
			return err
		},
	}
}
