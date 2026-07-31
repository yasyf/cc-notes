package fold

import (
	"fmt"
	"slices"

	"github.com/yasyf/cc-notes/model"
)

type planFolder struct {
	plan       model.Plan
	labels     map[string]bool
	anchors    map[model.Anchor]bool
	superseded map[model.EntityID]bool
}

func newPlanFolder() *planFolder {
	return &planFolder{
		labels:     map[string]bool{},
		anchors:    map[model.Anchor]bool{},
		superseded: map[model.EntityID]bool{},
	}
}

func foldPlan(ordered []model.PackCommit, m mode) (model.Plan, error) {
	return run[model.Plan](ordered, newPlanFolder(), m)
}

func (f *planFolder) fresh(sha model.SHA, createdAt int64) {
	f.plan = model.Plan{ID: model.EntityID(sha), CreatedAt: createdAt, Comments: []model.Comment{}}
}

func (f *planFolder) seed(state model.Snapshot) error {
	seed, ok := state.(model.Plan)
	if !ok {
		return fmt.Errorf("%w: checkpoint over a non-plan folded as a plan", ErrKindMismatch)
	}
	f.plan = seed
	f.plan.Comments = slices.Clone(seed.Comments)
	for _, l := range seed.Labels {
		f.labels[l] = true
	}
	for _, a := range seed.Anchors {
		f.anchors[a] = true
	}
	for _, id := range seed.SupersededBy {
		f.superseded[id] = true
	}
	return nil
}

func (f *planFolder) create(op model.CreateOp, author model.Actor) error {
	o, ok := op.(model.CreatePlan)
	if !ok {
		return fmt.Errorf("%w: %s chain folded as a plan", ErrKindMismatch, op.OpKind())
	}
	f.plan.Title, f.plan.Body, f.plan.Author = o.Title, o.Body, author
	f.plan.Status = o.Status
	for _, l := range o.Labels {
		f.labels[l] = true
	}
	for _, a := range o.Anchors {
		f.anchors[a] = true
	}
	return nil
}

func (f *planFolder) apply(op model.Op, c model.PackCommit) bool {
	if applyLabel(f.labels, op) || applyAnchor(f.anchors, op) ||
		applySupersede(f.superseded, op) || applyComment(&f.plan.Comments, op, c) {
		return true
	}
	switch o := op.(type) {
	case model.SetTitle:
		f.plan.Title = o.Title
	case model.SetBody:
		f.plan.Body = o.Body
	case model.SetPlanStatus:
		applyPlanStatus(&f.plan, o.Status, c.Author, c.AuthorTime)
	case model.SetPlanOutcome:
		f.plan.Outcome = o.Outcome
	case model.DeleteNote:
		f.plan.Deleted = true
	default:
		return false
	}
	return true
}

func (f *planFolder) touch(c model.PackCommit) {
	f.plan.UpdatedAt = c.AuthorTime
}

func (f *planFolder) finalize(head model.SHA, skipped int) model.Plan {
	f.plan.Labels = sortedKeys(f.labels)
	f.plan.Anchors = sortedAnchors(f.anchors)
	f.plan.SupersededBy = sortedKeys(f.superseded)
	f.plan.Head = head
	f.plan.SkippedOps = skipped
	return f.plan
}

// applyPlanStatus sets the LWW status, stamping StartedAt on every entry into
// executing (a reopen restarts the clock) and ClosedAt/ClosedBy from the
// carrying commit when the status is terminal, zeroing both otherwise. The fold
// is total: it never checks transition legality.
func applyPlanStatus(p *model.Plan, status model.PlanStatus, by model.Actor, at int64) {
	if status == model.PlanExecuting && p.Status != model.PlanExecuting {
		p.StartedAt = at
	}
	p.Status = status
	switch status {
	case model.PlanDone, model.PlanAbandoned:
		p.ClosedAt = at
		p.ClosedBy = by
	default:
		p.ClosedAt = 0
		p.ClosedBy = ""
	}
}
