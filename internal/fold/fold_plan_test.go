package fold_test

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

// TestFoldPlan folds a plan through its whole lifecycle — draft born status,
// approve, execute, revise the body, close with an outcome, then supersede —
// and pins every folded field, including the shared bundles (label, anchor,
// comment, supersede) the plan folder chains before its own op cases. A folder
// that forgot to chain them would leave the bundle ops skipped, so SkippedOps
// is asserted at zero.
func TestFoldPlan(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreatePlan{
			Nonce:   "n",
			Title:   "Add the plan kind",
			Body:    "## Context\ndraft",
			Status:  model.PlanDraft,
			Labels:  []string{"core"},
			Anchors: []model.Anchor{{Kind: model.AnchorDir, Value: "internal/fold"}},
		}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2,
			model.AddComment{Body: "approved as written"},
			model.AddLabel{Label: "model"},
			model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "model/kind.go"}},
			model.SetPlanStatus{Status: model.PlanApproved},
		),
		mk("ccc", []string{"bbb"}, "carol", 300, 3,
			model.SetBody{Body: "## Context\napproved text"},
			model.SetPlanStatus{Status: model.PlanExecuting},
		),
		mk("ddd", []string{"ccc"}, "dave", 400, 4,
			model.SetPlanOutcome{Outcome: "shipped in v0.51.0"},
			model.SetPlanStatus{Status: model.PlanDone},
		),
		mk("eee", []string{"ddd"}, "erin", 500, 5, model.AddSupersededBy{ID: "plan2"}),
	}
	want := model.Plan{
		ID:      "aaa",
		Title:   "Add the plan kind",
		Body:    "## Context\napproved text",
		Status:  model.PlanDone,
		Outcome: "shipped in v0.51.0",
		Labels:  []string{"core", "model"},
		Comments: []model.Comment{
			{Author: "bob", TS: 200, Body: "approved as written"},
		},
		Anchors: []model.Anchor{
			{Kind: model.AnchorDir, Value: "internal/fold"},
			{Kind: model.AnchorPath, Value: "model/kind.go"},
		},
		SupersededBy: []model.EntityID{"plan2"},
		Author:       "alice",
		CreatedAt:    100,
		UpdatedAt:    500,
		StartedAt:    300,
		ClosedAt:     400,
		ClosedBy:     "dave",
		Head:         "eee",
	}
	got, err := fold.Plan(chain)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Plan =\n%#v\nwant\n%#v", got, want)
	}
	if got.SkippedOps != 0 {
		t.Fatalf("SkippedOps = %d, want 0: the folder skipped an op it should apply", got.SkippedOps)
	}
}

// TestFoldPlanStatusStamps pins the lifecycle stamping applyPlanStatus owns:
// StartedAt on every entry into executing, ClosedAt/ClosedBy from the commit
// carrying a terminal status, and both cleared on reopen.
func TestFoldPlanStatusStamps(t *testing.T) {
	cases := []struct {
		name      string
		after     []model.PlanStatus
		wantStart int64
		wantClose int64
		wantBy    model.Actor
	}{
		{"approved is not started", []model.PlanStatus{model.PlanApproved}, 0, 0, ""},
		{"executing stamps started", []model.PlanStatus{model.PlanApproved, model.PlanExecuting}, 300, 0, ""},
		{"done stamps closed", []model.PlanStatus{model.PlanExecuting, model.PlanDone}, 200, 300, "actor300"},
		{"abandoned stamps closed", []model.PlanStatus{model.PlanExecuting, model.PlanAbandoned}, 200, 300, "actor300"},
		{"reopen clears closed and restamps started", []model.PlanStatus{model.PlanExecuting, model.PlanDone, model.PlanExecuting}, 400, 0, ""},
		// The fold is total: it replays whatever a chain records, including the
		// transitions notes.ensurePlanTransition refuses to write. A fold that
		// grew a legality check would reject history an older binary wrote.
		{"draft straight to done", []model.PlanStatus{model.PlanDone}, 0, 200, "actor200"},
		{"executing back to draft", []model.PlanStatus{model.PlanExecuting, model.PlanDraft}, 200, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chain := []model.PackCommit{
				mk("c0", nil, "alice", 100, 1, model.CreatePlan{Nonce: "n", Status: model.PlanDraft}),
			}
			for i, status := range tc.after {
				at := int64(200 + i*100)
				chain = append(chain, mk(
					fmt.Sprintf("c%d", i+1),
					[]string{string(chain[len(chain)-1].SHA)},
					fmt.Sprintf("actor%d", at),
					at, uint64(i+2),
					model.SetPlanStatus{Status: status},
				))
			}
			got, err := fold.Plan(chain)
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if got.StartedAt != tc.wantStart {
				t.Errorf("StartedAt = %d, want %d", got.StartedAt, tc.wantStart)
			}
			if got.ClosedAt != tc.wantClose {
				t.Errorf("ClosedAt = %d, want %d", got.ClosedAt, tc.wantClose)
			}
			if got.ClosedBy != tc.wantBy {
				t.Errorf("ClosedBy = %q, want %q", got.ClosedBy, tc.wantBy)
			}
		})
	}
}

// TestFoldPlanConcurrentBranchesConverge folds a plan two replicas edited
// concurrently and joined by a union merge. The two edits carry the same
// lamport and author time, so the sha tiebreak decides: bbb linearizes last and
// its body and status win, while the set-valued fields union instead of
// overwriting. The op-less merge commit carries the head without touching
// UpdatedAt. Every input order must produce the same snapshot.
func TestFoldPlanConcurrentBranchesConverge(t *testing.T) {
	c0 := mk("c0", nil, "root", 100, 1, model.CreatePlan{Nonce: "n", Title: "T", Body: "draft", Status: model.PlanApproved})
	a1 := mk("aaa", []string{"c0"}, "alice", 200, 2,
		model.SetBody{Body: "alice's approach"},
		model.SetPlanStatus{Status: model.PlanAbandoned},
		model.AddLabel{Label: "alice"},
		model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "a.go"}},
		model.AddComment{Body: "from alice"},
	)
	b1 := mk("bbb", []string{"c0"}, "bob", 200, 2,
		model.SetBody{Body: "bob's approach"},
		model.SetPlanStatus{Status: model.PlanExecuting},
		model.AddLabel{Label: "bob"},
		model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "b.go"}},
		model.AddComment{Body: "from bob"},
	)
	merge := mk("mmm", []string{"aaa", "bbb"}, "merger", 300, 3)
	want := model.Plan{
		ID:     "c0",
		Title:  "T",
		Body:   "bob's approach",
		Status: model.PlanExecuting,
		Labels: []string{"alice", "bob"},
		Comments: []model.Comment{
			{Author: "alice", TS: 200, Body: "from alice"},
			{Author: "bob", TS: 200, Body: "from bob"},
		},
		Anchors: []model.Anchor{
			{Kind: model.AnchorPath, Value: "a.go"},
			{Kind: model.AnchorPath, Value: "b.go"},
		},
		SupersededBy: []model.EntityID{},
		Author:       "root",
		CreatedAt:    100,
		UpdatedAt:    200,
		StartedAt:    200,
		Head:         "mmm",
	}
	chain := []model.PackCommit{c0, a1, b1, merge}
	for _, order := range [][]model.PackCommit{
		chain,
		{merge, b1, a1, c0},
		{b1, c0, merge, a1},
	} {
		got, err := fold.Plan(order)
		if err != nil {
			t.Fatalf("Plan(%v): %v", shas(order), err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Plan(%v) =\n%#v\nwant\n%#v", shas(order), got, want)
		}
	}
}

// TestFoldTaskPlanPointer pins the membership edge's only stored direction: an
// LWW pointer on the task that a later SetPlan overwrites and an empty one
// clears.
func TestFoldTaskPlanPointer(t *testing.T) {
	root := mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Type: model.TypeTask, Branch: "main"})
	cases := []struct {
		name string
		ops  []model.Op
		want model.EntityID
	}{
		{"unset", nil, ""},
		{"set", []model.Op{model.SetPlan{Plan: "plan1"}}, "plan1"},
		{"last write wins", []model.Op{model.SetPlan{Plan: "plan1"}, model.SetPlan{Plan: "plan2"}}, "plan2"},
		{"empty clears", []model.Op{model.SetPlan{Plan: "plan1"}, model.SetPlan{Plan: ""}}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chain := []model.PackCommit{root}
			if len(tc.ops) > 0 {
				chain = append(chain, mk("bbb", []string{"aaa"}, "bob", 200, 2, tc.ops...))
			}
			got, err := fold.Task(chain)
			if err != nil {
				t.Fatalf("Task: %v", err)
			}
			if got.Plan != tc.want {
				t.Fatalf("Plan = %q, want %q", got.Plan, tc.want)
			}
			if got.SkippedOps != 0 {
				t.Fatalf("SkippedOps = %d, want 0: set_plan must apply to a task", got.SkippedOps)
			}
		})
	}
}
