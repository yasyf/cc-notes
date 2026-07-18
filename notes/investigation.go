package notes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// InvestigationSpec is the input to CreateInvestigation. Title and Premise are
// required; Premise is immutable once set, so it has no edit path. Anchors are
// attached in commit, path, dir, then branch order; commit values are resolved
// to full shas at write time. Attachments are added in the create pack, in slice
// order. Findings are the initial suspect texts, each rooted as an open finding
// in the create pack itself, in slice order. A finding-carrying create is never
// dedupe-eligible (dedupeCovered excludes AddFinding), so identical opens that
// carry findings each root a distinct record.
type InvestigationSpec struct {
	Title       string
	Premise     string
	Tags        []string
	Anchors     AnchorSpec
	Findings    []string
	Attachments []model.Attachment
}

// InvestigationEdit is the field mask for EditInvestigation. Premise is
// deliberately absent — it is immutable by construction, so the wrong initial
// suspicion can never be destroyed. A nil Title or Body leaves the field
// untouched, a non-nil pointer sets it; the tag and anchor slices apply in order
// (RemoveAnchors is matched verbatim). An all-empty mask is ErrEmptyEdit.
type InvestigationEdit struct {
	Title         *string
	Body          *string
	AddTags       []string
	RemoveTags    []string
	AddAnchors    AnchorSpec
	RemoveAnchors AnchorSpec
}

// empty reports whether the mask sets nothing.
func (e InvestigationEdit) empty() bool {
	return e.Title == nil && e.Body == nil &&
		len(e.AddTags) == 0 && len(e.RemoveTags) == 0 &&
		e.AddAnchors.isEmpty() && e.RemoveAnchors.isEmpty()
}

// InvestigationAppend is the input to AppendInvestigation: an entry text with an
// optional model identity, plus attachments. Text may be empty only alongside at
// least one attachment; an empty append with no attachments records nothing and
// is ErrEmptyEdit. An attachment whose name collides with a live one is an
// *AttachmentExistsError unless ReplaceAttachments overwrites it.
type InvestigationAppend struct {
	Text               string
	Model              string
	Attachments        []model.Attachment
	ReplaceAttachments bool
}

// InvestigationFilter narrows an investigation listing. The zero value matches
// every live investigation. Statuses, when non-empty, keeps only investigations
// whose status is in the set; Labels are ANDed; Anchors constrains to
// investigations carrying the given anchor.
type InvestigationFilter struct {
	Statuses []model.InvestigationStatus
	Labels   []string
	Anchors  AnchorFilter
}

// CreateInvestigation roots an investigation from spec and returns its folded
// snapshot. Title and Premise are required — an empty one is ErrEmptyTitle or
// ErrEmptyPremise, the latter unrepairable since the premise is immutable. The
// create pack carries the investigation op, the attachment ops, then one
// AddFinding per initial finding, in that order — a single write. Findings make
// the pack dedupe-ineligible (dedupeCovered excludes AddFinding), so a
// finding-carrying create never converges on an existing record; a finding-free
// create still can, and the returned bool reports that its best-effort duplicate
// guard did. Investigations have no freshness lifecycle, so the create is
// verify-free.
func (c *Client) CreateInvestigation(ctx context.Context, spec InvestigationSpec) (model.Investigation, bool, error) {
	if spec.Title == "" {
		return model.Investigation{}, false, ErrEmptyTitle
	}
	if spec.Premise == "" {
		return model.Investigation{}, false, ErrEmptyPremise
	}
	for _, finding := range spec.Findings {
		if finding == "" {
			return model.Investigation{}, false, ErrEmptyFinding
		}
	}
	commits, err := c.resolveCommits(ctx, spec.Anchors.Commits)
	if err != nil {
		return model.Investigation{}, false, err
	}
	anchors := buildAnchors(AnchorSpec{Commits: commits, Paths: spec.Anchors.Paths, Dirs: spec.Anchors.Dirs, Branches: spec.Anchors.Branches})
	ops := make([]model.Op, 0, 1+len(spec.Attachments)+len(spec.Findings))
	ops = append(ops, model.CreateInvestigation{Nonce: model.NewNonce(), Title: spec.Title, Premise: spec.Premise, Tags: spec.Tags, Anchors: anchors})
	ops = append(ops, attachmentAddOps(spec.Attachments)...)
	for _, text := range spec.Findings {
		ops = append(ops, model.AddFinding{ID: model.NewNonce(), Text: text})
	}
	snap, err := c.s.Create(ctx, ops)
	reused := false
	var dup *store.DuplicateError
	if errors.As(err, &dup) {
		snap, reused = dup.Existing, true
	} else if err != nil {
		return model.Investigation{}, false, err
	}
	return snap.(model.Investigation), reused, nil
}

// Investigations folds the investigation set the filter selects and returns the
// live records in UpdatedAt-desc, id-asc order — tombstoned and superseded ones
// stay hidden.
func (c *Client) Investigations(ctx context.Context, f InvestigationFilter) ([]model.Investigation, error) {
	invs, err := c.s.ListInvestigations(ctx)
	if err != nil {
		return nil, err
	}
	invs = slices.DeleteFunc(invs, func(inv model.Investigation) bool {
		if len(f.Statuses) > 0 && !slices.Contains(f.Statuses, inv.Status) {
			return true
		}
		return !hasAll(inv.Tags, f.Labels) || !matchesAnchorFilter(inv.Anchors, f.Anchors)
	})
	sortDocuments(invs)
	return invs, nil
}

// EditInvestigation applies the mask to the investigation. An all-empty mask is
// ErrEmptyEdit; the premise is immutable and has no edit path. AddAnchors'
// commits are resolved first, so a bad revision mutates nothing. Ops apply in
// title, body, add-tag, remove-tag, add-anchor, remove-anchor order.
func (c *Client) EditInvestigation(ctx context.Context, id model.EntityID, edit InvestigationEdit) (model.Investigation, error) {
	if edit.empty() {
		return model.Investigation{}, ErrEmptyEdit
	}
	addAnchors, err := c.resolveAnchors(ctx, edit.AddAnchors)
	if err != nil {
		return model.Investigation{}, err
	}
	var ops []model.Op
	if edit.Title != nil {
		ops = append(ops, model.SetTitle{Title: *edit.Title})
	}
	if edit.Body != nil {
		ops = append(ops, model.SetBody{Body: *edit.Body})
	}
	ops = appendEditOps(ops, edit.AddTags, edit.RemoveTags, addAnchors, edit.RemoveAnchors, nil, nil, nil)
	return c.appendInvestigation(ctx, id, ops)
}

// RemoveInvestigation tombstones the investigation, returning the folded
// snapshot. DeleteNote is a soft tombstone — the ref survives, so the
// investigation still resolves for show and append.
func (c *Client) RemoveInvestigation(ctx context.Context, id model.EntityID) (model.Investigation, error) {
	return c.appendInvestigation(ctx, id, []model.Op{model.DeleteNote{}})
}

// SearchInvestigations ranks the live investigation set against query, filtered
// by the SearchFilter, and returns the top results by tier, then UpdatedAt
// descending, then id ascending. An investigation matches when its title, a tag,
// its premise, resolution body, root cause, any timeline entry, or any finding
// text contains query.
func (c *Client) SearchInvestigations(ctx context.Context, query string, f SearchFilter) ([]model.Investigation, error) {
	invs, err := c.s.ListInvestigations(ctx)
	if err != nil {
		return nil, err
	}
	return rankDocuments(invs, query, f, investigationRanker), nil
}

var investigationRanker = documentRanker[model.Investigation]{
	tags:    func(i model.Investigation) []string { return i.Tags },
	author:  func(i model.Investigation) string { return string(i.Author) },
	anchors: func(i model.Investigation) []model.Anchor { return i.Anchors },
	tier:    func(i model.Investigation, q string) int { return textTier(i.Title, i.Tags, investigationBodies(i), q) },
}

// investigationBodies is an investigation's searchable body: premise, resolution
// body, root cause, then each timeline entry and finding text.
func investigationBodies(inv model.Investigation) []string {
	texts := make([]string, 0, 3+len(inv.Entries)+len(inv.Findings))
	texts = append(texts, inv.Premise, inv.Body, inv.RootCause)
	for _, e := range inv.Entries {
		texts = append(texts, e.Text)
	}
	for _, f := range inv.Findings {
		texts = append(texts, f.Text)
	}
	return texts
}

// AppendInvestigation appends one timeline entry to the investigation, plus any
// attachments. Text may be empty only alongside at least one attachment; an empty
// append with no attachments is ErrEmptyEdit. Unless ReplaceAttachments is set,
// an attachment whose name collides with a live one is an *AttachmentExistsError.
// The empty check, load, and collision check all run before any write.
func (c *Client) AppendInvestigation(ctx context.Context, id model.EntityID, in InvestigationAppend) (model.Investigation, error) {
	if in.Text == "" && len(in.Attachments) == 0 {
		return model.Investigation{}, ErrEmptyEdit
	}
	inv, err := c.Investigation(ctx, id)
	if err != nil {
		return model.Investigation{}, err
	}
	if !in.ReplaceAttachments {
		if err := checkAttachmentCollisions(inv.Attachments, in.Attachments); err != nil {
			return model.Investigation{}, err
		}
	}
	var ops []model.Op
	if in.Text != "" {
		ops = append(ops, model.AppendEntry{Text: in.Text, Model: in.Model})
	}
	ops = append(ops, attachmentAddOps(in.Attachments)...)
	return c.appendInvestigation(ctx, id, ops)
}

// AddFinding adds a suspect finding to the investigation; new findings start
// open. An empty text is ErrEmptyFinding.
func (c *Client) AddFinding(ctx context.Context, id model.EntityID, text string) (model.Investigation, error) {
	if text == "" {
		return model.Investigation{}, ErrEmptyFinding
	}
	return c.appendInvestigation(ctx, id, []model.Op{model.AddFinding{ID: model.NewNonce(), Text: text}})
}

// EditFinding replaces the text of the finding whose id prefix uniquely matches.
// An empty text is ErrEmptyFinding; an unknown or ambiguous prefix fails with
// ErrNotFound or ErrAmbiguous.
func (c *Client) EditFinding(ctx context.Context, id model.EntityID, finding, text string) (model.Investigation, error) {
	if text == "" {
		return model.Investigation{}, ErrEmptyFinding
	}
	resolved, err := c.resolveFinding(ctx, id, finding)
	if err != nil {
		return model.Investigation{}, err
	}
	return c.appendInvestigation(ctx, id, []model.Op{model.SetFindingText{ID: resolved.ID, Text: text}})
}

// RemoveFinding removes the finding whose id prefix uniquely matches. An unknown
// or ambiguous prefix fails with ErrNotFound or ErrAmbiguous.
func (c *Client) RemoveFinding(ctx context.Context, id model.EntityID, finding string) (model.Investigation, error) {
	resolved, err := c.resolveFinding(ctx, id, finding)
	if err != nil {
		return model.Investigation{}, err
	}
	return c.appendInvestigation(ctx, id, []model.Op{model.RemoveFinding{ID: resolved.ID}})
}

// SetFindingCleared marks the finding cleared — the suspect was exonerated — with
// the evidence why recorded on the finding. An empty why is ErrMissingReason.
func (c *Client) SetFindingCleared(ctx context.Context, id model.EntityID, finding, why string) (model.Investigation, error) {
	return c.setFindingStatus(ctx, id, finding, model.FindingCleared, why)
}

// SetFindingConfirmed marks the finding confirmed — the suspect is the cause —
// with the evidence why recorded on the finding. An empty why is
// ErrMissingReason.
func (c *Client) SetFindingConfirmed(ctx context.Context, id model.EntityID, finding, why string) (model.Investigation, error) {
	return c.setFindingStatus(ctx, id, finding, model.FindingConfirmed, why)
}

// setFindingStatus disposes the finding whose id prefix uniquely matches, with a
// required evidence why folded into the finding's note.
func (c *Client) setFindingStatus(ctx context.Context, id model.EntityID, finding string, status model.FindingStatus, why string) (model.Investigation, error) {
	if why == "" {
		return model.Investigation{}, ErrMissingReason
	}
	resolved, err := c.resolveFinding(ctx, id, finding)
	if err != nil {
		return model.Investigation{}, err
	}
	return c.appendInvestigation(ctx, id, []model.Op{model.SetFindingStatus{ID: resolved.ID, Status: status, Note: why}})
}

// RootCause records the true root cause and transitions the investigation to
// root_caused in one pack commit: [SetRootCause, AppendEntry, status]. It is
// legal only from open; an empty cause is ErrMissingReason and an illegal
// transition is ErrIllegalTransition.
func (c *Client) RootCause(ctx context.Context, id model.EntityID, text string) (model.Investigation, error) {
	if text == "" {
		return model.Investigation{}, ErrMissingReason
	}
	inv, err := c.Investigation(ctx, id)
	if err != nil {
		return model.Investigation{}, err
	}
	if err := ensureInvestigationTransition(inv, model.InvestigationRootCaused); err != nil {
		return model.Investigation{}, err
	}
	ops := []model.Op{
		model.SetRootCause{Text: text},
		model.AppendEntry{Text: text},
		model.SetInvestigationStatus{Status: model.InvestigationRootCaused},
	}
	return c.appendInvestigation(ctx, id, ops)
}

// Fix links the fixing commits and transitions the investigation to fixed in one
// pack commit: [AddFixCommit…, optional AppendEntry, status]. A fix must record
// evidence — at least one commit or a text entry, else ErrMissingReason. Commits
// are resolved strictly — a revision absent from the local ODB fails with
// ErrNotFound before any mutation. It is legal only from root_caused.
func (c *Client) Fix(ctx context.Context, id model.EntityID, text string, commits []string) (model.Investigation, error) {
	if text == "" && len(commits) == 0 {
		return model.Investigation{}, ErrMissingReason
	}
	full, err := c.resolveCommits(ctx, commits)
	if err != nil {
		return model.Investigation{}, err
	}
	inv, err := c.Investigation(ctx, id)
	if err != nil {
		return model.Investigation{}, err
	}
	if err := ensureInvestigationTransition(inv, model.InvestigationFixed); err != nil {
		return model.Investigation{}, err
	}
	ops := make([]model.Op, 0, len(full)+2)
	for _, sha := range full {
		ops = append(ops, model.AddFixCommit{SHA: model.SHA(sha)})
	}
	if text != "" {
		ops = append(ops, model.AppendEntry{Text: text})
	}
	ops = append(ops, model.SetInvestigationStatus{Status: model.InvestigationFixed})
	return c.appendInvestigation(ctx, id, ops)
}

// Confirm records proof that the fix held and transitions the investigation to
// confirmed in one pack commit: [AppendEntry{proof}, status]. It is legal only
// from fixed; an empty proof is ErrMissingReason.
func (c *Client) Confirm(ctx context.Context, id model.EntityID, proof string) (model.Investigation, error) {
	return c.transitionWithEntry(ctx, id, model.InvestigationConfirmed, proof, true)
}

// Exonerate falsifies the premise and transitions the investigation to
// exonerated in one pack commit: [AppendEntry{reason}, status]. It is legal from
// open or root_caused; an empty reason is ErrMissingReason.
func (c *Client) Exonerate(ctx context.Context, id model.EntityID, reason string) (model.Investigation, error) {
	return c.transitionWithEntry(ctx, id, model.InvestigationExonerated, reason, true)
}

// Abandon walks away from the investigation with no verdict, transitioning it to
// abandoned in one pack commit: [optional AppendEntry, status]. It is legal from
// any non-terminal status; text is optional.
func (c *Client) Abandon(ctx context.Context, id model.EntityID, text string) (model.Investigation, error) {
	return c.transitionWithEntry(ctx, id, model.InvestigationAbandoned, text, false)
}

// Reopen reopens a non-open investigation — a regression on a confirmed one, a
// re-examined verdict — transitioning it to open in one pack commit:
// [AppendEntry{reason}, status]. The reason is required (ErrMissingReason when
// empty); reopening an already-open investigation is ErrIllegalTransition.
func (c *Client) Reopen(ctx context.Context, id model.EntityID, reason string) (model.Investigation, error) {
	return c.transitionWithEntry(ctx, id, model.InvestigationOpen, reason, true)
}

// AddFollowUp records an outbound follow-up edge from the investigation to
// another entity — a spawned task, a graduated invariant note, or a follow-up
// investigation.
func (c *Client) AddFollowUp(ctx context.Context, id, followUp model.EntityID) (model.Investigation, error) {
	return c.appendInvestigation(ctx, id, []model.Op{model.AddFollowUp{ID: followUp}})
}

// RemoveFollowUp removes a follow-up edge from the investigation.
func (c *Client) RemoveFollowUp(ctx context.Context, id, followUp model.EntityID) (model.Investigation, error) {
	return c.appendInvestigation(ctx, id, []model.Op{model.RemoveFollowUp{ID: followUp}})
}

// transitionWithEntry is the shared body of the confirm/exonerate/abandon/reopen
// verbs: an optional (or required) timeline entry followed by the LWW status set,
// in one pack commit, gated on transition legality at op-build time.
func (c *Client) transitionWithEntry(ctx context.Context, id model.EntityID, target model.InvestigationStatus, text string, requireText bool) (model.Investigation, error) {
	if requireText && text == "" {
		return model.Investigation{}, ErrMissingReason
	}
	inv, err := c.Investigation(ctx, id)
	if err != nil {
		return model.Investigation{}, err
	}
	if err := ensureInvestigationTransition(inv, target); err != nil {
		return model.Investigation{}, err
	}
	var ops []model.Op
	if text != "" {
		ops = append(ops, model.AppendEntry{Text: text})
	}
	ops = append(ops, model.SetInvestigationStatus{Status: target})
	return c.appendInvestigation(ctx, id, ops)
}

// legalInvestigationTransitions is the lifecycle machine: each status maps to the
// statuses a client verb may move it to. Legality is enforced here at op-build
// time so the fold stays total and deterministic. Terminals reach only open, via
// reopen. The gate is best-effort against the snapshot the verb loaded: concurrent
// histories — including a CAS retry that re-folds a newer tip — are reconciled by
// LWW in fold by design, so two transitions each legal at load time may interleave
// and the fold picks one deterministic winner.
var legalInvestigationTransitions = map[model.InvestigationStatus][]model.InvestigationStatus{
	model.InvestigationOpen:       {model.InvestigationRootCaused, model.InvestigationExonerated, model.InvestigationAbandoned},
	model.InvestigationRootCaused: {model.InvestigationFixed, model.InvestigationOpen, model.InvestigationExonerated, model.InvestigationAbandoned},
	model.InvestigationFixed:      {model.InvestigationConfirmed, model.InvestigationRootCaused, model.InvestigationOpen, model.InvestigationAbandoned},
	model.InvestigationConfirmed:  {model.InvestigationOpen},
	model.InvestigationExonerated: {model.InvestigationOpen},
	model.InvestigationAbandoned:  {model.InvestigationOpen},
}

// ensureInvestigationTransition reports an ErrIllegalTransition, naming the
// current and requested status, unless the move is in the lifecycle machine.
func ensureInvestigationTransition(inv model.Investigation, target model.InvestigationStatus) error {
	if slices.Contains(legalInvestigationTransitions[inv.Status], target) {
		return nil
	}
	return fmt.Errorf("%w: %s cannot go %s→%s", ErrIllegalTransition, inv.ID.Short(), inv.Status, target)
}

// nonTerminalInvestigation reports whether an investigation is still in flight —
// open, root_caused, or fixed — as opposed to a terminal verdict (confirmed,
// exonerated, abandoned).
func nonTerminalInvestigation(status model.InvestigationStatus) bool {
	switch status {
	case model.InvestigationConfirmed, model.InvestigationExonerated, model.InvestigationAbandoned:
		return false
	}
	return true
}

// ResolveFinding expands a finding id prefix — matched case-insensitively —
// against an investigation's findings. An empty prefix is refused with ErrNotFound
// rather than silently matching a sole finding. No match fails with ErrNotFound;
// several matches fail with ErrAmbiguous listing each candidate's short id and text.
func ResolveFinding(inv model.Investigation, prefix string) (model.Finding, error) {
	if prefix == "" {
		return model.Finding{}, fmt.Errorf("%w: a finding id is required", ErrNotFound)
	}
	lowered := strings.ToLower(prefix)
	var matches []model.Finding
	for _, f := range inv.Findings {
		if strings.HasPrefix(strings.ToLower(f.ID), lowered) {
			matches = append(matches, f)
		}
	}
	switch len(matches) {
	case 0:
		return model.Finding{}, fmt.Errorf("%w: no finding matches %q", ErrNotFound, prefix)
	case 1:
		return matches[0], nil
	default:
		var b strings.Builder
		for i, f := range matches {
			if i > 0 {
				b.WriteString("; ")
			}
			fmt.Fprintf(&b, "%s %s", render.ShortWireID(f.ID), f.Text)
		}
		return model.Finding{}, fmt.Errorf("%w: finding prefix %q matches %d: %s", ErrAmbiguous, prefix, len(matches), b.String())
	}
}

// resolveFinding loads the investigation and resolves a finding id prefix against
// it.
func (c *Client) resolveFinding(ctx context.Context, id model.EntityID, prefix string) (model.Finding, error) {
	inv, err := c.Investigation(ctx, id)
	if err != nil {
		return model.Finding{}, err
	}
	return ResolveFinding(inv, prefix)
}

// appendInvestigation appends ops to the investigation chain and returns the
// folded snapshot.
func (c *Client) appendInvestigation(ctx context.Context, id model.EntityID, ops []model.Op) (model.Investigation, error) {
	snap, err := c.s.Append(ctx, refs.For(model.KindInvestigation, id), ops)
	if err != nil {
		return model.Investigation{}, err
	}
	return snap.(model.Investigation), nil
}
