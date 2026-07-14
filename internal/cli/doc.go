package cli

import (
	"context"
	"slices"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
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
	return docSpec.rmCmd("Tombstone a doc", (*notes.Client).ResolveDoc, (*notes.Client).RemoveDoc)
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
