package fold_test

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

func requireWalkMatchesHistory(t *testing.T, chain []model.PackCommit) {
	t.Helper()
	steps, wantErr := fold.History(chain)
	n := 0
	gotErr := fold.Walk(chain, func(st fold.Step) error {
		if wantErr == nil {
			if n >= len(steps) {
				t.Fatalf("Walk step %d (%s) past History's %d steps", n, st.Commit.SHA, len(steps))
			}
			if st.Commit.SHA != steps[n].Commit.SHA {
				t.Fatalf("step %d sha = %s, want %s", n, st.Commit.SHA, steps[n].Commit.SHA)
			}
			if !reflect.DeepEqual(st.Snapshot, steps[n].Snapshot) {
				t.Fatalf("step %d (%s) snapshot:\n walk    = %+v\n history = %+v", n, st.Commit.SHA, st.Snapshot, steps[n].Snapshot)
			}
		}
		n++
		return nil
	})
	if wantErr != nil {
		if gotErr == nil || gotErr.Error() != wantErr.Error() {
			t.Fatalf("Walk error = %v, want History's %v", gotErr, wantErr)
		}
		return
	}
	if gotErr != nil {
		t.Fatalf("Walk error = %v, History succeeded", gotErr)
	}
	if n != len(steps) {
		t.Fatalf("Walk stepped %d commits, History %d", n, len(steps))
	}
}

func foldSnapshot(t *testing.T, commits ...model.PackCommit) model.Snapshot {
	t.Helper()
	s, err := fold.Fold(commits)
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	return s
}

func sentinelLedger(t *testing.T, label string, commits ...model.PackCommit) model.Ledger {
	t.Helper()
	l, err := fold.Ledger(commits)
	if err != nil {
		t.Fatalf("Ledger: %v", err)
	}
	l.Labels = []string{label}
	return l
}

func TestWalkMatchesHistory(t *testing.T) {
	noteC0 := mk("c0", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T0", Body: "B0", Tags: []string{"a"}})
	noteC1 := mk("c1", []string{"c0"}, "bob", 200, 2, model.SetTitle{Title: "T1"}, model.AddTag{Tag: "b"})
	noteC2 := mk("c2", []string{"c1"}, "carol", 300, 3, model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "x.go"}})
	noteK := cp("cK", "c2", "compactor", 350, 4, foldSnapshot(t, noteC0, noteC1, noteC2), 3, "c0", "c1", "c2")
	noteC3 := mk("c3", []string{"cK"}, "dave", 400, 5, model.SetBody{Body: "B3"})

	rootC1 := mk("c1", []string{"c0"}, "root", 150, 2, model.AddTag{Tag: "common"})
	a1 := mk("a1", []string{"c1"}, "alice", 200, 3, model.SetTitle{Title: "from-a"}, model.AddTag{Tag: "a"})
	b1 := mk("b1", []string{"c1"}, "bob", 210, 3, model.SetTitle{Title: "from-b"}, model.AddTag{Tag: "b"})
	ka := cp("ka", "a1", "alice", 250, 4, foldSnapshot(t, noteC0, rootC1, a1), 3, "c0", "c1", "a1")
	kb := cp("kb", "b1", "bob", 260, 4, foldSnapshot(t, noteC0, rootC1, b1), 3, "c0", "c1", "b1")

	rbC0 := mk("c0", nil, "alice", 100, 1,
		model.CreateRunbook{Nonce: "n", Title: "RB"},
		model.AddStep{ID: "s1", Text: "build", Command: "", Position: "a"},
	)
	rbC1 := mk("c1", []string{"c0"}, "bob", 200, 2,
		model.StartRun{ID: "r1", Task: "task0"},
		model.SetRunStepStatus{RunID: "r1", StepID: "s1", Status: model.StepDone, Note: "seed"},
	)
	rbK := cp("cK", "c1", "compactor", 250, 3, foldSnapshot(t, rbC0, rbC1), 2, "c0", "c1")
	rbC2 := mk("c2", []string{"cK"}, "carol", 300, 4,
		model.SetRunStepStatus{RunID: "r1", StepID: "s1", Status: model.StepFailed, Note: "post"},
	)

	ledgerSeed := model.Ledger{
		ID: "aaa", Title: "lg", Status: model.LedgerActive,
		Columns: []string{"state"},
		Rows: []model.LedgerRow{{
			Key: "k", Fields: map[string]string{"state": "failure"}, Position: "a", UpdatedAt: 100, UpdatedBy: "alice",
		}},
		Comments: []model.Comment{}, Author: "alice", CreatedAt: 100, UpdatedAt: 100,
	}
	ledgerRoot := mk("aaa", nil, "alice", 100, 1, model.CreateLedger{Nonce: "n", Title: "lg"})

	tests := []struct {
		name  string
		chain []model.PackCommit
	}{
		{"linear note", []model.PackCommit{
			mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T", Body: "B"}),
			mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetTitle{Title: "T2"}, model.AddTag{Tag: "x"}),
			mk("ccc", []string{"bbb"}, "carol", 300, 3, model.SetBody{Body: "B2"}),
		}},
		{"concurrent claims", []model.PackCommit{
			mk("aaa", nil, "alice", 100, 1, model.CreateTask{Nonce: "n", Title: "race", Type: model.TypeTask, Branch: "main"}),
			mk("bbb", []string{"aaa"}, "agent-b", 200, 2, model.Claim{Assignee: "agent-b"}),
			mk("ccc", []string{"aaa"}, "agent-c", 150, 2, model.Claim{Assignee: "agent-c"}),
			mk("ddd", []string{"bbb", "ccc"}, "dave", 300, 3),
		}},
		{"note seed-safe checkpoint", []model.PackCommit{noteC0, noteC1, noteC2, noteK, noteC3}},
		{"concurrent note checkpoints", []model.PackCommit{
			noteC0, rootC1, a1, ka, b1, kb, mk("mmm", []string{"ka", "kb"}, "alice", 300, 5),
		}},
		{"runbook results seeded", []model.PackCommit{rbC0, rbC1, rbK, rbC2}},
		{"ledger seed the ops do not rebuild", []model.PackCommit{
			ledgerRoot,
			mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Checkpoint{EntityID: "aaa", State: ledgerSeed, CoversLamport: 1, CoversShas: []model.SHA{"aaa"}}),
			mk("ccc", []string{"bbb"}, "carol", 300, 3, model.UpsertRow{Key: "k", Fields: map[string]string{"state": "success"}}),
		}},
		{"ledger checkpoint over a note", []model.PackCommit{
			ledgerRoot,
			cp("bbb", "aaa", "bob", 200, 2, foldSnapshot(t, noteC0), 1, "aaa"),
		}},
		{"duplicate create", []model.PackCommit{
			noteC0,
			mk("c1", []string{"c0"}, "bob", 200, 2, model.CreateNote{Nonce: "m", Title: "again"}),
		}},
		{"root without ops", []model.PackCommit{
			mk("c0", nil, "alice", 100, 1),
			mk("c1", []string{"c0"}, "bob", 200, 2, model.CreateNote{Nonce: "n", Title: "T"}),
		}},
		{"no create", []model.PackCommit{mk("aaa", nil, "alice", 100, 1, model.SetTitle{Title: "T"})}},
		{"unregistered kind", []model.PackCommit{mk("aaa", nil, "alice", 100, 1, alienCreate{})}},
		{"empty chain", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireWalkMatchesHistory(t, tt.chain)
		})
	}
}

func TestWalkRefoldsWhenSeedChanges(t *testing.T) {
	l0 := mk("l0", nil, "alice", 100, 1, model.CreateLedger{Nonce: "n", Title: "lg", Columns: []string{"state"}})
	l1 := mk("l1", []string{"l0"}, "bob", 200, 2, model.UpsertRow{Key: "a", Fields: map[string]string{"state": "one"}, Position: "a"})
	k1 := cp("k1", "l1", "compactor", 250, 3, sentinelLedger(t, "k1", l0, l1), 2, "l0", "l1")
	l2 := mk("l2", []string{"k1"}, "carol", 300, 4, model.UpsertRow{Key: "b", Fields: map[string]string{"state": "two"}, Position: "b"})
	k2 := cp("k2", "l2", "compactor", 350, 5, sentinelLedger(t, "k2", l0, l1, k1, l2), 4, "l0", "l1", "k1", "l2")
	l3 := mk("l3", []string{"k2"}, "dave", 400, 6, model.RemoveRow{Key: "a"})

	lK := cp("lK", "l1", "compactor", 250, 2, sentinelLedger(t, "lK", l0, l1), 2, "l0", "l1")
	x := mk("x", []string{"l0"}, "carol", 300, 2, model.UpsertRow{Key: "b", Fields: map[string]string{"state": "two"}, Position: "b"})
	m := mk("m", []string{"lK", "x"}, "dave", 400, 3, model.UpsertRow{Key: "a", Fields: map[string]string{"state": "three"}})

	tests := []struct {
		name       string
		chain      []model.PackCommit
		wantLabels [][]string
	}{
		{"better checkpoint", []model.PackCommit{l0, l1, k1, l2, k2, l3}, [][]string{{}, {}, {"k1"}, {"k1"}, {"k2"}, {"k2"}}},
		{"gate breaks after seeding", []model.PackCommit{l0, l1, lK, x, m}, [][]string{{}, {}, {"lK"}, {}, {}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requireWalkMatchesHistory(t, tt.chain)
			var got [][]string
			if err := fold.Walk(tt.chain, func(st fold.Step) error {
				got = append(got, slices.Clone(st.Snapshot.(model.Ledger).Labels))
				return nil
			}); err != nil {
				t.Fatalf("Walk: %v", err)
			}
			if !reflect.DeepEqual(got, tt.wantLabels) {
				t.Fatalf("labels per step = %v, want %v", got, tt.wantLabels)
			}
		})
	}
}

func TestWalkMatchesHistoryEveryKind(t *testing.T) {
	creates := []model.CreateOp{
		model.CreateNote{Nonce: "n", Title: "T", Body: "B"},
		model.CreateDoc{Nonce: "n", Title: "T", Body: "B", When: "W"},
		model.CreateLog{Nonce: "n", Title: "T"},
		model.CreateTask{Nonce: "n", Title: "T", Type: model.TypeTask, Branch: "main"},
		model.CreateSprint{Nonce: "n", Title: "T"},
		model.CreateProject{Nonce: "n", Title: "T"},
		model.CreateRunbook{Nonce: "n", Title: "T"},
		model.CreateInvestigation{Nonce: "n", Title: "T", Premise: "P"},
		model.CreatePlan{Nonce: "n", Title: "T", Body: "B", Status: model.PlanDraft},
		model.CreateAnswer{Nonce: "n", Title: "T", Body: "B"},
		model.CreateLedger{Nonce: "n", Title: "T"},
	}
	kinds := make([]model.Kind, len(creates))
	for i, c := range creates {
		kinds[i] = c.CreateKind()
	}
	if want := model.Kinds(); !slices.Equal(slices.Sorted(slices.Values(kinds)), slices.Sorted(slices.Values(want))) {
		t.Fatalf("creates cover %v, want every kind %v", kinds, want)
	}
	edits := [][]model.Op{
		{model.SetTitle{Title: "T1"}, model.AddTag{Tag: "x"}, model.AddLabel{Label: "x"}},
		{model.AddComment{Body: "c"}, model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "x.go"}}},
		{model.SetTitle{Title: "T2"}, model.RemoveTag{Tag: "x"}, model.RemoveLabel{Label: "x"}},
		{model.SetBody{Body: "B3"}, model.AddTag{Tag: "y"}},
	}
	for _, create := range creates {
		t.Run(string(create.CreateKind()), func(t *testing.T) {
			c0 := mk("c0", nil, "alice", 100, 1, create)
			c1 := mk("c1", []string{"c0"}, "bob", 200, 2, edits[0]...)
			state := foldSnapshot(t, c0, c1)
			safe := cp("cK", "c1", "compactor", 250, 3, state, 2, "c0", "c1")
			late := cp("cK", "c1", "compactor", 250, 2, state, 2, "c0", "c1")
			early := mk("x", []string{"c0"}, "carol", 220, 2, edits[2]...)
			after := mk("x", []string{"c0"}, "carol", 300, 2, edits[2]...)
			chains := map[string][]model.PackCommit{
				"seed-safe":  {c0, c1, safe, mk("c2", []string{"cK"}, "carol", 300, 4, edits[1]...), mk("c3", []string{"c2"}, "dave", 400, 5, edits[3]...)},
				"unsafe":     {c0, c1, safe, early, mk("m", []string{"cK", "x"}, "dave", 400, 4, edits[3]...)},
				"gate-break": {c0, c1, late, after, mk("m", []string{"cK", "x"}, "dave", 400, 3, edits[3]...)},
			}
			for name, chain := range chains {
				t.Run(name, func(t *testing.T) {
					requireWalkMatchesHistory(t, chain)
				})
			}
		})
	}
}

func TestWalkMatchesHistoryRandomLedgers(t *testing.T) {
	r := rand.New(rand.NewPCG(11, 13))
	for i := range 150 {
		chain := randomLedgerChain(t, r, 2+r.IntN(60))
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			requireWalkMatchesHistory(t, chain)
		})
	}
}

func randomLedgerChain(t *testing.T, r *rand.Rand, n int) []model.PackCommit {
	t.Helper()
	keys := []string{"a", "b", "c", "d", "e", "f"}
	chain := []model.PackCommit{mk("c000", nil, "alice", 100, 1, model.CreateLedger{Nonce: "n", Title: "lg", Columns: []string{"state"}})}
	for k := 1; k < n; k++ {
		sha := fmt.Sprintf("c%03d", k)
		at, lamport := int64(100+100*k), uint64(k+1)
		parent := string(chain[k-1].SHA)
		if r.IntN(6) == 0 {
			state, err := fold.Ledger(chain)
			if err != nil {
				t.Fatalf("Ledger: %v", err)
			}
			if r.IntN(3) == 0 {
				state.Labels = append(slices.Clone(state.Labels), "seed-"+sha)
			}
			covers := make([]string, len(chain))
			for i, c := range chain {
				covers[i] = string(c.SHA)
			}
			if len(covers) > 1 && r.IntN(3) == 0 {
				drop := 1 + r.IntN(len(covers)-1)
				covers = slices.Delete(covers, drop, drop+1)
			}
			chain = append(chain, cp(sha, parent, "compactor", at, lamport, state, uint64(chain[k-1].Pack.Lamport), covers...))
			continue
		}
		ops := make([]model.Op, 0, 6)
		for range 1 + r.IntN(3) {
			key := keys[r.IntN(len(keys))]
			switch r.IntN(7) {
			case 0:
				ops = append(ops, model.RemoveRow{Key: key})
			case 1:
				ops = append(ops, model.UpsertRow{Key: key, Fields: map[string]string{"state": sha}, Replace: true})
			case 2:
				ops = append(ops, model.SetColumns{Columns: []string{"state", sha}})
			case 3:
				ops = append(ops, model.AddComment{Body: sha})
			case 4:
				ops = append(ops, model.AddLabel{Label: key}, model.RemoveLabel{Label: keys[r.IntN(len(keys))]})
			case 5:
				ops = append(ops, model.UpsertRow{Key: key, Position: keys[r.IntN(len(keys))]})
			default:
				ops = append(ops, model.UpsertRow{Key: key, Fields: map[string]string{sha: "v", "state": key}, Position: keys[r.IntN(len(keys))]})
			}
		}
		chain = append(chain, mk(sha, []string{parent}, "actor", at, lamport, ops...))
	}
	return chain
}

func TestWalkStopsAtFnError(t *testing.T) {
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateNote{Nonce: "n", Title: "T"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.SetTitle{Title: "T2"}),
		mk("ccc", []string{"bbb"}, "carol", 300, 3, model.SetBody{Body: "B2"}),
	}
	stop := errors.New("stop")
	var seen []model.SHA
	err := fold.Walk(chain, func(st fold.Step) error {
		seen = append(seen, st.Commit.SHA)
		if len(seen) == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("Walk error = %v, want %v", err, stop)
	}
	if want := []model.SHA{"aaa", "bbb"}; !slices.Equal(seen, want) {
		t.Fatalf("stepped %v, want %v", seen, want)
	}
}

func TestWalkLeavesCheckpointStateUnaliased(t *testing.T) {
	seed := model.Ledger{
		ID: "aaa", Title: "lg", Status: model.LedgerActive,
		Columns: []string{"state"},
		Rows: []model.LedgerRow{{
			Key: "k", Fields: map[string]string{"state": "failure"}, Position: "a", UpdatedAt: 100, UpdatedBy: "alice",
		}},
		Comments: []model.Comment{}, Author: "alice", CreatedAt: 100, UpdatedAt: 100,
	}
	chain := []model.PackCommit{
		mk("aaa", nil, "alice", 100, 1, model.CreateLedger{Nonce: "n", Title: "lg"}),
		mk("bbb", []string{"aaa"}, "bob", 200, 2, model.Checkpoint{EntityID: "aaa", State: seed, CoversLamport: 1, CoversShas: []model.SHA{"aaa"}}),
		mk("ccc", []string{"bbb"}, "carol", 300, 3, model.UpsertRow{Key: "k", Fields: map[string]string{"state": "success"}}),
	}
	var last string
	if err := fold.Walk(chain, func(st fold.Step) error {
		if st.Commit.SHA == "ccc" {
			last = st.Snapshot.(model.Ledger).Rows[0].Fields["state"]
		}
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if last != "success" {
		t.Fatalf("last step state = %q, want success", last)
	}
	if got := seed.Rows[0].Fields["state"]; got != "failure" {
		t.Fatalf("checkpoint state mutated by Walk: state = %q, want failure", got)
	}
}
