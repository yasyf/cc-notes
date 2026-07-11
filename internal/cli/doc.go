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

	"github.com/yasyf/cc-notes/internal/refs"
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

func newDocAddCmd() *cobra.Command {
	var body, when string
	var labels, attach []string
	var anchors anchorSets
	var jsonOut, checkout, apply, abort bool
	cmd := &cobra.Command{
		Use:   "add TITLE",
		Short: "Create a doc",
		Long: "Create a doc from flags, or as a file: --checkout writes a template —\n" +
			"prefilled from any TITLE and anchor/label flags — to an editable file and\n" +
			"prints its path; fill in the body, then --apply <path> to create the doc\n" +
			"(--abort <path> discards it). --apply also accepts --attach, but the buffer\n" +
			"must carry a body, so attach-only docs use flag-mode --attach.",
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
					p = prefill{title: optionalTitle(args), when: when, tags: labels, commits: anchors.commits, paths: anchors.paths, dirs: anchors.dirs, branches: anchors.branches}
				}
				return runFileMode(cmd, docAdapter(), true, args, fileModeOpts{
					checkout: checkout, apply: apply, abort: abort, jsonOut: jsonOut,
					prefill: p, attach: attach,
				})
			}
			if err := validateTitle(args[0], titleHintBody); err != nil {
				return err
			}
			text, err := bodyArg(cmd, body)
			if err != nil {
				return err
			}
			if text == "" && len(attach) == 0 {
				return errEmptyDocBody(docBodyHintAdd)
			}
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			commits, err := resolveCommits(ctx, s.Git, anchors.commits)
			if err != nil {
				return err
			}
			create := model.CreateDoc{
				Nonce:   model.NewNonce(),
				Title:   args[0],
				Body:    text,
				When:    when,
				Tags:    labels,
				Anchors: buildAnchors(commits, anchors.paths, anchors.dirs, anchors.branches),
			}
			ops := []model.Op{create}
			attOps, err := attachOps(ctx, cmd, s, attach)
			if err != nil {
				return err
			}
			snapshot, err := createEntity(ctx, cmd, s, append(ops, attOps...))
			if err != nil {
				return err
			}
			doc := snapshot.(model.Doc)
			head, err := resolveHead(ctx, s)
			if err != nil {
				return err
			}
			witness, err := buildWitness(ctx, s, head, doc.Anchors)
			if err != nil {
				return err
			}
			// A doc add re-asserts the fact now, so a dedupe hit re-verifies the
			// reused doc rather than skipping it: VerifyNote refreshes the
			// survivor's witness, verified_at/by, and verified_commit and clears
			// any stale flag (fold.foldDoc), exactly as a fresh add is born
			// verified. The dedupe scan excludes stale twins, so this survivor is live.
			verified, err := s.Append(ctx, refs.For(model.KindDoc, doc.ID), []model.Op{model.VerifyNote{Witness: witness, VerifiedCommit: head}})
			if err != nil {
				return err
			}
			return printDoc(cmd, s, verified.(model.Doc), "", jsonOut)
		},
	}
	flags := cmd.Flags()
	bindBody(flags, &body, "doc body; - reads stdin")
	flags.StringVar(&when, "when", "", "free-text read-this-when trigger")
	flags.StringArrayVar(&attach, "attach", nil, "attach a file's content via git-lfs (repeatable; uploads on sync)")
	bindLabels(flags, &labels, "label (repeatable)")
	anchors.bind(flags)
	bindJSON(flags, &jsonOut)
	flags.BoolVar(&checkout, "checkout", false, "write a doc template (prefilled from TITLE and anchor/label flags) to an editable file and print its path")
	flags.BoolVar(&apply, "apply", false, "create the doc from the checked-out file (add --apply PATH); may carry --attach")
	flags.BoolVar(&abort, "abort", false, "discard the checked-out file (add --abort PATH)")
	return cmd
}

func newDocListCmd() *cobra.Command {
	var labels []string
	var filters anchorFilters
	var all, includeSuperseded, jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List docs",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			docs, err := s.ListDocs(cmd.Context(), all, includeSuperseded)
			if err != nil {
				return err
			}
			docs = slices.DeleteFunc(docs, func(d model.Doc) bool {
				return !hasAll(d.Tags, labels) ||
					(filters.commit != "" && !hasAnchorIn(d.Anchors, model.AnchorCommit, filters.commit)) ||
					(filters.path != "" && !hasAnchorIn(d.Anchors, model.AnchorPath, filters.path)) ||
					(filters.dir != "" && !hasAnchorIn(d.Anchors, model.AnchorDir, filters.dir)) ||
					(filters.branch != "" && !hasAnchorIn(d.Anchors, model.AnchorBranch, filters.branch))
			})
			sortDocs(docs)
			return printDocList(cmd, s, docs, jsonOut)
		},
	}
	flags := cmd.Flags()
	bindLabels(flags, &labels, "require label (repeatable, ANDed)")
	filters.bind(flags)
	flags.BoolVar(&all, "all", false, "include tombstoned docs")
	flags.BoolVar(&includeSuperseded, "include-superseded", false, "include superseded docs")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newDocShowCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show one doc",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			return showDoc(cmd, s, args[0], jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newDocEditCmd() *cobra.Command {
	var title, body, when string
	var rmAttachments, attach []string
	var labels labelEdits
	var anchors anchorEdits
	var jsonOut, checkout, apply, abort, replace bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Edit a doc",
		Long: "Edit a doc by flags, or as a file: --checkout writes the doc to an editable\n" +
			"Markdown file and prints its path; edit that file with your normal tools, then\n" +
			"--apply to commit the change (or --abort to discard).",
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if checkout || apply || abort {
				return runFileMode(cmd, docAdapter(), false, args, fileModeOpts{checkout: checkout, apply: apply, abort: abort, jsonOut: jsonOut})
			}
			ctx := cmd.Context()
			if replace && len(attach) == 0 {
				return &UsageError{Err: errors.New("--replace requires --attach")}
			}
			var ops []model.Op
			if cmd.Flags().Changed("title") {
				if err := validateTitle(title, titleHintBody); err != nil {
					return err
				}
				ops = append(ops, model.SetTitle{Title: title})
			}
			if cmd.Flags().Changed("body") {
				text, err := bodyArg(cmd, body)
				if err != nil {
					return err
				}
				if text == "" && len(attach) == 0 {
					return errEmptyDocBody(docBodyHintAdd)
				}
				ops = append(ops, model.SetBody{Body: text})
			}
			if cmd.Flags().Changed("when") {
				ops = append(ops, model.SetWhen{When: when})
			}
			for _, l := range labels.add {
				ops = append(ops, model.AddTag{Tag: l})
			}
			for _, l := range labels.rm {
				ops = append(ops, model.RemoveTag{Tag: l})
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			addCommits, err := resolveCommits(ctx, s.Git, anchors.addCommits)
			if err != nil {
				return err
			}
			for _, a := range buildAnchors(addCommits, anchors.addPaths, anchors.addDirs, anchors.addBranches) {
				ops = append(ops, model.AddAnchor{Anchor: a})
			}
			for _, a := range buildAnchors(anchors.rmCommits, anchors.rmPaths, anchors.rmDirs, anchors.rmBranches) {
				ops = append(ops, model.RemoveAnchor{Anchor: a})
			}
			for _, name := range rmAttachments {
				ops = append(ops, model.RemoveAttachment{Name: name})
			}
			if len(ops) == 0 && len(attach) == 0 {
				return &UsageError{Err: errors.New("doc edit requires at least one flag")}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, doc, err := loadDoc(ctx, s, args[0])
			if err != nil {
				return err
			}
			if !replace {
				if err := checkAttachCollisions(doc.Attachments, attach); err != nil {
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
			return printDoc(cmd, s, snapshot.(model.Doc), "", jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&title, "title", "", "new title")
	bindBody(flags, &body, "new body; - reads stdin")
	flags.StringVar(&when, "when", "", "new read-this-when trigger")
	flags.StringArrayVar(&attach, "attach", nil, "attach a file's content via git-lfs (repeatable; uploads on sync)")
	flags.BoolVar(&replace, "replace", false, "allow --attach to overwrite a live attachment with the same name")
	labels.bind(flags)
	anchors.bind(flags)
	flags.StringArrayVar(&rmAttachments, "rm-attachment", nil, "remove attachment by name (repeatable)")
	bindJSON(flags, &jsonOut)
	flags.BoolVar(&checkout, "checkout", false, "write the doc to an editable file and print its path")
	flags.BoolVar(&apply, "apply", false, "apply edits from the checked-out file")
	flags.BoolVar(&abort, "abort", false, "discard the checked-out file")
	return cmd
}

func newDocRmCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "rm ID",
		Short: "Tombstone a doc",
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
			ref, _, err := loadDoc(ctx, s, args[0])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.DeleteNote{}})
			if err != nil {
				return err
			}
			return printDoc(cmd, s, snapshot.(model.Doc), "", jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newDocSearchCmd() *cobra.Command {
	var labels []string
	var author string
	var filters anchorFilters
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: "Ranked search across doc titles, labels, and bodies",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			docs, err := s.ListDocs(cmd.Context(), false, false)
			if err != nil {
				return err
			}
			docs = rankDocs(docs, args[0], labels, author, filters.path, filters.dir, filters.branch, filters.commit, limit)
			return printDocList(cmd, s, docs, jsonOut)
		},
	}
	flags := cmd.Flags()
	bindLabels(flags, &labels, "require label (repeatable, ANDed)")
	flags.IntVar(&limit, "limit", 20, "maximum results")
	flags.StringVar(&author, "author", "", "require author")
	filters.bind(flags)
	bindJSON(flags, &jsonOut)
	return cmd
}

func newDocVerifyCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "verify ID",
		Short: "Re-verify a doc, refreshing its witness against current HEAD",
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
			ref, doc, err := loadDoc(ctx, s, args[0])
			if err != nil {
				return err
			}
			head, err := resolveHead(ctx, s)
			if err != nil {
				return err
			}
			witness, err := buildWitness(ctx, s, head, doc.Anchors)
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.VerifyNote{Witness: witness, VerifiedCommit: head}})
			if err != nil {
				return err
			}
			return printDoc(cmd, s, snapshot.(model.Doc), "", jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newDocSupersedeCmd() *cobra.Command {
	var by string
	var clearFlag, jsonOut bool
	cmd := &cobra.Command{
		Use:   "supersede OLD --by NEW",
		Short: "Record that NEW replaces OLD (--clear undoes the edge)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if by == "" {
				return &UsageError{Err: errors.New("doc supersede requires --by NEW")}
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			oldRef, _, err := loadDoc(ctx, s, args[0])
			if err != nil {
				return err
			}
			_, newDoc, err := loadDoc(ctx, s, by)
			if err != nil {
				return err
			}
			var op model.Op = model.AddSupersededBy{ID: newDoc.ID}
			if clearFlag {
				op = model.RemoveSupersededBy{ID: newDoc.ID}
			}
			snapshot, err := s.Append(ctx, oldRef, []model.Op{op})
			if err != nil {
				return err
			}
			return printDoc(cmd, s, snapshot.(model.Doc), "", jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&by, "by", "", "the replacement doc (required)")
	flags.BoolVar(&clearFlag, "clear", false, "remove the supersede edge")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newDocExpireCmd() *cobra.Command {
	var reason string
	var clearFlag, jsonOut bool
	cmd := &cobra.Command{
		Use:   "expire ID",
		Short: "Flag a doc as out-of-date (agent-asserted), or --clear to remove the flag",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if clearFlag && reason != "" {
				return &UsageError{Err: errors.New("doc expire --clear takes no --reason")}
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, _, err := loadDoc(ctx, s, args[0])
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
			return printDoc(cmd, s, snapshot.(model.Doc), "", jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&reason, "reason", "", "why the doc is out-of-date")
	flags.BoolVar(&clearFlag, "clear", false, "remove the out-of-date flag")
	bindJSON(flags, &jsonOut)
	return cmd
}

func newDocReviewCmd() *cobra.Command {
	var staleAfterFlag string
	var drift, unverified, expired, jsonOut bool
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Surface docs needing attention, each with a verdict",
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
			reviewed, err := reviewDocs(ctx, s, head, now, staleAfter)
			if err != nil {
				return err
			}
			return printDocReview(cmd, s, filterDocVerdicts(reviewed, drift, unverified, expired), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&staleAfterFlag, "stale-after", "", "staleness threshold (Go duration)")
	flags.BoolVar(&drift, "drift", false, "limit to drifted docs")
	flags.BoolVar(&unverified, "unverified", false, "limit to never-verified docs")
	flags.BoolVar(&expired, "expired", false, "limit to expired docs")
	bindJSON(flags, &jsonOut)
	return cmd
}

// filterDocVerdicts restricts reviewed to a single verdict class when --drift,
// --unverified, or --expired is set; with none, every flagged doc passes.
func filterDocVerdicts(reviewed []reviewedDoc, drift, unverified, expired bool) []reviewedDoc {
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
	return slices.DeleteFunc(reviewed, func(r reviewedDoc) bool { return r.verdict != want })
}

// printDocReview writes the review set as doc DTOs carrying their verdict in
// drift, or as lean lines with the verdict appended after a tab.
func printDocReview(cmd *cobra.Command, s *store.Store, reviewed []reviewedDoc, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]docDTO, len(reviewed))
		for i, r := range reviewed {
			atts, err := entityAttachments(cmd.Context(), s, r.doc.Attachments)
			if err != nil {
				return err
			}
			dtos[i] = newDocDTO(r.doc, r.verdict, atts)
		}
		return printJSON(out, dtos)
	}
	for _, r := range reviewed {
		if _, err := fmt.Fprintf(out, "%s\t%s\n", leanDocLine(r.doc), r.verdict); err != nil {
			return err
		}
	}
	return nil
}

// rankDocs filters docs by tag, author, and anchors, keeps those matching
// query in their title, a tag, or body, then orders by match tier
// (title > tag > body), UpdatedAt descending, id ascending, truncated to limit.
func rankDocs(docs []model.Doc, query string, tags []string, author, anchorPath, anchorDir, anchorBranch, anchorCommit string, limit int) []model.Doc {
	q := strings.ToLower(query)
	type scored struct {
		doc  model.Doc
		tier int
	}
	var ranked []scored
	for _, d := range docs {
		if !hasAll(d.Tags, tags) ||
			(author != "" && string(d.Author) != author) ||
			(anchorPath != "" && !hasAnchorIn(d.Anchors, model.AnchorPath, anchorPath)) ||
			(anchorDir != "" && !hasAnchorIn(d.Anchors, model.AnchorDir, anchorDir)) ||
			(anchorBranch != "" && !hasAnchorIn(d.Anchors, model.AnchorBranch, anchorBranch)) ||
			(anchorCommit != "" && !hasAnchorIn(d.Anchors, model.AnchorCommit, anchorCommit)) {
			continue
		}
		tier := docTier(d, q)
		if tier == 0 {
			continue
		}
		ranked = append(ranked, scored{doc: d, tier: tier})
	}
	slices.SortFunc(ranked, func(a, b scored) int {
		if c := cmp.Compare(b.tier, a.tier); c != 0 {
			return c
		}
		if c := cmp.Compare(b.doc.UpdatedAt, a.doc.UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.doc.ID, b.doc.ID)
	})
	if limit >= 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]model.Doc, len(ranked))
	for i, r := range ranked {
		out[i] = r.doc
	}
	return out
}

// docTier ranks how d matches q: a title substring is tier 3, a tag substring
// tier 2, a body substring tier 1, and no match tier 0. The comparison is
// case-insensitive; q must already be lowercased.
func docTier(d model.Doc, q string) int {
	if strings.Contains(strings.ToLower(d.Title), q) {
		return 3
	}
	for _, tag := range d.Tags {
		if strings.Contains(strings.ToLower(tag), q) {
			return 2
		}
	}
	if strings.Contains(strings.ToLower(d.Body), q) {
		return 1
	}
	return 0
}

func printDocList(cmd *cobra.Command, s *store.Store, docs []model.Doc, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]docDTO, len(docs))
		for i, d := range docs {
			atts, err := entityAttachments(cmd.Context(), s, d.Attachments)
			if err != nil {
				return err
			}
			dtos[i] = newDocDTO(d, "", atts)
		}
		return printJSON(out, dtos)
	}
	for _, d := range docs {
		if _, err := fmt.Fprintln(out, leanDocLine(d)); err != nil {
			return err
		}
	}
	return nil
}

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
