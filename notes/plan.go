package notes

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// PlanSpec is the input to CreatePlan. Title and Body are required — a plan is
// its body, the approved text carried verbatim. Status is the born status; the
// zero value means draft, and only draft and approved are legal. It rides on the
// create op rather than a trailing SetPlanStatus because a create pack that
// converges on a live plan is discarded whole, taking any trailing op with it.
// Anchors are attached in commit, path, dir, then branch order; commit values
// are resolved to full shas at write time.
type PlanSpec struct {
	Title   string
	Body    string
	Status  model.PlanStatus
	Labels  []string
	Anchors AnchorSpec
}

// PlanEdit is the field mask for EditPlan: a nil Title, Body, or Outcome leaves
// the field untouched, a non-nil pointer sets it; the label and anchor slices
// apply in order (RemoveAnchors is matched verbatim). Status is deliberately
// absent — the lifecycle moves only through the transition verbs, which gate
// legality. An all-empty mask is ErrEmptyEdit.
type PlanEdit struct {
	Title         *string
	Body          *string
	Outcome       *string
	AddLabels     []string
	RemoveLabels  []string
	AddAnchors    AnchorSpec
	RemoveAnchors AnchorSpec
}

// empty reports whether the mask sets nothing.
func (e PlanEdit) empty() bool {
	return e.Title == nil && e.Body == nil && e.Outcome == nil &&
		len(e.AddLabels) == 0 && len(e.RemoveLabels) == 0 &&
		e.AddAnchors.isEmpty() && e.RemoveAnchors.isEmpty()
}

// PlanFilter narrows a plan listing. The zero value matches every live plan.
// Statuses, when non-empty, keeps only plans whose status is in the set; Labels
// are ANDed; Anchors constrains to plans carrying the given anchor.
type PlanFilter struct {
	Statuses []model.PlanStatus
	Labels   []string
	Anchors  AnchorFilter
}

// CreatePlan roots a plan from spec and returns its folded snapshot. Title and
// Body are required — an empty one is ErrEmptyTitle or ErrEmptyBody. An empty
// Status is born draft; a status other than draft or approved fails validation
// before any write. The returned bool reports that Create's best-effort
// duplicate guard converged on an existing plan rather than rooting a new one.
// Plans carry no freshness lifecycle, so the create is verify-free.
func (c *Client) CreatePlan(ctx context.Context, spec PlanSpec) (model.Plan, bool, error) {
	if spec.Title == "" {
		return model.Plan{}, false, ErrEmptyTitle
	}
	if spec.Body == "" {
		return model.Plan{}, false, ErrEmptyBody
	}
	status := spec.Status
	if status == "" {
		status = model.PlanDraft
	}
	anchors, err := c.resolveAnchors(ctx, spec.Anchors)
	if err != nil {
		return model.Plan{}, false, err
	}
	snap, err := c.s.Create(ctx, []model.Op{model.CreatePlan{
		Nonce:   model.NewNonce(),
		Title:   spec.Title,
		Body:    spec.Body,
		Status:  status,
		Labels:  spec.Labels,
		Anchors: anchors,
	}})
	reused := false
	var dup *store.DuplicateError
	if errors.As(err, &dup) {
		snap, reused = dup.Existing, true
	} else if err != nil {
		return model.Plan{}, false, err
	}
	return snap.(model.Plan), reused, nil
}

// Plans folds the plan set the filter selects and returns the live records in
// UpdatedAt-desc, id-asc order — tombstoned and superseded ones stay hidden.
func (c *Client) Plans(ctx context.Context, f PlanFilter) ([]model.Plan, error) {
	plans, err := c.s.ListPlans(ctx)
	if err != nil {
		return nil, err
	}
	plans = slices.DeleteFunc(plans, func(p model.Plan) bool {
		if len(f.Statuses) > 0 && !slices.Contains(f.Statuses, p.Status) {
			return true
		}
		return !hasAll(p.Labels, f.Labels) || !matchesAnchorFilter(p.Anchors, f.Anchors)
	})
	sortDocuments(plans)
	return plans, nil
}

// EditPlan applies the mask to the plan. An all-empty mask is ErrEmptyEdit and
// a Body set to the empty string is ErrEmptyBody — a plan is its body on every
// write path, not just the create. AddAnchors' commits are resolved first, so a
// bad revision mutates nothing. Ops apply in title, body, outcome, add-label,
// remove-label, add-anchor, remove-anchor order. Body is LWW, so a revision
// overwrites the recorded plan and the previous text survives only in history.
func (c *Client) EditPlan(ctx context.Context, id model.EntityID, edit PlanEdit) (model.Plan, error) {
	if edit.empty() {
		return model.Plan{}, ErrEmptyEdit
	}
	if edit.Body != nil && *edit.Body == "" {
		return model.Plan{}, ErrEmptyBody
	}
	addAnchors, err := c.resolveAnchors(ctx, edit.AddAnchors)
	if err != nil {
		return model.Plan{}, err
	}
	var ops []model.Op
	if edit.Title != nil {
		ops = append(ops, model.SetTitle{Title: *edit.Title})
	}
	if edit.Body != nil {
		ops = append(ops, model.SetBody{Body: *edit.Body})
	}
	if edit.Outcome != nil {
		ops = append(ops, model.SetPlanOutcome{Outcome: *edit.Outcome})
	}
	for _, l := range edit.AddLabels {
		ops = append(ops, model.AddLabel{Label: l})
	}
	for _, l := range edit.RemoveLabels {
		ops = append(ops, model.RemoveLabel{Label: l})
	}
	ops = anchorEditOps(ops, addAnchors, edit.RemoveAnchors)
	return c.appendPlan(ctx, id, ops)
}

// RemovePlan tombstones the plan, returning the folded snapshot. DeleteNote is
// a soft tombstone — the ref survives, so the plan still resolves for show.
func (c *Client) RemovePlan(ctx context.Context, id model.EntityID) (model.Plan, error) {
	return c.appendPlan(ctx, id, []model.Op{model.DeleteNote{}})
}

// CommentPlan appends a comment to the plan — review notes on the approach,
// which never touch the recorded body.
func (c *Client) CommentPlan(ctx context.Context, id model.EntityID, body string) (model.Plan, error) {
	return c.appendPlan(ctx, id, []model.Op{model.AddComment{Body: body}})
}

// SearchPlans ranks the live plan set against query, filtered by the
// SearchFilter, and returns the top results by tier, then UpdatedAt descending,
// then id ascending. A plan matches when its title, a label, its body, or its
// outcome contains query.
func (c *Client) SearchPlans(ctx context.Context, query string, f SearchFilter) ([]model.Plan, error) {
	plans, err := c.s.ListPlans(ctx)
	if err != nil {
		return nil, err
	}
	return rankDocuments(plans, query, f, planRanker), nil
}

var planRanker = documentRanker[model.Plan]{
	tags:    func(p model.Plan) []string { return p.Labels },
	author:  func(p model.Plan) string { return string(p.Author) },
	anchors: func(p model.Plan) []model.Anchor { return p.Anchors },
	tier:    func(p model.Plan, q string) int { return textTier(p.Title, p.Labels, []string{p.Body, p.Outcome}, q) },
}

// ApprovePlan approves a drafted plan, the gate the executing lifecycle starts
// from. It is legal only from draft; anything else is ErrIllegalTransition.
func (c *Client) ApprovePlan(ctx context.Context, id model.EntityID) (model.Plan, error) {
	return c.transitionPlan(ctx, id, model.PlanApproved, "")
}

// StartPlan moves the plan into executing and stamps StartedAt. It is legal
// from approved and, as the reopen edge, from either terminal status; starting
// an already-executing plan is ErrIllegalTransition.
func (c *Client) StartPlan(ctx context.Context, id model.EntityID) (model.Plan, error) {
	return c.transitionPlan(ctx, id, model.PlanExecuting, "")
}

// DonePlan closes the plan as done in one pack commit — the optional outcome
// prose, then the status. It is legal only from executing.
func (c *Client) DonePlan(ctx context.Context, id model.EntityID, outcome string) (model.Plan, error) {
	return c.transitionPlan(ctx, id, model.PlanDone, outcome)
}

// AbandonPlan closes the plan as abandoned in one pack commit — the optional
// outcome prose, then the status. It is legal from any non-terminal status: a
// plan can be walked away from before it is ever approved or started.
func (c *Client) AbandonPlan(ctx context.Context, id model.EntityID, outcome string) (model.Plan, error) {
	return c.transitionPlan(ctx, id, model.PlanAbandoned, outcome)
}

// SupersedePlan records that the plan by replaces id — a replan approved once
// execution has started, not the in-place body edit a revision re-approved
// before work starts takes. Supersession is an edge, not a status, so a
// superseded plan keeps whatever lifecycle status it closed in. by must resolve
// to a live plan and is loaded to validate before the edge is written.
func (c *Client) SupersedePlan(ctx context.Context, id, by model.EntityID) (model.Plan, error) {
	if _, err := c.Plan(ctx, by); err != nil {
		return model.Plan{}, err
	}
	return c.appendPlan(ctx, id, []model.Op{model.AddSupersededBy{ID: by}})
}

// UnsupersedePlan clears the edge recording that by replaces id.
func (c *Client) UnsupersedePlan(ctx context.Context, id, by model.EntityID) (model.Plan, error) {
	if _, err := c.Plan(ctx, by); err != nil {
		return model.Plan{}, err
	}
	return c.appendPlan(ctx, id, []model.Op{model.RemoveSupersededBy{ID: by}})
}

// transitionPlan moves the plan to target in one pack commit: the outcome prose
// when given, then the LWW status. Legality is gated here at op-build time so
// the fold stays total and deterministic.
func (c *Client) transitionPlan(ctx context.Context, id model.EntityID, target model.PlanStatus, outcome string) (model.Plan, error) {
	plan, err := c.Plan(ctx, id)
	if err != nil {
		return model.Plan{}, err
	}
	if err := ensurePlanTransition(plan, target); err != nil {
		return model.Plan{}, err
	}
	var ops []model.Op
	if outcome != "" {
		ops = append(ops, model.SetPlanOutcome{Outcome: outcome})
	}
	ops = append(ops, model.SetPlanStatus{Status: target})
	return c.appendPlan(ctx, id, ops)
}

// legalPlanTransitions is the lifecycle machine: each status maps to the
// statuses a client verb may move it to. draft is approved or walked away from;
// approved starts executing; executing closes done or abandoned; either terminal
// status reopens into executing. Legality is enforced at op-build time so the
// fold stays total. The gate is best-effort against the snapshot the verb
// loaded: concurrent histories — including a CAS retry that re-folds a newer tip
// — are reconciled by LWW in fold by design, so two transitions each legal at
// load time may interleave and the fold picks one deterministic winner.
var legalPlanTransitions = map[model.PlanStatus][]model.PlanStatus{
	model.PlanDraft:     {model.PlanApproved, model.PlanAbandoned},
	model.PlanApproved:  {model.PlanExecuting, model.PlanAbandoned},
	model.PlanExecuting: {model.PlanDone, model.PlanAbandoned},
	model.PlanDone:      {model.PlanExecuting},
	model.PlanAbandoned: {model.PlanExecuting},
}

// ensurePlanTransition reports an ErrIllegalTransition, naming the current and
// requested status, unless the move is in the lifecycle machine.
func ensurePlanTransition(plan model.Plan, target model.PlanStatus) error {
	if !slices.Contains(legalPlanTransitions[plan.Status], target) {
		return fmt.Errorf("%w: %s cannot go %s→%s", ErrIllegalTransition, plan.ID.Short(), plan.Status, target)
	}
	return nil
}

// nonTerminalPlan reports whether a plan is still in flight — draft, approved,
// or executing — as opposed to closed (done or abandoned).
func nonTerminalPlan(status model.PlanStatus) bool {
	switch status {
	case model.PlanDone, model.PlanAbandoned:
		return false
	}
	return true
}

// appendPlan appends ops to the plan chain and returns the folded snapshot.
func (c *Client) appendPlan(ctx context.Context, id model.EntityID, ops []model.Op) (model.Plan, error) {
	snap, err := c.s.Append(ctx, refs.For(model.KindPlan, id), ops)
	if err != nil {
		return model.Plan{}, err
	}
	return snap.(model.Plan), nil
}
