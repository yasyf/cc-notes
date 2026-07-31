package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestDupCheckersCoverKinds(t *testing.T) {
	kinds := model.Kinds()
	if got, want := len(dupCheckers), len(kinds); got != want {
		t.Fatalf("dupCheckers has %d entries, model.Kinds() has %d", got, want)
	}
	for _, k := range kinds {
		if _, ok := dupCheckers[k]; !ok {
			t.Errorf("dupCheckers missing kind %q", k)
		}
	}
}

// mustDedupe asserts Create hit an existing live duplicate — a *DuplicateError
// matching ErrDuplicate — and returns the reused survivor snapshot.
func mustDedupe(t *testing.T, s *Store, ops []model.Op) model.Snapshot {
	t.Helper()
	_, err := s.Create(t.Context(), ops)
	var dup *DuplicateError
	if !errors.As(err, &dup) || !errors.Is(err, ErrDuplicate) {
		t.Fatalf("Create err = %v, want *DuplicateError matching ErrDuplicate", err)
	}
	return dup.Existing
}

func TestDedupeNote(t *testing.T) {
	anchors := []model.Anchor{
		{Kind: model.AnchorPath, Value: "x"},
		{Kind: model.AnchorBranch, Value: "main"},
	}
	revAnchors := []model.Anchor{anchors[1], anchors[0]}
	mk := func(title, body string, tags []string, anch []model.Anchor) []model.Op {
		return []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: title, Body: body, Tags: tags, Anchors: anch}}
	}
	base := mk("T", "B", []string{"a", "b"}, anchors)

	for _, tc := range []struct {
		name       string
		ops        []model.Op
		wantDedupe bool
	}{
		{"exact", mk("T", "B", []string{"a", "b"}, anchors), true},
		{"permuted tags", mk("T", "B", []string{"b", "a"}, anchors), true},
		{"permuted anchors", mk("T", "B", []string{"a", "b"}, revAnchors), true},
		{"diff title", mk("T2", "B", []string{"a", "b"}, anchors), false},
		{"diff body", mk("T", "B2", []string{"a", "b"}, anchors), false},
		{"diff tag", mk("T", "B", []string{"a", "c"}, anchors), false},
		{"extra tag", mk("T", "B", []string{"a", "b", "c"}, anchors), false},
		{"diff anchor", mk("T", "B", []string{"a", "b"}, []model.Anchor{{Kind: model.AnchorPath, Value: "y"}}), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := initStore(t)
			first := create(t, s, base)
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

func TestDedupePerKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		base []model.Op
		diff []model.Op
	}{
		{
			name: "doc",
			base: []model.Op{model.CreateDoc{Nonce: model.NewNonce(), Title: "T", Body: "B", When: "release", Tags: []string{"a"}}},
			diff: []model.Op{model.CreateDoc{Nonce: model.NewNonce(), Title: "T", Body: "B", When: "later", Tags: []string{"a"}}},
		},
		{
			name: "log",
			base: []model.Op{model.CreateLog{Nonce: model.NewNonce(), Title: "T", Tags: []string{"a"}}},
			diff: []model.Op{model.CreateLog{Nonce: model.NewNonce(), Title: "T2", Tags: []string{"a"}}},
		},
		{
			name: "task",
			base: []model.Op{
				model.CreateTask{Nonce: model.NewNonce(), Title: "T", Description: "D", Type: model.TypeTask, Priority: 2, Branch: "main", Labels: []string{"a"}},
				model.AddCriterion{ID: model.NewNonce(), Text: "builds"},
			},
			diff: []model.Op{
				model.CreateTask{Nonce: model.NewNonce(), Title: "T", Description: "D", Type: model.TypeTask, Priority: 1, Branch: "main", Labels: []string{"a"}},
				model.AddCriterion{ID: model.NewNonce(), Text: "builds"},
			},
		},
		{
			name: "sprint",
			base: []model.Op{
				model.CreateSprint{Nonce: model.NewNonce(), Title: "T", Description: "D", Labels: []string{"a"}},
				model.SetStartDate{Date: 1000},
			},
			diff: []model.Op{
				model.CreateSprint{Nonce: model.NewNonce(), Title: "T", Description: "D", Labels: []string{"a"}},
				model.SetStartDate{Date: 2000},
			},
		},
		{
			name: "project",
			base: []model.Op{model.CreateProject{Nonce: model.NewNonce(), Title: "T", Description: "D", Labels: []string{"a"}}},
			diff: []model.Op{model.CreateProject{Nonce: model.NewNonce(), Title: "T", Description: "D", Labels: []string{"a", "b"}}},
		},
		{
			name: "investigation",
			base: []model.Op{model.CreateInvestigation{Nonce: model.NewNonce(), Title: "T", Premise: "P"}},
			diff: []model.Op{model.CreateInvestigation{Nonce: model.NewNonce(), Title: "T", Premise: "different suspicion"}},
		},
		{
			name: "plan",
			base: []model.Op{model.CreatePlan{Nonce: model.NewNonce(), Title: "T", Body: "B", Status: model.PlanDraft, Labels: []string{"a"}}},
			diff: []model.Op{model.CreatePlan{Nonce: model.NewNonce(), Title: "T", Body: "revised approach", Status: model.PlanDraft, Labels: []string{"a"}}},
		},
		{
			// The born status is content: recording an approved plan must not
			// collapse into the draft of the same text it was revised from.
			name: "plan born status",
			base: []model.Op{model.CreatePlan{Nonce: model.NewNonce(), Title: "T", Body: "B", Status: model.PlanDraft}},
			diff: []model.Op{model.CreatePlan{Nonce: model.NewNonce(), Title: "T", Body: "B", Status: model.PlanApproved}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := initStore(t)
			first := create(t, s, tc.base)
			got := mustDedupe(t, s, tc.base)
			if got.EntityID() != first.EntityID() {
				t.Errorf("reused id = %s, want existing %s", got.EntityID(), first.EntityID())
			}
			got = create(t, s, tc.diff)
			if got.EntityID() == first.EntityID() {
				t.Errorf("differing content reused id %s", got.EntityID())
			}
		})
	}
}

// TestDedupeTaskCriteriaIgnoresID proves the comparison is over criterion
// content: two tasks whose criteria carry the same text but different nonce ids
// dedupe.
func TestDedupeTaskCriteriaIgnoresID(t *testing.T) {
	s := initStore(t)
	mk := func() []model.Op {
		return []model.Op{
			model.CreateTask{Nonce: model.NewNonce(), Title: "T", Type: model.TypeTask, Branch: "main"},
			model.AddCriterion{ID: model.NewNonce(), Text: "builds", Script: "go build ./..."},
		}
	}
	first := create(t, s, mk())
	got := mustDedupe(t, s, mk())
	if got.EntityID() != first.EntityID() {
		t.Errorf("reused id = %s, want existing %s", got.EntityID(), first.EntityID())
	}
}

// TestDedupeTaskDistinguishesPlans proves the plan pointer is task content:
// "write the tests" under one plan and the same title under another are two
// tasks, and re-adding one under no plan at all is a third. Without a.Plan ==
// b.Plan in sameTaskContent the second create would silently collapse into the
// first, filing the work under the wrong plan.
func TestDedupeTaskDistinguishesPlans(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	planA := create(t, s, planOps("perf")).(model.Plan)
	planB := create(t, s, planOps("the plan kind")).(model.Plan)

	under := func(plan model.EntityID) []model.Op {
		ops := []model.Op{model.CreateTask{
			Nonce: model.NewNonce(), Title: "write the tests", Type: model.TypeTask, Branch: "main",
		}}
		if plan != "" {
			ops = append(ops, model.SetPlan{Plan: plan})
		}
		return ops
	}

	first := create(t, s, under(planA.ID)).(model.Task)
	if first.Plan != planA.ID {
		t.Fatalf("first task Plan = %q, want %q", first.Plan, planA.ID)
	}
	second := create(t, s, under(planB.ID)).(model.Task)
	if second.ID == first.ID {
		t.Fatalf("same title under a different plan reused id %s", second.ID)
	}
	if second.Plan != planB.ID {
		t.Errorf("second task Plan = %q, want %q", second.Plan, planB.ID)
	}
	planless := create(t, s, under("")).(model.Task)
	if planless.ID == first.ID || planless.ID == second.ID {
		t.Fatalf("planless twin reused id %s", planless.ID)
	}

	// The pointer is the only difference, so an identical re-record under the
	// same plan must still dedupe.
	got := mustDedupe(t, s, under(planA.ID))
	if got.EntityID() != first.ID {
		t.Errorf("reused id = %s, want existing %s", got.EntityID(), first.ID)
	}

	// Re-pointing an existing task does not retroactively fuse it with a twin.
	if _, err := s.Append(ctx, refs.For(model.KindTask, second.ID), []model.Op{model.SetPlan{Plan: planA.ID}}); err != nil {
		t.Fatalf("re-point: %v", err)
	}
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(tasks) != 3 {
		t.Errorf("ListTasks = %d tasks, want 3", len(tasks))
	}
}

// TestDedupeSkipsTombstonedNote proves a soft-deleted twin never suppresses a
// re-add: after DeleteNote the identical content roots a fresh note.
func TestDedupeSkipsTombstonedNote(t *testing.T) {
	s := initStore(t)
	ops := []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "gone"}}
	first := create(t, s, ops).(model.Note)
	if _, err := s.Append(t.Context(), refs.For(model.KindNote, first.ID), []model.Op{model.DeleteNote{}}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got := create(t, s, []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "gone"}})
	if got.EntityID() == first.EntityID() {
		t.Errorf("re-create reused tombstoned id %s", got.EntityID())
	}
}

// TestDedupeSkipsSupersededNote proves a superseded twin never suppresses a
// re-add: the survivor is skipped by the live-set query.
func TestDedupeSkipsSupersededNote(t *testing.T) {
	s := initStore(t)
	old := create(t, s, []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "old"}}).(model.Note)
	replacement := create(t, s, []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "new"}}).(model.Note)
	if _, err := s.Append(t.Context(), refs.For(model.KindNote, old.ID), []model.Op{model.AddSupersededBy{ID: replacement.ID}}); err != nil {
		t.Fatalf("supersede: %v", err)
	}
	got := create(t, s, []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "old"}})
	if got.EntityID() == old.EntityID() {
		t.Errorf("re-create reused superseded id %s", got.EntityID())
	}
}

// TestDedupeSkipsClosed proves a done task, completed sprint, archived project,
// closed investigation, and closed plan never block re-adding identical
// content — the live-set filter drops entities in a terminal state.
func TestDedupeSkipsClosed(t *testing.T) {
	mkInvestigation := func() []model.Op {
		return []model.Op{model.CreateInvestigation{Nonce: model.NewNonce(), Title: "T", Premise: "P"}}
	}
	mkPlan := func() []model.Op {
		return []model.Op{model.CreatePlan{Nonce: model.NewNonce(), Title: "T", Body: "B", Status: model.PlanApproved}}
	}
	for _, tc := range []struct {
		name  string
		mk    func() []model.Op
		close model.Op
		kind  model.Kind
	}{
		{
			name: "task",
			mk: func() []model.Op {
				return []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: "T", Type: model.TypeTask, Branch: "main"}}
			},
			close: model.SetStatus{Status: model.StatusDone},
			kind:  model.KindTask,
		},
		{
			name:  "sprint",
			mk:    func() []model.Op { return []model.Op{model.CreateSprint{Nonce: model.NewNonce(), Title: "T"}} },
			close: model.SetSprintStatus{Status: model.SprintCompleted},
			kind:  model.KindSprint,
		},
		{
			name:  "project",
			mk:    func() []model.Op { return []model.Op{model.CreateProject{Nonce: model.NewNonce(), Title: "T"}} },
			close: model.SetProjectStatus{Status: model.ProjectArchived},
			kind:  model.KindProject,
		},
		{
			name:  "investigation confirmed",
			mk:    mkInvestigation,
			close: model.SetInvestigationStatus{Status: model.InvestigationConfirmed},
			kind:  model.KindInvestigation,
		},
		{
			name:  "investigation exonerated",
			mk:    mkInvestigation,
			close: model.SetInvestigationStatus{Status: model.InvestigationExonerated},
			kind:  model.KindInvestigation,
		},
		{
			name:  "investigation abandoned",
			mk:    mkInvestigation,
			close: model.SetInvestigationStatus{Status: model.InvestigationAbandoned},
			kind:  model.KindInvestigation,
		},
		{
			name:  "plan done",
			mk:    mkPlan,
			close: model.SetPlanStatus{Status: model.PlanDone},
			kind:  model.KindPlan,
		},
		{
			name:  "plan abandoned",
			mk:    mkPlan,
			close: model.SetPlanStatus{Status: model.PlanAbandoned},
			kind:  model.KindPlan,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := initStore(t)
			first := create(t, s, tc.mk())
			if _, err := s.Append(t.Context(), refs.For(tc.kind, first.EntityID()), []model.Op{tc.close}); err != nil {
				t.Fatalf("close: %v", err)
			}
			got := create(t, s, tc.mk())
			if got.EntityID() == first.EntityID() {
				t.Errorf("re-create reused closed id %s", got.EntityID())
			}
		})
	}
}

// TestLivePlanTargetsOnlyBornStatuses pins the plan live set to the statuses
// CreatePlan can be born into. Widening it to executing — the state a plan
// spends most of its life in — states a rule that samePlanContent's Status
// comparison can never let fire, and narrowing it below the born set would stop
// dedupe from firing at all.
func TestLivePlanTargetsOnlyBornStatuses(t *testing.T) {
	for _, tc := range []struct {
		status model.PlanStatus
		want   bool
	}{
		{model.PlanDraft, true},
		{model.PlanApproved, true},
		{model.PlanExecuting, false},
		{model.PlanDone, false},
		{model.PlanAbandoned, false},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			if got := livePlan(model.Plan{Status: tc.status}); got != tc.want {
				t.Errorf("livePlan(%s) = %v, want %v", tc.status, got, tc.want)
			}
			if livePlan(model.Plan{Status: tc.status, Deleted: true}) {
				t.Errorf("livePlan(tombstoned %s) = true, want false", tc.status)
			}
		})
	}
}

// TestDedupeSkipsExecutingPlan proves a plan already under execution never
// blocks re-recording its text: plan_start moves the record past capture, so
// the identical plan roots a fresh chain the executing one keeps out of.
func TestDedupeSkipsExecutingPlan(t *testing.T) {
	s := initStore(t)
	mk := func() []model.Op {
		return []model.Op{model.CreatePlan{Nonce: model.NewNonce(), Title: "T", Body: "B", Status: model.PlanApproved}}
	}
	first := create(t, s, mk()).(model.Plan)
	if got := mustDedupe(t, s, mk()); got.EntityID() != first.ID {
		t.Fatalf("approved twin deduped to %s, want %s", got.EntityID(), first.ID)
	}
	if _, err := s.Append(t.Context(), refs.For(model.KindPlan, first.ID), []model.Op{
		model.SetPlanStatus{Status: model.PlanExecuting},
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	second := create(t, s, mk())
	if second.EntityID() == first.ID {
		t.Fatalf("re-create reused executing id %s", second.EntityID())
	}
}

// TestDedupeSkipsStale proves an expired note or doc never blocks re-asserting
// its fact: after MarkStale the identical content roots a fresh entity — the
// live-set query keeps stale entities, so the scan itself skips them.
func TestDedupeSkipsStale(t *testing.T) {
	for _, tc := range []struct {
		name string
		mk   func() []model.Op
		kind model.Kind
	}{
		{
			name: "note",
			mk:   func() []model.Op { return []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "fact"}} },
			kind: model.KindNote,
		},
		{
			name: "doc",
			mk: func() []model.Op {
				return []model.Op{model.CreateDoc{Nonce: model.NewNonce(), Title: "fact", Body: "B"}}
			},
			kind: model.KindDoc,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := initStore(t)
			first := create(t, s, tc.mk())
			if _, err := s.Append(t.Context(), refs.For(tc.kind, first.EntityID()), []model.Op{model.MarkStale{Reason: "outdated"}}); err != nil {
				t.Fatalf("mark stale: %v", err)
			}
			got := create(t, s, tc.mk())
			if got.EntityID() == first.EntityID() {
				t.Errorf("re-create reused stale id %s", got.EntityID())
			}
		})
	}
}

// TestDedupeReturnsOldest seeds two live notes with identical folded content
// (the second reaches it by an appended SetTitle, bypassing Create's guard) and
// checks a third identical create reuses the (CreatedAt, ID)-smallest survivor.
func TestDedupeReturnsOldest(t *testing.T) {
	s := initStore(t)
	var tick int64
	s.now = func() time.Time { tick++; return time.Unix(1000+tick, 0) }

	oldest := create(t, s, []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "dup"}}).(model.Note)
	twin := create(t, s, []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "other"}}).(model.Note)
	if _, err := s.Append(t.Context(), refs.For(model.KindNote, twin.ID), []model.Op{model.SetTitle{Title: "dup"}}); err != nil {
		t.Fatalf("retitle twin: %v", err)
	}
	if oldest.CreatedAt >= twin.CreatedAt {
		t.Fatalf("oldest CreatedAt %d not before twin %d", oldest.CreatedAt, twin.CreatedAt)
	}

	got := mustDedupe(t, s, []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "dup"}})
	if got.EntityID() != oldest.EntityID() {
		t.Errorf("reused id = %s, want oldest %s", got.EntityID(), oldest.EntityID())
	}
}

// TestDedupeSkipsUncoveredPack proves the pack-shape gate: a create pack that
// bundles an op folding into a field the comparator ignores — AppendEntry into a
// log's Entries, AddComment into a task's Comments — roots
// a fresh entity even when its comparator-covered fields match an existing one,
// so the bundled op is never silently dropped by a reuse.
func TestDedupeSkipsUncoveredPack(t *testing.T) {
	for _, tc := range []struct {
		name string
		base []model.Op
		pack []model.Op
	}{
		{
			name: "log with initial entry",
			base: []model.Op{model.CreateLog{Nonce: model.NewNonce(), Title: "T", Tags: []string{"a"}}},
			pack: []model.Op{
				model.CreateLog{Nonce: model.NewNonce(), Title: "T", Tags: []string{"a"}},
				model.AppendEntry{Text: "first"},
			},
		},
		{
			name: "task with comment",
			base: []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: "T", Type: model.TypeTask, Branch: "main"}},
			pack: []model.Op{
				model.CreateTask{Nonce: model.NewNonce(), Title: "T", Type: model.TypeTask, Branch: "main"},
				model.AddComment{Body: "note"},
			},
		},
		{
			name: "investigation with initial finding",
			base: []model.Op{model.CreateInvestigation{Nonce: model.NewNonce(), Title: "T", Premise: "P"}},
			pack: []model.Op{
				model.CreateInvestigation{Nonce: model.NewNonce(), Title: "T", Premise: "P"},
				model.AddFinding{ID: model.NewNonce(), Text: "suspect"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := initStore(t)
			first := create(t, s, tc.base)
			got := create(t, s, tc.pack)
			if got.EntityID() == first.EntityID() {
				t.Errorf("uncovered-pack create reused id %s", got.EntityID())
			}
		})
	}
}

// TestDedupeInvestigation proves the investigation comparator spans every field
// a create pack can carry: title, premise, and the shared tag, anchor, and
// attachment bundles. Only an exact match (tags and anchors permuted) dedupes; a
// create bringing new metadata roots a fresh entity so its metadata is never
// silently lost.
func TestDedupeInvestigation(t *testing.T) {
	anchors := []model.Anchor{
		{Kind: model.AnchorPath, Value: "internal/pool/pool.go"},
		{Kind: model.AnchorDir, Value: "internal/pool"},
	}
	revAnchors := []model.Anchor{anchors[1], anchors[0]}
	att := model.Attachment{Name: "stacks.txt", OID: strings.Repeat("a", 64), Size: 4096}
	att2 := model.Attachment{Name: "goroutines.txt", OID: strings.Repeat("b", 64), Size: 2048}
	mk := func(title, premise string, tags []string, anch []model.Anchor, atts ...model.Attachment) []model.Op {
		ops := make([]model.Op, 0, 1+len(atts))
		ops = append(ops, model.CreateInvestigation{Nonce: model.NewNonce(), Title: title, Premise: premise, Tags: tags, Anchors: anch})
		for _, a := range atts {
			ops = append(ops, model.AddAttachment(a))
		}
		return ops
	}
	base := mk("T", "P", []string{"a", "b"}, anchors, att)

	for _, tc := range []struct {
		name       string
		ops        []model.Op
		wantDedupe bool
	}{
		{"exact", mk("T", "P", []string{"a", "b"}, anchors, att), true},
		{"permuted tags", mk("T", "P", []string{"b", "a"}, anchors, att), true},
		{"permuted anchors", mk("T", "P", []string{"a", "b"}, revAnchors, att), true},
		{"diff title", mk("T2", "P", []string{"a", "b"}, anchors, att), false},
		{"diff premise", mk("T", "P2", []string{"a", "b"}, anchors, att), false},
		{"diff tag", mk("T", "P", []string{"a", "c"}, anchors, att), false},
		{"extra tag", mk("T", "P", []string{"a", "b", "c"}, anchors, att), false},
		{"diff anchor", mk("T", "P", []string{"a", "b"}, []model.Anchor{{Kind: model.AnchorPath, Value: "internal/other.go"}}, att), false},
		{"diff attachment", mk("T", "P", []string{"a", "b"}, anchors, att2), false},
		{"no attachment", mk("T", "P", []string{"a", "b"}, anchors), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := initStore(t)
			first := create(t, s, base)
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

// TestDedupeLogIgnoresEntries proves the log comparator no longer compares
// Entries: a fresh create with the same title and tags dedupes against an
// existing log that has since accrued entries, where comparing Entries would
// have minted a twin.
func TestDedupeLogIgnoresEntries(t *testing.T) {
	s := initStore(t)
	first := create(t, s, []model.Op{model.CreateLog{Nonce: model.NewNonce(), Title: "incident", Tags: []string{"ops"}}}).(model.Log)
	if _, err := s.Append(t.Context(), refs.For(model.KindLog, first.ID), []model.Op{model.AppendEntry{Text: "started"}}); err != nil {
		t.Fatalf("append entry: %v", err)
	}
	got := mustDedupe(t, s, []model.Op{model.CreateLog{Nonce: model.NewNonce(), Title: "incident", Tags: []string{"ops"}}})
	if got.EntityID() != first.ID {
		t.Errorf("reused id = %s, want existing %s", got.EntityID(), first.ID)
	}
}
