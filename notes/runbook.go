package notes

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// RunbookSpec is the input to CreateRunbook. Title is required; the rest are
// optional. Steps are the initial step texts, in order — CreateRunbook positions
// them sequentially via model.PositionBetween. Anchors are attached in commit,
// path, dir, then branch order; commit values are resolved to full shas at
// write time.
type RunbookSpec struct {
	Title       string
	Description string
	Labels      []string
	Steps       []string
	Anchors     AnchorSpec
}

// RunbookEdit is the field mask for EditRunbook: a nil Title or Description
// leaves the field untouched, a non-nil pointer sets it; the label and anchor
// slices apply in order. AddAnchors' commit values are resolved to full shas;
// RemoveAnchors is matched verbatim. An all-empty mask is ErrEmptyEdit.
type RunbookEdit struct {
	Title         *string
	Description   *string
	AddLabels     []string
	RemoveLabels  []string
	AddAnchors    AnchorSpec
	RemoveAnchors AnchorSpec
}

// empty reports whether the mask sets nothing.
func (e RunbookEdit) empty() bool {
	return e.Title == nil && e.Description == nil &&
		len(e.AddLabels) == 0 && len(e.RemoveLabels) == 0 &&
		e.AddAnchors.isEmpty() && e.RemoveAnchors.isEmpty()
}

// StepEdit is the field mask for EditStep. A nil field leaves it untouched; a
// non-nil Command pointer sets the command (a pointer to the empty string clears
// it). An all-nil mask is ErrEmptyEdit.
type StepEdit struct {
	Text    *string
	Command *string
}

// empty reports whether the mask sets nothing.
func (e StepEdit) empty() bool {
	return e.Text == nil && e.Command == nil
}

// PlacementAnchor selects where a step lands within a runbook's ordered steps.
type PlacementAnchor int

const (
	// PlaceLast appends after all steps (the default).
	PlaceLast PlacementAnchor = iota
	// PlaceFirst places before all steps.
	PlaceFirst
	// PlaceBefore places immediately before the step named by Placement.Step.
	PlaceBefore
	// PlaceAfter places immediately after the step named by Placement.Step.
	PlaceAfter
)

// Placement positions a step within a runbook. Step is the id prefix of the
// neighbor for PlaceBefore and PlaceAfter, and is ignored otherwise.
type Placement struct {
	Anchor PlacementAnchor
	Step   string
}

// ErrSelfRelative reports a move that places a step before or after itself.
var ErrSelfRelative = errors.New("cannot place a step relative to itself")

// CreateRunbook roots a runbook from spec and returns its folded snapshot. The
// create pack carries the runbook op followed by one AddStep op per initial
// step, positioned sequentially via model.PositionBetween in slice order. The
// returned bool reports that Create's best-effort duplicate guard converged on
// an existing runbook.
func (c *Client) CreateRunbook(ctx context.Context, spec RunbookSpec) (model.Runbook, bool, error) {
	anchors, err := c.resolveAnchors(ctx, spec.Anchors)
	if err != nil {
		return model.Runbook{}, false, err
	}
	ops := make([]model.Op, 0, 1+len(spec.Steps))
	ops = append(ops, model.CreateRunbook{
		Nonce:       model.NewNonce(),
		Title:       spec.Title,
		Description: spec.Description,
		Labels:      spec.Labels,
		Anchors:     anchors,
	})
	last := ""
	for _, text := range spec.Steps {
		pos := model.PositionBetween(last, "")
		ops = append(ops, model.AddStep{ID: model.NewNonce(), Text: text, Position: pos})
		last = pos
	}
	snap, err := c.s.Create(ctx, ops)
	reused := false
	var dup *store.DuplicateError
	if errors.As(err, &dup) {
		snap, reused = dup.Existing, true
	} else if err != nil {
		return model.Runbook{}, false, err
	}
	return snap.(model.Runbook), reused, nil
}

// RunbookFilter narrows a runbook listing. The zero value matches every active
// runbook. Labels are ANDed; Anchors constrains to runbooks carrying the given
// anchor; IncludeArchived widens the set to archived runbooks.
type RunbookFilter struct {
	IncludeArchived bool
	Labels          []string
	Anchors         AnchorFilter
}

// Runbooks folds the runbook set the filter selects, in store order (creation
// time then id). Tombstoned runbooks are always dropped; archived runbooks are
// dropped unless IncludeArchived is set.
func (c *Client) Runbooks(ctx context.Context, f RunbookFilter) ([]model.Runbook, error) {
	runbooks, err := c.s.ListRunbooks(ctx)
	if err != nil {
		return nil, err
	}
	runbooks = slices.DeleteFunc(runbooks, func(rb model.Runbook) bool {
		if !f.IncludeArchived && rb.Status != model.RunbookActive {
			return true
		}
		return !hasAll(rb.Labels, f.Labels) || !matchesAnchorFilter(rb.Anchors, f.Anchors)
	})
	return runbooks, nil
}

// ActivateRunbook marks the runbook active. A runbook already active is refused
// with a *ConflictError; an archived one is reactivated.
func (c *Client) ActivateRunbook(ctx context.Context, id model.EntityID) (model.Runbook, error) {
	return c.setRunbookStatus(ctx, id, model.RunbookActive)
}

// ArchiveRunbook marks the runbook archived. A runbook already archived is
// refused with a *ConflictError.
func (c *Client) ArchiveRunbook(ctx context.Context, id model.EntityID) (model.Runbook, error) {
	return c.setRunbookStatus(ctx, id, model.RunbookArchived)
}

// setRunbookStatus transitions the runbook to status, refusing only a same-status
// no-op with a *ConflictError — the sole gate on activate and archive (neither is
// EnsureRunbookActive-gated, so an archived runbook can still be reactivated).
func (c *Client) setRunbookStatus(ctx context.Context, id model.EntityID, status model.RunbookStatus) (model.Runbook, error) {
	rb, err := c.Runbook(ctx, id)
	if err != nil {
		return model.Runbook{}, err
	}
	if rb.Status == status {
		return model.Runbook{}, &ConflictError{ID: rb.ID, Msg: "already " + string(status)}
	}
	snap, err := c.s.Append(ctx, refs.For(model.KindRunbook, id), []model.Op{model.SetRunbookStatus{Status: status}})
	if err != nil {
		return model.Runbook{}, err
	}
	return snap.(model.Runbook), nil
}

// EditRunbook applies the mask to the runbook. An all-empty mask is ErrEmptyEdit;
// the runbook must be active. AddAnchors' commits are resolved first, so a bad
// revision mutates nothing. Ops apply in title, description, add-label,
// remove-label, add-anchor, remove-anchor order.
func (c *Client) EditRunbook(ctx context.Context, id model.EntityID, edit RunbookEdit) (model.Runbook, error) {
	if edit.empty() {
		return model.Runbook{}, ErrEmptyEdit
	}
	addAnchors, err := c.resolveAnchors(ctx, edit.AddAnchors)
	if err != nil {
		return model.Runbook{}, err
	}
	rb, err := c.Runbook(ctx, id)
	if err != nil {
		return model.Runbook{}, err
	}
	if err := EnsureRunbookActive(rb); err != nil {
		return model.Runbook{}, err
	}
	var ops []model.Op
	if edit.Title != nil {
		ops = append(ops, model.SetTitle{Title: *edit.Title})
	}
	if edit.Description != nil {
		ops = append(ops, model.SetDescription{Description: *edit.Description})
	}
	for _, l := range edit.AddLabels {
		ops = append(ops, model.AddLabel{Label: l})
	}
	for _, l := range edit.RemoveLabels {
		ops = append(ops, model.RemoveLabel{Label: l})
	}
	ops = anchorEditOps(ops, addAnchors, edit.RemoveAnchors)
	return c.appendRunbook(ctx, id, ops)
}

// RemoveRunbook tombstones the runbook, returning the folded snapshot.
// DeleteNote is a soft tombstone — the ref survives, so the runbook still
// resolves for show.
func (c *Client) RemoveRunbook(ctx context.Context, id model.EntityID) (model.Runbook, error) {
	return c.appendRunbook(ctx, id, []model.Op{model.DeleteNote{}})
}

// SearchRunbooks ranks the active runbook set against query, filtered by the
// SearchFilter, and returns the top results by tier, then UpdatedAt descending,
// then id ascending. A runbook matches when its title, a label, its
// description, or any step text contains query.
func (c *Client) SearchRunbooks(ctx context.Context, query string, f SearchFilter) ([]model.Runbook, error) {
	runbooks, err := c.Runbooks(ctx, RunbookFilter{})
	if err != nil {
		return nil, err
	}
	return rankDocuments(runbooks, query, f, runbookRanker), nil
}

var runbookRanker = documentRanker[model.Runbook]{
	tags:    func(rb model.Runbook) []string { return rb.Labels },
	author:  func(rb model.Runbook) string { return string(rb.Author) },
	anchors: func(rb model.Runbook) []model.Anchor { return rb.Anchors },
	tier:    func(rb model.Runbook, q string) int { return textTier(rb.Title, rb.Labels, runbookBodies(rb), q) },
}

// runbookBodies is a runbook's searchable body: the description, then each
// step's text in folded order.
func runbookBodies(rb model.Runbook) []string {
	texts := make([]string, 0, 1+len(rb.Steps))
	texts = append(texts, rb.Description)
	for _, s := range rb.Steps {
		texts = append(texts, s.Text)
	}
	return texts
}

// CommentRunbook appends an operational comment; the runbook must be active.
func (c *Client) CommentRunbook(ctx context.Context, id model.EntityID, body string) (model.Runbook, error) {
	rb, err := c.Runbook(ctx, id)
	if err != nil {
		return model.Runbook{}, err
	}
	if err := EnsureRunbookActive(rb); err != nil {
		return model.Runbook{}, err
	}
	return c.appendRunbook(ctx, id, []model.Op{model.AddComment{Body: body}})
}

// AddStep adds a step with text and command at the placement, resolving the
// position via model.PositionBetween over the loaded runbook. The runbook must be
// active.
func (c *Client) AddStep(ctx context.Context, id model.EntityID, text, command string, place Placement) (model.Runbook, error) {
	rb, err := c.Runbook(ctx, id)
	if err != nil {
		return model.Runbook{}, err
	}
	if err := EnsureRunbookActive(rb); err != nil {
		return model.Runbook{}, err
	}
	pos, err := resolvePlacement(rb, place, "")
	if err != nil {
		return model.Runbook{}, err
	}
	return c.appendRunbook(ctx, id, []model.Op{model.AddStep{ID: model.NewNonce(), Text: text, Command: command, Position: pos}})
}

// RemoveStep removes the step named by the id prefix. The runbook must be active.
func (c *Client) RemoveStep(ctx context.Context, id model.EntityID, step string) (model.Runbook, error) {
	rb, err := c.Runbook(ctx, id)
	if err != nil {
		return model.Runbook{}, err
	}
	if err := EnsureRunbookActive(rb); err != nil {
		return model.Runbook{}, err
	}
	st, err := ResolveStep(rb, step)
	if err != nil {
		return model.Runbook{}, err
	}
	return c.appendRunbook(ctx, id, []model.Op{model.RemoveStep{ID: st.ID}})
}

// EditStep applies the mask to the step named by the id prefix. An all-nil mask
// is ErrEmptyEdit; the runbook must be active. Ops apply in text, command order.
func (c *Client) EditStep(ctx context.Context, id model.EntityID, step string, edit StepEdit) (model.Runbook, error) {
	if edit.empty() {
		return model.Runbook{}, ErrEmptyEdit
	}
	rb, err := c.Runbook(ctx, id)
	if err != nil {
		return model.Runbook{}, err
	}
	if err := EnsureRunbookActive(rb); err != nil {
		return model.Runbook{}, err
	}
	st, err := ResolveStep(rb, step)
	if err != nil {
		return model.Runbook{}, err
	}
	var ops []model.Op
	if edit.Text != nil {
		ops = append(ops, model.SetStepText{ID: st.ID, Text: *edit.Text})
	}
	if edit.Command != nil {
		ops = append(ops, model.SetStepCommand{ID: st.ID, Command: *edit.Command})
	}
	return c.appendRunbook(ctx, id, ops)
}

// MoveStep repositions the step named by the id prefix to the placement. Placing
// a step relative to itself is an error. The runbook must be active.
func (c *Client) MoveStep(ctx context.Context, id model.EntityID, step string, place Placement) (model.Runbook, error) {
	rb, err := c.Runbook(ctx, id)
	if err != nil {
		return model.Runbook{}, err
	}
	if err := EnsureRunbookActive(rb); err != nil {
		return model.Runbook{}, err
	}
	st, err := ResolveStep(rb, step)
	if err != nil {
		return model.Runbook{}, err
	}
	pos, err := resolvePlacement(rb, place, st.ID)
	if err != nil {
		return model.Runbook{}, err
	}
	return c.appendRunbook(ctx, id, []model.Op{model.SetStepPosition{ID: st.ID, Position: pos}})
}

// StartRun begins a tracked run of the runbook, optionally citing the task it
// serves. The runbook must be active.
func (c *Client) StartRun(ctx context.Context, id, task model.EntityID) (model.Runbook, error) {
	rb, err := c.Runbook(ctx, id)
	if err != nil {
		return model.Runbook{}, err
	}
	if err := EnsureRunbookActive(rb); err != nil {
		return model.Runbook{}, err
	}
	return c.appendRunbook(ctx, id, []model.Op{model.StartRun{ID: model.NewNonce(), Task: task}})
}

// SetRunStep records status (with an optional note) for the step named by the
// step id prefix within the target run. run selects the run: an empty run targets
// the sole running run (zero is a *ConflictError, several is ErrAmbiguous). The
// runbook must be active.
func (c *Client) SetRunStep(ctx context.Context, id model.EntityID, run, step string, status model.StepResultStatus, note string) (model.Runbook, error) {
	rb, err := c.Runbook(ctx, id)
	if err != nil {
		return model.Runbook{}, err
	}
	if err := EnsureRunbookActive(rb); err != nil {
		return model.Runbook{}, err
	}
	st, err := ResolveStep(rb, step)
	if err != nil {
		return model.Runbook{}, err
	}
	target, err := ResolveTargetRun(rb, run)
	if err != nil {
		return model.Runbook{}, err
	}
	return c.appendRunbook(ctx, id, []model.Op{model.SetRunStepStatus{RunID: target.ID, StepID: st.ID, Status: status, Note: note}})
}

// FinishRun ends the target run with status. run selects the run as SetRunStep
// does. A run already finished (not running) is a *ConflictError. The runbook must
// be active.
func (c *Client) FinishRun(ctx context.Context, id model.EntityID, run string, status model.RunStatus) (model.Runbook, error) {
	rb, err := c.Runbook(ctx, id)
	if err != nil {
		return model.Runbook{}, err
	}
	if err := EnsureRunbookActive(rb); err != nil {
		return model.Runbook{}, err
	}
	target, err := ResolveTargetRun(rb, run)
	if err != nil {
		return model.Runbook{}, err
	}
	if target.Status != model.RunRunning {
		return model.Runbook{}, &ConflictError{ID: rb.ID, Msg: fmt.Sprintf("run %s already %s", render.ShortWireID(target.ID), target.Status)}
	}
	return c.appendRunbook(ctx, id, []model.Op{model.FinishRun{ID: target.ID, Status: status}})
}

// appendRunbook appends ops to the runbook chain and returns the folded snapshot.
func (c *Client) appendRunbook(ctx context.Context, id model.EntityID, ops []model.Op) (model.Runbook, error) {
	snap, err := c.s.Append(ctx, refs.For(model.KindRunbook, id), ops)
	if err != nil {
		return model.Runbook{}, err
	}
	return snap.(model.Runbook), nil
}

// DerivedRunStatus is a finishing run's default terminal status: failed when any
// step result failed, else succeeded. The explicit --failed/--abandoned override
// is the caller's to apply.
func DerivedRunStatus(run model.RunbookRun) model.RunStatus {
	for _, r := range run.Results {
		if r.Status == model.StepFailed {
			return model.RunFailed
		}
	}
	return model.RunSucceeded
}

// ResolveStep expands a step id prefix — matched case-insensitively — against a
// runbook's steps. No match fails with ErrNotFound; several matches fail with an
// error listing each candidate's short id and text; one match returns it.
func ResolveStep(rb model.Runbook, prefix string) (model.RunbookStep, error) {
	lowered := strings.ToLower(prefix)
	var matches []model.RunbookStep
	for _, st := range rb.Steps {
		if strings.HasPrefix(strings.ToLower(st.ID), lowered) {
			matches = append(matches, st)
		}
	}
	switch len(matches) {
	case 0:
		return model.RunbookStep{}, fmt.Errorf("%w: no step matches %q", ErrNotFound, prefix)
	case 1:
		return matches[0], nil
	default:
		var b strings.Builder
		for i, st := range matches {
			if i > 0 {
				b.WriteString("; ")
			}
			fmt.Fprintf(&b, "%s %s", render.ShortWireID(st.ID), st.Text)
		}
		return model.RunbookStep{}, fmt.Errorf("%w: step prefix %q matches %d: %s", ErrAmbiguous, prefix, len(matches), b.String())
	}
}

// ResolveRun expands a run id prefix — matched case-insensitively — against a
// runbook's runs. No match fails with ErrNotFound; several matches fail with an
// error listing each candidate's short id, status, and start date; one match
// returns it.
func ResolveRun(rb model.Runbook, prefix string) (model.RunbookRun, error) {
	lowered := strings.ToLower(prefix)
	var matches []model.RunbookRun
	for _, r := range rb.Runs {
		if strings.HasPrefix(strings.ToLower(r.ID), lowered) {
			matches = append(matches, r)
		}
	}
	switch len(matches) {
	case 0:
		return model.RunbookRun{}, fmt.Errorf("%w: no run matches %q", ErrNotFound, prefix)
	case 1:
		return matches[0], nil
	default:
		return model.RunbookRun{}, fmt.Errorf("%w: run prefix %q matches %d: %s", ErrAmbiguous, prefix, len(matches), runCandidates(matches))
	}
}

// ResolveTargetRun picks the run a step-status or finish operation targets: the
// run id prefix when set (may target a finished run — the core upserts results
// after finish), else the runbook's sole running run. Zero running runs is a
// *ConflictError; several is ErrAmbiguous listing them.
func ResolveTargetRun(rb model.Runbook, runPrefix string) (model.RunbookRun, error) {
	if runPrefix != "" {
		return ResolveRun(rb, runPrefix)
	}
	var running []model.RunbookRun
	for _, r := range rb.Runs {
		if r.Status == model.RunRunning {
			running = append(running, r)
		}
	}
	switch len(running) {
	case 1:
		return running[0], nil
	case 0:
		return model.RunbookRun{}, &ConflictError{ID: rb.ID, Msg: "has no running run; start one with `runbook run start` or pass --run"}
	default:
		return model.RunbookRun{}, fmt.Errorf("%w: %s has %d running runs; pass --run: %s", ErrAmbiguous, rb.ID.Short(), len(running), runCandidates(running))
	}
}

// EnsureRunbookActive rejects a write to an archived runbook with a
// *ConflictError; every runbook write but activate and archive is gated by it.
func EnsureRunbookActive(rb model.Runbook) error {
	if rb.Status == model.RunbookArchived {
		return &ConflictError{ID: rb.ID, Msg: "is archived"}
	}
	return nil
}

// resolvePlacement resolves place to a step position within rb, excluding the
// step being moved (movingID, empty for an add) from the neighbor computation. A
// PlaceBefore/PlaceAfter that names the moving step is ErrSelfRelative.
func resolvePlacement(rb model.Runbook, place Placement, movingID string) (string, error) {
	steps := stepsExcluding(rb.Steps, movingID)
	switch place.Anchor {
	case PlaceFirst:
		next := ""
		if len(steps) > 0 {
			next = steps[0].Position
		}
		return model.PositionBetween("", next), nil
	case PlaceBefore:
		target, err := ResolveStep(rb, place.Step)
		if err != nil {
			return "", err
		}
		if target.ID == movingID {
			return "", ErrSelfRelative
		}
		idx := stepIndex(steps, target.ID)
		prev := ""
		if idx > 0 {
			prev = steps[idx-1].Position
		}
		return model.PositionBetween(prev, target.Position), nil
	case PlaceAfter:
		target, err := ResolveStep(rb, place.Step)
		if err != nil {
			return "", err
		}
		if target.ID == movingID {
			return "", ErrSelfRelative
		}
		idx := stepIndex(steps, target.ID)
		next := ""
		if idx < len(steps)-1 {
			next = steps[idx+1].Position
		}
		return model.PositionBetween(target.Position, next), nil
	default:
		prev := ""
		if len(steps) > 0 {
			prev = steps[len(steps)-1].Position
		}
		return model.PositionBetween(prev, ""), nil
	}
}

// stepsExcluding returns rb's steps with excludeID dropped, preserving the folded
// (Position, ID) order; an empty excludeID returns the steps unchanged.
func stepsExcluding(steps []model.RunbookStep, excludeID string) []model.RunbookStep {
	if excludeID == "" {
		return steps
	}
	out := make([]model.RunbookStep, 0, len(steps))
	for _, st := range steps {
		if st.ID != excludeID {
			out = append(out, st)
		}
	}
	return out
}

// stepIndex returns the position of the step with id in steps, or -1.
func stepIndex(steps []model.RunbookStep, id string) int {
	for i, st := range steps {
		if st.ID == id {
			return i
		}
	}
	return -1
}

// runCandidates renders an ambiguity list of runs as "<id7> <status> <date>"
// joined by "; ".
func runCandidates(runs []model.RunbookRun) string {
	var b strings.Builder
	for i, r := range runs {
		if i > 0 {
			b.WriteString("; ")
		}
		fmt.Fprintf(&b, "%s %s %s", render.ShortWireID(r.ID), r.Status, dateUTC(r.StartedAt))
	}
	return b.String()
}

// dateUTC renders a unix timestamp as a UTC calendar date.
func dateUTC(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02") }
