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

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// documentSpec is the presentation vocabulary the note and doc noun groups share
// for the freshness lifecycle. It binds the load/print kind, the display noun,
// the file-mode adapter, the review DTO/lean projections, and the notes.Client
// methods that own the domain logic — the verb methods parse flags, validate,
// call the bound notes method, and render; they hold no store writes of their
// own.
type documentSpec[T model.Snapshot] struct {
	kind         kindSpec[T]
	noun         string
	hasWhen      bool
	bodyRequired bool
	searchShort  string
	addLong      string
	editLong     string
	adapter      func() editAdapter
	newDTO       func(T, string, []attachmentDTO) any
	lean         func(T) string

	resolve   func(ctx context.Context, c *notes.Client, prefix string) (model.EntityID, error)
	create    func(ctx context.Context, c *notes.Client, title, body, when string, tags []string, anchors notes.AnchorSpec, atts []model.Attachment) (T, bool, error)
	edit      func(ctx context.Context, c *notes.Client, id model.EntityID, in documentEdit) (T, error)
	verify    func(ctx context.Context, c *notes.Client, id model.EntityID) (T, error)
	supersede func(ctx context.Context, c *notes.Client, id, by model.EntityID, clearFlag bool) (T, error)
	expire    func(ctx context.Context, c *notes.Client, id model.EntityID, reason string, clearFlag bool) (T, error)
	review    func(ctx context.Context, c *notes.Client, staleAfter time.Duration) ([]reviewed[T], error)
	list      func(ctx context.Context, c *notes.Client, f notes.DocumentFilter) ([]T, error)
	search    func(ctx context.Context, c *notes.Client, query string, f notes.SearchFilter) ([]T, error)
}

// documentEdit is the parsed note or doc edit, projected onto notes.NoteEdit or
// notes.DocEdit by the per-kind binding. A nil title/body/when pointer leaves the
// field untouched; attachments and replaceAttachments split by the --replace flag.
type documentEdit struct {
	title, body, when               *string
	addTags, rmTags                 []string
	addAnchors, rmAnchors           notes.AnchorSpec
	attachments, replaceAttachments []model.Attachment
	rmAttachments                   []string
}

// isEmpty reports whether the edit sets nothing beyond the --attach files (which
// the caller counts separately), matching the pre-migration "at least one flag"
// guard.
func (e documentEdit) isEmpty() bool {
	return e.title == nil && e.body == nil && e.when == nil &&
		len(e.addTags) == 0 && len(e.rmTags) == 0 &&
		anchorSpecEmpty(e.addAnchors) && anchorSpecEmpty(e.rmAnchors) &&
		len(e.rmAttachments) == 0
}

// anchorSpecEmpty reports whether the spec names no anchor.
func anchorSpecEmpty(s notes.AnchorSpec) bool {
	return len(s.Commits) == 0 && len(s.Paths) == 0 && len(s.Dirs) == 0 && len(s.Branches) == 0
}

// addVerb builds "add TITLE": create the entity from flags via notes.Client, or
// as a checked-out file via --checkout/--apply/--abort. The body is read and (for
// body-required kinds) validated before the store opens, so a bad title or empty
// body never runs auto-install. A fresh entity is born verified against HEAD, and
// a dedupe hit re-verifies the reused survivor.
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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			if anchors.commits, err = resolveCommits(ctx, s.Git, anchors.commits); err != nil {
				return err
			}
			atts, err := attachFiles(ctx, cmd, s, attach)
			if err != nil {
				return err
			}
			ent, reused, err := spec.create(ctx, c, args[0], text, when, labels, anchorSetsSpec(anchors), atts)
			if err != nil {
				return err
			}
			if reused {
				warnDuplicate(cmd, spec.noun, ent.EntityID())
			}
			return spec.kind.print(cmd, s, ent, jsonOut)
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

// editVerb builds "edit ID": mutate the entity by flags via notes.Client, or as a
// checked-out file. At least one mutation is required.
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
			var in documentEdit
			if cmd.Flags().Changed("title") {
				if err := validateTitle(title, titleHintBody); err != nil {
					return err
				}
				in.title = &title
			}
			if cmd.Flags().Changed("body") {
				text, err := bodyArg(cmd, body)
				if err != nil {
					return err
				}
				if spec.bodyRequired && text == "" && len(attach) == 0 {
					return errEmptyDocBody(docBodyHintAdd)
				}
				in.body = &text
			}
			if spec.hasWhen && cmd.Flags().Changed("when") {
				in.when = &when
			}
			in.addTags, in.rmTags = labels.add, labels.rm
			in.addAnchors = notes.AnchorSpec{Commits: anchors.addCommits, Paths: anchors.addPaths, Dirs: anchors.addDirs, Branches: anchors.addBranches}
			in.rmAnchors = notes.AnchorSpec{Commits: anchors.rmCommits, Paths: anchors.rmPaths, Dirs: anchors.rmDirs, Branches: anchors.rmBranches}
			in.rmAttachments = rmAttachments
			if in.isEmpty() && len(attach) == 0 {
				return &UsageError{Err: errors.New(spec.noun + " edit requires at least one flag")}
			}
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := spec.resolve(ctx, c, args[0])
			if err != nil {
				return err
			}
			atts, err := attachFiles(ctx, cmd, s, attach)
			if err != nil {
				return err
			}
			if replace {
				in.replaceAttachments = atts
			} else {
				in.attachments = atts
			}
			ent, err := spec.edit(ctx, c, id, in)
			if err != nil {
				return err
			}
			return spec.kind.print(cmd, s, ent, jsonOut)
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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := spec.resolve(ctx, c, args[0])
			if err != nil {
				return err
			}
			ent, err := spec.verify(ctx, c, id)
			if err != nil {
				return err
			}
			return spec.kind.print(cmd, s, ent, jsonOut)
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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := spec.resolve(ctx, c, args[0])
			if err != nil {
				return err
			}
			byID, err := spec.resolve(ctx, c, by)
			if err != nil {
				return err
			}
			ent, err := spec.supersede(ctx, c, id, byID, clearFlag)
			if err != nil {
				return err
			}
			return spec.kind.print(cmd, s, ent, jsonOut)
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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := spec.resolve(ctx, c, args[0])
			if err != nil {
				return err
			}
			ent, err := spec.expire(ctx, c, id, reason, clearFlag)
			if err != nil {
				return err
			}
			return spec.kind.print(cmd, s, ent, jsonOut)
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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			staleAfter, err := resolveNoteStaleAfter(ctx, s.Git, staleAfterFlag)
			if err != nil {
				return err
			}
			flagged, err := spec.review(ctx, c, staleAfter)
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

// listVerb builds "list": filter by labels and anchors, order by UpdatedAt, and
// print. Note and doc are supersedable, so --include-superseded is always bound.
func (spec documentSpec[T]) listVerb() *cobra.Command {
	var labels []string
	var filters anchorFilters
	var all, includeSuperseded, jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List " + spec.noun + "s",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			items, err := spec.list(cmd.Context(), c, notes.DocumentFilter{
				Labels:            labels,
				Anchor:            anchorFiltersToNotes(filters),
				IncludeTombstoned: all,
				IncludeSuperseded: includeSuperseded,
			})
			if err != nil {
				return err
			}
			return printEntityList(cmd, s, items, jsonOut, spec.listDTO, spec.lean)
		},
	}
	flags := cmd.Flags()
	bindLabels(flags, &labels, "require label (repeatable, ANDed)")
	filters.bind(flags)
	flags.BoolVar(&all, "all", false, "include tombstoned "+spec.noun+"s")
	flags.BoolVar(&includeSuperseded, "include-superseded", false, "include superseded "+spec.noun+"s")
	bindJSON(flags, &jsonOut)
	return cmd
}

// searchVerb builds "search QUERY": a ranked, tag/author/anchor filtered lookup
// over the live set.
func (spec documentSpec[T]) searchVerb() *cobra.Command {
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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			items, err := spec.search(cmd.Context(), c, args[0], notes.SearchFilter{
				Labels:  labels,
				Author:  author,
				Anchors: anchorFiltersToNotes(filters),
				Limit:   limit,
			})
			if err != nil {
				return err
			}
			return printEntityList(cmd, s, items, jsonOut, spec.listDTO, spec.lean)
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

// listDTO adapts the spec's review DTO (which carries a drift verdict) to the
// list projection by passing an empty verdict.
func (spec documentSpec[T]) listDTO(v T, atts []attachmentDTO) any {
	return spec.newDTO(v, "", atts)
}

// anchorSetsSpec projects the add-anchor flag set onto a notes.AnchorSpec.
func anchorSetsSpec(a anchorSets) notes.AnchorSpec {
	return notes.AnchorSpec{Commits: a.commits, Paths: a.paths, Dirs: a.dirs, Branches: a.branches}
}

// anchorFiltersToNotes projects the anchor filter flags onto a notes.AnchorFilter.
func anchorFiltersToNotes(f anchorFilters) notes.AnchorFilter {
	return notes.AnchorFilter{Commit: f.commit, Path: f.path, Dir: f.dir, Branch: f.branch}
}

// attachFiles ingests every --attach file into the local LFS store and returns
// the resulting attachments, reusing attachOps for the ingestion, prune-guard
// announcement, and duplicate-name/missing-file guards.
func attachFiles(ctx context.Context, cmd *cobra.Command, s *store.Store, paths []string) ([]model.Attachment, error) {
	ops, err := attachOps(ctx, cmd, s, paths)
	if err != nil {
		return nil, err
	}
	atts := make([]model.Attachment, len(ops))
	for i, op := range ops {
		atts[i] = model.Attachment(op.(model.AddAttachment))
	}
	return atts, nil
}

var noteDocument = documentSpec[model.Note]{
	kind:        noteSpec,
	noun:        "note",
	searchShort: "Ranked search across note titles, labels, and bodies",
	adapter:     noteAdapter,
	addLong: "Create a note from flags, or as a file: --checkout writes a template —\n" +
		"prefilled from any TITLE and anchor/label flags — to an editable file and\n" +
		"prints its path; fill it in, then --apply <path> to create the note\n" +
		"(--abort <path> discards it). --apply also accepts --attach.",
	editLong: "Edit a note by flags, or as a file: --checkout writes the note to an editable\n" +
		"Markdown file and prints its path; edit that file with your normal tools, then\n" +
		"--apply to commit the change (or --abort to discard).",
	newDTO: func(n model.Note, drift string, atts []attachmentDTO) any { return newNoteDTO(n, drift, atts) },
	lean:   leanNoteLine,
	resolve: func(ctx context.Context, c *notes.Client, prefix string) (model.EntityID, error) {
		return c.ResolveNote(ctx, prefix)
	},
	create: func(ctx context.Context, c *notes.Client, title, body, _ string, tags []string, anchors notes.AnchorSpec, atts []model.Attachment) (model.Note, bool, error) {
		return c.CreateNote(ctx, notes.NoteSpec{Title: title, Body: body, Tags: tags, Anchors: anchors, Attachments: atts})
	},
	edit: func(ctx context.Context, c *notes.Client, id model.EntityID, in documentEdit) (model.Note, error) {
		return c.EditNote(ctx, id, notes.NoteEdit{
			Title: in.title, Body: in.body,
			AddTags: in.addTags, RemoveTags: in.rmTags,
			AddAnchors: in.addAnchors, RemoveAnchors: in.rmAnchors,
			Attachments: in.attachments, ReplaceAttachments: in.replaceAttachments, RemoveAttachments: in.rmAttachments,
		})
	},
	verify: func(ctx context.Context, c *notes.Client, id model.EntityID) (model.Note, error) {
		return c.VerifyNote(ctx, id)
	},
	supersede: func(ctx context.Context, c *notes.Client, id, by model.EntityID, clearFlag bool) (model.Note, error) {
		if clearFlag {
			return c.UnsupersedeNote(ctx, id, by)
		}
		return c.SupersedeNote(ctx, id, by)
	},
	expire: func(ctx context.Context, c *notes.Client, id model.EntityID, reason string, clearFlag bool) (model.Note, error) {
		if clearFlag {
			return c.UnexpireNote(ctx, id)
		}
		return c.ExpireNote(ctx, id, reason)
	},
	review: func(ctx context.Context, c *notes.Client, staleAfter time.Duration) ([]reviewed[model.Note], error) {
		rs, err := c.ReviewNotes(ctx, staleAfter)
		if err != nil {
			return nil, err
		}
		out := make([]reviewed[model.Note], len(rs))
		for i, r := range rs {
			out[i] = reviewed[model.Note]{entity: r.Note, verdict: string(r.Verdict)}
		}
		return out, nil
	},
	list: func(ctx context.Context, c *notes.Client, f notes.DocumentFilter) ([]model.Note, error) {
		return c.Notes(ctx, f)
	},
	search: func(ctx context.Context, c *notes.Client, query string, f notes.SearchFilter) ([]model.Note, error) {
		return c.SearchNotes(ctx, query, f)
	},
}

var docDocument = documentSpec[model.Doc]{
	kind:         docSpec,
	noun:         "doc",
	hasWhen:      true,
	bodyRequired: true,
	searchShort:  "Ranked search across doc titles, labels, and bodies",
	adapter:      docAdapter,
	addLong: "Create a doc from flags, or as a file: --checkout writes a template —\n" +
		"prefilled from any TITLE and anchor/label flags — to an editable file and\n" +
		"prints its path; fill in the body, then --apply <path> to create the doc\n" +
		"(--abort <path> discards it). --apply also accepts --attach, but the buffer\n" +
		"must carry a body, so attach-only docs use flag-mode --attach.",
	editLong: "Edit a doc by flags, or as a file: --checkout writes the doc to an editable\n" +
		"Markdown file and prints its path; edit that file with your normal tools, then\n" +
		"--apply to commit the change (or --abort to discard).",
	newDTO: func(d model.Doc, drift string, atts []attachmentDTO) any { return newDocDTO(d, drift, atts) },
	lean:   leanDocLine,
	resolve: func(ctx context.Context, c *notes.Client, prefix string) (model.EntityID, error) {
		return c.ResolveDoc(ctx, prefix)
	},
	create: func(ctx context.Context, c *notes.Client, title, body, when string, tags []string, anchors notes.AnchorSpec, atts []model.Attachment) (model.Doc, bool, error) {
		return c.CreateDoc(ctx, notes.DocSpec{Title: title, Body: body, When: when, Tags: tags, Anchors: anchors, Attachments: atts})
	},
	edit: func(ctx context.Context, c *notes.Client, id model.EntityID, in documentEdit) (model.Doc, error) {
		return c.EditDoc(ctx, id, notes.DocEdit{
			Title: in.title, Body: in.body, When: in.when,
			AddTags: in.addTags, RemoveTags: in.rmTags,
			AddAnchors: in.addAnchors, RemoveAnchors: in.rmAnchors,
			Attachments: in.attachments, ReplaceAttachments: in.replaceAttachments, RemoveAttachments: in.rmAttachments,
		})
	},
	verify: func(ctx context.Context, c *notes.Client, id model.EntityID) (model.Doc, error) {
		return c.VerifyDoc(ctx, id)
	},
	supersede: func(ctx context.Context, c *notes.Client, id, by model.EntityID, clearFlag bool) (model.Doc, error) {
		if clearFlag {
			return c.UnsupersedeDoc(ctx, id, by)
		}
		return c.SupersedeDoc(ctx, id, by)
	},
	expire: func(ctx context.Context, c *notes.Client, id model.EntityID, reason string, clearFlag bool) (model.Doc, error) {
		if clearFlag {
			return c.UnexpireDoc(ctx, id)
		}
		return c.ExpireDoc(ctx, id, reason)
	},
	review: func(ctx context.Context, c *notes.Client, staleAfter time.Duration) ([]reviewed[model.Doc], error) {
		rs, err := c.ReviewDocs(ctx, staleAfter)
		if err != nil {
			return nil, err
		}
		out := make([]reviewed[model.Doc], len(rs))
		for i, r := range rs {
			out[i] = reviewed[model.Doc]{entity: r.Doc, verdict: string(r.Verdict)}
		}
		return out, nil
	},
	list: func(ctx context.Context, c *notes.Client, f notes.DocumentFilter) ([]model.Doc, error) {
		return c.Docs(ctx, f)
	},
	search: func(ctx context.Context, c *notes.Client, query string, f notes.SearchFilter) ([]model.Doc, error) {
		return c.SearchDocs(ctx, query, f)
	},
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

var noteRank = rankAccessors[model.Note]{
	tags:    func(n model.Note) []string { return n.Tags },
	author:  func(n model.Note) string { return string(n.Author) },
	anchors: func(n model.Note) []model.Anchor { return n.Anchors },
	tier:    func(n model.Note, q string) int { return textTier(n.Title, n.Tags, []string{n.Body}, q) },
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
