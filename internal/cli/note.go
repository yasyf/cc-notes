package cli

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/refs"
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

func newNoteAddCmd() *cobra.Command {
	var body string
	var tags, commits, paths, dirs, branches, attach []string
	var jsonOut, checkout, apply, abort bool
	cmd := &cobra.Command{
		Use:   "add TITLE",
		Short: "Create a note",
		Long: "Create a note from flags, or as a file: --checkout writes a template —\n" +
			"prefilled from any TITLE and anchor/tag flags — to an editable file and\n" +
			"prints its path; fill it in, then --apply <path> to create the note\n" +
			"(--abort <path> discards it). --apply also accepts --attach.",
		Args: func(cmd *cobra.Command, args []string) error {
			if checkout {
				return maxArgs(1)(cmd, args)
			}
			return exactArgs(1)(cmd, args)
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if checkout || apply || abort {
				var p prefill
				if checkout {
					p = prefill{title: optionalTitle(args), tags: tags, commits: commits, paths: paths, dirs: dirs, branches: branches}
				}
				return runFileMode(cmd, noteAdapter(), true, args, fileModeOpts{
					checkout: checkout, apply: apply, abort: abort, jsonOut: jsonOut,
					prefill: p, attach: attach,
				})
			}
			if err := validateTitle(args[0], titleHintBody); err != nil {
				return err
			}
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
			commits, err := resolveCommits(ctx, s.Git, commits)
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
			ops := []model.Op{create}
			attOps, err := attachOps(ctx, cmd, s, attach)
			if err != nil {
				return err
			}
			snapshot, err := s.Create(ctx, append(ops, attOps...))
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
			return printNote(cmd, s, verified.(model.Note), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&body, "body", "", "note body; - reads stdin")
	flags.StringArrayVar(&attach, "attach", nil, "attach a file's content via git-lfs (repeatable; uploads on sync)")
	flags.StringArrayVar(&tags, "tag", nil, "tag (repeatable)")
	flags.StringArrayVar(&commits, "commit", nil, "commit anchor (repeatable)")
	flags.StringArrayVar(&paths, "path", nil, "path anchor (repeatable)")
	flags.StringArrayVar(&dirs, "dir", nil, "directory anchor (repeatable)")
	flags.StringArrayVar(&branches, "branch", nil, "branch anchor (repeatable)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	flags.BoolVar(&checkout, "checkout", false, "write a note template (prefilled from TITLE and anchor/tag flags) to an editable file and print its path")
	flags.BoolVar(&apply, "apply", false, "create the note from the checked-out file (add --apply PATH); may carry --attach")
	flags.BoolVar(&abort, "abort", false, "discard the checked-out file (add --abort PATH)")
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
			return printNoteList(cmd, s, notes, jsonOut)
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
			atts, err := entityAttachments(ctx, s, note.Attachments)
			if err != nil {
				return err
			}
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), newNoteDTO(note, verdict, atts))
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), renderNoteShow(note, verdict, supersedes, atts))
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newNoteEditCmd() *cobra.Command {
	var title, body string
	var addTags, rmTags, addPaths, rmPaths, addDirs, rmDirs, addCommits, rmCommits, addBranches, rmBranches, rmAttachments, attach []string
	var jsonOut, checkout, apply, abort, replace bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Edit a note",
		Long: "Edit a note by flags, or as a file: --checkout writes the note to an editable\n" +
			"Markdown file and prints its path; edit that file with your normal tools, then\n" +
			"--apply to commit the change (or --abort to discard).",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if checkout || apply || abort {
				return runFileMode(cmd, noteAdapter(), false, args, fileModeOpts{checkout: checkout, apply: apply, abort: abort, jsonOut: jsonOut})
			}
			ctx := cmd.Context()
			if replace && len(attach) == 0 {
				return &UsageError{Err: errors.New("--replace requires --attach")}
			}
			var ops []model.Op
			if cmd.Flags().Changed("title") {
				if err := validateTitle(title, titleHintBodyEdit); err != nil {
					return err
				}
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
			s, err := openStore()
			if err != nil {
				return err
			}
			addCommits, err := resolveCommits(ctx, s.Git, addCommits)
			if err != nil {
				return err
			}
			for _, a := range buildAnchors(addCommits, addPaths, addDirs, addBranches) {
				ops = append(ops, model.AddAnchor{Anchor: a})
			}
			for _, a := range buildAnchors(rmCommits, rmPaths, rmDirs, rmBranches) {
				ops = append(ops, model.RemoveAnchor{Anchor: a})
			}
			for _, name := range rmAttachments {
				ops = append(ops, model.RemoveAttachment{Name: name})
			}
			if len(ops) == 0 && len(attach) == 0 {
				return &UsageError{Err: errors.New("note edit requires at least one flag")}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, note, err := loadNote(ctx, s, args[0])
			if err != nil {
				return err
			}
			if !replace {
				if err := checkAttachCollisions(note.Attachments, attach); err != nil {
					return err
				}
			}
			attOps, err := attachOps(ctx, cmd, s, attach)
			if err != nil {
				return err
			}
			ops = append(ops, attOps...)
			snapshot, err := s.Append(ctx, ref, ops)
			if err != nil {
				return err
			}
			return printNote(cmd, s, snapshot.(model.Note), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&title, "title", "", "new title")
	flags.StringVar(&body, "body", "", "new body; - reads stdin")
	flags.StringArrayVar(&attach, "attach", nil, "attach a file's content via git-lfs (repeatable; uploads on sync)")
	flags.BoolVar(&replace, "replace", false, "allow --attach to overwrite a live attachment with the same name")
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
	flags.StringArrayVar(&rmAttachments, "rm-attachment", nil, "remove attachment by name (repeatable)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	flags.BoolVar(&checkout, "checkout", false, "write the note to an editable file and print its path")
	flags.BoolVar(&apply, "apply", false, "apply edits from the checked-out file")
	flags.BoolVar(&abort, "abort", false, "discard the checked-out file")
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
			return printNote(cmd, s, snapshot.(model.Note), jsonOut)
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
			return printNoteList(cmd, s, notes, jsonOut)
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
			return printNote(cmd, s, snapshot.(model.Note), jsonOut)
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
			return printNote(cmd, s, snapshot.(model.Note), jsonOut)
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
	var clearFlag, jsonOut bool
	cmd := &cobra.Command{
		Use:   "expire ID",
		Short: "Flag a note as out-of-date (agent-asserted), or --clear to remove the flag",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if clearFlag && reason != "" {
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
			if clearFlag {
				ops = []model.Op{model.ClearStale{}}
			} else {
				ops = []model.Op{model.MarkStale{Reason: reason}}
			}
			snapshot, err := s.Append(ctx, ref, ops)
			if err != nil {
				return err
			}
			return printNote(cmd, s, snapshot.(model.Note), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&reason, "reason", "", "why the note is out-of-date")
	flags.BoolVar(&clearFlag, "clear", false, "remove the out-of-date flag")
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
			return printNoteReview(cmd, s, filterVerdicts(reviewed, drift, unverified, expired), jsonOut)
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
func printNoteReview(cmd *cobra.Command, s *store.Store, reviewed []reviewedNote, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]noteDTO, len(reviewed))
		for i, r := range reviewed {
			atts, err := entityAttachments(cmd.Context(), s, r.note.Attachments)
			if err != nil {
				return err
			}
			dtos[i] = newNoteDTO(r.note, r.verdict, atts)
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

func printNoteList(cmd *cobra.Command, s *store.Store, notes []model.Note, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]noteDTO, len(notes))
		for i, n := range notes {
			atts, err := entityAttachments(cmd.Context(), s, n.Attachments)
			if err != nil {
				return err
			}
			dtos[i] = newNoteDTO(n, "", atts)
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
