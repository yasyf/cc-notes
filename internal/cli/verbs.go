package cli

import (
	"context"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
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
			s, err := openStore(cmd)
			if err != nil {
				return err
			}
			return show(cmd, s, args[0], jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

// rmCmd builds the "rm ID" tombstone command: resolve the id prefix, remove the
// entity, then echo the tombstoned snapshot via the kind's own printer. short is
// the kind's help line; resolve and remove are the notes-client methods (passed
// as method expressions) that the id flows through.
func (k kindSpec[T]) rmCmd(
	short string,
	resolve func(*notes.Client, context.Context, string) (model.EntityID, error),
	remove func(*notes.Client, context.Context, model.EntityID) (T, error),
) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "rm ID",
		Short: short,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, c, err := openStoreClient(cmd)
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := resolve(c, ctx, args[0])
			if err != nil {
				return err
			}
			v, err := remove(c, ctx, id)
			if err != nil {
				return err
			}
			return k.print(cmd, c, v, jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}
