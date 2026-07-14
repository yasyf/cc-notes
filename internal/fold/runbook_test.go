package fold_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

func runbookChain(ops ...model.Op) []model.PackCommit {
	chain := make([]model.PackCommit, 0, 1+len(ops))
	chain = append(chain, mk("aaa", nil, "alice", 100, 1, model.CreateRunbook{Nonce: "n", Title: "rb"}))
	for i, op := range ops {
		sha := fmt.Sprintf("c%02d", i)
		chain = append(chain, mk(sha, []string{string(chain[len(chain)-1].SHA)}, "actor", 200+100*int64(i), uint64(i)+2, op))
	}
	return chain
}

func TestFoldRunbookLifecycle(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateRunbook{
			Nonce:       "n",
			Title:       "Deploy",
			Description: "ship it",
			Labels:      []string{"ops"},
		}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2,
			model.AddStep{ID: "s1", Text: "build", Command: "make", Position: "a"},
			model.AddStep{ID: "s2", Text: "test", Command: "go test", Position: "i"},
			model.AddStep{ID: "s3", Text: "ship", Command: "", Position: "t"},
		),
		mk("ccc", []string{"bbb"}, "carol", 300, 3,
			model.SetStepText{ID: "s2", Text: "run tests"},
			model.SetStepCommand{ID: "s1", Command: "make build"},
		),
		mk("ddd", []string{"ccc"}, "dave", 400, 4,
			model.SetStepPosition{ID: "s3", Position: "5"},
		),
		mk("eee", []string{"ddd"}, "erin", 500, 5,
			model.StartRun{ID: "r1", Task: "task0"},
		),
		mk("fff", []string{"eee"}, "frank", 600, 6,
			model.SetRunStepStatus{RunID: "r1", StepID: "s1", Status: model.StepDone, Note: "built"},
		),
		mk("ggg", []string{"fff"}, "grace", 700, 7,
			model.SetRunStepStatus{RunID: "r1", StepID: "s1", Status: model.StepFailed, Note: "rebuild"},
			model.SetRunStepStatus{RunID: "r1", StepID: "s2", Status: model.StepDone, Note: "green"},
		),
		mk("hhh", []string{"ggg"}, "heidi", 800, 8,
			model.FinishRun{ID: "r1", Status: model.RunSucceeded},
		),
		mk("iii", []string{"hhh"}, "ivan", 900, 9,
			model.AddComment{Body: "done"},
		),
		mk("jjj", []string{"iii"}, "judy", 1000, 10,
			model.SetRunbookStatus{Status: model.RunbookArchived},
		),
	}
	want := model.Runbook{
		ID:          "aaa",
		Title:       "Deploy",
		Description: "ship it",
		Status:      model.RunbookArchived,
		Steps: []model.RunbookStep{
			{ID: "s3", Text: "ship", Command: "", Position: "5"},
			{ID: "s1", Text: "build", Command: "make build", Position: "a"},
			{ID: "s2", Text: "run tests", Command: "go test", Position: "i"},
		},
		Runs: []model.RunbookRun{
			{
				ID: "r1", Task: "task0", Status: model.RunSucceeded,
				Runner: "erin", StartedAt: 500, FinishedAt: 800,
				Results: []model.RunbookStepResult{
					{StepID: "s1", Status: model.StepFailed, Note: "rebuild", Actor: "grace", TS: 700},
					{StepID: "s2", Status: model.StepDone, Note: "green", Actor: "grace", TS: 700},
				},
			},
		},
		Labels:     []string{"ops"},
		Comments:   []model.Comment{{Author: "ivan", TS: 900, Body: "done"}},
		Author:     "alice",
		CreatedAt:  100,
		UpdatedAt:  1000,
		ArchivedAt: 1000,
		Head:       "jjj",
	}
	got, err := fold.Runbook(chain)
	if err != nil {
		t.Fatalf("Runbook() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Runbook() = %+v, want %+v", got, want)
	}
	snap, err := fold.Fold(chain)
	if err != nil {
		t.Fatalf("Fold() error = %v", err)
	}
	dispatched, ok := snap.(model.Runbook)
	if !ok {
		t.Fatalf("Fold() = %T, want model.Runbook", snap)
	}
	if !reflect.DeepEqual(dispatched, want) {
		t.Fatalf("Fold() = %+v, want %+v", dispatched, want)
	}
	if snap.EntityID() != "aaa" {
		t.Fatalf("EntityID() = %q, want %q", snap.EntityID(), "aaa")
	}
}

func TestFoldRunbookStepOrderDeterministic(t *testing.T) {
	diamond := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1,
			model.CreateRunbook{Nonce: "n", Title: "rb"},
			model.AddStep{ID: "s1", Text: "one", Position: "a"},
			model.AddStep{ID: "s2", Text: "two", Position: "c"},
		),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.AddStep{ID: "s3", Text: "mid", Position: "b"}),
		mk("ccc", []string{"aaa"}, "carol", 250, 2, model.AddStep{ID: "s4", Text: "tie", Position: "a"}),
		mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
	}
	wantIDs := []string{"s1", "s4", "s3", "s2"}
	for i, input := range permutations(diamond) {
		got, err := fold.Runbook(input)
		if err != nil {
			t.Fatalf("permutation %d: Runbook() error = %v", i, err)
		}
		ids := make([]string, len(got.Steps))
		for j, s := range got.Steps {
			ids[j] = s.ID
		}
		if !reflect.DeepEqual(ids, wantIDs) {
			t.Fatalf("permutation %d: step order = %v, want %v", i, ids, wantIDs)
		}
	}
}

func TestFoldRunbookConcurrentPositionEdit(t *testing.T) {
	diamond := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1,
			model.CreateRunbook{Nonce: "n", Title: "rb"},
			model.AddStep{ID: "s1", Text: "step", Position: "a"},
		),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetStepPosition{ID: "s1", Position: "x"}),
		mk("ccc", []string{"aaa"}, "carol", 250, 2, model.SetStepPosition{ID: "s1", Position: "z"}),
		mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
	}
	for i, input := range permutations(diamond) {
		got, err := fold.Runbook(input)
		if err != nil {
			t.Fatalf("permutation %d: Runbook() error = %v", i, err)
		}
		if len(got.Steps) != 1 {
			t.Fatalf("permutation %d: %d steps, want 1", i, len(got.Steps))
		}
		if got.Steps[0].Position != "z" {
			t.Fatalf("permutation %d: position = %q, want z (later-lww)", i, got.Steps[0].Position)
		}
	}
}

func TestFoldRunbookConcurrentRuns(t *testing.T) {
	diamond := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateRunbook{Nonce: "n", Title: "rb"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2,
			model.StartRun{ID: "r1", Task: "taskA"},
			model.SetRunStepStatus{RunID: "r1", StepID: "s1", Status: model.StepDone, Note: "a"},
			model.FinishRun{ID: "r1", Status: model.RunSucceeded},
		),
		mk("ccc", []string{"aaa"}, "carol", 250, 2,
			model.StartRun{ID: "r2", Task: "taskB"},
			model.SetRunStepStatus{RunID: "r2", StepID: "s1", Status: model.StepFailed, Note: "b"},
			model.FinishRun{ID: "r2", Status: model.RunFailed},
		),
		mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
	}
	for i, input := range permutations(diamond) {
		got, err := fold.Runbook(input)
		if err != nil {
			t.Fatalf("permutation %d: Runbook() error = %v", i, err)
		}
		if len(got.Runs) != 2 {
			t.Fatalf("permutation %d: %d runs, want 2", i, len(got.Runs))
		}
		if got.Runs[0].ID != "r1" || got.Runs[1].ID != "r2" {
			t.Fatalf("permutation %d: run order = [%s %s], want [r1 r2]", i, got.Runs[0].ID, got.Runs[1].ID)
		}
		if got.Runs[0].Status != model.RunSucceeded || got.Runs[1].Status != model.RunFailed {
			t.Fatalf("permutation %d: statuses = [%s %s]", i, got.Runs[0].Status, got.Runs[1].Status)
		}
		if got.Runs[0].Runner != "bob" || got.Runs[1].Runner != "carol" {
			t.Fatalf("permutation %d: runners = [%s %s]", i, got.Runs[0].Runner, got.Runs[1].Runner)
		}
	}
}

func TestFoldRunbookStartRunIdempotent(t *testing.T) {
	diamond := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateRunbook{Nonce: "n", Title: "rb"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.StartRun{ID: "r1", Task: "taskA"}),
		mk("ccc", []string{"aaa"}, "carol", 250, 2, model.StartRun{ID: "r1", Task: "taskB"}),
		mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
	}
	for i, input := range permutations(diamond) {
		got, err := fold.Runbook(input)
		if err != nil {
			t.Fatalf("permutation %d: Runbook() error = %v", i, err)
		}
		if len(got.Runs) != 1 {
			t.Fatalf("permutation %d: %d runs, want 1", i, len(got.Runs))
		}
		if got.Runs[0].Task != "taskA" || got.Runs[0].Runner != "bob" || got.Runs[0].StartedAt != 200 {
			t.Fatalf("permutation %d: run = %+v, want first-in-linearization (bob/taskA/200)", i, got.Runs[0])
		}
	}
}

func TestFoldRunbookOrphanAndPostFinish(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1,
			model.CreateRunbook{Nonce: "n", Title: "RB"},
			model.AddStep{ID: "s1", Text: "step", Command: "", Position: "a"},
		),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.StartRun{ID: "r1", Task: "task0"}),
		mk("ccc", []string{"bbb"}, "carol", 300, 3,
			model.SetRunStepStatus{RunID: "r1", StepID: "s1", Status: model.StepDone, Note: "ok"},
		),
		mk("ddd", []string{"ccc"}, "dave", 400, 4,
			model.SetRunStepStatus{RunID: "ghost", StepID: "s1", Status: model.StepDone, Note: "x"},
			model.SetStepText{ID: "ghost", Text: "y"},
			model.RemoveStep{ID: "ghost"},
			model.FinishRun{ID: "ghost", Status: model.RunSucceeded},
		),
		mk("eee", []string{"ddd"}, "erin", 500, 5, model.RemoveStep{ID: "s1"}),
		mk("fff", []string{"eee"}, "frank", 600, 6, model.FinishRun{ID: "r1", Status: model.RunSucceeded}),
		mk("ggg", []string{"fff"}, "grace", 700, 7,
			model.SetRunStepStatus{RunID: "r1", StepID: "s1", Status: model.StepFailed, Note: "post"},
		),
		mk("hhh", []string{"ggg"}, "heidi", 800, 8, model.FinishRun{ID: "r1", Status: model.RunFailed}),
	}
	want := model.Runbook{
		ID:     "aaa",
		Title:  "RB",
		Status: model.RunbookActive,
		Steps:  []model.RunbookStep{},
		Runs: []model.RunbookRun{
			{
				ID: "r1", Task: "task0", Status: model.RunFailed,
				Runner: "bob", StartedAt: 200, FinishedAt: 800,
				Results: []model.RunbookStepResult{
					{StepID: "s1", Status: model.StepFailed, Note: "post", Actor: "grace", TS: 700},
				},
			},
		},
		Labels:    []string{},
		Comments:  []model.Comment{},
		Author:    "alice",
		CreatedAt: 100,
		UpdatedAt: 800,
		Head:      "hhh",
	}
	got, err := fold.Runbook(chain)
	if err != nil {
		t.Fatalf("Runbook() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Runbook() = %+v, want %+v", got, want)
	}
}

func TestFoldRunbookStatusTimestamps(t *testing.T) {
	chain := runbookChain(
		model.SetRunbookStatus{Status: model.RunbookArchived}, // t=200
		model.SetRunbookStatus{Status: model.RunbookActive},   // t=300
	)
	cases := []struct {
		name           string
		prefix         int
		wantStatus     model.RunbookStatus
		wantArchivedAt int64
	}{
		{name: "created active", prefix: 1, wantStatus: model.RunbookActive},
		{name: "archived stamps archived_at", prefix: 2, wantStatus: model.RunbookArchived, wantArchivedAt: 200},
		{name: "reactivate clears archived_at", prefix: 3, wantStatus: model.RunbookActive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fold.Runbook(chain[:tc.prefix])
			if err != nil {
				t.Fatalf("Runbook() error = %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.ArchivedAt != tc.wantArchivedAt {
				t.Fatalf("ArchivedAt = %d, want %d", got.ArchivedAt, tc.wantArchivedAt)
			}
		})
	}
}

func TestFoldRunbookAnchorsAndDelete(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateRunbook{
			Nonce: "n", Title: "Deploy",
			Anchors: []model.Anchor{
				{Kind: model.AnchorPath, Value: "scripts/deploy.sh"},
				{Kind: model.AnchorDir, Value: "scripts"},
			},
		}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2,
			model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorBranch, Value: "main"}},
			model.RemoveAnchor{Anchor: model.Anchor{Kind: model.AnchorDir, Value: "scripts"}},
		),
		mk("ccc", []string{"bbb"}, "carol", 300, 3, model.DeleteNote{}),
	}
	want := model.Runbook{
		ID: "aaa", Title: "Deploy", Status: model.RunbookActive,
		Steps: []model.RunbookStep{}, Runs: []model.RunbookRun{},
		Labels: []string{}, Comments: []model.Comment{},
		Author: "alice", CreatedAt: 100, UpdatedAt: 300, Head: "ccc",
		Anchors: []model.Anchor{
			{Kind: model.AnchorBranch, Value: "main"},
			{Kind: model.AnchorPath, Value: "scripts/deploy.sh"},
		},
		Deleted: true,
	}
	got, err := fold.Runbook(chain)
	if err != nil {
		t.Fatalf("Runbook() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Runbook() = %+v, want %+v", got, want)
	}
}

// codecCheckpoint builds a checkpoint commit whose Checkpoint op round-trips
// through the real pack codec (marshal then DecodePack), so the seeded State —
// its anchors and tombstone included — must survive encoding, not just an
// in-memory struct copy.
func codecCheckpoint(t *testing.T, sha, parent, author string, at int64, lamport uint64, state model.Snapshot, coversLamport uint64, covers ...string) model.PackCommit {
	t.Helper()
	shas := make([]model.SHA, len(covers))
	for i, c := range covers {
		shas[i] = model.SHA(c)
	}
	wire, err := json.Marshal(model.Pack{Lamport: model.Lamport(lamport), Ops: []model.Op{model.Checkpoint{
		EntityID: state.EntityID(), State: state, CoversLamport: model.Lamport(coversLamport), CoversShas: shas,
	}}})
	if err != nil {
		t.Fatalf("marshal checkpoint pack: %v", err)
	}
	pack, err := model.DecodePack(wire)
	if err != nil {
		t.Fatalf("decode checkpoint pack: %v", err)
	}
	return model.PackCommit{SHA: model.SHA(sha), Parents: []model.SHA{model.SHA(parent)}, Author: model.Actor(author), AuthorTime: at, Pack: pack}
}

// TestFoldRunbookCompactedEqualsFullWithAnchors folds runbook chains against the
// identical chain with an empty commit in the checkpoint slot, with the
// checkpoint round-tripped through the real codec. In "delete after checkpoint"
// the suffix removes an anchor and tombstones a live checkpoint State; in
// "delete before checkpoint" the tombstone is baked into the checkpoint State
// itself, so seeding alone must restore Deleted.
func TestFoldRunbookCompactedEqualsFullWithAnchors(t *testing.T) {
	wantAnchors := []model.Anchor{{Kind: model.AnchorBranch, Value: "main"}}

	t.Run("delete after checkpoint", func(t *testing.T) {
		c0 := mk("c0", nil, "alice", 100, 1, model.CreateRunbook{
			Nonce: "n", Title: "R",
			Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "a.go"}},
		})
		c1 := mk("c1", []string{"c0"}, "bob", 200, 2,
			model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorBranch, Value: "main"}},
		)
		state, err := fold.Runbook([]model.PackCommit{c0, c1})
		if err != nil {
			t.Fatalf("fold prefix: %v", err)
		}
		if state.Deleted {
			t.Fatal("prefix state already deleted, want a live checkpoint")
		}
		cK := codecCheckpoint(t, "cK", "c1", "compactor", 250, 3, state, 2, "c0", "c1")
		cKempty := mk("cK", []string{"c1"}, "compactor", 250, 3)
		c2 := mk("c2", []string{"cK"}, "carol", 300, 4,
			model.RemoveAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "a.go"}},
			model.DeleteNote{},
		)

		gotFull, err := fold.Runbook([]model.PackCommit{c0, c1, cKempty, c2})
		if err != nil {
			t.Fatalf("fold full: %v", err)
		}
		gotCompact, err := fold.Runbook([]model.PackCommit{c0, c1, cK, c2})
		if err != nil {
			t.Fatalf("fold compacted: %v", err)
		}
		if !reflect.DeepEqual(gotCompact, gotFull) {
			t.Fatalf("compacted = %+v\nfull = %+v", gotCompact, gotFull)
		}
		if !reflect.DeepEqual(gotCompact.Anchors, wantAnchors) {
			t.Errorf("Anchors = %+v, want %+v", gotCompact.Anchors, wantAnchors)
		}
		if !gotCompact.Deleted {
			t.Error("Deleted = false, want true")
		}
	})

	t.Run("delete before checkpoint", func(t *testing.T) {
		c0 := mk("c0", nil, "alice", 100, 1, model.CreateRunbook{
			Nonce: "n", Title: "R",
			Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "a.go"}},
		})
		c1 := mk("c1", []string{"c0"}, "bob", 200, 2,
			model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorBranch, Value: "main"}},
			model.RemoveAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "a.go"}},
			model.DeleteNote{},
		)
		state, err := fold.Runbook([]model.PackCommit{c0, c1})
		if err != nil {
			t.Fatalf("fold prefix: %v", err)
		}
		if !state.Deleted {
			t.Fatal("prefix state not deleted, want a tombstoned checkpoint")
		}
		cK := codecCheckpoint(t, "cK", "c1", "compactor", 250, 3, state, 2, "c0", "c1")
		cKempty := mk("cK", []string{"c1"}, "compactor", 250, 3)
		c2 := mk("c2", []string{"cK"}, "carol", 300, 4, model.AddComment{Body: "post"})

		gotFull, err := fold.Runbook([]model.PackCommit{c0, c1, cKempty, c2})
		if err != nil {
			t.Fatalf("fold full: %v", err)
		}
		gotCompact, err := fold.Runbook([]model.PackCommit{c0, c1, cK, c2})
		if err != nil {
			t.Fatalf("fold compacted: %v", err)
		}
		if !reflect.DeepEqual(gotCompact, gotFull) {
			t.Fatalf("compacted = %+v\nfull = %+v", gotCompact, gotFull)
		}
		if !reflect.DeepEqual(gotCompact.Anchors, wantAnchors) {
			t.Errorf("Anchors = %+v, want %+v", gotCompact.Anchors, wantAnchors)
		}
		if !gotCompact.Deleted {
			t.Error("seeded Deleted = false, want true (restored from the checkpoint State)")
		}
	})
}

// TestFoldRunbookPreAnchorWireIdentity replays a fixture chain of pre-anchor
// runbook wire bytes (no anchors key anywhere) through the real codec and
// asserts the fold equals the pre-change snapshot exactly — Anchors nil,
// Deleted false, marshaled bytes free of both keys — including a checkpoint
// round-trip through the codec that must seed to the identical fold.
func TestFoldRunbookPreAnchorWireIdentity(t *testing.T) {
	wires := []struct {
		sha, parent, author string
		at                  int64
		pack                string
	}{
		{"c0", "", "alice", 100, `{"v":1,"lamport":1,"ops":[{"kind":"create_runbook","nonce":"n1","title":"Deploy","description":"ship","labels":["ops"]}]}`},
		{"c1", "c0", "bob", 200, `{"v":1,"lamport":2,"ops":[{"kind":"add_step","id":"s1","text":"build","command":"make","position":"i"}]}`},
		{"c2", "c1", "carol", 300, `{"v":1,"lamport":3,"ops":[{"kind":"start_run","id":"r1","task":""}]}`},
	}
	chain := make([]model.PackCommit, len(wires))
	for i, w := range wires {
		pack, err := model.DecodePack([]byte(w.pack))
		if err != nil {
			t.Fatalf("decode wire %s: %v", w.sha, err)
		}
		var parents []model.SHA
		if w.parent != "" {
			parents = []model.SHA{model.SHA(w.parent)}
		}
		chain[i] = model.PackCommit{SHA: model.SHA(w.sha), Parents: parents, Author: model.Actor(w.author), AuthorTime: w.at, Pack: pack}
	}
	want := model.Runbook{
		ID: "c0", Title: "Deploy", Description: "ship", Status: model.RunbookActive,
		Steps: []model.RunbookStep{{ID: "s1", Text: "build", Command: "make", Position: "i"}},
		Runs: []model.RunbookRun{{
			ID: "r1", Status: model.RunRunning, Runner: "carol", StartedAt: 300,
			Results: []model.RunbookStepResult{},
		}},
		Labels: []string{"ops"}, Comments: []model.Comment{},
		Author: "alice", CreatedAt: 100, UpdatedAt: 300, Head: "c2",
	}
	got, err := fold.Runbook(chain)
	if err != nil {
		t.Fatalf("fold pre-anchor chain: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fold = %+v, want %+v", got, want)
	}

	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	for _, key := range []string{`"anchors"`, `"deleted"`} {
		if strings.Contains(string(raw), key) {
			t.Errorf("pre-anchor snapshot bytes contain %s", key)
		}
	}

	cpWire, err := json.Marshal(model.Pack{Lamport: 4, Ops: []model.Op{model.Checkpoint{
		EntityID: got.EntityID(), State: got, CoversLamport: 3, CoversShas: []model.SHA{"c0", "c1", "c2"},
	}}})
	if err != nil {
		t.Fatalf("marshal checkpoint pack: %v", err)
	}
	cpPack, err := model.DecodePack(cpWire)
	if err != nil {
		t.Fatalf("decode checkpoint pack: %v", err)
	}
	cK := model.PackCommit{SHA: "cK", Parents: []model.SHA{"c2"}, Author: "compactor", AuthorTime: 350, Pack: cpPack}
	c3 := mk("c3", []string{"cK"}, "dave", 400, 5, model.AddComment{Body: "post-compaction"})

	seeded, err := fold.Runbook(append(slices.Clone(chain), cK, c3))
	if err != nil {
		t.Fatalf("fold seeded: %v", err)
	}
	full, err := fold.Runbook(append(slices.Clone(chain), mk("cK", []string{"c2"}, "compactor", 350, 4), c3))
	if err != nil {
		t.Fatalf("fold full: %v", err)
	}
	if !reflect.DeepEqual(seeded, full) {
		t.Fatalf("seeded = %+v\nfull = %+v", seeded, full)
	}
}
