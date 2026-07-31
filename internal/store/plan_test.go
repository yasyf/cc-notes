package store

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestCreatePlanRoundTrip(t *testing.T) {
	ops := []model.Op{model.CreatePlan{
		Nonce:   model.NewNonce(),
		Title:   "Add the plan kind",
		Body:    "## Context\nA plan is never durably recorded.\n",
		Status:  model.PlanApproved,
		Labels:  []string{"b", "a"},
		Anchors: []model.Anchor{{Kind: model.AnchorDir, Value: "model"}},
	}}
	s := initStore(t)
	snapshot := create(t, s, ops)
	plan, ok := snapshot.(model.Plan)
	if !ok {
		t.Fatalf("Create returned %T, want model.Plan", snapshot)
	}

	if plan.Title != "Add the plan kind" {
		t.Errorf("Title = %q, want %q", plan.Title, "Add the plan kind")
	}
	if want := "## Context\nA plan is never durably recorded.\n"; plan.Body != want {
		t.Errorf("Body = %q, want %q verbatim", plan.Body, want)
	}
	if plan.Status != model.PlanApproved {
		t.Errorf("Status = %q, want %q from the create op", plan.Status, model.PlanApproved)
	}
	if plan.StartedAt != 0 || plan.ClosedAt != 0 || plan.ClosedBy != "" {
		t.Errorf("lifecycle stamps = (%d,%d,%q), want all zero at birth", plan.StartedAt, plan.ClosedAt, plan.ClosedBy)
	}
	if want := []string{"a", "b"}; !slices.Equal(plan.Labels, want) {
		t.Errorf("Labels = %v, want %v", plan.Labels, want)
	}
	wantAnchors := []model.Anchor{{Kind: model.AnchorDir, Value: "model"}}
	if !reflect.DeepEqual(plan.Anchors, wantAnchors) {
		t.Errorf("Anchors = %+v, want %+v", plan.Anchors, wantAnchors)
	}
	if plan.Comments == nil || len(plan.Comments) != 0 {
		t.Errorf("Comments = %+v, want empty non-nil", plan.Comments)
	}
	if plan.Author != testActor {
		t.Errorf("Author = %q, want %q", plan.Author, testActor)
	}
	if plan.Head != model.SHA(plan.ID) {
		t.Errorf("Head = %s, want root %s", plan.Head, plan.ID)
	}
	if plan.CreatedAt == 0 || plan.UpdatedAt != plan.CreatedAt {
		t.Errorf("timestamps = %d/%d, want equal non-zero", plan.CreatedAt, plan.UpdatedAt)
	}

	ref := refs.For(model.KindPlan, plan.ID)
	if want := "refs/cc-notes/plans/" + string(plan.ID); ref != want {
		t.Errorf("ref = %q, want %q", ref, want)
	}
	if got := gittest.Git(t, s.Git.Dir, "rev-parse", ref); got != string(plan.ID) {
		t.Errorf("ref %s -> %s, want %s", ref, got, plan.ID)
	}
	loaded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded, snapshot) {
		t.Errorf("Load = %+v, want Create snapshot %+v", loaded, snapshot)
	}
	if msg := gittest.Git(t, s.Git.Dir, "log", "-1", "--format=%s", ref); msg != "cc-notes: plan create" {
		t.Errorf("commit message = %q, want %q", msg, "cc-notes: plan create")
	}

	list, err := s.ListPlans(t.Context())
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	if want := []model.Plan{plan}; !reflect.DeepEqual(list, want) {
		t.Errorf("ListPlans = %+v, want %+v", list, want)
	}
}

// TestCreatePlanRejectsUnbornStatus pins the born-status contract at the store
// boundary: only draft and approved are legal on a create op, because a create
// pack cannot carry a second SetPlanStatus (Create discards the ops on dedupe).
func TestCreatePlanRejectsUnbornStatus(t *testing.T) {
	for _, status := range []model.PlanStatus{model.PlanExecuting, model.PlanDone, model.PlanAbandoned, ""} {
		t.Run(string(status)+"_", func(t *testing.T) {
			s := initStore(t)
			ops := []model.Op{model.CreatePlan{Nonce: model.NewNonce(), Title: "T", Body: "B", Status: status}}
			if _, err := s.Create(t.Context(), ops); !errors.Is(err, model.ErrInvalidValue) {
				t.Fatalf("Create with born status %q = %v, want ErrInvalidValue", status, err)
			}
		})
	}
}

func TestDedupePlan(t *testing.T) {
	base := func() []model.Op {
		return []model.Op{model.CreatePlan{
			Nonce: model.NewNonce(), Title: "Add the plan kind", Body: "## Approach\nninth kind",
			Status: model.PlanDraft, Labels: []string{"model"},
			Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "model/kind.go"}},
		}}
	}
	for _, tc := range []struct {
		name       string
		ops        []model.Op
		wantDedupe bool
	}{
		{"exact", base(), true},
		{
			name: "diff title",
			ops: []model.Op{model.CreatePlan{
				Nonce: model.NewNonce(), Title: "Add a ninth kind", Body: "## Approach\nninth kind",
				Status: model.PlanDraft, Labels: []string{"model"},
				Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "model/kind.go"}},
			}},
		},
		{
			name: "diff body",
			ops: []model.Op{model.CreatePlan{
				Nonce: model.NewNonce(), Title: "Add the plan kind", Body: "## Approach\nrevised after review",
				Status: model.PlanDraft, Labels: []string{"model"},
				Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "model/kind.go"}},
			}},
		},
		{
			name: "diff born status",
			ops: []model.Op{model.CreatePlan{
				Nonce: model.NewNonce(), Title: "Add the plan kind", Body: "## Approach\nninth kind",
				Status: model.PlanApproved, Labels: []string{"model"},
				Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "model/kind.go"}},
			}},
		},
		{
			name: "diff labels",
			ops: []model.Op{model.CreatePlan{
				Nonce: model.NewNonce(), Title: "Add the plan kind", Body: "## Approach\nninth kind",
				Status: model.PlanDraft, Labels: []string{"model", "fold"},
				Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "model/kind.go"}},
			}},
		},
		{
			name: "diff anchors",
			ops: []model.Op{model.CreatePlan{
				Nonce: model.NewNonce(), Title: "Add the plan kind", Body: "## Approach\nninth kind",
				Status: model.PlanDraft, Labels: []string{"model"},
				Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "model/model.go"}},
			}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := initStore(t)
			first := create(t, s, base())
			if tc.wantDedupe {
				got := mustDedupe(t, s, tc.ops)
				if got.EntityID() != first.EntityID() {
					t.Errorf("reused id = %s, want existing %s", got.EntityID(), first.EntityID())
				}
				return
			}
			got := create(t, s, tc.ops)
			if got.EntityID() == first.EntityID() {
				t.Errorf("distinct content reused id %s", got.EntityID())
			}
		})
	}
}

func TestDedupePlanSkipsDeleted(t *testing.T) {
	s := initStore(t)
	first := create(t, s, planOps("Add the plan kind")).(model.Plan)
	if _, err := s.Append(t.Context(), refs.For(model.KindPlan, first.ID), []model.Op{model.DeleteNote{}}); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	got := create(t, s, planOps("Add the plan kind"))
	if got.EntityID() == first.ID {
		t.Errorf("deleted twin suppressed re-create: reused id %s", got.EntityID())
	}
}

// TestDedupePlanLiveSkipsDeleted drives scanDup with a list that surfaces a
// content-matching tombstoned plan, which ListPlans would normally hide. Only
// the livePlan predicate stands between the candidate and a false match.
func TestDedupePlanLiveSkipsDeleted(t *testing.T) {
	candidate := []model.PackCommit{{SHA: "candidate", Pack: model.Pack{Ops: []model.Op{
		model.CreatePlan{Nonce: model.NewNonce(), Title: "Add the plan kind", Body: "B", Status: model.PlanDraft},
	}}}}
	for _, tc := range []struct {
		name string
		live model.Plan
	}{
		{"deleted", model.Plan{ID: "pldead", Title: "Add the plan kind", Body: "B", Status: model.PlanDraft, Deleted: true}},
		{"done", model.Plan{ID: "pldone", Title: "Add the plan kind", Body: "B", Status: model.PlanDone}},
		{"abandoned", model.Plan{ID: "plgone", Title: "Add the plan kind", Body: "B", Status: model.PlanAbandoned}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := scanDup(candidate, fold.Plan,
				func() ([]model.Plan, error) { return []model.Plan{tc.live}, nil },
				livePlan, samePlanContent)
			if err != nil {
				t.Fatalf("scanDup: %v", err)
			}
			if got != nil {
				t.Errorf("scanDup matched %#v; a create must proceed as new", got)
			}
		})
	}
}

func TestListPlansSkipsDeleted(t *testing.T) {
	s := initStore(t)
	keep := create(t, s, planOps("Keep")).(model.Plan)
	drop := create(t, s, planOps("Drop")).(model.Plan)
	if _, err := s.Append(t.Context(), refs.For(model.KindPlan, drop.ID), []model.Op{model.DeleteNote{}}); err != nil {
		t.Fatalf("tombstone: %v", err)
	}
	list, err := s.ListPlans(t.Context())
	if err != nil {
		t.Fatalf("ListPlans: %v", err)
	}
	if len(list) != 1 || list[0].ID != keep.ID {
		t.Fatalf("ListPlans = %+v, want only %s", list, keep.ID)
	}
	loaded, err := s.Load(t.Context(), refs.For(model.KindPlan, drop.ID))
	if err != nil {
		t.Fatalf("Load tombstoned: %v", err)
	}
	if !loaded.(model.Plan).Deleted {
		t.Error("loaded.Deleted = false, want true")
	}
}

func TestResolvePlan(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	titles := map[model.EntityID]string{}
	buckets := map[byte][]model.EntityID{}
	var shared []model.EntityID
	for i := 0; len(shared) == 0; i++ {
		if i > 17 {
			t.Fatal("no shared 1-char prefix after 17 creates")
		}
		title := fmt.Sprintf("plan-%d", i)
		plan := create(t, s, planOps(title)).(model.Plan)
		titles[plan.ID] = title
		first := plan.ID[0]
		buckets[first] = append(buckets[first], plan.ID)
		if len(buckets[first]) == 2 {
			shared = buckets[first]
		}
	}

	full := shared[0]
	got, err := s.Resolve(ctx, model.KindPlan, string(full))
	if err != nil {
		t.Fatalf("Resolve(%q): %v", full, err)
	}
	if want := refs.For(model.KindPlan, full); got != want {
		t.Errorf("Resolve(%q) = %q, want %q", full, got, want)
	}

	if _, err := s.Resolve(ctx, model.KindPlan, "zzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(zzz) = %v, want ErrNotFound", err)
	}

	prefix := string(shared[0])[:1]
	_, err = s.Resolve(ctx, model.KindPlan, prefix)
	var ambiguous *AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("Resolve(%q) = %v, want *AmbiguousError", prefix, err)
	}
	if ambiguous.Kind != model.KindPlan || ambiguous.Prefix != prefix {
		t.Errorf("AmbiguousError = %+v, want kind plan prefix %q", ambiguous, prefix)
	}
	slices.Sort(shared)
	want := []Candidate{
		{ID: shared[0], Title: titles[shared[0]]},
		{ID: shared[1], Title: titles[shared[1]]},
	}
	if !reflect.DeepEqual(ambiguous.Candidates, want) {
		t.Errorf("Candidates = %+v, want %+v", ambiguous.Candidates, want)
	}
}

// TestMergePlan joins two replicas' concurrent edits to one plan. The LWW body
// and status resolve by linearization while the label, comment, and supersede
// sets union — the union-merge property sync depends on.
func TestMergePlan(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	plan := create(t, s, planOps("Add the plan kind")).(model.Plan)
	ref := refs.For(model.KindPlan, plan.ID)

	snapshot, err := s.Append(ctx, ref, []model.Op{
		model.SetBody{Body: "ours"},
		model.AddLabel{Label: "ours"},
		model.SetPlanStatus{Status: model.PlanExecuting},
	})
	if err != nil {
		t.Fatalf("Append ours: %v", err)
	}
	ours := snapshot.(model.Plan).Head

	sig := gitobj.Signature{Name: testName, Email: testEmail, When: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	theirs, err := s.Repo.WriteOpsCommit(ctx, []model.SHA{model.SHA(plan.ID)}, sig, "cc-notes: plan",
		model.Pack{Lamport: 2, Ops: []model.Op{
			model.AddLabel{Label: "theirs"},
			model.AddComment{Body: "from the other clone"},
			model.AddSupersededBy{ID: "plan2"},
		}})
	if err != nil {
		t.Fatalf("WriteOpsCommit theirs: %v", err)
	}

	if _, err := s.Merge(ctx, ref, ours, theirs); err != nil {
		t.Fatalf("Merge: %v", err)
	}
	loaded, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	merged := loaded.(model.Plan)
	if merged.Body != "ours" {
		t.Errorf("Body = %q, want ours: only our side wrote it", merged.Body)
	}
	if merged.Status != model.PlanExecuting || merged.StartedAt == 0 {
		t.Errorf("status/started = %q/%d, want executing with a stamp", merged.Status, merged.StartedAt)
	}
	if want := []string{"ours", "theirs"}; !slices.Equal(merged.Labels, want) {
		t.Errorf("Labels = %v, want %v (union)", merged.Labels, want)
	}
	if len(merged.Comments) != 1 || merged.Comments[0].Body != "from the other clone" {
		t.Errorf("Comments = %+v, want the remote comment", merged.Comments)
	}
	if want := []model.EntityID{"plan2"}; !slices.Equal(merged.SupersededBy, want) {
		t.Errorf("SupersededBy = %v, want %v", merged.SupersededBy, want)
	}
}

// TestFoldCachePlanRoundTrip pins the cache codec over a fully populated plan:
// every field a fold can produce must survive the encode/decode, or a warm read
// silently differs from a cold one.
func TestFoldCachePlanRoundTrip(t *testing.T) {
	dir := t.TempDir()
	c := newFoldCache(dir, foldCacheCap)

	tip := model.SHA("dddd333333333333333333333333333333333333")
	plan := model.Plan{
		ID: "planid", Title: "Add the plan kind", Body: "## Context\nninth kind",
		Status: model.PlanDone, Outcome: "shipped in v0.51.0",
		Labels:       []string{"model"},
		Comments:     []model.Comment{{Author: testActor, TS: 8, Body: "approved as written"}},
		Anchors:      []model.Anchor{{Kind: model.AnchorDir, Value: "model"}},
		SupersededBy: []model.EntityID{"plan2"},
		Author:       testActor,
		CreatedAt:    1, UpdatedAt: 40, StartedAt: 20, ClosedAt: 30, ClosedBy: testActor,
		Head: tip, Deleted: true,
	}
	c.put(tip, plan)
	got, ok := c.get(tip)
	if !ok {
		t.Fatal("cache miss on the entry just written")
	}
	if !reflect.DeepEqual(got, plan) {
		t.Fatalf("round-trip = %#v, want %#v", got, plan)
	}
}
