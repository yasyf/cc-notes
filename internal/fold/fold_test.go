package fold_test

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

func TestFoldNote(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateNote{
			Nonce:   "n",
			Title:   "T",
			Body:    "B",
			Tags:    []string{"beta", "alpha"},
			Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "x.go"}},
		}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2,
			model.SetTitle{Title: "T2"},
			model.AddTag{Tag: "gamma"},
			model.RemoveTag{Tag: "alpha"},
			model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorCommit, Value: "deadbeef"}},
		),
		mk("ccc", []string{"bbb"}, "carol", 300, 3,
			model.SetBody{Body: "B2"},
			model.RemoveAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "x.go"}},
		),
	}
	want := model.Note{
		ID:           "aaa",
		Title:        "T2",
		Body:         "B2",
		Tags:         []string{"beta", "gamma"},
		Anchors:      []model.Anchor{{Kind: model.AnchorCommit, Value: "deadbeef"}},
		Author:       "alice",
		CreatedAt:    100,
		UpdatedAt:    300,
		SupersededBy: []model.EntityID{},
		Head:         "ccc",
	}
	got, err := fold.Note(chain)
	if err != nil {
		t.Fatalf("Note() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Note() = %+v, want %+v", got, want)
	}
	snap, err := fold.Fold(chain)
	if err != nil {
		t.Fatalf("Fold() error = %v", err)
	}
	dispatched, ok := snap.(model.Note)
	if !ok {
		t.Fatalf("Fold() = %T, want model.Note", snap)
	}
	if !reflect.DeepEqual(dispatched, want) {
		t.Fatalf("Fold() = %+v, want %+v", dispatched, want)
	}
	if snap.EntityID() != "aaa" {
		t.Fatalf("EntityID() = %q, want %q", snap.EntityID(), "aaa")
	}
}

func TestFoldTaskLifecycle(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateTask{
			Nonce:       "n",
			Title:       "Fix flaky sync",
			Description: "round-trip flakes",
			Type:        model.TypeBug,
			Priority:    1,
			Branch:      "main",
			Labels:      []string{"ci"},
		}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Claim{Assignee: "bob"}),
		mk("ccc", []string{"bbb"}, "bob", 300, 3,
			model.AddComment{Body: "on it"},
			model.AddLabel{Label: "sync"},
			model.AddDep{ID: "feedface"},
		),
		mk("ddd", []string{"ccc"}, "bob", 400, 4, model.SetStatus{Status: model.StatusDone}),
	}
	want := model.Task{
		ID:               "aaa",
		Branch:           "main",
		Title:            "Fix flaky sync",
		Description:      "round-trip flakes",
		Type:             model.TypeBug,
		Status:           model.StatusDone,
		Priority:         1,
		Assignee:         "bob",
		HeartbeatAt:      400,
		HeartbeatLamport: 4,
		Labels:           []string{"ci", "sync"},
		BlockedBy:        []model.EntityID{"feedface"},
		Comments:         []model.Comment{{Author: "bob", TS: 300, Body: "on it"}},
		CreatedAt:        100,
		UpdatedAt:        400,
		StartedAt:        200,
		ClosedAt:         400,
		Commits:          []model.SHA{},
		Criteria:         []model.Criterion{},
		Head:             "ddd",
	}
	got, err := fold.Task(chain)
	if err != nil {
		t.Fatalf("Task() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Task() = %+v, want %+v", got, want)
	}
	snap, err := fold.Fold(chain)
	if err != nil {
		t.Fatalf("Fold() error = %v", err)
	}
	dispatched, ok := snap.(model.Task)
	if !ok {
		t.Fatalf("Fold() = %T, want model.Task", snap)
	}
	if !reflect.DeepEqual(dispatched, want) {
		t.Fatalf("Fold() = %+v, want %+v", dispatched, want)
	}
	if snap.EntityID() != "aaa" {
		t.Fatalf("EntityID() = %q, want %q", snap.EntityID(), "aaa")
	}
}

func TestFoldDeterminismDiamond(t *testing.T) {
	diamond := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "orig"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetTitle{Title: "from-b"}),
		mk("ccc", []string{"aaa"}, "carol", 200, 2, model.SetTitle{Title: "from-c"}),
		mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
	}
	want := model.Note{
		ID:           "aaa",
		Title:        "from-c",
		Tags:         []string{},
		Anchors:      []model.Anchor{},
		Author:       "alice",
		CreatedAt:    100,
		UpdatedAt:    200,
		SupersededBy: []model.EntityID{},
		Head:         "ddd",
	}
	for i, input := range permutations(diamond) {
		got, err := fold.Note(input)
		if err != nil {
			t.Fatalf("permutation %d: Note() error = %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("permutation %d: Note() = %+v, want %+v", i, got, want)
		}
	}
}

func TestFoldLWWTiebreaks(t *testing.T) {
	cases := []struct {
		name               string
		bLamport, cLamport uint64
		bTime, cTime       int64
		want               string
	}{
		{name: "higher lamport wins", bLamport: 3, bTime: 150, cLamport: 2, cTime: 250, want: "from-b"},
		{name: "later author time wins on lamport tie", bLamport: 2, bTime: 150, cLamport: 2, cTime: 250, want: "from-c"},
		{name: "higher sha wins on full tie", bLamport: 2, bTime: 200, cLamport: 2, cTime: 200, want: "from-c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diamond := []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "orig"}),
				mk("bbb", []string{"aaa"}, "bob", tc.bTime, tc.bLamport, model.SetTitle{Title: "from-b"}),
				mk("ccc", []string{"aaa"}, "carol", tc.cTime, tc.cLamport, model.SetTitle{Title: "from-c"}),
				mk("ddd", []string{"bbb", "ccc"}, "dave", 400, 4),
			}
			for i, input := range permutations(diamond) {
				got, err := fold.Note(input)
				if err != nil {
					t.Fatalf("permutation %d: Note() error = %v", i, err)
				}
				if got.Title != tc.want {
					t.Fatalf("permutation %d: Title = %q, want %q", i, got.Title, tc.want)
				}
			}
		})
	}
}

func TestFoldConcurrentClaim(t *testing.T) {
	cases := []struct {
		name         string
		bTime, cTime int64
		want         model.Actor
		wantStarted  int64
	}{
		{name: "sha tiebreak picks b", bTime: 200, cTime: 200, want: "agent-b", wantStarted: 200},
		{name: "earlier time picks c", bTime: 200, cTime: 150, want: "agent-c", wantStarted: 150},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diamond := []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "race", Type: model.TypeTask, Branch: "main"}),
				mk("bbb", []string{"aaa"}, "agent-b", tc.bTime, 2, model.Claim{Assignee: "agent-b"}),
				mk("ccc", []string{"aaa"}, "agent-c", tc.cTime, 2, model.Claim{Assignee: "agent-c"}),
				mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
			}
			for i, input := range permutations(diamond) {
				got, err := fold.Task(input)
				if err != nil {
					t.Fatalf("permutation %d: Task() error = %v", i, err)
				}
				if got.Assignee != tc.want {
					t.Fatalf("permutation %d: Assignee = %q, want %q", i, got.Assignee, tc.want)
				}
				if got.Status != model.StatusInProgress {
					t.Fatalf("permutation %d: Status = %q, want %q", i, got.Status, model.StatusInProgress)
				}
				if got.StartedAt != tc.wantStarted {
					t.Fatalf("permutation %d: StartedAt = %d, want %d", i, got.StartedAt, tc.wantStarted)
				}
			}
		})
	}
}

// taskChain builds a linear task chain: a create at t=100, then one commit
// per op at t=200+100*i with lamport i+2.
func taskChain(ops ...model.Op) []model.PackCommit {
	chain := make([]model.PackCommit, 0, 1+len(ops))
	chain = append(chain, mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "t", Type: model.TypeTask, Branch: "main"}))
	for i, op := range ops {
		sha := fmt.Sprintf("c%02d", i)
		chain = append(chain, mk(sha, []string{string(chain[len(chain)-1].SHA)}, "actor", 200+100*int64(i), uint64(i)+2, op))
	}
	return chain
}

// sprintChain builds a linear sprint chain in the shape of taskChain.
func sprintChain(ops ...model.Op) []model.PackCommit {
	chain := make([]model.PackCommit, 0, 1+len(ops))
	chain = append(chain, mk("aaa", nil, "alice", 100, 1, model.CreateSprint{Nonce: "n", Title: "s"}))
	for i, op := range ops {
		sha := fmt.Sprintf("c%02d", i)
		chain = append(chain, mk(sha, []string{string(chain[len(chain)-1].SHA)}, "actor", 200+100*int64(i), uint64(i)+2, op))
	}
	return chain
}

// projectChain builds a linear project chain in the shape of taskChain.
func projectChain(ops ...model.Op) []model.PackCommit {
	chain := make([]model.PackCommit, 0, 1+len(ops))
	chain = append(chain, mk("aaa", nil, "alice", 100, 1, model.CreateProject{Nonce: "n", Title: "p"}))
	for i, op := range ops {
		sha := fmt.Sprintf("c%02d", i)
		chain = append(chain, mk(sha, []string{string(chain[len(chain)-1].SHA)}, "actor", 200+100*int64(i), uint64(i)+2, op))
	}
	return chain
}

func TestFoldClaimConditions(t *testing.T) {
	cases := []struct {
		name         string
		ops          []model.Op
		wantAssignee model.Actor
		wantStatus   model.Status
		wantStarted  int64
		wantClosed   int64
	}{
		{
			name:         "claims open unassigned",
			ops:          []model.Op{model.Claim{Assignee: "bob"}},
			wantAssignee: "bob",
			wantStatus:   model.StatusInProgress,
			wantStarted:  200,
		},
		{
			name:         "noop when already assigned",
			ops:          []model.Op{model.SetAssignee{Assignee: "alice"}, model.Claim{Assignee: "bob"}},
			wantAssignee: "alice",
			wantStatus:   model.StatusOpen,
		},
		{
			name:        "noop when in progress",
			ops:         []model.Op{model.SetStatus{Status: model.StatusInProgress}, model.Claim{Assignee: "bob"}},
			wantStatus:  model.StatusInProgress,
			wantStarted: 200,
		},
		{
			name:       "noop when done",
			ops:        []model.Op{model.SetStatus{Status: model.StatusDone}, model.Claim{Assignee: "bob"}},
			wantStatus: model.StatusDone,
			wantClosed: 200,
		},
		{
			name: "reapplies after reopen and unassign",
			ops: []model.Op{
				model.Claim{Assignee: "alice"},
				model.SetStatus{Status: model.StatusOpen},
				model.SetAssignee{Assignee: ""},
				model.Claim{Assignee: "bob"},
			},
			wantAssignee: "bob",
			wantStatus:   model.StatusInProgress,
			wantStarted:  500,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fold.Task(taskChain(tc.ops...))
			if err != nil {
				t.Fatalf("Task() error = %v", err)
			}
			if got.Assignee != tc.wantAssignee {
				t.Fatalf("Assignee = %q, want %q", got.Assignee, tc.wantAssignee)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.StartedAt != tc.wantStarted {
				t.Fatalf("StartedAt = %d, want %d", got.StartedAt, tc.wantStarted)
			}
			if got.ClosedAt != tc.wantClosed {
				t.Fatalf("ClosedAt = %d, want %d", got.ClosedAt, tc.wantClosed)
			}
		})
	}
}

func TestFoldStatusTimestamps(t *testing.T) {
	chain := taskChain(
		model.SetStatus{Status: model.StatusDone},       // t=200
		model.SetStatus{Status: model.StatusOpen},       // t=300
		model.SetStatus{Status: model.StatusInProgress}, // t=400
		model.SetStatus{Status: model.StatusDone},       // t=500
		model.SetStatus{Status: model.StatusInProgress}, // t=600
	)
	cases := []struct {
		name        string
		prefix      int
		wantStatus  model.Status
		wantStarted int64
		wantClosed  int64
	}{
		{name: "done sets closed", prefix: 2, wantStatus: model.StatusDone, wantClosed: 200},
		{name: "reopen clears closed", prefix: 3, wantStatus: model.StatusOpen},
		{name: "in progress sets started", prefix: 4, wantStatus: model.StatusInProgress, wantStarted: 400},
		{name: "done keeps started", prefix: 5, wantStatus: model.StatusDone, wantStarted: 400, wantClosed: 500},
		{name: "re-entering in progress resets started", prefix: 6, wantStatus: model.StatusInProgress, wantStarted: 600},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fold.Task(chain[:tc.prefix])
			if err != nil {
				t.Fatalf("Task() error = %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.StartedAt != tc.wantStarted {
				t.Fatalf("StartedAt = %d, want %d", got.StartedAt, tc.wantStarted)
			}
			if got.ClosedAt != tc.wantClosed {
				t.Fatalf("ClosedAt = %d, want %d", got.ClosedAt, tc.wantClosed)
			}
		})
	}
}

func TestFoldTagInterleavings(t *testing.T) {
	cases := []struct {
		name         string
		bTime, cTime int64
		bOp, cOp     model.Op
		want         []string
	}{
		{
			name:  "add after remove keeps tag",
			bTime: 200, cTime: 250,
			bOp: model.RemoveTag{Tag: "x"}, cOp: model.AddTag{Tag: "x"},
			want: []string{"x"},
		},
		{
			name:  "remove after add drops tag",
			bTime: 250, cTime: 200,
			bOp: model.RemoveTag{Tag: "x"}, cOp: model.AddTag{Tag: "x"},
			want: []string{},
		},
		{
			name:  "concurrent adds dedupe",
			bTime: 200, cTime: 250,
			bOp: model.AddTag{Tag: "y"}, cOp: model.AddTag{Tag: "y"},
			want: []string{"x", "y"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diamond := []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Tags: []string{"x"}}),
				mk("bbb", []string{"aaa"}, "bob", tc.bTime, 2, tc.bOp),
				mk("ccc", []string{"aaa"}, "carol", tc.cTime, 2, tc.cOp),
				mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
			}
			for i, input := range permutations(diamond) {
				got, err := fold.Note(input)
				if err != nil {
					t.Fatalf("permutation %d: Note() error = %v", i, err)
				}
				if !reflect.DeepEqual(got.Tags, tc.want) {
					t.Fatalf("permutation %d: Tags = %v, want %v", i, got.Tags, tc.want)
				}
			}
		})
	}
}

func TestFoldDepInterleavings(t *testing.T) {
	cases := []struct {
		name         string
		bTime, cTime int64
		want         []model.EntityID
	}{
		{name: "add after remove keeps dep", bTime: 200, cTime: 250, want: []model.EntityID{"dep1"}},
		{name: "remove after add drops dep", bTime: 250, cTime: 200, want: []model.EntityID{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diamond := []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Type: model.TypeTask, Branch: "main"}),
				mk("bbb", []string{"aaa"}, "bob", tc.bTime, 2, model.RemoveDep{ID: "dep1"}),
				mk("ccc", []string{"aaa"}, "carol", tc.cTime, 2, model.AddDep{ID: "dep1"}),
				mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
			}
			for i, input := range permutations(diamond) {
				got, err := fold.Task(input)
				if err != nil {
					t.Fatalf("permutation %d: Task() error = %v", i, err)
				}
				if !reflect.DeepEqual(got.BlockedBy, tc.want) {
					t.Fatalf("permutation %d: BlockedBy = %v, want %v", i, got.BlockedBy, tc.want)
				}
			}
		})
	}
}

func TestFoldSetBranchLWW(t *testing.T) {
	cases := []struct {
		name       string
		bBranch    model.Branch
		cBranch    model.Branch
		bTime      int64
		cTime      int64
		wantBranch model.Branch
	}{
		{name: "later set wins", bBranch: "feat", cBranch: "hotfix", bTime: 200, cTime: 250, wantBranch: "hotfix"},
		{name: "earlier set loses", bBranch: "hotfix", cBranch: "feat", bTime: 250, cTime: 200, wantBranch: "hotfix"},
		{name: "later set to backlog wins", bBranch: "feat", cBranch: "", bTime: 200, cTime: 250, wantBranch: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diamond := []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Type: model.TypeTask, Branch: "main"}),
				mk("bbb", []string{"aaa"}, "bob", tc.bTime, 2, model.SetBranch{Branch: tc.bBranch}),
				mk("ccc", []string{"aaa"}, "carol", tc.cTime, 2, model.SetBranch{Branch: tc.cBranch}),
				mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
			}
			for i, input := range permutations(diamond) {
				got, err := fold.Task(input)
				if err != nil {
					t.Fatalf("permutation %d: Task() error = %v", i, err)
				}
				if got.Branch != tc.wantBranch {
					t.Fatalf("permutation %d: Branch = %q, want %q", i, got.Branch, tc.wantBranch)
				}
			}
		})
	}
}

func TestFoldTombstoneMonotone(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "t"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.DeleteNote{}),
		mk("ccc", []string{"aaa"}, "carol", 250, 2, model.SetTitle{Title: "still updates"}),
		mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
		mk("eee", []string{"ddd"}, "erin", 400, 4, model.AddTag{Tag: "z"}),
	}
	for i, input := range permutations(chain) {
		got, err := fold.Note(input)
		if err != nil {
			t.Fatalf("permutation %d: Note() error = %v", i, err)
		}
		if !got.Deleted {
			t.Fatalf("permutation %d: Deleted = false, want true", i)
		}
		if got.Title != "still updates" {
			t.Fatalf("permutation %d: Title = %q, want %q", i, got.Title, "still updates")
		}
		if !reflect.DeepEqual(got.Tags, []string{"z"}) {
			t.Fatalf("permutation %d: Tags = %v, want [z]", i, got.Tags)
		}
	}
}

func TestFoldMultiMergeDeterminism(t *testing.T) {
	dag := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "t", Type: model.TypeTask, Branch: "main"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetTitle{Title: "from-b"}),
		mk("ccc", []string{"aaa"}, "carol", 200, 2, model.AddLabel{Label: "c"}),
		mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
		mk("eee", []string{"ddd"}, "eve", 400, 4, model.Claim{Assignee: "eve"}),
		mk("fff", []string{"ddd"}, "frank", 350, 4, model.AddComment{Body: "hi"}),
		mk("ggg", []string{"eee", "fff"}, "gail", 500, 5),
		mk("hhh", []string{"ggg"}, "hank", 600, 6, model.SetStatus{Status: model.StatusDone}),
	}
	want := model.Task{
		ID:               "aaa",
		Branch:           "main",
		Title:            "from-b",
		Type:             model.TypeTask,
		Status:           model.StatusDone,
		Assignee:         "eve",
		HeartbeatAt:      400,
		HeartbeatLamport: 4,
		Labels:           []string{"c"},
		BlockedBy:        []model.EntityID{},
		Comments:         []model.Comment{{Author: "frank", TS: 350, Body: "hi"}},
		CreatedAt:        100,
		UpdatedAt:        600,
		StartedAt:        400,
		ClosedAt:         600,
		Commits:          []model.SHA{},
		Criteria:         []model.Criterion{},
		Head:             "hhh",
	}
	for i, input := range shuffles(dag, 50) {
		got, err := fold.Task(input)
		if err != nil {
			t.Fatalf("shuffle %d: Task() error = %v", i, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("shuffle %d: Task() = %+v, want %+v", i, got, want)
		}
	}
}

func TestFoldCreateInLaterCommit(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.CreateNote{Nonce: "n", Title: "late"}),
	}
	got, err := fold.Note(chain)
	if err != nil {
		t.Fatalf("Note() error = %v", err)
	}
	want := model.Note{
		ID:           "aaa",
		Title:        "late",
		Tags:         []string{},
		Anchors:      []model.Anchor{},
		Author:       "bob",
		CreatedAt:    100,
		UpdatedAt:    200,
		SupersededBy: []model.EntityID{},
		Head:         "bbb",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Note() = %+v, want %+v", got, want)
	}
}

func TestFoldNoteVerify(t *testing.T) {
	witness := []model.AnchorWitness{
		{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "x.go"}, OID: "0011"},
	}
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateNote{
			Nonce:   "n",
			Title:   "T",
			Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "x.go"}},
		}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.VerifyNote{
			Witness:        witness,
			VerifiedCommit: "headsha",
		}),
	}
	want := model.Note{
		ID:             "aaa",
		Title:          "T",
		Tags:           []string{},
		Anchors:        []model.Anchor{{Kind: model.AnchorPath, Value: "x.go"}},
		Author:         "alice",
		CreatedAt:      100,
		UpdatedAt:      200,
		VerifiedAt:     200,
		VerifiedBy:     "bob",
		VerifiedCommit: "headsha",
		Witness:        witness,
		SupersededBy:   []model.EntityID{},
		Head:           "bbb",
	}
	got, err := fold.Note(chain)
	if err != nil {
		t.Fatalf("Note() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Note() = %+v, want %+v", got, want)
	}
	// The verify is a second commit: the root sha (== EntityID) is untouched.
	if got.ID != "aaa" {
		t.Fatalf("EntityID = %q, want %q (verify must not change the root)", got.ID, "aaa")
	}
}

func TestFoldNoteStale(t *testing.T) {
	cases := []struct {
		name            string
		ops             []model.PackCommit
		wantStaleAt     int64
		wantStaleBy     model.Actor
		wantStaleReason string
		wantVerifiedAt  int64
		wantVerifiedBy  model.Actor
	}{
		{
			name: "mark_stale flags the note from the commit",
			ops: []model.PackCommit{
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.MarkStale{Reason: "x"}),
			},
			wantStaleAt:     200,
			wantStaleBy:     "bob",
			wantStaleReason: "x",
		},
		{
			name: "clear_stale resets the flag",
			ops: []model.PackCommit{
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.MarkStale{Reason: "x"}),
				mk("ccc", []string{"bbb"}, "carol", 300, 3, model.ClearStale{}),
			},
		},
		{
			name: "verify_note clears stale and sets verified",
			ops: []model.PackCommit{
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.MarkStale{Reason: "x"}),
				mk("ccc", []string{"bbb"}, "carol", 300, 3, model.VerifyNote{VerifiedCommit: "headsha"}),
			},
			wantVerifiedAt: 300,
			wantVerifiedBy: "carol",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chain := append([]model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T"}),
			}, tc.ops...)
			got, err := fold.Note(chain)
			if err != nil {
				t.Fatalf("Note() error = %v", err)
			}
			if got.StaleAt != tc.wantStaleAt {
				t.Fatalf("StaleAt = %d, want %d", got.StaleAt, tc.wantStaleAt)
			}
			if got.StaleBy != tc.wantStaleBy {
				t.Fatalf("StaleBy = %q, want %q", got.StaleBy, tc.wantStaleBy)
			}
			if got.StaleReason != tc.wantStaleReason {
				t.Fatalf("StaleReason = %q, want %q", got.StaleReason, tc.wantStaleReason)
			}
			if got.VerifiedAt != tc.wantVerifiedAt {
				t.Fatalf("VerifiedAt = %d, want %d", got.VerifiedAt, tc.wantVerifiedAt)
			}
			if got.VerifiedBy != tc.wantVerifiedBy {
				t.Fatalf("VerifiedBy = %q, want %q", got.VerifiedBy, tc.wantVerifiedBy)
			}
		})
	}
}

func TestFoldStaleAfterVerifyLWW(t *testing.T) {
	diamond := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "t"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.VerifyNote{VerifiedCommit: "b-head"}),
		mk("ccc", []string{"bbb"}, "carol", 300, 3, model.MarkStale{Reason: "drifted"}),
	}
	for i, input := range permutations(diamond) {
		got, err := fold.Note(input)
		if err != nil {
			t.Fatalf("permutation %d: Note() error = %v", i, err)
		}
		if got.StaleAt != 300 {
			t.Fatalf("permutation %d: StaleAt = %d, want %d", i, got.StaleAt, 300)
		}
		if got.StaleBy != "carol" {
			t.Fatalf("permutation %d: StaleBy = %q, want %q", i, got.StaleBy, "carol")
		}
		if got.StaleReason != "drifted" {
			t.Fatalf("permutation %d: StaleReason = %q, want %q", i, got.StaleReason, "drifted")
		}
		if got.VerifiedAt != 200 {
			t.Fatalf("permutation %d: VerifiedAt = %d, want %d", i, got.VerifiedAt, 200)
		}
	}
}

func TestFoldVerifyLWW(t *testing.T) {
	bWitness := []model.AnchorWitness{{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "x.go"}, OID: "bbbb"}}
	cWitness := []model.AnchorWitness{{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "x.go"}, OID: "cccc"}}
	cases := []struct {
		name           string
		bTime, cTime   int64
		wantOID        model.SHA
		wantCommit     model.SHA
		wantVerifiedAt int64
		wantBy         model.Actor
	}{
		{name: "later verify wins", bTime: 200, cTime: 250, wantOID: "cccc", wantCommit: "c-head", wantVerifiedAt: 250, wantBy: "carol"},
		{name: "earlier verify loses", bTime: 250, cTime: 200, wantOID: "bbbb", wantCommit: "b-head", wantVerifiedAt: 250, wantBy: "bob"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diamond := []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "t", Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "x.go"}}}),
				mk("bbb", []string{"aaa"}, "bob", tc.bTime, 2, model.VerifyNote{Witness: bWitness, VerifiedCommit: "b-head"}),
				mk("ccc", []string{"aaa"}, "carol", tc.cTime, 2, model.VerifyNote{Witness: cWitness, VerifiedCommit: "c-head"}),
				mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
			}
			for i, input := range permutations(diamond) {
				got, err := fold.Note(input)
				if err != nil {
					t.Fatalf("permutation %d: Note() error = %v", i, err)
				}
				if len(got.Witness) != 1 || got.Witness[0].OID != tc.wantOID {
					t.Fatalf("permutation %d: Witness = %+v, want OID %q", i, got.Witness, tc.wantOID)
				}
				if got.VerifiedCommit != tc.wantCommit {
					t.Fatalf("permutation %d: VerifiedCommit = %q, want %q", i, got.VerifiedCommit, tc.wantCommit)
				}
				if got.VerifiedAt != tc.wantVerifiedAt {
					t.Fatalf("permutation %d: VerifiedAt = %d, want %d", i, got.VerifiedAt, tc.wantVerifiedAt)
				}
				if got.VerifiedBy != tc.wantBy {
					t.Fatalf("permutation %d: VerifiedBy = %q, want %q", i, got.VerifiedBy, tc.wantBy)
				}
			}
		})
	}
}

func TestFoldSupersededConverges(t *testing.T) {
	cases := []struct {
		name         string
		bTime, cTime int64
		bOp, cOp     model.Op
		want         []model.EntityID
	}{
		{
			name:  "add after remove keeps edge",
			bTime: 200, cTime: 250,
			bOp: model.RemoveSupersededBy{ID: "newid"}, cOp: model.AddSupersededBy{ID: "newid"},
			want: []model.EntityID{"newid"},
		},
		{
			name:  "remove after add drops edge",
			bTime: 250, cTime: 200,
			bOp: model.RemoveSupersededBy{ID: "newid"}, cOp: model.AddSupersededBy{ID: "newid"},
			want: []model.EntityID{},
		},
		{
			name:  "concurrent adds dedupe and sort",
			bTime: 200, cTime: 250,
			bOp: model.AddSupersededBy{ID: "zzz"}, cOp: model.AddSupersededBy{ID: "aaa2"},
			want: []model.EntityID{"aaa2", "zzz"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diamond := []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "t"}),
				mk("bbb", []string{"aaa"}, "bob", tc.bTime, 2, tc.bOp),
				mk("ccc", []string{"aaa"}, "carol", tc.cTime, 2, tc.cOp),
				mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
			}
			for i, input := range permutations(diamond) {
				got, err := fold.Note(input)
				if err != nil {
					t.Fatalf("permutation %d: Note() error = %v", i, err)
				}
				if !reflect.DeepEqual(got.SupersededBy, tc.want) {
					t.Fatalf("permutation %d: SupersededBy = %v, want %v", i, got.SupersededBy, tc.want)
				}
			}
		})
	}
}

func TestFoldErrors(t *testing.T) {
	noteRoot := mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n"})
	taskRoot := mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Type: model.TypeTask, Branch: "main"})
	sprintRoot := mk("aaa", nil, "alice", 100, 1, model.CreateSprint{Nonce: "n"})
	projectRoot := mk("aaa", nil, "alice", 100, 1, model.CreateProject{Nonce: "n"})
	cases := []struct {
		name    string
		commits []model.PackCommit
		via     func([]model.PackCommit) error
		want    error
	}{
		{
			name:    "first op not a create",
			commits: []model.PackCommit{mk("aaa", nil, "alice", 100, 1, model.SetTitle{Title: "t"})},
			via:     foldErr,
			want:    fold.ErrNoCreate,
		},
		{
			name:    "no ops at all",
			commits: []model.PackCommit{mk("aaa", nil, "alice", 100, 1)},
			via:     foldErr,
			want:    fold.ErrNoCreate,
		},
		{
			name:    "first op not a create via Note",
			commits: []model.PackCommit{mk("aaa", nil, "alice", 100, 1, model.AddTag{Tag: "x"})},
			via:     noteErr,
			want:    fold.ErrNoCreate,
		},
		{
			name: "second create_note",
			commits: []model.PackCommit{
				noteRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.CreateNote{Nonce: "m"}),
			},
			via:  noteErr,
			want: fold.ErrDuplicateCreate,
		},
		{
			name: "create_task after create_note",
			commits: []model.PackCommit{
				noteRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.CreateTask{Nonce: "m", Type: model.TypeTask, Branch: "main"}),
			},
			via:  noteErr,
			want: fold.ErrDuplicateCreate,
		},
		{
			name: "task op on a note chain",
			commits: []model.PackCommit{
				noteRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetStatus{Status: model.StatusDone}),
			},
			via:  noteErr,
			want: fold.ErrKindMismatch,
		},
		{
			name: "note op on a task chain",
			commits: []model.PackCommit{
				taskRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.AddTag{Tag: "x"}),
			},
			via:  taskErr,
			want: fold.ErrKindMismatch,
		},
		{
			name: "verify_note on a task chain",
			commits: []model.PackCommit{
				taskRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.VerifyNote{VerifiedCommit: "deadbeef"}),
			},
			via:  taskErr,
			want: fold.ErrKindMismatch,
		},
		{
			name: "add_superseded_by on a task chain",
			commits: []model.PackCommit{
				taskRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.AddSupersededBy{ID: "feedface"}),
			},
			via:  taskErr,
			want: fold.ErrKindMismatch,
		},
		{
			name:    "task chain folded as note",
			commits: []model.PackCommit{taskRoot},
			via:     noteErr,
			want:    fold.ErrKindMismatch,
		},
		{
			name:    "note chain folded as task",
			commits: []model.PackCommit{noteRoot},
			via:     taskErr,
			want:    fold.ErrKindMismatch,
		},
		{
			name: "sprint status op on a task chain",
			commits: []model.PackCommit{
				taskRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetSprintStatus{Status: model.SprintActive}),
			},
			via:  taskErr,
			want: fold.ErrKindMismatch,
		},
		{
			name: "project status op on a task chain",
			commits: []model.PackCommit{
				taskRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetProjectStatus{Status: model.ProjectArchived}),
			},
			via:  taskErr,
			want: fold.ErrKindMismatch,
		},
		{
			name: "start date op on a task chain",
			commits: []model.PackCommit{
				taskRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetStartDate{Date: 1000}),
			},
			via:  taskErr,
			want: fold.ErrKindMismatch,
		},
		{
			name: "end date op on a task chain",
			commits: []model.PackCommit{
				taskRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetEndDate{Date: 2000}),
			},
			via:  taskErr,
			want: fold.ErrKindMismatch,
		},
		{
			name: "task status op on a sprint chain",
			commits: []model.PackCommit{
				sprintRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetStatus{Status: model.StatusDone}),
			},
			via:  sprintErr,
			want: fold.ErrKindMismatch,
		},
		{
			name: "set_sprint op on a sprint chain",
			commits: []model.PackCommit{
				sprintRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetSprint{Sprint: "feedface"}),
			},
			via:  sprintErr,
			want: fold.ErrKindMismatch,
		},
		{
			name:    "task chain folded as sprint",
			commits: []model.PackCommit{taskRoot},
			via:     sprintErr,
			want:    fold.ErrKindMismatch,
		},
		{
			name:    "note chain folded as sprint",
			commits: []model.PackCommit{noteRoot},
			via:     sprintErr,
			want:    fold.ErrKindMismatch,
		},
		{
			name: "task status op on a project chain",
			commits: []model.PackCommit{
				projectRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetStatus{Status: model.StatusDone}),
			},
			via:  projectErr,
			want: fold.ErrKindMismatch,
		},
		{
			name: "sprint status op on a project chain",
			commits: []model.PackCommit{
				projectRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetSprintStatus{Status: model.SprintActive}),
			},
			via:  projectErr,
			want: fold.ErrKindMismatch,
		},
		{
			name:    "sprint chain folded as project",
			commits: []model.PackCommit{sprintRoot},
			via:     projectErr,
			want:    fold.ErrKindMismatch,
		},
		{
			name: "second create_sprint",
			commits: []model.PackCommit{
				sprintRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.CreateSprint{Nonce: "m"}),
			},
			via:  sprintErr,
			want: fold.ErrDuplicateCreate,
		},
		{
			name: "second create_project",
			commits: []model.PackCommit{
				projectRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.CreateProject{Nonce: "m"}),
			},
			via:  projectErr,
			want: fold.ErrDuplicateCreate,
		},
		{
			name: "linearize error propagates through Fold",
			commits: []model.PackCommit{
				noteRoot,
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetTitle{Title: "b"}),
				mk("ccc", []string{"aaa"}, "carol", 300, 2, model.SetTitle{Title: "c"}),
			},
			via:  foldErr,
			want: fold.ErrMultipleHeads,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.via(tc.commits); !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestFoldHeartbeat(t *testing.T) {
	cases := []struct {
		name         string
		chain        []model.PackCommit
		wantAt       int64
		wantLamport  model.Lamport
		wantAssignee model.Actor
	}{
		{
			name: "claim stamps heartbeat from claim commit",
			chain: []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "t", Type: model.TypeTask, Branch: "main"}),
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Claim{Assignee: "bob"}),
			},
			wantAt: 200, wantLamport: 2, wantAssignee: "bob",
		},
		{
			name: "renew by assignee advances heartbeat",
			chain: []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "t", Type: model.TypeTask, Branch: "main"}),
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Claim{Assignee: "bob"}),
				mk("ccc", []string{"bbb"}, "bob", 300, 3, model.Renew{}),
			},
			wantAt: 300, wantLamport: 3, wantAssignee: "bob",
		},
		{
			name: "renew by non-assignee does not refresh",
			chain: []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "t", Type: model.TypeTask, Branch: "main"}),
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Claim{Assignee: "bob"}),
				mk("ccc", []string{"bbb"}, "carol", 300, 3, model.Renew{}),
			},
			wantAt: 200, wantLamport: 2, wantAssignee: "bob",
		},
		{
			name: "assignee comment refreshes heartbeat",
			chain: []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "t", Type: model.TypeTask, Branch: "main"}),
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Claim{Assignee: "bob"}),
				mk("ccc", []string{"bbb"}, "bob", 300, 3, model.AddComment{Body: "wip"}),
			},
			wantAt: 300, wantLamport: 3, wantAssignee: "bob",
		},
		{
			name: "non-assignee edit does not refresh",
			chain: []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "t", Type: model.TypeTask, Branch: "main"}),
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Claim{Assignee: "bob"}),
				mk("ccc", []string{"bbb"}, "carol", 300, 3, model.SetTitle{Title: "renamed"}),
			},
			wantAt: 200, wantLamport: 2, wantAssignee: "bob",
		},
		{
			name: "empty commit by assignee does not refresh",
			chain: []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "t", Type: model.TypeTask, Branch: "main"}),
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Claim{Assignee: "bob"}),
				mk("ccc", []string{"bbb"}, "bob", 300, 3),
			},
			wantAt: 200, wantLamport: 2, wantAssignee: "bob",
		},
		{
			name: "no heartbeat before any claim",
			chain: []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "t", Type: model.TypeTask, Branch: "main"}),
				mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetTitle{Title: "x"}),
			},
			wantAt: 0, wantLamport: 0, wantAssignee: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fold.Task(tc.chain)
			if err != nil {
				t.Fatalf("Task() error = %v", err)
			}
			if got.HeartbeatAt != tc.wantAt {
				t.Fatalf("HeartbeatAt = %d, want %d", got.HeartbeatAt, tc.wantAt)
			}
			if got.HeartbeatLamport != tc.wantLamport {
				t.Fatalf("HeartbeatLamport = %d, want %d", got.HeartbeatLamport, tc.wantLamport)
			}
			if got.Assignee != tc.wantAssignee {
				t.Fatalf("Assignee = %q, want %q", got.Assignee, tc.wantAssignee)
			}
		})
	}
}

func TestFoldReclaim(t *testing.T) {
	base := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "t", Type: model.TypeTask, Branch: "main"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Claim{Assignee: "bob"}),
	}
	cases := []struct {
		name         string
		tail         model.PackCommit
		wantAssignee model.Actor
		wantStatus   model.Status
		wantAt       int64
		wantLamport  model.Lamport
	}{
		{
			name:         "applies for matching holder at or below after_lamport",
			tail:         mk("ccc", []string{"bbb"}, "carol", 300, 3, model.Reclaim{Assignee: "carol", From: "bob", AfterLamport: 2}),
			wantAssignee: "carol", wantStatus: model.StatusInProgress, wantAt: 300, wantLamport: 3,
		},
		{
			name:         "noop on from mismatch",
			tail:         mk("ccc", []string{"bbb"}, "carol", 300, 3, model.Reclaim{Assignee: "carol", From: "dave", AfterLamport: 2}),
			wantAssignee: "bob", wantStatus: model.StatusInProgress, wantAt: 200, wantLamport: 2,
		},
		{
			name:         "noop when heartbeat advanced past after_lamport",
			tail:         mk("ccc", []string{"bbb"}, "carol", 300, 3, model.Reclaim{Assignee: "carol", From: "bob", AfterLamport: 1}),
			wantAssignee: "bob", wantStatus: model.StatusInProgress, wantAt: 200, wantLamport: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fold.Task(append(slices.Clone(base), tc.tail))
			if err != nil {
				t.Fatalf("Task() error = %v", err)
			}
			if got.Assignee != tc.wantAssignee {
				t.Fatalf("Assignee = %q, want %q", got.Assignee, tc.wantAssignee)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.HeartbeatAt != tc.wantAt {
				t.Fatalf("HeartbeatAt = %d, want %d", got.HeartbeatAt, tc.wantAt)
			}
			if got.HeartbeatLamport != tc.wantLamport {
				t.Fatalf("HeartbeatLamport = %d, want %d", got.HeartbeatLamport, tc.wantLamport)
			}
			// Reclaim never resets the original claim's StartedAt.
			if got.StartedAt != 200 {
				t.Fatalf("StartedAt = %d, want 200 (reclaim must not reset)", got.StartedAt)
			}
		})
	}
}

func TestFoldReclaimConvergence(t *testing.T) {
	// Holder renewed past the reclaim's observation: the renew linearizes before
	// the reclaim, so the reclaim is a no-op on every replica and bob keeps it.
	renewedPast := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "t", Type: model.TypeTask, Branch: "main"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Claim{Assignee: "bob"}),
		mk("ccc", []string{"bbb"}, "bob", 300, 3, model.Renew{}),
		mk("ddd", []string{"bbb"}, "carol", 300, 3, model.Reclaim{Assignee: "carol", From: "bob", AfterLamport: 2}),
		mk("eee", []string{"ccc", "ddd"}, "dave", 400, 4),
	}
	for i, input := range permutations(renewedPast) {
		got, err := fold.Task(input)
		if err != nil {
			t.Fatalf("renewedPast permutation %d: error = %v", i, err)
		}
		if got.Assignee != "bob" {
			t.Fatalf("renewedPast permutation %d: Assignee = %q, want bob (holder renewed past)", i, got.Assignee)
		}
	}

	// Stale holder: reclaim observed the live heartbeat lamport, so it steals on
	// every replica.
	stolen := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "t", Type: model.TypeTask, Branch: "main"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Claim{Assignee: "bob"}),
		mk("ccc", []string{"bbb"}, "carol", 300, 3, model.Reclaim{Assignee: "carol", From: "bob", AfterLamport: 2}),
	}
	for i, input := range permutations(stolen) {
		got, err := fold.Task(input)
		if err != nil {
			t.Fatalf("stolen permutation %d: error = %v", i, err)
		}
		if got.Assignee != "carol" || got.Status != model.StatusInProgress {
			t.Fatalf("stolen permutation %d: Assignee=%q Status=%q, want carol/in_progress", i, got.Assignee, got.Status)
		}
		if got.StartedAt != 200 {
			t.Fatalf("stolen permutation %d: StartedAt = %d, want 200", i, got.StartedAt)
		}
	}

	// Two concurrent stealers: first in linearization order wins (ccc < ddd on
	// the sha tiebreak), the second sees a From mismatch — deterministic across
	// every permutation.
	twoStealers := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "t", Type: model.TypeTask, Branch: "main"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Claim{Assignee: "bob"}),
		mk("ccc", []string{"bbb"}, "carol", 300, 3, model.Reclaim{Assignee: "carol", From: "bob", AfterLamport: 2}),
		mk("ddd", []string{"bbb"}, "dave", 300, 3, model.Reclaim{Assignee: "dave", From: "bob", AfterLamport: 2}),
		mk("eee", []string{"ccc", "ddd"}, "erin", 400, 4),
	}
	for i, input := range permutations(twoStealers) {
		got, err := fold.Task(input)
		if err != nil {
			t.Fatalf("twoStealers permutation %d: error = %v", i, err)
		}
		if got.Assignee != "carol" {
			t.Fatalf("twoStealers permutation %d: Assignee = %q, want carol (first-wins)", i, got.Assignee)
		}
	}
}

func TestFoldReclaimClearsClosedAt(t *testing.T) {
	// create → bob claims → bob marks done (stamps ClosedAt) → carol reclaims.
	// The reclaim re-opens the task to in_progress, which must clear the close
	// stamp while leaving the original claim's StartedAt intact. bob's heartbeat
	// lamport rides up to 3 on the done commit, so after_lamport=3 makes the
	// reclaim fire.
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "t", Type: model.TypeTask, Branch: "main"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Claim{Assignee: "bob"}),
		mk("ccc", []string{"bbb"}, "bob", 300, 3, model.SetStatus{Status: model.StatusDone}),
		mk("ddd", []string{"ccc"}, "carol", 400, 4, model.Reclaim{Assignee: "carol", From: "bob", AfterLamport: 3}),
	}
	got, err := fold.Task(chain)
	if err != nil {
		t.Fatalf("Task() error = %v", err)
	}
	if got.Status != model.StatusInProgress {
		t.Fatalf("Status = %q, want %q", got.Status, model.StatusInProgress)
	}
	if got.Assignee != "carol" {
		t.Fatalf("Assignee = %q, want carol", got.Assignee)
	}
	if got.ClosedAt != 0 {
		t.Fatalf("ClosedAt = %d, want 0 (reclaim re-opens and clears the close stamp)", got.ClosedAt)
	}
	if got.StartedAt != 200 {
		t.Fatalf("StartedAt = %d, want 200 (reclaim must not reset the original claim's start)", got.StartedAt)
	}
}

func TestFoldCommitLinkInterleavings(t *testing.T) {
	cases := []struct {
		name         string
		bTime, cTime int64
		bOp, cOp     model.Op
		want         []model.SHA
	}{
		{
			name:  "link after unlink keeps commit",
			bTime: 200, cTime: 250,
			bOp: model.UnlinkCommit{SHA: "sha1"}, cOp: model.LinkCommit{SHA: "sha1"},
			want: []model.SHA{"sha1"},
		},
		{
			name:  "unlink after link drops commit",
			bTime: 250, cTime: 200,
			bOp: model.UnlinkCommit{SHA: "sha1"}, cOp: model.LinkCommit{SHA: "sha1"},
			want: []model.SHA{},
		},
		{
			name:  "concurrent links dedupe and sort",
			bTime: 200, cTime: 250,
			bOp: model.LinkCommit{SHA: "zzz"}, cOp: model.LinkCommit{SHA: "aaa2"},
			want: []model.SHA{"aaa2", "zzz"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diamond := []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Type: model.TypeTask, Branch: "main"}),
				mk("bbb", []string{"aaa"}, "bob", tc.bTime, 2, tc.bOp),
				mk("ccc", []string{"aaa"}, "carol", tc.cTime, 2, tc.cOp),
				mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
			}
			for i, input := range permutations(diamond) {
				got, err := fold.Task(input)
				if err != nil {
					t.Fatalf("permutation %d: Task() error = %v", i, err)
				}
				if !reflect.DeepEqual(got.Commits, tc.want) {
					t.Fatalf("permutation %d: Commits = %v, want %v", i, got.Commits, tc.want)
				}
			}
		})
	}
}

func TestFoldSprintLifecycle(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateSprint{
			Nonce:       "n",
			Title:       "Sprint 1",
			Description: "first sprint",
			Project:     "proj0",
			Labels:      []string{"q3"},
		}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2,
			model.SetTitle{Title: "Sprint One"},
			model.SetDescription{Description: "the first sprint"},
			model.AddLabel{Label: "planning"},
		),
		mk("ccc", []string{"bbb"}, "bob", 300, 3,
			model.SetProject{Project: "proj1"},
			model.SetStartDate{Date: 1000},
			model.SetEndDate{Date: 2000},
		),
		mk("ddd", []string{"ccc"}, "carol", 400, 4,
			model.SetSprintStatus{Status: model.SprintActive},
			model.LinkCommit{SHA: "sha1"},
			model.AddComment{Body: "kickoff"},
		),
		mk("eee", []string{"ddd"}, "dave", 500, 5,
			model.AddLabel{Label: "alpha"},
			model.RemoveLabel{Label: "q3"},
			model.UnlinkCommit{SHA: "sha1"},
			model.LinkCommit{SHA: "sha2"},
			model.AddComment{Body: "midpoint"},
		),
	}
	want := model.Sprint{
		ID:          "aaa",
		Project:     "proj1",
		Title:       "Sprint One",
		Description: "the first sprint",
		Status:      model.SprintActive,
		StartDate:   1000,
		EndDate:     2000,
		Labels:      []string{"alpha", "planning"},
		Commits:     []model.SHA{"sha2"},
		Comments: []model.Comment{
			{Author: "carol", TS: 400, Body: "kickoff"},
			{Author: "dave", TS: 500, Body: "midpoint"},
		},
		Author:    "alice",
		CreatedAt: 100,
		UpdatedAt: 500,
		StartedAt: 400,
		Head:      "eee",
	}
	got, err := fold.Sprint(chain)
	if err != nil {
		t.Fatalf("Sprint() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Sprint() = %+v, want %+v", got, want)
	}
	snap, err := fold.Fold(chain)
	if err != nil {
		t.Fatalf("Fold() error = %v", err)
	}
	dispatched, ok := snap.(model.Sprint)
	if !ok {
		t.Fatalf("Fold() = %T, want model.Sprint", snap)
	}
	if !reflect.DeepEqual(dispatched, want) {
		t.Fatalf("Fold() = %+v, want %+v", dispatched, want)
	}
	if snap.EntityID() != "aaa" {
		t.Fatalf("EntityID() = %q, want %q", snap.EntityID(), "aaa")
	}
}

func TestFoldSprintStatusTimestamps(t *testing.T) {
	chain := sprintChain(
		model.SetSprintStatus{Status: model.SprintActive},    // t=200
		model.SetSprintStatus{Status: model.SprintCompleted}, // t=300
		model.SetSprintStatus{Status: model.SprintActive},    // t=400
		model.SetSprintStatus{Status: model.SprintCancelled}, // t=500
	)
	cases := []struct {
		name        string
		prefix      int
		wantStatus  model.SprintStatus
		wantStarted int64
		wantClosed  int64
	}{
		{name: "active stamps started", prefix: 2, wantStatus: model.SprintActive, wantStarted: 200},
		{name: "completed stamps closed", prefix: 3, wantStatus: model.SprintCompleted, wantStarted: 200, wantClosed: 300},
		{name: "reactivate resets started and clears closed", prefix: 4, wantStatus: model.SprintActive, wantStarted: 400},
		{name: "cancelled stamps closed keeps started", prefix: 5, wantStatus: model.SprintCancelled, wantStarted: 400, wantClosed: 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fold.Sprint(chain[:tc.prefix])
			if err != nil {
				t.Fatalf("Sprint() error = %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.StartedAt != tc.wantStarted {
				t.Fatalf("StartedAt = %d, want %d", got.StartedAt, tc.wantStarted)
			}
			if got.ClosedAt != tc.wantClosed {
				t.Fatalf("ClosedAt = %d, want %d", got.ClosedAt, tc.wantClosed)
			}
		})
	}
}

func TestFoldSprintDates(t *testing.T) {
	chain := sprintChain(
		model.SetStartDate{Date: 1000}, // t=200
		model.SetEndDate{Date: 2000},   // t=300
		model.SetStartDate{Date: 0},    // t=400 clears start
		model.SetEndDate{Date: 0},      // t=500 clears end
	)
	cases := []struct {
		name          string
		prefix        int
		wantStartDate int64
		wantEndDate   int64
	}{
		{name: "start set", prefix: 2, wantStartDate: 1000},
		{name: "end set", prefix: 3, wantStartDate: 1000, wantEndDate: 2000},
		{name: "start cleared", prefix: 4, wantStartDate: 0, wantEndDate: 2000},
		{name: "end cleared", prefix: 5, wantStartDate: 0, wantEndDate: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fold.Sprint(chain[:tc.prefix])
			if err != nil {
				t.Fatalf("Sprint() error = %v", err)
			}
			if got.StartDate != tc.wantStartDate {
				t.Fatalf("StartDate = %d, want %d", got.StartDate, tc.wantStartDate)
			}
			if got.EndDate != tc.wantEndDate {
				t.Fatalf("EndDate = %d, want %d", got.EndDate, tc.wantEndDate)
			}
		})
	}
}

func TestFoldProjectLifecycle(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateProject{
			Nonce:       "n",
			Title:       "Proj",
			Description: "the project",
			Labels:      []string{"core"},
		}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2,
			model.SetTitle{Title: "Project X"},
			model.SetDescription{Description: "project x"},
			model.AddLabel{Label: "active-work"},
			model.AddComment{Body: "started"},
		),
		mk("ccc", []string{"bbb"}, "carol", 300, 3,
			model.SetProjectStatus{Status: model.ProjectCompleted},
			model.RemoveLabel{Label: "core"},
			model.LinkCommit{SHA: "c1"},
			model.AddComment{Body: "shipped"},
		),
	}
	want := model.Project{
		ID:          "aaa",
		Title:       "Project X",
		Description: "project x",
		Status:      model.ProjectCompleted,
		Labels:      []string{"active-work"},
		Commits:     []model.SHA{"c1"},
		Comments: []model.Comment{
			{Author: "bob", TS: 200, Body: "started"},
			{Author: "carol", TS: 300, Body: "shipped"},
		},
		Author:    "alice",
		CreatedAt: 100,
		UpdatedAt: 300,
		ClosedAt:  300,
		Head:      "ccc",
	}
	got, err := fold.Project(chain)
	if err != nil {
		t.Fatalf("Project() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Project() = %+v, want %+v", got, want)
	}
	snap, err := fold.Fold(chain)
	if err != nil {
		t.Fatalf("Fold() error = %v", err)
	}
	dispatched, ok := snap.(model.Project)
	if !ok {
		t.Fatalf("Fold() = %T, want model.Project", snap)
	}
	if !reflect.DeepEqual(dispatched, want) {
		t.Fatalf("Fold() = %+v, want %+v", dispatched, want)
	}
	if snap.EntityID() != "aaa" {
		t.Fatalf("EntityID() = %q, want %q", snap.EntityID(), "aaa")
	}
}

func TestFoldProjectStatusTimestamps(t *testing.T) {
	chain := projectChain(
		model.SetProjectStatus{Status: model.ProjectCompleted}, // t=200
		model.SetProjectStatus{Status: model.ProjectActive},    // t=300
		model.SetProjectStatus{Status: model.ProjectArchived},  // t=400
		model.SetProjectStatus{Status: model.ProjectCancelled}, // t=500
	)
	cases := []struct {
		name       string
		prefix     int
		wantStatus model.ProjectStatus
		wantClosed int64
	}{
		{name: "completed stamps closed", prefix: 2, wantStatus: model.ProjectCompleted, wantClosed: 200},
		{name: "reactivate clears closed", prefix: 3, wantStatus: model.ProjectActive},
		{name: "archived stamps closed", prefix: 4, wantStatus: model.ProjectArchived, wantClosed: 400},
		{name: "cancelled stamps closed", prefix: 5, wantStatus: model.ProjectCancelled, wantClosed: 500},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fold.Project(chain[:tc.prefix])
			if err != nil {
				t.Fatalf("Project() error = %v", err)
			}
			if got.Status != tc.wantStatus {
				t.Fatalf("Status = %q, want %q", got.Status, tc.wantStatus)
			}
			if got.ClosedAt != tc.wantClosed {
				t.Fatalf("ClosedAt = %d, want %d", got.ClosedAt, tc.wantClosed)
			}
		})
	}
}

func TestFoldTaskCriteria(t *testing.T) {
	cases := []struct {
		name string
		ops  []model.Op
		want []model.Criterion
	}{
		{
			name: "insertion order preserved",
			ops: []model.Op{
				model.AddCriterion{ID: "c1", Text: "one"},
				model.AddCriterion{ID: "c2", Text: "two"},
				model.AddCriterion{ID: "c3", Text: "three"},
			},
			want: []model.Criterion{
				{ID: "c1", Text: "one", Status: model.CriterionPending},
				{ID: "c2", Text: "two", Status: model.CriterionPending},
				{ID: "c3", Text: "three", Status: model.CriterionPending},
			},
		},
		{
			name: "duplicate add id is a no-op",
			ops: []model.Op{
				model.AddCriterion{ID: "c1", Text: "one", Script: "keep"},
				model.AddCriterion{ID: "c1", Text: "shadow", Script: "drop"},
			},
			want: []model.Criterion{
				{ID: "c1", Text: "one", Script: "keep", Status: model.CriterionPending},
			},
		},
		{
			name: "set text status script by id",
			ops: []model.Op{
				model.AddCriterion{ID: "c1", Text: "one", Script: "old"},
				model.SetCriterionText{ID: "c1", Text: "uno"},
				model.SetCriterionStatus{ID: "c1", Status: model.CriterionMet},
				model.SetCriterionScript{ID: "c1", Script: "new"},
			},
			want: []model.Criterion{
				{ID: "c1", Text: "uno", Script: "new", Status: model.CriterionMet},
			},
		},
		{
			name: "remove middle preserves order",
			ops: []model.Op{
				model.AddCriterion{ID: "c1", Text: "one"},
				model.AddCriterion{ID: "c2", Text: "two"},
				model.AddCriterion{ID: "c3", Text: "three"},
				model.RemoveCriterion{ID: "c2"},
			},
			want: []model.Criterion{
				{ID: "c1", Text: "one", Status: model.CriterionPending},
				{ID: "c3", Text: "three", Status: model.CriterionPending},
			},
		},
		{
			name: "set on absent id is a no-op",
			ops: []model.Op{
				model.AddCriterion{ID: "c1", Text: "one"},
				model.SetCriterionText{ID: "ghost", Text: "x"},
				model.SetCriterionStatus{ID: "ghost", Status: model.CriterionFailed},
				model.SetCriterionScript{ID: "ghost", Script: "x"},
			},
			want: []model.Criterion{
				{ID: "c1", Text: "one", Status: model.CriterionPending},
			},
		},
		{
			name: "remove on absent id is a no-op",
			ops: []model.Op{
				model.AddCriterion{ID: "c1", Text: "one"},
				model.RemoveCriterion{ID: "ghost"},
			},
			want: []model.Criterion{
				{ID: "c1", Text: "one", Status: model.CriterionPending},
			},
		},
		{
			name: "re-add after remove appends at end",
			ops: []model.Op{
				model.AddCriterion{ID: "c1", Text: "one"},
				model.AddCriterion{ID: "c2", Text: "two"},
				model.RemoveCriterion{ID: "c1"},
				model.AddCriterion{ID: "c1", Text: "again"},
			},
			want: []model.Criterion{
				{ID: "c2", Text: "two", Status: model.CriterionPending},
				{ID: "c1", Text: "again", Status: model.CriterionPending},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := fold.Task(taskChain(tc.ops...))
			if err != nil {
				t.Fatalf("Task() error = %v", err)
			}
			if !reflect.DeepEqual(got.Criteria, tc.want) {
				t.Fatalf("Criteria = %+v, want %+v", got.Criteria, tc.want)
			}
		})
	}
}

func TestFoldTaskCriteriaEmpty(t *testing.T) {
	got, err := fold.Task(taskChain())
	if err != nil {
		t.Fatalf("Task() error = %v", err)
	}
	if got.Criteria == nil {
		t.Fatalf("Criteria = nil, want non-nil empty slice")
	}
	if len(got.Criteria) != 0 {
		t.Fatalf("Criteria = %+v, want empty", got.Criteria)
	}
}

func TestFoldTaskCriteriaConcurrent(t *testing.T) {
	// Two concurrent AddCriterion: bbb and ccc share lamport and time, so the
	// sha tiebreak (bbb < ccc) fixes the append order on every replica.
	diamond := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Type: model.TypeTask, Branch: "main"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.AddCriterion{ID: "cb", Text: "from-b"}),
		mk("ccc", []string{"aaa"}, "carol", 200, 2, model.AddCriterion{ID: "cc", Text: "from-c"}),
		mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
	}
	want := []model.Criterion{
		{ID: "cb", Text: "from-b", Status: model.CriterionPending},
		{ID: "cc", Text: "from-c", Status: model.CriterionPending},
	}
	for i, input := range permutations(diamond) {
		got, err := fold.Task(input)
		if err != nil {
			t.Fatalf("permutation %d: Task() error = %v", i, err)
		}
		if !reflect.DeepEqual(got.Criteria, want) {
			t.Fatalf("permutation %d: Criteria = %+v, want %+v", i, got.Criteria, want)
		}
	}
}

func TestFoldTaskMembershipLWW(t *testing.T) {
	cases := []struct {
		name         string
		bOp, cOp     model.Op
		bTime, cTime int64
		wantSprint   model.EntityID
		wantProject  model.EntityID
	}{
		{
			name: "later sprint set wins",
			bOp:  model.SetSprint{Sprint: "spr-b"}, cOp: model.SetSprint{Sprint: "spr-c"},
			bTime: 200, cTime: 250, wantSprint: "spr-c",
		},
		{
			name: "earlier sprint set loses",
			bOp:  model.SetSprint{Sprint: "spr-b"}, cOp: model.SetSprint{Sprint: "spr-c"},
			bTime: 250, cTime: 200, wantSprint: "spr-b",
		},
		{
			name: "later sprint clear wins",
			bOp:  model.SetSprint{Sprint: "spr-b"}, cOp: model.SetSprint{Sprint: ""},
			bTime: 200, cTime: 250, wantSprint: "",
		},
		{
			name: "later project set wins",
			bOp:  model.SetProject{Project: "prj-b"}, cOp: model.SetProject{Project: "prj-c"},
			bTime: 200, cTime: 250, wantProject: "prj-c",
		},
		{
			name: "earlier project set loses",
			bOp:  model.SetProject{Project: "prj-b"}, cOp: model.SetProject{Project: "prj-c"},
			bTime: 250, cTime: 200, wantProject: "prj-b",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			diamond := []model.PackCommit{
				mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Type: model.TypeTask, Branch: "main"}),
				mk("bbb", []string{"aaa"}, "bob", tc.bTime, 2, tc.bOp),
				mk("ccc", []string{"aaa"}, "carol", tc.cTime, 2, tc.cOp),
				mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
			}
			for i, input := range permutations(diamond) {
				got, err := fold.Task(input)
				if err != nil {
					t.Fatalf("permutation %d: Task() error = %v", i, err)
				}
				if got.Sprint != tc.wantSprint {
					t.Fatalf("permutation %d: Sprint = %q, want %q", i, got.Sprint, tc.wantSprint)
				}
				if got.Project != tc.wantProject {
					t.Fatalf("permutation %d: Project = %q, want %q", i, got.Project, tc.wantProject)
				}
			}
		})
	}
}

func foldErr(commits []model.PackCommit) error {
	_, err := fold.Fold(commits)
	return err
}

func noteErr(commits []model.PackCommit) error {
	_, err := fold.Note(commits)
	return err
}

func taskErr(commits []model.PackCommit) error {
	_, err := fold.Task(commits)
	return err
}

func sprintErr(commits []model.PackCommit) error {
	_, err := fold.Sprint(commits)
	return err
}

func projectErr(commits []model.PackCommit) error {
	_, err := fold.Project(commits)
	return err
}
