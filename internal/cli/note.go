package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/model"
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
	)
	return cmd
}

func newNoteAddCmd() *cobra.Command {
	var body string
	var tags, commits, paths, branches []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add TITLE",
		Short: "Create a note",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, s.Git); err != nil {
				return err
			}
			text, err := bodyArg(cmd, body)
			if err != nil {
				return err
			}
			create := model.CreateNote{
				Nonce:   model.NewNonce(),
				Title:   args[0],
				Body:    text,
				Tags:    tags,
				Anchors: buildAnchors(commits, paths, branches),
			}
			snapshot, err := s.Create(ctx, []model.Op{create})
			if err != nil {
				return err
			}
			return printNote(cmd, snapshot.(model.Note), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&body, "body", "", "note body; - reads stdin")
	flags.StringArrayVar(&tags, "tag", nil, "tag (repeatable)")
	flags.StringArrayVar(&commits, "commit", nil, "commit anchor (repeatable)")
	flags.StringArrayVar(&paths, "path", nil, "path anchor (repeatable)")
	flags.StringArrayVar(&branches, "branch", nil, "branch anchor (repeatable)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newNoteListCmd() *cobra.Command {
	var tags []string
	var path, commit, branch string
	var all, jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List notes",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			notes, err := s.ListNotes(cmd.Context(), all)
			if err != nil {
				return err
			}
			notes = slices.DeleteFunc(notes, func(n model.Note) bool {
				return !hasAll(n.Tags, tags) ||
					(commit != "" && !hasAnchor(n, model.AnchorCommit, commit)) ||
					(path != "" && !hasAnchor(n, model.AnchorPath, path)) ||
					(branch != "" && !hasAnchor(n, model.AnchorBranch, branch))
			})
			sortNotes(notes)
			return printNoteList(cmd, notes, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringArrayVar(&tags, "tag", nil, "require tag (repeatable, ANDed)")
	flags.StringVar(&path, "path", "", "require path anchor")
	flags.StringVar(&commit, "commit", "", "require commit anchor")
	flags.StringVar(&branch, "branch", "", "require branch anchor")
	flags.BoolVar(&all, "all", false, "include deleted notes")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newNoteShowCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show one note",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			_, note, err := loadNote(cmd.Context(), s, args[0])
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), newNoteDTO(note))
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), renderNoteShow(note))
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newNoteEditCmd() *cobra.Command {
	var title, body string
	var addTags, rmTags, addPaths, rmPaths, addCommits, rmCommits, addBranches, rmBranches []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Edit a note",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			var ops []model.Op
			if cmd.Flags().Changed("title") {
				ops = append(ops, model.SetTitle{Title: title})
			}
			if cmd.Flags().Changed("body") {
				text, err := bodyArg(cmd, body)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetBody{Body: text})
			}
			for _, tag := range addTags {
				ops = append(ops, model.AddTag{Tag: tag})
			}
			for _, tag := range rmTags {
				ops = append(ops, model.RemoveTag{Tag: tag})
			}
			for _, a := range buildAnchors(addCommits, addPaths, addBranches) {
				ops = append(ops, model.AddAnchor{Anchor: a})
			}
			for _, a := range buildAnchors(rmCommits, rmPaths, rmBranches) {
				ops = append(ops, model.RemoveAnchor{Anchor: a})
			}
			if len(ops) == 0 {
				return &UsageError{Err: errors.New("note edit requires at least one flag")}
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, s.Git); err != nil {
				return err
			}
			ref, _, err := loadNote(ctx, s, args[0])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, ops)
			if err != nil {
				return err
			}
			return printNote(cmd, snapshot.(model.Note), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&title, "title", "", "new title")
	flags.StringVar(&body, "body", "", "new body; - reads stdin")
	flags.StringArrayVar(&addTags, "add-tag", nil, "add tag (repeatable)")
	flags.StringArrayVar(&rmTags, "rm-tag", nil, "remove tag (repeatable)")
	flags.StringArrayVar(&addPaths, "add-path", nil, "add path anchor (repeatable)")
	flags.StringArrayVar(&rmPaths, "rm-path", nil, "remove path anchor (repeatable)")
	flags.StringArrayVar(&addCommits, "add-commit", nil, "add commit anchor (repeatable)")
	flags.StringArrayVar(&rmCommits, "rm-commit", nil, "remove commit anchor (repeatable)")
	flags.StringArrayVar(&addBranches, "add-branch", nil, "add branch anchor (repeatable)")
	flags.StringArrayVar(&rmBranches, "rm-branch", nil, "remove branch anchor (repeatable)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newNoteRmCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "rm ID",
		Short: "Tombstone a note",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, s.Git); err != nil {
				return err
			}
			ref, _, err := loadNote(ctx, s, args[0])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.DeleteNote{}})
			if err != nil {
				return err
			}
			return printNote(cmd, snapshot.(model.Note), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newNoteSearchCmd() *cobra.Command {
	var tags []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Search notes by title, body, and tags",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			notes, err := s.ListNotes(cmd.Context(), false)
			if err != nil {
				return err
			}
			query := strings.ToLower(args[0])
			notes = slices.DeleteFunc(notes, func(n model.Note) bool {
				return !hasAll(n.Tags, tags) || !matchesQuery(n, query)
			})
			sortNotes(notes)
			return printNoteList(cmd, notes, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringArrayVar(&tags, "tag", nil, "require tag (repeatable, ANDed)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func printNoteList(cmd *cobra.Command, notes []model.Note, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]noteDTO, len(notes))
		for i, n := range notes {
			dtos[i] = newNoteDTO(n)
		}
		return printJSON(out, dtos)
	}
	for _, n := range notes {
		if _, err := fmt.Fprintln(out, leanNoteLine(n)); err != nil {
			return err
		}
	}
	return nil
}

func matchesQuery(n model.Note, query string) bool {
	if strings.Contains(strings.ToLower(n.Title), query) || strings.Contains(strings.ToLower(n.Body), query) {
		return true
	}
	return slices.ContainsFunc(n.Tags, func(tag string) bool {
		return strings.Contains(strings.ToLower(tag), query)
	})
}

func hasAnchor(n model.Note, kind model.AnchorKind, value string) bool {
	return slices.Contains(n.Anchors, model.Anchor{Kind: kind, Value: value})
}

func buildAnchors(commits, paths, branches []string) []model.Anchor {
	anchors := make([]model.Anchor, 0, len(commits)+len(paths)+len(branches))
	for _, v := range commits {
		anchors = append(anchors, model.Anchor{Kind: model.AnchorCommit, Value: v})
	}
	for _, v := range paths {
		anchors = append(anchors, model.Anchor{Kind: model.AnchorPath, Value: v})
	}
	for _, v := range branches {
		anchors = append(anchors, model.Anchor{Kind: model.AnchorBranch, Value: v})
	}
	return anchors
}
