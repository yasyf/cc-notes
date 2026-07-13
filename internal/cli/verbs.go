package cli

import (
	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
)

// showVerb builds the read-only "show ID" command; short is the kind's own
// help line and show gathers the bespoke read-side data.
func (k kindSpec[T]) showVerb(short string, show func(cmd *cobra.Command, s *store.Store, prefix string, jsonOut bool) error) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: short,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			return show(cmd, s, args[0], jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}
