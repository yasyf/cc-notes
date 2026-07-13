package notes

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// Verdict is the single freshness verdict a note or doc carries in a review. An
// entity carries at most one, by the precedence EXPIRED > UNVERIFIED > DRIFTED >
// STALE; DANGLING is reported separately for a broken supersede edge. The empty
// Verdict means fresh.
type Verdict string

const (
	// VerdictExpired reports an entity flagged out-of-date (agent-asserted).
	VerdictExpired Verdict = "EXPIRED"
	// VerdictUnverified reports an entity never verified against any commit.
	VerdictUnverified Verdict = "UNVERIFIED"
	// VerdictDrifted reports an entity whose witnessed content changed at HEAD.
	VerdictDrifted Verdict = "DRIFTED"
	// VerdictStale reports an entity last verified longer ago than the threshold.
	VerdictStale Verdict = "STALE"
	// VerdictDangling reports a superseded entity whose target has been tombstoned.
	VerdictDangling Verdict = "DANGLING"
)

// NoteSpec is the input to CreateNote. Title is required. Body is the note's
// markdown content. Anchors are attached in commit, path, dir, then branch
// order; commit values are resolved to full shas at write time. Attachments are
// added in the create pack, in slice order.
type NoteSpec struct {
	Title       string
	Body        string
	Tags        []string
	Anchors     AnchorSpec
	Attachments []model.Attachment
}

// DocSpec is the input to CreateDoc. It mirrors NoteSpec plus When, the doc's
// free-text read-this-when trigger.
type DocSpec struct {
	Title       string
	Body        string
	When        string
	Tags        []string
	Anchors     AnchorSpec
	Attachments []model.Attachment
}

// NoteEdit is the field mask for EditNote: a pointer field is the sanctioned
// tri-state (nil leaves it untouched, a non-nil pointer sets it, a pointer to
// the zero value clears it); slice fields are applied in order. AddAnchors'
// commit values are resolved to full shas; RemoveAnchors is matched verbatim.
// Attachments must not collide with a live attachment; ReplaceAttachments may
// overwrite one; RemoveAttachments drops by name. An all-empty mask is
// ErrEmptyEdit.
type NoteEdit struct {
	Title              *string
	Body               *string
	AddTags            []string
	RemoveTags         []string
	AddAnchors         AnchorSpec
	RemoveAnchors      AnchorSpec
	Attachments        []model.Attachment
	ReplaceAttachments []model.Attachment
	RemoveAttachments  []string
}

// DocEdit is the field mask for EditDoc. It mirrors NoteEdit plus When, the
// doc's read-this-when trigger.
type DocEdit struct {
	Title              *string
	Body               *string
	When               *string
	AddTags            []string
	RemoveTags         []string
	AddAnchors         AnchorSpec
	RemoveAnchors      AnchorSpec
	Attachments        []model.Attachment
	ReplaceAttachments []model.Attachment
	RemoveAttachments  []string
}

// empty reports whether the mask sets nothing.
func (e NoteEdit) empty() bool {
	return e.Title == nil && e.Body == nil &&
		len(e.AddTags) == 0 && len(e.RemoveTags) == 0 &&
		e.AddAnchors.isEmpty() && e.RemoveAnchors.isEmpty() &&
		len(e.Attachments) == 0 && len(e.ReplaceAttachments) == 0 && len(e.RemoveAttachments) == 0
}

// empty reports whether the mask sets nothing.
func (e DocEdit) empty() bool {
	return e.Title == nil && e.Body == nil && e.When == nil &&
		len(e.AddTags) == 0 && len(e.RemoveTags) == 0 &&
		e.AddAnchors.isEmpty() && e.RemoveAnchors.isEmpty() &&
		len(e.Attachments) == 0 && len(e.ReplaceAttachments) == 0 && len(e.RemoveAttachments) == 0
}

// isEmpty reports whether the spec names no anchor.
func (s AnchorSpec) isEmpty() bool {
	return len(s.Commits) == 0 && len(s.Paths) == 0 && len(s.Dirs) == 0 && len(s.Branches) == 0
}

// NoteReview pairs a flagged note with its review verdict.
type NoteReview struct {
	Note    model.Note
	Verdict Verdict
}

// DocReview pairs a flagged doc with its review verdict.
type DocReview struct {
	Doc     model.Doc
	Verdict Verdict
}

// DocumentFilter narrows a note or doc listing. The zero value matches every
// live, non-superseded entity. Labels are ANDed; Anchor constrains to entries
// carrying the given anchor. IncludeTombstoned and IncludeSuperseded widen the
// set to deleted and superseded entities.
type DocumentFilter struct {
	Labels            []string
	Anchor            AnchorFilter
	IncludeTombstoned bool
	IncludeSuperseded bool
}

// SearchFilter narrows a ranked note or doc search. Labels are ANDed; Author,
// when set, requires an exact match; Anchors constrains by anchor. Limit caps
// the result count; a negative Limit imposes no cap.
type SearchFilter struct {
	Labels  []string
	Author  string
	Anchors AnchorFilter
	Limit   int
}

// CreateNote roots a note from spec, born verified against current HEAD. The
// create pack carries the note op followed by one attachment op per
// spec.Attachment, then a separate VerifyNote append refreshes its witness — the
// verify never folds into the create pack, which would change the entity id.
// The returned bool reports that Create's best-effort duplicate guard converged
// on an existing note; the survivor is re-verified all the same.
func (c *Client) CreateNote(ctx context.Context, spec NoteSpec) (model.Note, bool, error) {
	commits, err := c.resolveCommits(ctx, spec.Anchors.Commits)
	if err != nil {
		return model.Note{}, false, err
	}
	anchors := buildAnchors(AnchorSpec{Commits: commits, Paths: spec.Anchors.Paths, Dirs: spec.Anchors.Dirs, Branches: spec.Anchors.Branches})
	ops := make([]model.Op, 0, 1+len(spec.Attachments))
	ops = append(ops, model.CreateNote{Nonce: model.NewNonce(), Title: spec.Title, Body: spec.Body, Tags: spec.Tags, Anchors: anchors})
	ops = append(ops, attachmentAddOps(spec.Attachments)...)
	snap, reused, err := c.createDocument(ctx, ops)
	if err != nil {
		return model.Note{}, false, err
	}
	return snap.(model.Note), reused, nil
}

// CreateDoc roots a doc from spec, born verified against current HEAD, mirroring
// CreateNote (the create pack carries the doc op then the attachment ops, and a
// separate VerifyNote append refreshes the witness).
func (c *Client) CreateDoc(ctx context.Context, spec DocSpec) (model.Doc, bool, error) {
	commits, err := c.resolveCommits(ctx, spec.Anchors.Commits)
	if err != nil {
		return model.Doc{}, false, err
	}
	anchors := buildAnchors(AnchorSpec{Commits: commits, Paths: spec.Anchors.Paths, Dirs: spec.Anchors.Dirs, Branches: spec.Anchors.Branches})
	ops := make([]model.Op, 0, 1+len(spec.Attachments))
	ops = append(ops, model.CreateDoc{Nonce: model.NewNonce(), Title: spec.Title, Body: spec.Body, When: spec.When, Tags: spec.Tags, Anchors: anchors})
	ops = append(ops, attachmentAddOps(spec.Attachments)...)
	snap, reused, err := c.createDocument(ctx, ops)
	if err != nil {
		return model.Doc{}, false, err
	}
	return snap.(model.Doc), reused, nil
}

// createDocument roots the entity the ops describe, resolving a duplicate hit to
// the surviving snapshot (reused), then re-verifies that snapshot against HEAD.
// An add re-asserts the fact now, so a dedupe hit re-verifies the reused entity
// rather than skipping it: VerifyNote refreshes the survivor's witness,
// verified_at/by, and verified_commit and clears any stale flag, exactly as a
// fresh add is born verified. The dedupe scan excludes stale twins, so this
// survivor is live.
func (c *Client) createDocument(ctx context.Context, ops []model.Op) (model.Snapshot, bool, error) {
	snap, err := c.s.Create(ctx, ops)
	reused := false
	var dup *store.DuplicateError
	if errors.As(err, &dup) {
		snap, reused = dup.Existing, true
	} else if err != nil {
		return nil, false, err
	}
	verified, err := c.reVerify(ctx, snap)
	if err != nil {
		return nil, false, err
	}
	return verified, reused, nil
}

// reVerify witnesses snap's anchors against current HEAD and appends a VerifyNote
// op, returning the folded snapshot.
func (c *Client) reVerify(ctx context.Context, snap model.Snapshot) (model.Snapshot, error) {
	kind, anchors := documentAnchors(snap)
	head, err := c.head(ctx)
	if err != nil {
		return nil, err
	}
	witness, err := c.buildWitness(ctx, head, anchors)
	if err != nil {
		return nil, err
	}
	return c.s.Append(ctx, refs.For(kind, snap.EntityID()), []model.Op{model.VerifyNote{Witness: witness, VerifiedCommit: head}})
}

// documentAnchors reads a note or doc snapshot's kind and anchors.
func documentAnchors(snap model.Snapshot) (model.Kind, []model.Anchor) {
	switch e := snap.(type) {
	case model.Note:
		return model.KindNote, e.Anchors
	case model.Doc:
		return model.KindDoc, e.Anchors
	default:
		panic("notes: reVerify on a non-document snapshot")
	}
}

// EditNote applies the mask to the note. An all-empty mask is ErrEmptyEdit; an
// Attachments entry colliding with a live attachment is an *AttachmentExistsError
// — both refuse before any write. AddAnchors' commits are resolved first, so a
// bad revision mutates nothing.
func (c *Client) EditNote(ctx context.Context, id model.EntityID, edit NoteEdit) (model.Note, error) {
	if edit.empty() {
		return model.Note{}, ErrEmptyEdit
	}
	addAnchors, err := c.resolveAnchors(ctx, edit.AddAnchors)
	if err != nil {
		return model.Note{}, err
	}
	note, err := c.Note(ctx, id)
	if err != nil {
		return model.Note{}, err
	}
	if err := checkAttachmentCollisions(note.Attachments, edit.Attachments); err != nil {
		return model.Note{}, err
	}
	var ops []model.Op
	if edit.Title != nil {
		ops = append(ops, model.SetTitle{Title: *edit.Title})
	}
	if edit.Body != nil {
		ops = append(ops, model.SetBody{Body: *edit.Body})
	}
	ops = appendEditOps(ops, edit.AddTags, edit.RemoveTags, addAnchors, edit.RemoveAnchors, edit.RemoveAttachments, edit.Attachments, edit.ReplaceAttachments)
	snap, err := c.s.Append(ctx, refs.For(model.KindNote, id), ops)
	if err != nil {
		return model.Note{}, err
	}
	return snap.(model.Note), nil
}

// EditDoc applies the mask to the doc, mirroring EditNote plus the When trigger.
func (c *Client) EditDoc(ctx context.Context, id model.EntityID, edit DocEdit) (model.Doc, error) {
	if edit.empty() {
		return model.Doc{}, ErrEmptyEdit
	}
	addAnchors, err := c.resolveAnchors(ctx, edit.AddAnchors)
	if err != nil {
		return model.Doc{}, err
	}
	doc, err := c.Doc(ctx, id)
	if err != nil {
		return model.Doc{}, err
	}
	if err := checkAttachmentCollisions(doc.Attachments, edit.Attachments); err != nil {
		return model.Doc{}, err
	}
	var ops []model.Op
	if edit.Title != nil {
		ops = append(ops, model.SetTitle{Title: *edit.Title})
	}
	if edit.Body != nil {
		ops = append(ops, model.SetBody{Body: *edit.Body})
	}
	if edit.When != nil {
		ops = append(ops, model.SetWhen{When: *edit.When})
	}
	ops = appendEditOps(ops, edit.AddTags, edit.RemoveTags, addAnchors, edit.RemoveAnchors, edit.RemoveAttachments, edit.Attachments, edit.ReplaceAttachments)
	snap, err := c.s.Append(ctx, refs.For(model.KindDoc, id), ops)
	if err != nil {
		return model.Doc{}, err
	}
	return snap.(model.Doc), nil
}

// appendEditOps appends the tag, anchor, and attachment ops an edit shares, in
// the fixed order add-tag, remove-tag, add-anchor, remove-anchor,
// remove-attachment, then the attachment adds (collision-checked, then replace).
func appendEditOps(ops []model.Op, addTags, removeTags []string, addAnchors []model.Anchor, removeAnchors AnchorSpec, removeAttachments []string, attachments, replaceAttachments []model.Attachment) []model.Op {
	for _, t := range addTags {
		ops = append(ops, model.AddTag{Tag: t})
	}
	for _, t := range removeTags {
		ops = append(ops, model.RemoveTag{Tag: t})
	}
	for _, a := range addAnchors {
		ops = append(ops, model.AddAnchor{Anchor: a})
	}
	for _, a := range buildAnchors(removeAnchors) {
		ops = append(ops, model.RemoveAnchor{Anchor: a})
	}
	for _, name := range removeAttachments {
		ops = append(ops, model.RemoveAttachment{Name: name})
	}
	ops = append(ops, attachmentAddOps(attachments)...)
	ops = append(ops, attachmentAddOps(replaceAttachments)...)
	return ops
}

// resolveAnchors resolves a spec's commit revisions to full shas and flattens it
// into anchors.
func (c *Client) resolveAnchors(ctx context.Context, spec AnchorSpec) ([]model.Anchor, error) {
	commits, err := c.resolveCommits(ctx, spec.Commits)
	if err != nil {
		return nil, err
	}
	return buildAnchors(AnchorSpec{Commits: commits, Paths: spec.Paths, Dirs: spec.Dirs, Branches: spec.Branches}), nil
}

// attachmentAddOps builds one AddAttachment op per attachment, in slice order.
func attachmentAddOps(atts []model.Attachment) []model.Op {
	ops := make([]model.Op, 0, len(atts))
	for _, a := range atts {
		ops = append(ops, model.AddAttachment(a))
	}
	return ops
}

// checkAttachmentCollisions rejects an add whose name collides with a live
// attachment: replacing content silently would orphan the old bytes behind the
// same name, so the caller must route it through ReplaceAttachments instead.
func checkAttachmentCollisions(live, adds []model.Attachment) error {
	names := make(map[string]bool, len(live))
	for _, a := range live {
		names[a.Name] = true
	}
	for _, a := range adds {
		if names[a.Name] {
			return &AttachmentExistsError{Name: a.Name}
		}
	}
	return nil
}

// RemoveNote tombstones the note, returning the folded snapshot.
func (c *Client) RemoveNote(ctx context.Context, id model.EntityID) (model.Note, error) {
	snap, err := c.s.Append(ctx, refs.For(model.KindNote, id), []model.Op{model.DeleteNote{}})
	if err != nil {
		return model.Note{}, err
	}
	return snap.(model.Note), nil
}

// RemoveDoc tombstones the doc, returning the folded snapshot.
func (c *Client) RemoveDoc(ctx context.Context, id model.EntityID) (model.Doc, error) {
	snap, err := c.s.Append(ctx, refs.For(model.KindDoc, id), []model.Op{model.DeleteNote{}})
	if err != nil {
		return model.Doc{}, err
	}
	return snap.(model.Doc), nil
}

// VerifyNote re-witnesses the note against current HEAD, refreshing its
// verified_at/by and verified_commit and clearing any stale flag.
func (c *Client) VerifyNote(ctx context.Context, id model.EntityID) (model.Note, error) {
	note, err := c.Note(ctx, id)
	if err != nil {
		return model.Note{}, err
	}
	snap, err := c.reVerify(ctx, note)
	if err != nil {
		return model.Note{}, err
	}
	return snap.(model.Note), nil
}

// VerifyDoc re-witnesses the doc against current HEAD, mirroring VerifyNote.
func (c *Client) VerifyDoc(ctx context.Context, id model.EntityID) (model.Doc, error) {
	doc, err := c.Doc(ctx, id)
	if err != nil {
		return model.Doc{}, err
	}
	snap, err := c.reVerify(ctx, doc)
	if err != nil {
		return model.Doc{}, err
	}
	return snap.(model.Doc), nil
}

// SupersedeNote records that the note by replaces id. by must resolve to a live
// note; it is loaded to validate before the edge is written.
func (c *Client) SupersedeNote(ctx context.Context, id, by model.EntityID) (model.Note, error) {
	if _, err := c.Note(ctx, by); err != nil {
		return model.Note{}, err
	}
	snap, err := c.s.Append(ctx, refs.For(model.KindNote, id), []model.Op{model.AddSupersededBy{ID: by}})
	if err != nil {
		return model.Note{}, err
	}
	return snap.(model.Note), nil
}

// UnsupersedeNote clears the edge recording that by replaces id.
func (c *Client) UnsupersedeNote(ctx context.Context, id, by model.EntityID) (model.Note, error) {
	if _, err := c.Note(ctx, by); err != nil {
		return model.Note{}, err
	}
	snap, err := c.s.Append(ctx, refs.For(model.KindNote, id), []model.Op{model.RemoveSupersededBy{ID: by}})
	if err != nil {
		return model.Note{}, err
	}
	return snap.(model.Note), nil
}

// SupersedeDoc records that the doc by replaces id, mirroring SupersedeNote.
func (c *Client) SupersedeDoc(ctx context.Context, id, by model.EntityID) (model.Doc, error) {
	if _, err := c.Doc(ctx, by); err != nil {
		return model.Doc{}, err
	}
	snap, err := c.s.Append(ctx, refs.For(model.KindDoc, id), []model.Op{model.AddSupersededBy{ID: by}})
	if err != nil {
		return model.Doc{}, err
	}
	return snap.(model.Doc), nil
}

// UnsupersedeDoc clears the edge recording that by replaces id.
func (c *Client) UnsupersedeDoc(ctx context.Context, id, by model.EntityID) (model.Doc, error) {
	if _, err := c.Doc(ctx, by); err != nil {
		return model.Doc{}, err
	}
	snap, err := c.s.Append(ctx, refs.For(model.KindDoc, id), []model.Op{model.RemoveSupersededBy{ID: by}})
	if err != nil {
		return model.Doc{}, err
	}
	return snap.(model.Doc), nil
}

// ExpireNote flags the note out-of-date with an optional reason.
func (c *Client) ExpireNote(ctx context.Context, id model.EntityID, reason string) (model.Note, error) {
	snap, err := c.s.Append(ctx, refs.For(model.KindNote, id), []model.Op{model.MarkStale{Reason: reason}})
	if err != nil {
		return model.Note{}, err
	}
	return snap.(model.Note), nil
}

// UnexpireNote clears the note's out-of-date flag.
func (c *Client) UnexpireNote(ctx context.Context, id model.EntityID) (model.Note, error) {
	snap, err := c.s.Append(ctx, refs.For(model.KindNote, id), []model.Op{model.ClearStale{}})
	if err != nil {
		return model.Note{}, err
	}
	return snap.(model.Note), nil
}

// ExpireDoc flags the doc out-of-date with an optional reason.
func (c *Client) ExpireDoc(ctx context.Context, id model.EntityID, reason string) (model.Doc, error) {
	snap, err := c.s.Append(ctx, refs.For(model.KindDoc, id), []model.Op{model.MarkStale{Reason: reason}})
	if err != nil {
		return model.Doc{}, err
	}
	return snap.(model.Doc), nil
}

// UnexpireDoc clears the doc's out-of-date flag.
func (c *Client) UnexpireDoc(ctx context.Context, id model.EntityID) (model.Doc, error) {
	snap, err := c.s.Append(ctx, refs.For(model.KindDoc, id), []model.Op{model.ClearStale{}})
	if err != nil {
		return model.Doc{}, err
	}
	return snap.(model.Doc), nil
}

// Notes folds the note set the filter selects and returns it in UpdatedAt-desc,
// id-asc order.
func (c *Client) Notes(ctx context.Context, f DocumentFilter) ([]model.Note, error) {
	notes, err := c.s.ListNotes(ctx, f.IncludeTombstoned, f.IncludeSuperseded)
	if err != nil {
		return nil, err
	}
	notes = slices.DeleteFunc(notes, func(n model.Note) bool {
		return !matchesFilter(n.Tags, n.Anchors, f)
	})
	sortDocuments(notes)
	return notes, nil
}

// Docs folds the doc set the filter selects and returns it in UpdatedAt-desc,
// id-asc order.
func (c *Client) Docs(ctx context.Context, f DocumentFilter) ([]model.Doc, error) {
	docs, err := c.s.ListDocs(ctx, f.IncludeTombstoned, f.IncludeSuperseded)
	if err != nil {
		return nil, err
	}
	docs = slices.DeleteFunc(docs, func(d model.Doc) bool {
		return !matchesFilter(d.Tags, d.Anchors, f)
	})
	sortDocuments(docs)
	return docs, nil
}

// matchesFilter reports whether an entity's tags and anchors satisfy the filter.
func matchesFilter(tags []string, anchors []model.Anchor, f DocumentFilter) bool {
	return hasAll(tags, f.Labels) && matchesAnchorFilter(anchors, f.Anchor)
}

// matchesAnchorFilter reports whether anchors carry every anchor the filter
// names; an empty field imposes no constraint.
func matchesAnchorFilter(anchors []model.Anchor, f AnchorFilter) bool {
	return (f.Commit == "" || hasAnchor(anchors, model.AnchorCommit, f.Commit)) &&
		(f.Path == "" || hasAnchor(anchors, model.AnchorPath, f.Path)) &&
		(f.Dir == "" || hasAnchor(anchors, model.AnchorDir, f.Dir)) &&
		(f.Branch == "" || hasAnchor(anchors, model.AnchorBranch, f.Branch))
}

// hasAnchor reports whether anchors contains one of the given kind and value.
func hasAnchor(anchors []model.Anchor, kind model.AnchorKind, value string) bool {
	return slices.Contains(anchors, model.Anchor{Kind: kind, Value: value})
}

// sortDocuments orders any note or doc slice by UpdatedAt descending, then id
// ascending, reading both through the kind-agnostic Meta header.
func sortDocuments[T model.Snapshot](items []T) {
	slices.SortFunc(items, func(a, b T) int {
		if c := b.Meta().UpdatedAt.Compare(a.Meta().UpdatedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.EntityID(), b.EntityID())
	})
}

// SearchNotes ranks the live note set against query, filtered by the SearchFilter,
// and returns the top results by tier, then UpdatedAt descending, then id
// ascending. A note matches when its title, a tag, or its body contains query.
func (c *Client) SearchNotes(ctx context.Context, query string, f SearchFilter) ([]model.Note, error) {
	notes, err := c.s.ListNotes(ctx, false, false)
	if err != nil {
		return nil, err
	}
	return rankDocuments(notes, query, f, noteRanker), nil
}

// SearchDocs ranks the live doc set against query, mirroring SearchNotes.
func (c *Client) SearchDocs(ctx context.Context, query string, f SearchFilter) ([]model.Doc, error) {
	docs, err := c.s.ListDocs(ctx, false, false)
	if err != nil {
		return nil, err
	}
	return rankDocuments(docs, query, f, docRanker), nil
}

// documentRanker projects the fields a ranked search reads out of a note or doc
// that its Meta header does not carry: tags, author, anchors, and the search
// tier (title over tag over body).
type documentRanker[T model.Snapshot] struct {
	tags    func(T) []string
	author  func(T) string
	anchors func(T) []model.Anchor
	tier    func(T, string) int
}

var noteRanker = documentRanker[model.Note]{
	tags:    func(n model.Note) []string { return n.Tags },
	author:  func(n model.Note) string { return string(n.Author) },
	anchors: func(n model.Note) []model.Anchor { return n.Anchors },
	tier:    func(n model.Note, q string) int { return textTier(n.Title, n.Tags, []string{n.Body}, q) },
}

var docRanker = documentRanker[model.Doc]{
	tags:    func(d model.Doc) []string { return d.Tags },
	author:  func(d model.Doc) string { return string(d.Author) },
	anchors: func(d model.Doc) []model.Anchor { return d.Anchors },
	tier:    func(d model.Doc, q string) int { return textTier(d.Title, d.Tags, []string{d.Body}, q) },
}

// rankDocuments filters items by label, author, and anchor, keeps those whose
// tier is non-zero for query, then orders by tier, UpdatedAt descending, and id
// ascending, truncated to the filter's limit (a negative limit imposes no cap).
func rankDocuments[T model.Snapshot](items []T, query string, f SearchFilter, r documentRanker[T]) []T {
	q := strings.ToLower(query)
	type scored struct {
		item T
		tier int
	}
	var ranked []scored
	for _, it := range items {
		if !hasAll(r.tags(it), f.Labels) ||
			(f.Author != "" && r.author(it) != f.Author) ||
			!matchesAnchorFilter(r.anchors(it), f.Anchors) {
			continue
		}
		tier := r.tier(it, q)
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
	if f.Limit >= 0 && len(ranked) > f.Limit {
		ranked = ranked[:f.Limit]
	}
	out := make([]T, len(ranked))
	for i, r := range ranked {
		out[i] = r.item
	}
	return out
}

// textTier ranks how an entity matches q: a title substring is tier 3, a tag
// substring tier 2, a body substring tier 1, no match tier 0. The comparison is
// case-insensitive; q must already be lowercased.
func textTier(title string, tags, bodies []string, q string) int {
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

// ReviewNotes folds the review set (non-deleted, including superseded for
// dangling detection) and returns each flagged note with its verdict. A
// non-superseded note carries its content verdict (UNVERIFIED/DRIFTED/STALE/
// EXPIRED); a superseded note is surfaced only when its edge dangles. Fresh
// notes are dropped. Order follows the note list: creation time then id.
func (c *Client) ReviewNotes(ctx context.Context, staleAfter time.Duration) ([]NoteReview, error) {
	all, err := c.s.ListNotes(ctx, false, true)
	if err != nil {
		return nil, err
	}
	head, err := c.head(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	exists := existsSet(all, func(n model.Note) model.EntityID { return n.ID })
	var out []NoteReview
	for _, n := range all {
		if len(n.SupersededBy) > 0 {
			if supersedeDangling(n.SupersededBy, exists) {
				out = append(out, NoteReview{Note: n, Verdict: VerdictDangling})
			}
			continue
		}
		verdict, err := c.verdictOf(ctx, head, freshFromNote(n), now, staleAfter, false)
		if err != nil {
			return nil, err
		}
		if verdict != "" {
			out = append(out, NoteReview{Note: n, Verdict: verdict})
		}
	}
	return out, nil
}

// ReviewDocs folds the doc review set and returns each flagged doc with its
// verdict, mirroring ReviewNotes.
func (c *Client) ReviewDocs(ctx context.Context, staleAfter time.Duration) ([]DocReview, error) {
	all, err := c.s.ListDocs(ctx, false, true)
	if err != nil {
		return nil, err
	}
	head, err := c.head(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	exists := existsSet(all, func(d model.Doc) model.EntityID { return d.ID })
	var out []DocReview
	for _, d := range all {
		if len(d.SupersededBy) > 0 {
			if supersedeDangling(d.SupersededBy, exists) {
				out = append(out, DocReview{Doc: d, Verdict: VerdictDangling})
			}
			continue
		}
		verdict, err := c.verdictOf(ctx, head, freshFromDoc(d), now, staleAfter, false)
		if err != nil {
			return nil, err
		}
		if verdict != "" {
			out = append(out, DocReview{Doc: d, Verdict: verdict})
		}
	}
	return out, nil
}

// NoteVerdict computes n's single review verdict against live content, resolving
// HEAD and the clock itself. When worktree is true, a path anchor drift-checks
// against the on-disk working-tree file rather than the committed blob.
func (c *Client) NoteVerdict(ctx context.Context, n model.Note, staleAfter time.Duration, worktree bool) (Verdict, error) {
	head, err := c.head(ctx)
	if err != nil {
		return "", err
	}
	return c.verdictOf(ctx, head, freshFromNote(n), time.Now(), staleAfter, worktree)
}

// DocVerdict computes d's single review verdict against live content, mirroring
// NoteVerdict.
func (c *Client) DocVerdict(ctx context.Context, d model.Doc, staleAfter time.Duration, worktree bool) (Verdict, error) {
	head, err := c.head(ctx)
	if err != nil {
		return "", err
	}
	return c.verdictOf(ctx, head, freshFromDoc(d), time.Now(), staleAfter, worktree)
}

// NoteSuperseders returns the ids of notes that supersede id, sorted: the
// reverse of the supersede edge, computed at read.
func (c *Client) NoteSuperseders(ctx context.Context, id model.EntityID) ([]model.EntityID, error) {
	all, err := c.s.ListNotes(ctx, false, true)
	if err != nil {
		return nil, err
	}
	return superseders(all, func(n model.Note) (model.EntityID, []model.EntityID) { return n.ID, n.SupersededBy }, id), nil
}

// DocSuperseders returns the ids of docs that supersede id, sorted.
func (c *Client) DocSuperseders(ctx context.Context, id model.EntityID) ([]model.EntityID, error) {
	all, err := c.s.ListDocs(ctx, false, true)
	if err != nil {
		return nil, err
	}
	return superseders(all, func(d model.Doc) (model.EntityID, []model.EntityID) { return d.ID, d.SupersededBy }, id), nil
}

// superseders collects the ids of entities whose supersede edge points at id,
// sorted.
func superseders[T any](all []T, edge func(T) (model.EntityID, []model.EntityID), id model.EntityID) []model.EntityID {
	var out []model.EntityID
	for _, e := range all {
		self, targets := edge(e)
		if slices.Contains(targets, id) {
			out = append(out, self)
		}
	}
	slices.Sort(out)
	return out
}

// existsSet indexes each entity's id for O(1) live-membership checks.
func existsSet[T any](all []T, id func(T) model.EntityID) map[model.EntityID]bool {
	m := make(map[model.EntityID]bool, len(all))
	for _, e := range all {
		m[id(e)] = true
	}
	return m
}

// freshDocument carries the freshness-relevant fields a note and a doc share, so
// one verdict/drift implementation serves both kinds.
type freshDocument struct {
	Anchors      []model.Anchor
	Witness      []model.AnchorWitness
	VerifiedAt   int64
	StaleAt      int64
	SupersededBy []model.EntityID
}

// freshFromNote projects a note onto its freshness fields.
func freshFromNote(n model.Note) freshDocument {
	return freshDocument{Anchors: n.Anchors, Witness: n.Witness, VerifiedAt: n.VerifiedAt, StaleAt: n.StaleAt, SupersededBy: n.SupersededBy}
}

// freshFromDoc projects a doc onto its freshness fields.
func freshFromDoc(d model.Doc) freshDocument {
	return freshDocument{Anchors: d.Anchors, Witness: d.Witness, VerifiedAt: d.VerifiedAt, StaleAt: d.StaleAt, SupersededBy: d.SupersededBy}
}

// verdictOf computes the single review verdict for fe against live content at
// head, returning "" when fresh. Precedence is EXPIRED > UNVERIFIED > DRIFTED >
// STALE; dangling supersede edges are surfaced separately. An unborn HEAD skips
// drift detection unless worktree is set.
func (c *Client) verdictOf(ctx context.Context, head model.SHA, fe freshDocument, now time.Time, staleAfter time.Duration, worktree bool) (Verdict, error) {
	if fe.StaleAt != 0 {
		return VerdictExpired, nil
	}
	if fe.VerifiedAt == 0 {
		return VerdictUnverified, nil
	}
	if head != "" || worktree {
		drifted, err := c.driftedOf(ctx, head, fe, worktree)
		if err != nil {
			return "", err
		}
		if drifted {
			return VerdictDrifted, nil
		}
	}
	if now.Sub(time.Unix(fe.VerifiedAt, 0)) > staleAfter {
		return VerdictStale, nil
	}
	return "", nil
}

// driftedOf reports whether any witnessed anchor no longer matches live content
// at head: a path or directory whose content oid changed or vanished, or a
// commit no longer reachable from head. Anchors without a recorded witness are
// not drift-checked. When worktree is true, a path anchor's live oid is the
// on-disk working-tree blob, so an uncommitted edit drifts the entity.
func (c *Client) driftedOf(ctx context.Context, head model.SHA, fe freshDocument, worktree bool) (bool, error) {
	byAnchor := make(map[model.Anchor]model.AnchorWitness, len(fe.Witness))
	for _, w := range fe.Witness {
		byAnchor[w.Anchor] = w
	}
	for _, a := range fe.Anchors {
		w, ok := byAnchor[a]
		if !ok {
			continue
		}
		switch a.Kind {
		case model.AnchorPath, model.AnchorDir:
			oid, err := c.liveAnchorOID(ctx, head, a, worktree)
			if errors.Is(err, gitcmd.ErrPathNotFound) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			if model.SHA(oid) != w.OID {
				return true, nil
			}
		case model.AnchorCommit:
			reachable, err := c.s.Repo.IsAncestor(ctx, model.SHA(a.Value), head)
			if errors.Is(err, gitobj.ErrCommitNotFound) {
				return true, nil
			}
			if err != nil {
				return false, err
			}
			if !reachable {
				return true, nil
			}
		case model.AnchorBranch:
		}
	}
	return false, nil
}

// liveAnchorOID resolves the current content oid of a path or directory anchor.
// A path anchor under worktree mode reads the on-disk working-tree blob;
// otherwise, and always for a directory anchor, it reads the committed object at
// head.
func (c *Client) liveAnchorOID(ctx context.Context, head model.SHA, a model.Anchor, worktree bool) (string, error) {
	if worktree && a.Kind == model.AnchorPath {
		return c.s.Git.WorktreeBlobOID(ctx, a.Value)
	}
	return c.s.Git.PathOID(ctx, string(head), a.Value)
}

// supersedeDangling reports whether any of the supersede targets has been
// tombstoned — absent from the live (non-deleted) set.
func supersedeDangling(targets []model.EntityID, exists map[model.EntityID]bool) bool {
	for _, target := range targets {
		if !exists[target] {
			return true
		}
	}
	return false
}
