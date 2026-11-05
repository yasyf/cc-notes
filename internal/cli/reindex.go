package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newReindexCmd() *cobra.Command {
	var ifMissing bool
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "Import Git entity refs into the managed authority",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openRawStore(cmd)
			if err != nil {
				return err
			}
			if ifMissing {
				return s.ReindexIfMissing(cmd.Context())
			}
			if err := s.Reindex(cmd.Context()); err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), "reindexed: managed entity authority")
			return err
		},
	}
	cmd.Flags().BoolVar(&ifMissing, "if-missing", false, "import only when no authority exists, after any import already running finishes")
	return cmd
}
