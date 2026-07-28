package fold_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

const attachmentOID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// TestFoldSkipsForeignOps is the forward-compatibility contract, one row per
// (op, kind) pairing this binary has no case for: Fold skips the op, counts it,
// stamps UpdatedAt from the commit that carried it, and leaves no other trace,
// while Strict — the write path's fold — still refuses the same chain with
// ErrKindMismatch.
func TestFoldSkipsForeignOps(t *testing.T) {
	noteRoot := mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n"})
	docRoot := mk("aaa", nil, "alice", 100, 1, model.CreateDoc{Nonce: "n"})
	logRoot := mk("aaa", nil, "alice", 100, 1, model.CreateLog{Nonce: "n"})
	taskRoot := mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Type: model.TypeTask, Branch: "main"})
	sprintRoot := mk("aaa", nil, "alice", 100, 1, model.CreateSprint{Nonce: "n"})
	projectRoot := mk("aaa", nil, "alice", 100, 1, model.CreateProject{Nonce: "n"})
	runbookRoot := mk("aaa", nil, "alice", 100, 1, model.CreateRunbook{Nonce: "n"})
	invRoot := mk("aaa", nil, "alice", 100, 1, model.CreateInvestigation{Nonce: "n", Premise: "P"})
	attach := model.AddAttachment{Name: "trace.png", OID: attachmentOID, Size: 1}
	cases := []struct {
		name string
		root model.PackCommit
		op   model.Op
	}{
		{"task op on a note chain", noteRoot, model.SetStatus{Status: model.StatusDone}},
		{"set_when op on a note chain", noteRoot, model.SetWhen{When: "x"}},
		{"task op on a doc chain", docRoot, model.SetStatus{Status: model.StatusDone}},
		{"append_entry op on a doc chain", docRoot, model.AppendEntry{Text: "x"}},
		{"set_when op on a log chain", logRoot, model.SetWhen{When: "x"}},
		{"verify_note op on a log chain", logRoot, model.VerifyNote{VerifiedCommit: "deadbeef"}},
		{"mark_stale op on a log chain", logRoot, model.MarkStale{Reason: "x"}},
		{"set_body op on a log chain", logRoot, model.SetBody{Body: "x"}},
		{"note op on a task chain", taskRoot, model.AddTag{Tag: "x"}},
		{"verify_note on a task chain", taskRoot, model.VerifyNote{VerifiedCommit: "deadbeef"}},
		{"add_superseded_by on a task chain", taskRoot, model.AddSupersededBy{ID: "feedface"}},
		{"sprint status op on a task chain", taskRoot, model.SetSprintStatus{Status: model.SprintActive}},
		{"project status op on a task chain", taskRoot, model.SetProjectStatus{Status: model.ProjectArchived}},
		{"start date op on a task chain", taskRoot, model.SetStartDate{Date: 1000}},
		{"end date op on a task chain", taskRoot, model.SetEndDate{Date: 2000}},
		{"add_attachment on a task chain", taskRoot, attach},
		{"remove_attachment on a task chain", taskRoot, model.RemoveAttachment{Name: "trace.png"}},
		{"task status op on a sprint chain", sprintRoot, model.SetStatus{Status: model.StatusDone}},
		{"set_sprint op on a sprint chain", sprintRoot, model.SetSprint{Sprint: "feedface"}},
		{"add_attachment on a sprint chain", sprintRoot, attach},
		{"task status op on a project chain", projectRoot, model.SetStatus{Status: model.StatusDone}},
		{"sprint status op on a project chain", projectRoot, model.SetSprintStatus{Status: model.SprintActive}},
		{"add_attachment on a project chain", projectRoot, attach},
		{"sprint status op on a runbook chain", runbookRoot, model.SetSprintStatus{Status: model.SprintActive}},
		{"task status op on a runbook chain", runbookRoot, model.SetStatus{Status: model.StatusDone}},
		{"note op on a runbook chain", runbookRoot, model.AddTag{Tag: "x"}},
		{"set_when op on an investigation chain", invRoot, model.SetWhen{When: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chain := []model.PackCommit{tc.root, mk("bbb", []string{"aaa"}, "bob", 200, 2, tc.op)}
			got, err := fold.Fold(chain)
			if err != nil {
				t.Fatalf("Fold = %v, want a clean fold", err)
			}
			if got.Meta().SkippedOps != 1 {
				t.Fatalf("SkippedOps = %d, want 1", got.Meta().SkippedOps)
			}
			if ts := got.Meta().UpdatedAt.Unix(); ts != 200 {
				t.Fatalf("UpdatedAt = %d, want 200: a skipped op still touches", ts)
			}
			base, err := fold.Fold([]model.PackCommit{tc.root})
			if err != nil {
				t.Fatalf("Fold of the root alone = %v", err)
			}
			if state, want := stateFields(t, got), stateFields(t, base); !reflect.DeepEqual(state, want) {
				t.Fatalf("state = %v, want %v: the skipped op left a trace", state, want)
			}
			if _, err := fold.Strict(chain); !errors.Is(err, fold.ErrKindMismatch) {
				t.Fatalf("Strict = %v, want ErrKindMismatch", err)
			}
		})
	}
}

// TestFoldSkippedOpStillTouches pins run's convergence rule: a commit this
// binary skips entirely still stamps UpdatedAt, so a binary that applies the op
// and one that skips it agree on the entity's last-modified time. The chain's
// last commit carries nothing but the skipped op, at an author time strictly
// after every applied op, so the touch is the only thing that can produce the
// stamp.
func TestFoldSkippedOpStillTouches(t *testing.T) {
	cases := []struct {
		name    string
		create  model.Op
		applied model.Op
		skipped model.Op
	}{
		{
			"note",
			model.CreateNote{Nonce: "n"},
			model.SetTitle{Title: "T"},
			model.SetStatus{Status: model.StatusDone},
		},
		{
			"doc",
			model.CreateDoc{Nonce: "n"},
			model.SetBody{Body: "b"},
			model.AppendEntry{Text: "x"},
		},
		{
			"task",
			model.CreateTask{Nonce: "n", Type: model.TypeTask, Branch: "main"},
			model.SetStatus{Status: model.StatusDone},
			model.AddTag{Tag: "x"},
		},
		{
			"sprint",
			model.CreateSprint{Nonce: "n"},
			model.SetSprintStatus{Status: model.SprintActive},
			model.SetStatus{Status: model.StatusDone},
		},
		{
			"project",
			model.CreateProject{Nonce: "n"},
			model.SetProjectStatus{Status: model.ProjectArchived},
			model.SetStatus{Status: model.StatusDone},
		},
		{
			"investigation",
			model.CreateInvestigation{Nonce: "n", Premise: "P"},
			model.SetRootCause{Text: "r"},
			model.SetWhen{When: "x"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fold.Fold([]model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, tc.create),
				mk("bbb", []string{"aaa"}, "bob", 200, 2, tc.applied),
				mk("ccc", []string{"bbb"}, "bob", 300, 3, tc.skipped),
			})
			if err != nil {
				t.Fatalf("Fold = %v, want a clean fold", err)
			}
			if got.Meta().SkippedOps != 1 {
				t.Fatalf("SkippedOps = %d, want 1", got.Meta().SkippedOps)
			}
			if ts := got.Meta().UpdatedAt.Unix(); ts != 300 {
				t.Fatalf("UpdatedAt = %d, want 300: a commit carrying only a skipped op must still touch", ts)
			}
		})
	}
}

// TestFoldSkipCount pins the count itself: one per skipped op, several to a
// commit, with every applicable op in the same chain still folded.
func TestFoldSkipCount(t *testing.T) {
	chain := []model.PackCommit{
		mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T"}),
		mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T2"}),
		mk("c2", []string{"c1"}, "bob", 300, 3, model.SetStartDate{Date: 7}),
		mk("c3", []string{"c2"}, "bob", 400, 4, model.AddTag{Tag: "x"}),
		mk("c4", []string{"c3"}, "bob", 500, 5,
			model.SetStatus{Status: model.StatusDone}, model.SetSprint{Sprint: "feedface"}),
	}
	cases := []struct {
		name        string
		prefix      int
		wantSkipped int
		wantTags    []string
	}{
		{"no foreign ops", 2, 0, []string{}},
		{"one foreign op", 3, 1, []string{}},
		{"an applicable op behind the skip", 4, 1, []string{"x"}},
		{"two more foreign ops in one commit", 5, 3, []string{"x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fold.Note(chain[:tc.prefix])
			if err != nil {
				t.Fatalf("Note = %v, want a clean fold", err)
			}
			if got.SkippedOps != tc.wantSkipped {
				t.Fatalf("SkippedOps = %d, want %d", got.SkippedOps, tc.wantSkipped)
			}
			if got.Title != "T2" {
				t.Fatalf("Title = %q, want T2: an applicable op was dropped", got.Title)
			}
			if !reflect.DeepEqual(got.Tags, tc.wantTags) {
				t.Fatalf("Tags = %v, want %v", got.Tags, tc.wantTags)
			}
		})
	}
}

// TestFoldTaskNewerHistory replays the release-binary failure: a task chain a
// newer cc-notes extended with ops this binary's task folder does not all have.
// add_anchor on a task is one this binary does apply; add_attachment stands in
// for the next such op, and the ordinary op behind it must still land.
func TestFoldTaskNewerHistory(t *testing.T) {
	anchor := model.Anchor{Kind: model.AnchorPath, Value: "internal/fold/fold.go"}
	got, err := fold.Task([]model.PackCommit{
		mk("c0", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Type: model.TypeTask, Branch: "main"}),
		mk("c1", []string{"c0"}, "bob", 200, 2, model.AddAnchor{Anchor: anchor}),
		mk("c2", []string{"c1"}, "bob", 300, 3, model.AddAttachment{Name: "trace.png", OID: attachmentOID, Size: 1}),
		mk("c3", []string{"c2"}, "bob", 400, 4, model.SetStatus{Status: model.StatusDone}),
	})
	if err != nil {
		t.Fatalf("Task = %v, want a clean fold", err)
	}
	if got.SkippedOps != 1 {
		t.Fatalf("SkippedOps = %d, want 1", got.SkippedOps)
	}
	if !reflect.DeepEqual(got.Anchors, []model.Anchor{anchor}) {
		t.Fatalf("Anchors = %v, want %v", got.Anchors, []model.Anchor{anchor})
	}
	if got.Status != model.StatusDone {
		t.Fatalf("Status = %q, want done: the op behind the skip was dropped", got.Status)
	}
	if got.UpdatedAt != 400 {
		t.Fatalf("UpdatedAt = %d, want 400", got.UpdatedAt)
	}
}

// TestFoldSeedMismatchFatal keeps the checkpoint seed fatal: Checkpoint.State
// carries a content-addressed state_kind tag, so a foreign snapshot there is
// corruption, never a newer binary's vocabulary.
func TestFoldSeedMismatchFatal(t *testing.T) {
	n0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T"})
	n1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T2"})
	note, err := fold.Note([]model.PackCommit{n0, n1})
	if err != nil {
		t.Fatalf("fold note prefix: %v", err)
	}
	noteChain := []model.PackCommit{n0, n1, cp("cK", "c1", "compactor", 250, 3, note, 2, "c0", "c1")}

	t0 := mk("c0", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Type: model.TypeTask, Branch: "main"})
	t1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T2"})
	task, err := fold.Task([]model.PackCommit{t0, t1})
	if err != nil {
		t.Fatalf("fold task prefix: %v", err)
	}
	taskChain := []model.PackCommit{t0, t1, cp("cK", "c1", "compactor", 250, 3, task, 2, "c0", "c1")}

	cases := []struct {
		name  string
		chain []model.PackCommit
		via   func([]model.PackCommit) error
	}{
		{"note state seeded as doc", noteChain, docErr},
		{"note state seeded as log", noteChain, logErr},
		{"note state seeded as task", noteChain, taskErr},
		{"note state seeded as sprint", noteChain, sprintErr},
		{"note state seeded as project", noteChain, projectErr},
		{"note state seeded as runbook", noteChain, runbookErr},
		{"note state seeded as investigation", noteChain, investigationErr},
		{"task state seeded as note", taskChain, noteErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.via(tc.chain); !errors.Is(err, fold.ErrKindMismatch) {
				t.Fatalf("err = %v, want ErrKindMismatch", err)
			}
		})
	}
}

// stateFields is a snapshot's marshalled state minus head and updated_at, the
// two fields every extra commit moves — a skipped op moves updated_at too,
// which callers assert on its own. SkippedOps does not marshal, so it never
// shows up here.
func stateFields(t *testing.T, snap model.Snapshot) map[string]any {
	t.Helper()
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal %T: %v", snap, err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal %T: %v", snap, err)
	}
	delete(fields, "head")
	delete(fields, "updated_at")
	return fields
}
