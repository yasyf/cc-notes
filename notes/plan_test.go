package notes_test

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

const planBody = "## Context\nthe approved plan, verbatim.\n\n## Approach\n1. do the thing\n"

// mustPlan creates a plan from spec, failing on error, and returns the folded
// snapshot.
func mustPlan(t *testing.T, c *notes.Client, spec notes.PlanSpec) model.Plan {
	t.Helper()
	plan, _, err := c.CreatePlan(t.Context(), spec)
	if err != nil {
		t.Fatalf("CreatePlan(%q): %v", spec.Title, err)
	}
	return plan
}

// planCommits counts the commits in the plan's ref chain, so a transition verb
// can be proved to land its ops in exactly one pack commit.
func planCommits(t *testing.T, dir string, id model.EntityID) int {
	t.Helper()
	out := gittest.Git(t, dir, "rev-list", "--count", "refs/cc-notes/plans/"+string(id))
	n, err := strconv.Atoi(out)
	if err != nil {
		t.Fatalf("parse rev-list count %q: %v", out, err)
	}
	return n
}

// drivePlanTo creates a fresh plan and drives it to status through the legal
// verbs, returning its id. Each plan gets a unique body so repeated calls on one
// client never converge on a single deduped record.
func drivePlanTo(t *testing.T, c *notes.Client, status model.PlanStatus) model.EntityID {
	t.Helper()
	ctx := t.Context()
	plan := mustPlan(t, c, notes.PlanSpec{Title: "drive", Body: model.NewNonce()})
	step := func(_ model.Plan, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("drivePlanTo %s: %v", status, err)
		}
	}
	switch status {
	case model.PlanDraft:
	case model.PlanApproved:
		step(c.ApprovePlan(ctx, plan.ID))
	case model.PlanExecuting:
		step(c.ApprovePlan(ctx, plan.ID))
		step(c.StartPlan(ctx, plan.ID))
	case model.PlanDone:
		step(c.ApprovePlan(ctx, plan.ID))
		step(c.StartPlan(ctx, plan.ID))
		step(c.DonePlan(ctx, plan.ID, "shipped"))
	case model.PlanAbandoned:
		step(c.AbandonPlan(ctx, plan.ID, "superseded by a better idea"))
	default:
		t.Fatalf("drivePlanTo: unknown status %q", status)
	}
	return plan.ID
}

func TestCreatePlanBornStatus(t *testing.T) {
	cases := []struct {
		name string
		spec notes.PlanSpec
		want model.PlanStatus
		err  error
	}{
		{"empty status is born draft", notes.PlanSpec{Title: "t", Body: planBody}, model.PlanDraft, nil},
		{"explicit draft", notes.PlanSpec{Title: "t", Body: planBody, Status: model.PlanDraft}, model.PlanDraft, nil},
		{"approved rides the create op", notes.PlanSpec{Title: "t", Body: planBody, Status: model.PlanApproved}, model.PlanApproved, nil},
		{"empty title", notes.PlanSpec{Body: planBody}, "", notes.ErrEmptyTitle},
		{"empty body", notes.PlanSpec{Title: "t"}, "", notes.ErrEmptyBody},
		{"executing is not a born status", notes.PlanSpec{Title: "t", Body: planBody, Status: model.PlanExecuting}, "", model.ErrInvalidValue},
		{"done is not a born status", notes.PlanSpec{Title: "t", Body: planBody, Status: model.PlanDone}, "", model.ErrInvalidValue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, dir := newClient(t)
			// A unique body keeps the cases from deduping onto one another.
			if tc.spec.Body == planBody {
				tc.spec.Body = planBody + tc.name
			}
			plan, _, err := c.CreatePlan(t.Context(), tc.spec)
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("CreatePlan = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("CreatePlan: %v", err)
			}
			if plan.Status != tc.want {
				t.Errorf("status = %q, want %q", plan.Status, tc.want)
			}
			if plan.Body != tc.spec.Body {
				t.Errorf("body = %q, want %q", plan.Body, tc.spec.Body)
			}
			// The born status must ride on the create op itself: a second op in
			// the pack would be silently discarded by a dedupe.
			if got := planCommits(t, dir, plan.ID); got != 1 {
				t.Errorf("create wrote %d commits, want 1", got)
			}
		})
	}
}

// planVerb is one lifecycle verb under test, curried so the two closing verbs'
// outcome argument does not leak into the table.
type planVerb func(*notes.Client, context.Context, model.EntityID) (model.Plan, error)

var (
	planApprove planVerb = (*notes.Client).ApprovePlan
	planStart   planVerb = (*notes.Client).StartPlan
	planDone    planVerb = func(c *notes.Client, ctx context.Context, id model.EntityID) (model.Plan, error) {
		return c.DonePlan(ctx, id, "outcome")
	}
	planAbandon planVerb = func(c *notes.Client, ctx context.Context, id model.EntityID) (model.Plan, error) {
		return c.AbandonPlan(ctx, id, "")
	}
)

func TestPlanTransitions(t *testing.T) {
	cases := []struct {
		name  string
		from  model.PlanStatus
		verb  planVerb
		to    model.PlanStatus
		legal bool
	}{
		{"draft approves", model.PlanDraft, planApprove, model.PlanApproved, true},
		{"draft abandons", model.PlanDraft, planAbandon, model.PlanAbandoned, true},
		{"draft cannot start", model.PlanDraft, planStart, "", false},
		{"draft cannot finish", model.PlanDraft, planDone, "", false},
		{"approved starts", model.PlanApproved, planStart, model.PlanExecuting, true},
		{"approved abandons", model.PlanApproved, planAbandon, model.PlanAbandoned, true},
		{"approved cannot re-approve", model.PlanApproved, planApprove, "", false},
		{"approved cannot finish", model.PlanApproved, planDone, "", false},
		{"executing finishes", model.PlanExecuting, planDone, model.PlanDone, true},
		{"executing abandons", model.PlanExecuting, planAbandon, model.PlanAbandoned, true},
		{"executing cannot restart", model.PlanExecuting, planStart, "", false},
		{"executing cannot re-approve", model.PlanExecuting, planApprove, "", false},
		{"done reopens", model.PlanDone, planStart, model.PlanExecuting, true},
		{"done cannot re-finish", model.PlanDone, planDone, "", false},
		{"done cannot abandon", model.PlanDone, planAbandon, "", false},
		{"abandoned reopens", model.PlanAbandoned, planStart, model.PlanExecuting, true},
		{"abandoned cannot approve", model.PlanAbandoned, planApprove, "", false},
	}
	c, _ := newClient(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := drivePlanTo(t, c, tc.from)
			plan, err := tc.verb(c, t.Context(), id)
			if !tc.legal {
				if !errors.Is(err, notes.ErrIllegalTransition) {
					t.Fatalf("%s from %s = %v, want ErrIllegalTransition", tc.name, tc.from, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("%s from %s: %v", tc.name, tc.from, err)
			}
			if plan.Status != tc.to {
				t.Errorf("status = %q, want %q", plan.Status, tc.to)
			}
		})
	}
}

func TestPlanCloseStampsOutcomeInOnePack(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	id := drivePlanTo(t, c, model.PlanExecuting)
	before := planCommits(t, dir, id)

	plan, err := c.DonePlan(ctx, id, "landed in 5733259")
	if err != nil {
		t.Fatalf("DonePlan: %v", err)
	}
	if plan.Outcome != "landed in 5733259" {
		t.Errorf("outcome = %q, want the recorded text", plan.Outcome)
	}
	if plan.ClosedAt == 0 || plan.ClosedBy != model.Actor(testActor) {
		t.Errorf("closed = %d by %q, want a stamp by %q", plan.ClosedAt, plan.ClosedBy, testActor)
	}
	if plan.StartedAt == 0 {
		t.Error("started_at = 0, want the executing stamp to survive the close")
	}
	if got := planCommits(t, dir, id) - before; got != 1 {
		t.Errorf("done wrote %d commits, want the outcome and status in 1", got)
	}

	reopened, err := c.StartPlan(ctx, id)
	if err != nil {
		t.Fatalf("StartPlan (reopen): %v", err)
	}
	if reopened.ClosedAt != 0 || reopened.ClosedBy != "" {
		t.Errorf("reopened closed = %d by %q, want both cleared", reopened.ClosedAt, reopened.ClosedBy)
	}
	if reopened.Outcome != "landed in 5733259" {
		t.Errorf("reopened outcome = %q, want the recorded text to survive", reopened.Outcome)
	}
}

func TestPlanTasksInvertsTaskPointers(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	plan := mustPlan(t, c, notes.PlanSpec{Title: "the plan", Body: planBody})
	other := mustPlan(t, c, notes.PlanSpec{Title: "another plan", Body: planBody + "2"})

	member, err := c.CreateTask(ctx, notes.TaskSpec{Title: "step one", Plan: plan.ID})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if member.Task.Plan != plan.ID {
		t.Fatalf("task plan = %q, want %q", member.Task.Plan, plan.ID)
	}
	stray, err := c.CreateTask(ctx, notes.TaskSpec{Title: "unrelated"})
	if err != nil {
		t.Fatalf("CreateTask (stray): %v", err)
	}

	got, err := c.PlanTasks(ctx, plan.ID)
	if err != nil {
		t.Fatalf("PlanTasks: %v", err)
	}
	if len(got) != 1 || got[0] != member.Task.ID {
		t.Fatalf("PlanTasks = %v, want [%s]", got, member.Task.ID)
	}
	if empty, err := c.PlanTasks(ctx, other.ID); err != nil || len(empty) != 0 {
		t.Fatalf("PlanTasks(other) = %v/%v, want no members", empty, err)
	}

	moved := stray.Task.ID
	if _, err := c.EditTask(ctx, moved, notes.TaskEdit{Plan: &plan.ID}); err != nil {
		t.Fatalf("EditTask --plan: %v", err)
	}
	got, err = c.PlanTasks(ctx, plan.ID)
	if err != nil {
		t.Fatalf("PlanTasks after edit: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("PlanTasks after edit = %v, want both tasks", got)
	}

	var cleared model.EntityID
	if _, err := c.EditTask(ctx, moved, notes.TaskEdit{Plan: &cleared}); err != nil {
		t.Fatalf("EditTask --no-plan: %v", err)
	}
	got, err = c.PlanTasks(ctx, plan.ID)
	if err != nil {
		t.Fatalf("PlanTasks after clear: %v", err)
	}
	if len(got) != 1 || got[0] != member.Task.ID {
		t.Errorf("PlanTasks after clear = %v, want [%s]", got, member.Task.ID)
	}
}

func TestPlansFilterAndSearch(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	perf := mustPlan(t, c, notes.PlanSpec{Title: "monorepo read performance", Body: "walk the pack once", Labels: []string{"perf"}})
	kind := mustPlan(t, c, notes.PlanSpec{Title: "the plan kind", Body: "a ninth entity kind", Status: model.PlanApproved})

	all, err := c.Plans(ctx, notes.PlanFilter{})
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Plans() = %d records, want 2", len(all))
	}

	approved, err := c.Plans(ctx, notes.PlanFilter{Statuses: []model.PlanStatus{model.PlanApproved}})
	if err != nil {
		t.Fatalf("Plans(approved): %v", err)
	}
	if len(approved) != 1 || approved[0].ID != kind.ID {
		t.Errorf("Plans(approved) = %v, want [%s]", approved, kind.ID)
	}

	labeled, err := c.Plans(ctx, notes.PlanFilter{Labels: []string{"perf"}})
	if err != nil {
		t.Fatalf("Plans(label): %v", err)
	}
	if len(labeled) != 1 || labeled[0].ID != perf.ID {
		t.Errorf("Plans(label perf) = %v, want [%s]", labeled, perf.ID)
	}

	hits, err := c.SearchPlans(ctx, "ninth entity", notes.SearchFilter{Limit: -1})
	if err != nil {
		t.Fatalf("SearchPlans: %v", err)
	}
	if len(hits) != 1 || hits[0].ID != kind.ID {
		t.Errorf("SearchPlans(body) = %v, want [%s]", hits, kind.ID)
	}
}

// TestPlanAnchorsResolveFilterAndDetach covers the kind-agnostic anchor surface
// on a plan: a create resolves an abbreviated commit revision to its full sha,
// PlanFilter narrows by each anchor kind, and PlanEdit detaches one verbatim.
func TestPlanAnchorsResolveFilterAndDetach(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	head := commitFile(t, dir, "internal/fold/fold.go", "package fold\n")

	anchored := mustPlan(t, c, notes.PlanSpec{
		Title: "the plan kind", Body: planBody,
		Anchors: notes.AnchorSpec{
			Commits:  []string{string(head)[:8]},
			Paths:    []string{"internal/fold/fold.go"},
			Dirs:     []string{"internal/fold"},
			Branches: []string{"main"},
		},
	})
	other := mustPlan(t, c, notes.PlanSpec{
		Title: "unrelated", Body: "no anchors here",
		Anchors: notes.AnchorSpec{Paths: []string{"internal/store/write.go"}},
	})

	want := []model.Anchor{
		{Kind: model.AnchorBranch, Value: "main"},
		{Kind: model.AnchorCommit, Value: string(head)},
		{Kind: model.AnchorDir, Value: "internal/fold"},
		{Kind: model.AnchorPath, Value: "internal/fold/fold.go"},
	}
	if !slices.Equal(anchored.Anchors, want) {
		t.Fatalf("anchors = %v, want %v (the abbreviated commit resolved to its full sha)", anchored.Anchors, want)
	}

	for _, tc := range []struct {
		name   string
		filter notes.AnchorFilter
	}{
		{"commit", notes.AnchorFilter{Commit: string(head)}},
		{"path", notes.AnchorFilter{Path: "internal/fold/fold.go"}},
		{"dir", notes.AnchorFilter{Dir: "internal/fold"}},
		{"branch", notes.AnchorFilter{Branch: "main"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.Plans(ctx, notes.PlanFilter{Anchors: tc.filter})
			if err != nil {
				t.Fatalf("Plans: %v", err)
			}
			if len(got) != 1 || got[0].ID != anchored.ID {
				t.Errorf("Plans(%+v) = %v, want only %s (not %s)", tc.filter, got, anchored.ID, other.ID)
			}
		})
	}

	detached, err := c.EditPlan(ctx, anchored.ID, notes.PlanEdit{
		RemoveAnchors: notes.AnchorSpec{Dirs: []string{"internal/fold"}},
	})
	if err != nil {
		t.Fatalf("EditPlan(remove anchor): %v", err)
	}
	if slices.Contains(detached.Anchors, model.Anchor{Kind: model.AnchorDir, Value: "internal/fold"}) {
		t.Errorf("anchors = %v, want the dir anchor removed", detached.Anchors)
	}
	if len(detached.Anchors) != 3 {
		t.Errorf("anchors = %v, want the other three untouched", detached.Anchors)
	}
}

func TestStatusCountsInFlightPlans(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	for _, status := range []model.PlanStatus{model.PlanDraft, model.PlanApproved, model.PlanExecuting, model.PlanDone, model.PlanAbandoned} {
		drivePlanTo(t, c, status)
	}

	report, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if report.Plans != 3 {
		t.Errorf("Status().Plans = %d, want 3 (draft, approved, executing)", report.Plans)
	}
}

func TestSupersedePlanIsAnEdgeNotAStatus(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	old := drivePlanTo(t, c, model.PlanDone)
	next := mustPlan(t, c, notes.PlanSpec{Title: "v2", Body: planBody + "v2"})

	superseded, err := c.SupersedePlan(ctx, old, next.ID)
	if err != nil {
		t.Fatalf("SupersedePlan: %v", err)
	}
	if len(superseded.SupersededBy) != 1 || superseded.SupersededBy[0] != next.ID {
		t.Fatalf("superseded_by = %v, want [%s]", superseded.SupersededBy, next.ID)
	}
	if superseded.Status != model.PlanDone {
		t.Errorf("status = %q, want the lifecycle status untouched by supersession", superseded.Status)
	}

	// A superseded plan drops out of the listing, matching every other kind.
	listed, err := c.Plans(ctx, notes.PlanFilter{})
	if err != nil {
		t.Fatalf("Plans: %v", err)
	}
	for _, p := range listed {
		if p.ID == old {
			t.Fatalf("Plans() still lists superseded %s", old.Short())
		}
	}

	if _, err := c.UnsupersedePlan(ctx, old, next.ID); err != nil {
		t.Fatalf("UnsupersedePlan: %v", err)
	}
	restored, err := c.Plan(ctx, old)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(restored.SupersededBy) != 0 {
		t.Errorf("superseded_by = %v, want empty after clear", restored.SupersededBy)
	}
}

func TestEditPlanOverwritesBodyAndRefusesEmptyMask(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	plan := mustPlan(t, c, notes.PlanSpec{Title: "the plan", Body: planBody, Labels: []string{"stale"}})

	if _, err := c.EditPlan(ctx, plan.ID, notes.PlanEdit{}); !errors.Is(err, notes.ErrEmptyEdit) {
		t.Fatalf("EditPlan(empty mask) = %v, want ErrEmptyEdit", err)
	}

	revised := "## Context\nthe revised plan\n"
	outcome := "not started"
	edited, err := c.EditPlan(ctx, plan.ID, notes.PlanEdit{
		Body:         &revised,
		Outcome:      &outcome,
		AddLabels:    []string{"perf"},
		RemoveLabels: []string{"stale"},
		AddAnchors:   notes.AnchorSpec{Paths: []string{"internal/fold"}},
	})
	if err != nil {
		t.Fatalf("EditPlan: %v", err)
	}
	if edited.Body != revised {
		t.Errorf("body = %q, want the revision (Body is LWW)", edited.Body)
	}
	if edited.Outcome != outcome {
		t.Errorf("outcome = %q, want %q", edited.Outcome, outcome)
	}
	if len(edited.Labels) != 1 || edited.Labels[0] != "perf" {
		t.Errorf("labels = %v, want [perf]", edited.Labels)
	}
	want := model.Anchor{Kind: model.AnchorPath, Value: "internal/fold"}
	if len(edited.Anchors) != 1 || edited.Anchors[0] != want {
		t.Errorf("anchors = %v, want [%v]", edited.Anchors, want)
	}
}

func TestEditPlanRefusesEmptyBody(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	plan := mustPlan(t, c, notes.PlanSpec{Title: "the plan", Body: planBody})

	blank := ""
	if _, err := c.EditPlan(ctx, plan.ID, notes.PlanEdit{Body: &blank}); !errors.Is(err, notes.ErrEmptyBody) {
		t.Fatalf("EditPlan(empty body) = %v, want ErrEmptyBody", err)
	}
	reread, err := c.Plan(ctx, plan.ID)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if reread.Body != planBody {
		t.Errorf("body = %q, want the approved text %q untouched", reread.Body, planBody)
	}

	cleared, err := c.EditPlan(ctx, plan.ID, notes.PlanEdit{Outcome: &blank})
	if err != nil {
		t.Fatalf("EditPlan(empty outcome): %v", err)
	}
	if cleared.Outcome != "" {
		t.Errorf("outcome = %q, want an outcome still clearable", cleared.Outcome)
	}
}
