package fold_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

// TestHistoryLinearNote pins the per-step snapshots of a linear note chain:
// each step holds the folded state through its commit, in linearization order,
// and the last step equals a full fold.
func TestHistoryLinearNote(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T", Body: "B"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetTitle{Title: "T2"}, model.AddTag{Tag: "x"}),
		mk("ccc", []string{"bbb"}, "carol", 300, 3, model.SetBody{Body: "B2"}),
	}
	steps, err := fold.History(chain)
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("len(steps) = %d, want 3", len(steps))
	}
	want := []struct {
		sha    model.SHA
		author model.Actor
		title  string
		body   string
	}{
		{"aaa", "alice", "T", "B"},
		{"bbb", "bob", "T2", "B"},
		{"ccc", "carol", "T2", "B2"},
	}
	for i, w := range want {
		s := steps[i]
		if s.Commit.SHA != w.sha {
			t.Errorf("step %d sha = %q, want %q", i, s.Commit.SHA, w.sha)
		}
		if s.Commit.Author != w.author {
			t.Errorf("step %d author = %q, want %q", i, s.Commit.Author, w.author)
		}
		n, ok := s.Snapshot.(model.Note)
		if !ok {
			t.Fatalf("step %d snapshot = %T, want model.Note", i, s.Snapshot)
		}
		if n.Title != w.title {
			t.Errorf("step %d title = %q, want %q", i, n.Title, w.title)
		}
		if n.Body != w.body {
			t.Errorf("step %d body = %q, want %q", i, n.Body, w.body)
		}
		if n.Head != w.sha {
			t.Errorf("step %d head = %q, want %q", i, n.Head, w.sha)
		}
	}
	full, err := fold.Note(chain)
	if err != nil {
		t.Fatalf("Note() error = %v", err)
	}
	if !reflect.DeepEqual(steps[2].Snapshot, full) {
		t.Fatalf("last step = %+v, want %+v", steps[2].Snapshot, full)
	}
}

// TestHistoryConcurrentClaim folds a diamond task chain: two concurrent claims
// race and the merge has no ops. History must report the trail in
// linearization order (aaa, ccc, bbb, ddd: ccc precedes bbb on the earlier
// author time at equal lamport) with first-wins claim semantics — and it must
// not error, even though a mid-fork prefix has two heads. fold.Fold on such a
// prefix would fail ErrMultipleHeads; History folds the prefix directly.
func TestHistoryConcurrentClaim(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "race", Type: model.TypeTask, Branch: "main"}),
		mk("bbb", []string{"aaa"}, "agent-b", 200, 2, model.Claim{Assignee: "agent-b"}),
		mk("ccc", []string{"aaa"}, "agent-c", 150, 2, model.Claim{Assignee: "agent-c"}),
		mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
	}
	steps, err := fold.History(chain)
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	want := []struct {
		sha      model.SHA
		status   model.Status
		assignee model.Actor
	}{
		{"aaa", model.StatusOpen, ""},
		{"ccc", model.StatusInProgress, "agent-c"},
		{"bbb", model.StatusInProgress, "agent-c"},
		{"ddd", model.StatusInProgress, "agent-c"},
	}
	if len(steps) != len(want) {
		t.Fatalf("len(steps) = %d, want %d", len(steps), len(want))
	}
	for i, w := range want {
		s := steps[i]
		if s.Commit.SHA != w.sha {
			t.Errorf("step %d sha = %q, want %q", i, s.Commit.SHA, w.sha)
		}
		task, ok := s.Snapshot.(model.Task)
		if !ok {
			t.Fatalf("step %d snapshot = %T, want model.Task", i, s.Snapshot)
		}
		if task.Status != w.status {
			t.Errorf("step %d status = %q, want %q", i, task.Status, w.status)
		}
		if task.Assignee != w.assignee {
			t.Errorf("step %d assignee = %q, want %q", i, task.Assignee, w.assignee)
		}
	}
}

// TestHistoryCheckpointStateNeutral folds a note chain compacted by a seed-safe
// checkpoint. The checkpoint commit is a step in the trail whose snapshot
// equals the prior step's in every field but Head (a bookkeeping field the CLI
// excludes), and the final snapshot still equals a full fold of the chain.
func TestHistoryCheckpointStateNeutral(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T0", Body: "B0", Tags: []string{"a"}})
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T1"})
	c2 := mk("c2", []string{"c1"}, "carol", 300, 3, model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "x.go"}})
	state, err := fold.Note([]model.PackCommit{c0, c1, c2})
	if err != nil {
		t.Fatalf("Note() error = %v", err)
	}
	cK := cp("cK", "c2", "compactor", 350, 4, state, 3, "c0", "c1", "c2")
	c3 := mk("c3", []string{"cK"}, "dave", 400, 5, model.SetBody{Body: "B3"})
	chain := []model.PackCommit{c0, c1, c2, cK, c3}

	steps, err := fold.History(chain)
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if len(steps) != 5 {
		t.Fatalf("len(steps) = %d, want 5", len(steps))
	}
	if steps[3].Commit.SHA != "cK" {
		t.Fatalf("step 3 sha = %q, want cK", steps[3].Commit.SHA)
	}
	before := steps[2].Snapshot.(model.Note)
	at := steps[3].Snapshot.(model.Note)
	if at.Head != "cK" || before.Head != "c2" {
		t.Fatalf("heads = (%q, %q), want (c2, cK)", before.Head, at.Head)
	}
	before.Head, at.Head = "", ""
	if !reflect.DeepEqual(before, at) {
		t.Fatalf("checkpoint step changed state beyond Head:\n before = %+v\n at     = %+v", before, at)
	}
	full, err := fold.Note(chain)
	if err != nil {
		t.Fatalf("Note() error = %v", err)
	}
	if got := steps[4].Snapshot.(model.Note); got.Body != "B3" || !reflect.DeepEqual(got, full) {
		t.Fatalf("last step = %+v, want %+v", got, full)
	}
}

// TestHistoryRunbookRunResultsStateNeutral is the cloneRuns regression. History
// re-folds every prefix over the same decoded chain, so the seed-safe
// checkpoint's State is shared across folds. A post-checkpoint result upsert
// overwrites an existing result in place; a shallow clone of the seed's runs
// would write through into the checkpoint State and into the already-stored
// checkpoint-step snapshot, corrupting both. The seed step must keep its
// original result.
func TestHistoryRunbookRunResultsStateNeutral(t *testing.T) {
	c0 := mk("c0", nil, "alice", 100, 1,
		model.CreateRunbook{Nonce: "n", Title: "RB"},
		model.AddStep{ID: "s1", Text: "build", Command: "", Position: "a"},
	)
	c1 := mk("c1", []string{"c0"}, "bob", 200, 2,
		model.StartRun{ID: "r1", Task: "task0"},
		model.SetRunStepStatus{RunID: "r1", StepID: "s1", Status: model.StepDone, Note: "seed"},
	)
	state, err := fold.Runbook([]model.PackCommit{c0, c1})
	if err != nil {
		t.Fatalf("Runbook() error = %v", err)
	}
	cK := cp("cK", "c1", "compactor", 250, 3, state, 2, "c0", "c1")
	c2 := mk("c2", []string{"cK"}, "carol", 300, 4,
		model.SetRunStepStatus{RunID: "r1", StepID: "s1", Status: model.StepFailed, Note: "post"},
	)
	chain := []model.PackCommit{c0, c1, cK, c2}

	steps, err := fold.History(chain)
	if err != nil {
		t.Fatalf("History() error = %v", err)
	}
	if len(steps) != 4 {
		t.Fatalf("len(steps) = %d, want 4", len(steps))
	}
	if steps[2].Commit.SHA != "cK" {
		t.Fatalf("step 2 sha = %q, want cK", steps[2].Commit.SHA)
	}
	seedResult := steps[2].Snapshot.(model.Runbook).Runs[0].Results[0]
	wantSeed := model.RunbookStepResult{StepID: "s1", Status: model.StepDone, Note: "seed", Actor: "bob", TS: 200}
	if seedResult != wantSeed {
		t.Fatalf("checkpoint-step result = %+v, want %+v (seed corrupted by shallow clone)", seedResult, wantSeed)
	}
	before := steps[1].Snapshot.(model.Runbook)
	at := steps[2].Snapshot.(model.Runbook)
	before.Head, at.Head = "", ""
	if !reflect.DeepEqual(before, at) {
		t.Fatalf("checkpoint step changed state beyond Head:\n before = %+v\n at     = %+v", before, at)
	}
	full, err := fold.Runbook(chain)
	if err != nil {
		t.Fatalf("Runbook() error = %v", err)
	}
	last := steps[3].Snapshot.(model.Runbook)
	postResult := last.Runs[0].Results[0]
	wantPost := model.RunbookStepResult{StepID: "s1", Status: model.StepFailed, Note: "post", Actor: "carol", TS: 300}
	if postResult != wantPost {
		t.Fatalf("last-step result = %+v, want %+v", postResult, wantPost)
	}
	if !reflect.DeepEqual(last, full) {
		t.Fatalf("last step = %+v, want %+v", last, full)
	}
}

// TestHistoryErrors covers the empty chain and a chain whose root carries no
// create op.
func TestHistoryErrors(t *testing.T) {
	if _, err := fold.History(nil); !errors.Is(err, fold.ErrEmptyChain) {
		t.Fatalf("History(nil) error = %v, want ErrEmptyChain", err)
	}
	noCreate := []model.PackCommit{mk("aaa", nil, "alice", 100, 1, model.SetTitle{Title: "T"})}
	if _, err := fold.History(noCreate); !errors.Is(err, fold.ErrNoCreate) {
		t.Fatalf("History(no-create) error = %v, want ErrNoCreate", err)
	}
}
