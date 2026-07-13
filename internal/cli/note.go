package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

func newNoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "note",
		Short: "Repo-global notes with optional commit, path, and branch anchors",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newNoteAddCmd(),
		newNoteListCmd(),
		newNoteShowCmd(),
		newNoteEditCmd(),
		newNoteRmCmd(),
		newNoteSearchCmd(),
		newNoteVerifyCmd(),
		newNoteSupersedeCmd(),
		newNoteExpireCmd(),
		newNoteReviewCmd(),
		newNoteHistoryCmd(),
	)
	return cmd
}

func newNoteAddCmd() *cobra.Command { return noteDocument.addVerb() }

func newNoteListCmd() *cobra.Command {
	return noteDocument.listVerb()
}

func newNoteShowCmd() *cobra.Command {
	return noteSpec.showVerb("Show one note", showNote)
}

func newNoteEditCmd() *cobra.Command { return noteDocument.editVerb() }

func newNoteRmCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "rm ID",
		Short: "Tombstone a note",
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
			id, err := c.ResolveNote(ctx, args[0])
			if err != nil {
				return err
			}
			note, err := c.RemoveNote(ctx, id)
			if err != nil {
				return err
			}
			return printNote(cmd, c, note, jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newNoteSearchCmd() *cobra.Command {
	return noteDocument.searchVerb()
}

func newNoteVerifyCmd() *cobra.Command { return noteDocument.verifyVerb() }

func newNoteSupersedeCmd() *cobra.Command { return noteDocument.supersedeVerb() }

func newNoteExpireCmd() *cobra.Command { return noteDocument.expireVerb() }

func newNoteReviewCmd() *cobra.Command { return noteDocument.reviewVerb() }

// resolveCommits expands every user-supplied commit anchor — an abbreviated
// sha or a revision like HEAD — to its full 40-char commit sha, so the value
// stored on the anchor is what every read path (status, show, drift) can
// resolve. An anchor naming no commit, or an ambiguous prefix, is a hard
// error surfaced at add time: nothing is stored on a bad value. The result
// preserves order and is freshly allocated, so the caller's slice is left
// untouched.
func resolveCommits(ctx context.Context, g gitcmd.Git, commits []string) ([]string, error) {
	if len(commits) == 0 {
		return commits, nil
	}
	full := make([]string, len(commits))
	for i, c := range commits {
		sha, err := g.CommitSHA(ctx, c)
		if errors.Is(err, gitcmd.ErrRevNotFound) {
			return nil, fmt.Errorf("%w: no commit %s", store.ErrNotFound, c)
		}
		if err != nil {
			return nil, err
		}
		full[i] = string(sha)
	}
	return full, nil
}

func buildAnchors(commits, paths, dirs, branches []string) []model.Anchor {
	anchors := make([]model.Anchor, 0, len(commits)+len(paths)+len(dirs)+len(branches))
	for _, v := range commits {
		anchors = append(anchors, model.Anchor{Kind: model.AnchorCommit, Value: v})
	}
	for _, v := range paths {
		anchors = append(anchors, model.Anchor{Kind: model.AnchorPath, Value: v})
	}
	for _, v := range dirs {
		anchors = append(anchors, model.Anchor{Kind: model.AnchorDir, Value: v})
	}
	for _, v := range branches {
		anchors = append(anchors, model.Anchor{Kind: model.AnchorBranch, Value: v})
	}
	return anchors
}
