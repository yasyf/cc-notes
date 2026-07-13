package cli

import (
	"context"
	"slices"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

func newDocCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doc",
		Short: "Long-form agent docs with a free-text when trigger and the full note freshness lifecycle",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newDocAddCmd(),
		newDocListCmd(),
		newDocShowCmd(),
		newDocEditCmd(),
		newDocRmCmd(),
		newDocSearchCmd(),
		newDocVerifyCmd(),
		newDocSupersedeCmd(),
		newDocExpireCmd(),
		newDocReviewCmd(),
		newDocHistoryCmd(),
	)
	return cmd
}

func newDocAddCmd() *cobra.Command { return docDocument.addVerb() }

func newDocListCmd() *cobra.Command {
	return docDocument.listVerb()
}

func newDocShowCmd() *cobra.Command {
	return docSpec.showVerb("Show one doc", showDoc)
}

func newDocEditCmd() *cobra.Command { return docDocument.editVerb() }

func newDocRmCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "rm ID",
		Short: "Tombstone a doc",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveDoc(ctx, args[0])
			if err != nil {
				return err
			}
			doc, err := c.RemoveDoc(ctx, id)
			if err != nil {
				return err
			}
			return printDoc(cmd, c, doc, "", jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newDocSearchCmd() *cobra.Command {
	return docDocument.searchVerb()
}

func newDocVerifyCmd() *cobra.Command { return docDocument.verifyVerb() }

func newDocSupersedeCmd() *cobra.Command { return docDocument.supersedeVerb() }

func newDocExpireCmd() *cobra.Command { return docDocument.expireVerb() }

func newDocReviewCmd() *cobra.Command { return docDocument.reviewVerb() }

// reverseSupersedesDocs returns the ids of docs that supersede id, sorted: the
// reverse of the supersede edge, computed at read.
func reverseSupersedesDocs(ctx context.Context, s *store.Store, id model.EntityID) ([]model.EntityID, error) {
	all, err := s.ListDocs(ctx, false, true)
	if err != nil {
		return nil, err
	}
	var out []model.EntityID
	for _, d := range all {
		if slices.Contains(d.SupersededBy, id) {
			out = append(out, d.ID)
		}
	}
	slices.Sort(out)
	return out, nil
}
