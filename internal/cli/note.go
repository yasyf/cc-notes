package cli

import (
	"cmp"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/refs"
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
	)
	return cmd
}

func newNoteAddCmd() *cobra.Command {
	var body string
	var tags, commits, paths, dirs, branches []string
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
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
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
				Anchors: buildAnchors(commits, paths, dirs, branches),
			}
			snapshot, err := s.Create(ctx, []model.Op{create})
			if err != nil {
				return err
			}
			note := snapshot.(model.Note)
			head, err := resolveHead(ctx, s)
			if err != nil {
				return err
			}
			witness, err := buildWitness(ctx, s, head, note.Anchors)
			if err != nil {
				return err
			}
			verified, err := s.Append(ctx, refs.Note(note.ID), []model.Op{model.VerifyNote{Witness: witness, VerifiedCommit: head}})
			if err != nil {
				return err
			}
			return printNote(cmd, verified.(model.Note), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&body, "body", "", "note body; - reads stdin")
	flags.StringArrayVar(&tags, "tag", nil, "tag (repeatable)")
	flags.StringArrayVar(&commits, "commit", nil, "commit anchor (repeatable)")
	flags.StringArrayVar(&paths, "path", nil, "path anchor (repeatable)")
	flags.StringArrayVar(&dirs, "dir", nil, "directory anchor (repeatable)")
	flags.StringArrayVar(&branches, "branch", nil, "branch anchor (repeatable)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newNoteListCmd() *cobra.Command {
	var tags []string
	var path, commit, dir, branch string
	var all, includeSuperseded, jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List notes",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			notes, err := s.ListNotes(cmd.Context(), all, includeSuperseded)
			if err != nil {
				return err
			}
			notes = slices.DeleteFunc(notes, func(n model.Note) bool {
				return !hasAll(n.Tags, tags) ||
					(commit != "" && !hasAnchor(n, model.AnchorCommit, commit)) ||
					(path != "" && !hasAnchor(n, model.AnchorPath, path)) ||
					(dir != "" && !hasAnchor(n, model.AnchorDir, dir)) ||
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
	flags.StringVar(&dir, "dir", "", "require directory anchor")
	flags.StringVar(&branch, "branch", "", "require branch anchor")
	flags.BoolVar(&all, "all", false, "include tombstoned notes")
	flags.BoolVar(&includeSuperseded, "include-superseded", false, "include superseded notes")
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
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			_, note, err := loadNote(ctx, s, args[0])
			if err != nil {
				return err
			}
			head, err := resolveHead(ctx, s)
			if err != nil {
				return err
			}
			staleAfter, err := noteStaleAfter(ctx, s.Git)
			if err != nil {
				return err
			}
			verdict, err := noteVerdict(ctx, s, head, note, time.Now(), staleAfter, false)
			if err != nil {
				return err
			}
			supersedes, err := reverseSupersedes(ctx, s, note.ID)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), newNoteDTO(note, verdict))
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), renderNoteShow(note, verdict, supersedes))
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newNoteEditCmd() *cobra.Command {
	var title, body string
	var addTags, rmTags, addPaths, rmPaths, addDirs, rmDirs, addCommits, rmCommits, addBranches, rmBranches []string
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
			for _, a := range buildAnchors(addCommits, addPaths, addDirs, addBranches) {
				ops = append(ops, model.AddAnchor{Anchor: a})
			}
			for _, a := range buildAnchors(rmCommits, rmPaths, rmDirs, rmBranches) {
				ops = append(ops, model.RemoveAnchor{Anchor: a})
			}
			if len(ops) == 0 {
				return &UsageError{Err: errors.New("note edit requires at least one flag")}
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
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
	flags.StringArrayVar(&addDirs, "add-dir", nil, "add directory anchor (repeatable)")
	flags.StringArrayVar(&rmDirs, "rm-dir", nil, "remove directory anchor (repeatable)")
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
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
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
	var author, anchorPath, anchorDir, anchorBranch, anchorCommit string
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Ranked search across note titles, tags, and bodies",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			notes, err := s.ListNotes(cmd.Context(), false, false)
			if err != nil {
				return err
			}
			notes = rankNotes(notes, args[0], tags, author, anchorPath, anchorDir, anchorBranch, anchorCommit, limit)
			return printNoteList(cmd, notes, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringArrayVar(&tags, "tag", nil, "require tag (repeatable, ANDed)")
	flags.IntVar(&limit, "limit", 20, "maximum results")
	flags.StringVar(&author, "author", "", "require author")
	flags.StringVar(&anchorPath, "anchor-path", "", "require path anchor")
	flags.StringVar(&anchorDir, "anchor-dir", "", "require directory anchor")
	flags.StringVar(&anchorBranch, "anchor-branch", "", "require branch anchor")
	flags.StringVar(&anchorCommit, "anchor-commit", "", "require commit anchor")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newNoteVerifyCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "verify ID",
		Short: "Re-verify a note, refreshing its witness against current HEAD",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, note, err := loadNote(ctx, s, args[0])
			if err != nil {
				return err
			}
			head, err := resolveHead(ctx, s)
			if err != nil {
				return err
			}
			witness, err := buildWitness(ctx, s, head, note.Anchors)
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.VerifyNote{Witness: witness, VerifiedCommit: head}})
			if err != nil {
				return err
			}
			return printNote(cmd, snapshot.(model.Note), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newNoteSupersedeCmd() *cobra.Command {
	var by string
	var remove, jsonOut bool
	cmd := &cobra.Command{
		Use:   "supersede OLD --by NEW",
		Short: "Record that NEW replaces OLD (--remove undoes the edge)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if by == "" {
				return &UsageError{Err: errors.New("note supersede requires --by NEW")}
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			oldRef, _, err := loadNote(ctx, s, args[0])
			if err != nil {
				return err
			}
			_, newNote, err := loadNote(ctx, s, by)
			if err != nil {
				return err
			}
			var op model.Op = model.AddSupersededBy{ID: newNote.ID}
			if remove {
				op = model.RemoveSupersededBy{ID: newNote.ID}
			}
			snapshot, err := s.Append(ctx, oldRef, []model.Op{op})
			if err != nil {
				return err
			}
			return printNote(cmd, snapshot.(model.Note), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&by, "by", "", "the replacement note (required)")
	flags.BoolVar(&remove, "remove", false, "remove the supersede edge")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newNoteExpireCmd() *cobra.Command {
	var reason string
	var clear, jsonOut bool
	cmd := &cobra.Command{
		Use:   "expire ID",
		Short: "Flag a note as out-of-date (agent-asserted), or --clear to remove the flag",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if clear && reason != "" {
				return &UsageError{Err: errors.New("note expire --clear takes no --reason")}
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, _, err := loadNote(ctx, s, args[0])
			if err != nil {
				return err
			}
			var ops []model.Op
			if clear {
				ops = []model.Op{model.ClearStale{}}
			} else {
				ops = []model.Op{model.MarkStale{Reason: reason}}
			}
			snapshot, err := s.Append(ctx, ref, ops)
			if err != nil {
				return err
			}
			return printNote(cmd, snapshot.(model.Note), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&reason, "reason", "", "why the note is out-of-date")
	flags.BoolVar(&clear, "clear", false, "remove the out-of-date flag")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newNoteReviewCmd() *cobra.Command {
	var staleAfterFlag string
	var drift, unverified, expired, jsonOut bool
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Surface notes needing attention, each with a verdict",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			now := time.Now()
			s, err := openStore()
			if err != nil {
				return err
			}
			staleAfter, err := resolveNoteStaleAfter(ctx, s.Git, staleAfterFlag)
			if err != nil {
				return err
			}
			head, err := resolveHead(ctx, s)
			if err != nil {
				return err
			}
			reviewed, err := reviewNotes(ctx, s, head, now, staleAfter)
			if err != nil {
				return err
			}
			return printNoteReview(cmd, filterVerdicts(reviewed, drift, unverified, expired), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&staleAfterFlag, "stale-after", "", "staleness threshold (Go duration)")
	flags.BoolVar(&drift, "drift", false, "limit to drifted notes")
	flags.BoolVar(&unverified, "unverified", false, "limit to never-verified notes")
	flags.BoolVar(&expired, "expired", false, "limit to expired notes")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

// filterVerdicts restricts reviewed to a single verdict class when --drift,
// --unverified, or --expired is set; with none, every flagged note passes.
func filterVerdicts(reviewed []reviewedNote, drift, unverified, expired bool) []reviewedNote {
	var want string
	switch {
	case drift:
		want = verdictDrifted
	case unverified:
		want = verdictUnverified
	case expired:
		want = verdictExpired
	default:
		return reviewed
	}
	return slices.DeleteFunc(reviewed, func(r reviewedNote) bool { return r.verdict != want })
}

// printNoteReview writes the review set as note DTOs carrying their verdict in
// drift, or as lean lines with the verdict appended after a tab.
func printNoteReview(cmd *cobra.Command, reviewed []reviewedNote, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]noteDTO, len(reviewed))
		for i, r := range reviewed {
			dtos[i] = newNoteDTO(r.note, r.verdict)
		}
		return printJSON(out, dtos)
	}
	for _, r := range reviewed {
		if _, err := fmt.Fprintf(out, "%s\t%s\n", leanNoteLine(r.note), r.verdict); err != nil {
			return err
		}
	}
	return nil
}

// rankNotes filters notes by tag, author, and anchors, keeps those matching
// query in their title, a tag, or body, then orders by match tier
// (title > tag > body), UpdatedAt descending, id ascending, truncated to limit.
func rankNotes(notes []model.Note, query string, tags []string, author, anchorPath, anchorDir, anchorBranch, anchorCommit string, limit int) []model.Note {
	q := strings.ToLower(query)
	type scored struct {
		note model.Note
		tier int
	}
	var ranked []scored
	for _, n := range notes {
		if !hasAll(n.Tags, tags) ||
			(author != "" && string(n.Author) != author) ||
			(anchorPath != "" && !hasAnchor(n, model.AnchorPath, anchorPath)) ||
			(anchorDir != "" && !hasAnchor(n, model.AnchorDir, anchorDir)) ||
			(anchorBranch != "" && !hasAnchor(n, model.AnchorBranch, anchorBranch)) ||
			(anchorCommit != "" && !hasAnchor(n, model.AnchorCommit, anchorCommit)) {
			continue
		}
		tier := noteTier(n, q)
		if tier == 0 {
			continue
		}
		ranked = append(ranked, scored{note: n, tier: tier})
	}
	slices.SortFunc(ranked, func(a, b scored) int {
		if c := cmp.Compare(b.tier, a.tier); c != 0 {
			return c
		}
		if c := cmp.Compare(b.note.UpdatedAt, a.note.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.note.ID, b.note.ID)
	})
	if limit >= 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]model.Note, len(ranked))
	for i, r := range ranked {
		out[i] = r.note
	}
	return out
}

// noteTier ranks how n matches q: a title substring is tier 3, a tag substring
// tier 2, a body substring tier 1, and no match tier 0. The comparison is
// case-insensitive; q must already be lowercased.
func noteTier(n model.Note, q string) int {
	if strings.Contains(strings.ToLower(n.Title), q) {
		return 3
	}
	for _, tag := range n.Tags {
		if strings.Contains(strings.ToLower(tag), q) {
			return 2
		}
	}
	if strings.Contains(strings.ToLower(n.Body), q) {
		return 1
	}
	return 0
}

func printNoteList(cmd *cobra.Command, notes []model.Note, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]noteDTO, len(notes))
		for i, n := range notes {
			dtos[i] = newNoteDTO(n, "")
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

func hasAnchor(n model.Note, kind model.AnchorKind, value string) bool {
	return slices.Contains(n.Anchors, model.Anchor{Kind: kind, Value: value})
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
