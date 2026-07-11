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

// documentSpec is the vocabulary the note and doc noun groups share for the
// freshness lifecycle: the load/print binding, the display noun, the anchor
// projection a verify witnesses, the review folder, and the DTO/lean
// projections a review prints. Note and doc reuse the note-named storage ops
// (VerifyNote, MarkStale), so the lifecycle verbs carry no per-kind op hook.
type documentSpec[T model.Snapshot] struct {
	kind         kindSpec[T]
	noun         string
	hasWhen      bool
	bodyRequired bool
	addLong      string
	editLong     string
	adapter      func() editAdapter
	createOp     func(title, body, when string, tags []string, anchors []model.Anchor) model.Op
	anchors      func(T) []model.Anchor
	reviewSet    func(ctx context.Context, s *store.Store, head model.SHA, now time.Time, staleAfter time.Duration) ([]reviewed[T], error)
	newDTO       func(T, string, []attachmentDTO) any
	lean         func(T) string
}

// addVerb builds "add TITLE": create the entity from flags, or as a checked-out
// file via --checkout/--apply/--abort. The body is read and (for body-required
// kinds) validated before the store opens, so a bad title or empty body never
// runs auto-install. A fresh entity is born verified against current HEAD.
func (spec documentSpec[T]) addVerb() *cobra.Command {
	var body, when string
	var labels, attach []string
	var anchors anchorSets
	var jsonOut, checkout, apply, abort bool
	cmd := &cobra.Command{
		Use:   "add TITLE",
		Short: "Create a " + spec.noun,
		Long:  spec.addLong,
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
				return runFileMode(cmd, spec.adapter(), true, args, fileModeOpts{
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
			if spec.bodyRequired && text == "" && len(attach) == 0 {
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
			ops := []model.Op{spec.createOp(args[0], text, when, labels, buildAnchors(commits, anchors.paths, anchors.dirs, anchors.branches))}
			attOps, err := attachOps(ctx, cmd, s, attach)
			if err != nil {
				return err
			}
			snapshot, err := createEntity(ctx, cmd, s, append(ops, attOps...))
			if err != nil {
				return err
			}
			ent := snapshot.(T)
			head, err := resolveHead(ctx, s)
			if err != nil {
				return err
			}
			witness, err := buildWitness(ctx, s, head, spec.anchors(ent))
			if err != nil {
				return err
			}
			// An add re-asserts the fact now, so a dedupe hit re-verifies the reused
			// entity rather than skipping it: VerifyNote refreshes the survivor's
			// witness, verified_at/by, and verified_commit and clears any stale flag,
			// exactly as a fresh add is born verified. The dedupe scan excludes stale
			// twins, so this survivor is live.
			verified, err := s.Append(ctx, refs.For(spec.kind.kind, ent.EntityID()), []model.Op{model.VerifyNote{Witness: witness, VerifiedCommit: head}})
			if err != nil {
				return err
			}
			return spec.kind.print(cmd, s, verified.(T), jsonOut)
		},
	}
	flags := cmd.Flags()
	bindBody(flags, &body, spec.noun+" body; - reads stdin")
	if spec.hasWhen {
		flags.StringVar(&when, "when", "", "free-text read-this-when trigger")
	}
	flags.StringArrayVar(&attach, "attach", nil, "attach a file's content via git-lfs (repeatable; uploads on sync)")
	bindLabels(flags, &labels, "label (repeatable)")
	anchors.bind(flags)
	bindJSON(flags, &jsonOut)
	flags.BoolVar(&checkout, "checkout", false, "write a "+spec.noun+" template (prefilled from TITLE and anchor/label flags) to an editable file and print its path")
	flags.BoolVar(&apply, "apply", false, "create the "+spec.noun+" from the checked-out file (add --apply PATH); may carry --attach")
	flags.BoolVar(&abort, "abort", false, "discard the checked-out file (add --abort PATH)")
	return cmd
}

// editVerb builds "edit ID": mutate the entity by flags, or as a checked-out
// file. At least one mutation is required.
func (spec documentSpec[T]) editVerb() *cobra.Command {
	var title, body, when string
	var rmAttachments, attach []string
	var labels labelEdits
	var anchors anchorEdits
	var jsonOut, checkout, apply, abort, replace bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Edit a " + spec.noun,
		Long:  spec.editLong,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if checkout || apply || abort {
				return runFileMode(cmd, spec.adapter(), false, args, fileModeOpts{checkout: checkout, apply: apply, abort: abort, jsonOut: jsonOut})
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
				if spec.bodyRequired && text == "" && len(attach) == 0 {
					return errEmptyDocBody(docBodyHintAdd)
				}
				ops = append(ops, model.SetBody{Body: text})
			}
			if spec.hasWhen && cmd.Flags().Changed("when") {
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
				return &UsageError{Err: errors.New(spec.noun + " edit requires at least one flag")}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, ent, err := spec.kind.load(ctx, s, args[0])
			if err != nil {
				return err
			}
			if !replace {
				if err := checkAttachCollisions(ent.Meta().Attachments, attach); err != nil {
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
			return spec.kind.print(cmd, s, snapshot.(T), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&title, "title", "", "new title")
	bindBody(flags, &body, "new body; - reads stdin")
	if spec.hasWhen {
		flags.StringVar(&when, "when", "", "new read-this-when trigger")
	}
	flags.StringArrayVar(&attach, "attach", nil, "attach a file's content via git-lfs (repeatable; uploads on sync)")
	flags.BoolVar(&replace, "replace", false, "allow --attach to overwrite a live attachment with the same name")
	labels.bind(flags)
	anchors.bind(flags)
	flags.StringArrayVar(&rmAttachments, "rm-attachment", nil, "remove attachment by name (repeatable)")
	bindJSON(flags, &jsonOut)
	flags.BoolVar(&checkout, "checkout", false, "write the "+spec.noun+" to an editable file and print its path")
	flags.BoolVar(&apply, "apply", false, "apply edits from the checked-out file")
	flags.BoolVar(&abort, "abort", false, "discard the checked-out file")
	return cmd
}

// verifyVerb builds "verify ID": re-witness the entity against current HEAD.
func (spec documentSpec[T]) verifyVerb() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "verify ID",
		Short: "Re-verify a " + spec.noun + ", refreshing its witness against current HEAD",
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
			ref, ent, err := spec.kind.load(ctx, s, args[0])
			if err != nil {
				return err
			}
			head, err := resolveHead(ctx, s)
			if err != nil {
				return err
			}
			witness, err := buildWitness(ctx, s, head, spec.anchors(ent))
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.VerifyNote{Witness: witness, VerifiedCommit: head}})
			if err != nil {
				return err
			}
			return spec.kind.print(cmd, s, snapshot.(T), jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

// supersedeVerb builds "supersede OLD --by NEW": record or clear the edge that
// NEW replaces OLD.
func (spec documentSpec[T]) supersedeVerb() *cobra.Command {
	var by string
	var clearFlag, jsonOut bool
	cmd := &cobra.Command{
		Use:   "supersede OLD --by NEW",
		Short: "Record that NEW replaces OLD (--clear undoes the edge)",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if by == "" {
				return &UsageError{Err: errors.New(spec.noun + " supersede requires --by NEW")}
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			oldRef, _, err := spec.kind.load(ctx, s, args[0])
			if err != nil {
				return err
			}
			_, newEnt, err := spec.kind.load(ctx, s, by)
			if err != nil {
				return err
			}
			var op model.Op = model.AddSupersededBy{ID: newEnt.EntityID()}
			if clearFlag {
				op = model.RemoveSupersededBy{ID: newEnt.EntityID()}
			}
			snapshot, err := s.Append(ctx, oldRef, []model.Op{op})
			if err != nil {
				return err
			}
			return spec.kind.print(cmd, s, snapshot.(T), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&by, "by", "", "the replacement "+spec.noun+" (required)")
	flags.BoolVar(&clearFlag, "clear", false, "remove the supersede edge")
	bindJSON(flags, &jsonOut)
	return cmd
}

// expireVerb builds "expire ID": flag or unflag an entity as out-of-date.
func (spec documentSpec[T]) expireVerb() *cobra.Command {
	var reason string
	var clearFlag, jsonOut bool
	cmd := &cobra.Command{
		Use:   "expire ID",
		Short: "Flag a " + spec.noun + " as out-of-date (agent-asserted), or --clear to remove the flag",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if clearFlag && reason != "" {
				return &UsageError{Err: errors.New(spec.noun + " expire --clear takes no --reason")}
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, _, err := spec.kind.load(ctx, s, args[0])
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
			return spec.kind.print(cmd, s, snapshot.(T), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&reason, "reason", "", "why the "+spec.noun+" is out-of-date")
	flags.BoolVar(&clearFlag, "clear", false, "remove the out-of-date flag")
	bindJSON(flags, &jsonOut)
	return cmd
}

// reviewVerb builds "review": surface the entities needing attention with a
// verdict, optionally narrowed to one verdict class.
func (spec documentSpec[T]) reviewVerb() *cobra.Command {
	var staleAfterFlag string
	var drift, unverified, expired, jsonOut bool
	cmd := &cobra.Command{
		Use:   "review",
		Short: "Surface " + spec.noun + "s needing attention, each with a verdict",
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
			flagged, err := spec.reviewSet(ctx, s, head, now, staleAfter)
			if err != nil {
				return err
			}
			return reviewDocuments(cmd, s, filterVerdicts(flagged, drift, unverified, expired), jsonOut, spec.newDTO, spec.lean)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&staleAfterFlag, "stale-after", "", "staleness threshold (Go duration)")
	flags.BoolVar(&drift, "drift", false, "limit to drifted "+spec.noun+"s")
	flags.BoolVar(&unverified, "unverified", false, "limit to never-verified "+spec.noun+"s")
	flags.BoolVar(&expired, "expired", false, "limit to expired "+spec.noun+"s")
	bindJSON(flags, &jsonOut)
	return cmd
}

var noteDocument = documentSpec[model.Note]{
	kind:    noteSpec,
	noun:    "note",
	adapter: noteAdapter,
	addLong: "Create a note from flags, or as a file: --checkout writes a template —\n" +
		"prefilled from any TITLE and anchor/label flags — to an editable file and\n" +
		"prints its path; fill it in, then --apply <path> to create the note\n" +
		"(--abort <path> discards it). --apply also accepts --attach.",
	editLong: "Edit a note by flags, or as a file: --checkout writes the note to an editable\n" +
		"Markdown file and prints its path; edit that file with your normal tools, then\n" +
		"--apply to commit the change (or --abort to discard).",
	createOp: func(title, body, _ string, tags []string, anchors []model.Anchor) model.Op {
		return model.CreateNote{Nonce: model.NewNonce(), Title: title, Body: body, Tags: tags, Anchors: anchors}
	},
	anchors:   noteRank.anchors,
	reviewSet: reviewNotes,
	newDTO:    func(n model.Note, drift string, atts []attachmentDTO) any { return newNoteDTO(n, drift, atts) },
	lean:      leanNoteLine,
}

var docDocument = documentSpec[model.Doc]{
	kind:         docSpec,
	noun:         "doc",
	hasWhen:      true,
	bodyRequired: true,
	adapter:      docAdapter,
	addLong: "Create a doc from flags, or as a file: --checkout writes a template —\n" +
		"prefilled from any TITLE and anchor/label flags — to an editable file and\n" +
		"prints its path; fill in the body, then --apply <path> to create the doc\n" +
		"(--abort <path> discards it). --apply also accepts --attach, but the buffer\n" +
		"must carry a body, so attach-only docs use flag-mode --attach.",
	editLong: "Edit a doc by flags, or as a file: --checkout writes the doc to an editable\n" +
		"Markdown file and prints its path; edit that file with your normal tools, then\n" +
		"--apply to commit the change (or --abort to discard).",
	createOp: func(title, body, when string, tags []string, anchors []model.Anchor) model.Op {
		return model.CreateDoc{Nonce: model.NewNonce(), Title: title, Body: body, When: when, Tags: tags, Anchors: anchors}
	},
	anchors:   docRank.anchors,
	reviewSet: reviewDocs,
	newDTO:    func(d model.Doc, drift string, atts []attachmentDTO) any { return newDocDTO(d, drift, atts) },
	lean:      leanDocLine,
}

// rankAccessors projects the fields a ranked search reads out of an entity that
// its Meta header does not carry: tags, author, anchors, and the kind's match
// tier (title over tag over the kind's own body/entry text).
type rankAccessors[T model.Snapshot] struct {
	tags    func(T) []string
	author  func(T) string
	anchors func(T) []model.Anchor
	tier    func(T, string) int
}

// rankEntities filters items by tag, author, and anchors, keeps those whose
// tier is non-zero for query, then orders by tier, UpdatedAt descending, and id
// ascending, truncated to limit. query is lowercased before the per-kind tier
// compares against it.
func rankEntities[T model.Snapshot](items []T, query string, tags []string, author, anchorPath, anchorDir, anchorBranch, anchorCommit string, limit int, acc rankAccessors[T]) []T {
	q := strings.ToLower(query)
	type scored struct {
		item T
		tier int
	}
	var ranked []scored
	for _, it := range items {
		if !hasAll(acc.tags(it), tags) ||
			(author != "" && acc.author(it) != author) ||
			(anchorPath != "" && !hasAnchorIn(acc.anchors(it), model.AnchorPath, anchorPath)) ||
			(anchorDir != "" && !hasAnchorIn(acc.anchors(it), model.AnchorDir, anchorDir)) ||
			(anchorBranch != "" && !hasAnchorIn(acc.anchors(it), model.AnchorBranch, anchorBranch)) ||
			(anchorCommit != "" && !hasAnchorIn(acc.anchors(it), model.AnchorCommit, anchorCommit)) {
			continue
		}
		tier := acc.tier(it, q)
		if tier == 0 {
			continue
		}
		ranked = append(ranked, scored{item: it, tier: tier})
	}
	slices.SortFunc(ranked, func(a, b scored) int {
		if c := cmp.Compare(b.tier, a.tier); c != 0 {
			return c
		}
		if c := b.item.Meta().UpdatedAt.Compare(a.item.Meta().UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.item.EntityID(), b.item.EntityID())
	})
	if limit >= 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	out := make([]T, len(ranked))
	for i, r := range ranked {
		out[i] = r.item
	}
	return out
}

// textTier ranks how an entity matches q: a title substring is tier 3, a tag
// substring tier 2, a body/entry substring tier 1, no match tier 0. bodies
// supplies the kind's searchable free text (a note or doc body, a log's entry
// texts). The comparison is case-insensitive; q must already be lowercased.
func textTier(title string, tags []string, bodies []string, q string) int {
	if strings.Contains(strings.ToLower(title), q) {
		return 3
	}
	for _, tag := range tags {
		if strings.Contains(strings.ToLower(tag), q) {
			return 2
		}
	}
	for _, body := range bodies {
		if strings.Contains(strings.ToLower(body), q) {
			return 1
		}
	}
	return 0
}

// listSpec is the vocabulary the list and search verbs share across note, doc,
// and log: the display noun, whether the kind carries a supersede edge (which
// adds --include-superseded), the search help line, the store list source, the
// rank accessors, and the DTO/lean projections used to print a result set.
type listSpec[T model.Snapshot] struct {
	noun         string
	supersedable bool
	searchShort  string
	list         func(ctx context.Context, s *store.Store, all, includeSuperseded bool) ([]T, error)
	rank         rankAccessors[T]
	newDTO       func(T, []attachmentDTO) any
	lean         func(T) string
}

// listVerb builds the "list" command: filter by labels and anchors, order by
// UpdatedAt, and print. --include-superseded is bound only for supersedable
// kinds; a non-supersedable kind always lists with includeSuperseded false.
func (spec listSpec[T]) listVerb() *cobra.Command {
	var labels []string
	var filters anchorFilters
	var all, includeSuperseded, jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List " + spec.noun + "s",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			items, err := spec.list(cmd.Context(), s, all, includeSuperseded)
			if err != nil {
				return err
			}
			items = slices.DeleteFunc(items, func(it T) bool {
				return !hasAll(spec.rank.tags(it), labels) ||
					(filters.commit != "" && !hasAnchorIn(spec.rank.anchors(it), model.AnchorCommit, filters.commit)) ||
					(filters.path != "" && !hasAnchorIn(spec.rank.anchors(it), model.AnchorPath, filters.path)) ||
					(filters.dir != "" && !hasAnchorIn(spec.rank.anchors(it), model.AnchorDir, filters.dir)) ||
					(filters.branch != "" && !hasAnchorIn(spec.rank.anchors(it), model.AnchorBranch, filters.branch))
			})
			sortByUpdated(items)
			return printEntityList(cmd, s, items, jsonOut, spec.newDTO, spec.lean)
		},
	}
	flags := cmd.Flags()
	bindLabels(flags, &labels, "require label (repeatable, ANDed)")
	filters.bind(flags)
	flags.BoolVar(&all, "all", false, "include tombstoned "+spec.noun+"s")
	if spec.supersedable {
		flags.BoolVar(&includeSuperseded, "include-superseded", false, "include superseded "+spec.noun+"s")
	}
	bindJSON(flags, &jsonOut)
	return cmd
}

// searchVerb builds the "search QUERY" command: a ranked, tag/author/anchor
// filtered lookup over the live set.
func (spec listSpec[T]) searchVerb() *cobra.Command {
	var labels []string
	var author string
	var filters anchorFilters
	var limit int
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "search QUERY",
		Short: spec.searchShort,
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			items, err := spec.list(cmd.Context(), s, false, false)
			if err != nil {
				return err
			}
			items = rankEntities(items, args[0], labels, author, filters.path, filters.dir, filters.branch, filters.commit, limit, spec.rank)
			return printEntityList(cmd, s, items, jsonOut, spec.newDTO, spec.lean)
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

// printEntityList writes items as a JSON array of their list DTOs or one lean
// line each.
func printEntityList[T model.Snapshot](cmd *cobra.Command, s *store.Store, items []T, jsonOut bool, newDTO func(T, []attachmentDTO) any, lean func(T) string) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]any, len(items))
		for i, it := range items {
			atts, err := entityAttachments(cmd.Context(), s, it.Meta().Attachments)
			if err != nil {
				return err
			}
			dtos[i] = newDTO(it, atts)
		}
		return printJSON(out, dtos)
	}
	for _, it := range items {
		if _, err := fmt.Fprintln(out, lean(it)); err != nil {
			return err
		}
	}
	return nil
}

var noteList = listSpec[model.Note]{
	noun:         "note",
	supersedable: true,
	searchShort:  "Ranked search across note titles, labels, and bodies",
	list: func(ctx context.Context, s *store.Store, all, includeSuperseded bool) ([]model.Note, error) {
		return s.ListNotes(ctx, all, includeSuperseded)
	},
	rank:   noteRank,
	newDTO: func(n model.Note, atts []attachmentDTO) any { return newNoteDTO(n, "", atts) },
	lean:   leanNoteLine,
}

var docList = listSpec[model.Doc]{
	noun:         "doc",
	supersedable: true,
	searchShort:  "Ranked search across doc titles, labels, and bodies",
	list: func(ctx context.Context, s *store.Store, all, includeSuperseded bool) ([]model.Doc, error) {
		return s.ListDocs(ctx, all, includeSuperseded)
	},
	rank:   docRank,
	newDTO: func(d model.Doc, atts []attachmentDTO) any { return newDocDTO(d, "", atts) },
	lean:   leanDocLine,
}

var logList = listSpec[model.Log]{
	noun:         "log",
	supersedable: false,
	searchShort:  "Ranked search across log titles, labels, and entry text",
	list: func(ctx context.Context, s *store.Store, all, _ bool) ([]model.Log, error) {
		return s.ListLogs(ctx, all)
	},
	rank:   logRank,
	newDTO: func(l model.Log, atts []attachmentDTO) any { return newLogDTO(l, atts) },
	lean:   leanLogLine,
}

var noteRank = rankAccessors[model.Note]{
	tags:    func(n model.Note) []string { return n.Tags },
	author:  func(n model.Note) string { return string(n.Author) },
	anchors: func(n model.Note) []model.Anchor { return n.Anchors },
	tier:    func(n model.Note, q string) int { return textTier(n.Title, n.Tags, []string{n.Body}, q) },
}

var docRank = rankAccessors[model.Doc]{
	tags:    func(d model.Doc) []string { return d.Tags },
	author:  func(d model.Doc) string { return string(d.Author) },
	anchors: func(d model.Doc) []model.Anchor { return d.Anchors },
	tier:    func(d model.Doc, q string) int { return textTier(d.Title, d.Tags, []string{d.Body}, q) },
}

var logRank = rankAccessors[model.Log]{
	tags:    func(l model.Log) []string { return l.Tags },
	author:  func(l model.Log) string { return string(l.Author) },
	anchors: func(l model.Log) []model.Anchor { return l.Anchors },
	tier:    func(l model.Log, q string) int { return textTier(l.Title, l.Tags, logEntryTexts(l), q) },
}

// logEntryTexts is a log's searchable body: the text of each entry, in order.
func logEntryTexts(l model.Log) []string {
	texts := make([]string, len(l.Entries))
	for i, e := range l.Entries {
		texts[i] = e.Text
	}
	return texts
}

// reviewed pairs an entity with its computed review verdict.
type reviewed[T model.Snapshot] struct {
	entity  T
	verdict string
}

// filterVerdicts restricts rs to a single verdict class when --drift,
// --unverified, or --expired is set; with none, every flagged entity passes.
func filterVerdicts[T model.Snapshot](rs []reviewed[T], drift, unverified, expired bool) []reviewed[T] {
	var want string
	switch {
	case drift:
		want = verdictDrifted
	case unverified:
		want = verdictUnverified
	case expired:
		want = verdictExpired
	default:
		return rs
	}
	return slices.DeleteFunc(rs, func(r reviewed[T]) bool { return r.verdict != want })
}

// reviewDocuments writes the review set as DTOs carrying their verdict in
// drift, or as lean lines with the verdict appended after a tab.
func reviewDocuments[T model.Snapshot](cmd *cobra.Command, s *store.Store, rs []reviewed[T], jsonOut bool, newDTO func(T, string, []attachmentDTO) any, lean func(T) string) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		dtos := make([]any, len(rs))
		for i, r := range rs {
			atts, err := entityAttachments(cmd.Context(), s, r.entity.Meta().Attachments)
			if err != nil {
				return err
			}
			dtos[i] = newDTO(r.entity, r.verdict, atts)
		}
		return printJSON(out, dtos)
	}
	for _, r := range rs {
		if _, err := fmt.Fprintf(out, "%s\t%s\n", lean(r.entity), r.verdict); err != nil {
			return err
		}
	}
	return nil
}
