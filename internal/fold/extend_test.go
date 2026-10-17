package fold_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

const seedSentinel = "zz-seed"

var extendAuthors = []string{"alice", "bob", "carol"}

var allKinds = []model.Kind{
	model.KindNote, model.KindDoc, model.KindLog, model.KindTask, model.KindSprint, model.KindProject,
	model.KindRunbook, model.KindInvestigation, model.KindPlan, model.KindAnswer, model.KindLedger,
}

type extendCase struct {
	kind   model.Kind
	create model.Op
	steps  [][]model.Op
}

func extendCases() []extendCase {
	path := func(v string) model.Anchor { return model.Anchor{Kind: model.AnchorPath, Value: v} }
	dir := func(v string) model.Anchor { return model.Anchor{Kind: model.AnchorDir, Value: v} }
	branch := func(v string) model.Anchor { return model.Anchor{Kind: model.AnchorBranch, Value: v} }
	commit := func(v string) model.Anchor { return model.Anchor{Kind: model.AnchorCommit, Value: v} }
	return []extendCase{
		{
			kind:   model.KindNote,
			create: model.CreateNote{Nonce: "n", Title: "T0", Body: "B0", Tags: []string{"a"}, Anchors: []model.Anchor{path("x.go")}},
			steps: [][]model.Op{
				{model.SetTitle{Title: "T1"}, model.AddTag{Tag: "b"}},
				{model.AddAnchor{Anchor: commit("deadbeef")}},
				{model.VerifyNote{Witness: []model.AnchorWitness{{Anchor: path("x.go"), OID: "oid1"}}, VerifiedCommit: "c0ffee"}},
				{model.AddAttachment{Name: "trace.txt", OID: "oid-a", Size: 12}},
				{model.MarkStale{Reason: "moved"}},
				{model.AddSupersededBy{ID: "next1"}},
				{model.ClearStale{}},
				{model.SetBody{Body: "B1"}, model.RemoveTag{Tag: "a"}},
				{model.RemoveAttachment{Name: "trace.txt"}},
				{model.RemoveSupersededBy{ID: "next1"}, model.RemoveAnchor{Anchor: commit("deadbeef")}},
				{model.DeleteNote{}},
			},
		},
		{
			kind:   model.KindDoc,
			create: model.CreateDoc{Nonce: "n", Title: "D0", Body: "B0", When: "editing fold", Tags: []string{"guide"}, Anchors: []model.Anchor{dir("internal/fold")}},
			steps: [][]model.Op{
				{model.SetWhen{When: "editing fold or store"}},
				{model.AddTag{Tag: "fold"}, model.SetBody{Body: "B1"}},
				{model.VerifyNote{Witness: []model.AnchorWitness{{Anchor: dir("internal/fold"), OID: "tree1"}}, VerifiedCommit: "c0ffee"}},
				{model.AddAttachment{Name: "diagram.svg", OID: "oid-d", Size: 300}},
				{model.MarkStale{Reason: "engine rewrite"}},
				{model.AddSupersededBy{ID: "doc2"}, model.AddAnchor{Anchor: path("internal/fold/engine.go")}},
				{model.ClearStale{}, model.SetTitle{Title: "D1"}},
				{model.RemoveAttachment{Name: "diagram.svg"}, model.RemoveTag{Tag: "guide"}},
				{model.RemoveSupersededBy{ID: "doc2"}, model.RemoveAnchor{Anchor: dir("internal/fold")}},
				{model.DeleteNote{}},
			},
		},
		{
			kind:   model.KindLog,
			create: model.CreateLog{Nonce: "n", Title: "L0", Tags: []string{"incident"}, Anchors: []model.Anchor{dir("internal")}},
			steps: [][]model.Op{
				{model.AppendEntry{Text: "paged"}},
				{model.AppendEntry{Text: "rolled back", Model: "opus"}},
				{model.AddTag{Tag: "sev2"}, model.AppendEntry{Text: "mitigated"}},
				{model.AddAttachment{Name: "graph.png", OID: "oid-g", Size: 2048}},
				{model.AddAnchor{Anchor: branch("main")}},
				{model.SetTitle{Title: "L1"}},
				{model.RemoveTag{Tag: "incident"}, model.RemoveAnchor{Anchor: dir("internal")}},
				{model.RemoveAttachment{Name: "graph.png"}},
				{model.AppendEntry{Text: "closed"}},
				{model.DeleteNote{}},
			},
		},
		{
			kind: model.KindTask,
			create: model.CreateTask{
				Nonce: "n", Title: "t", Description: "d", Type: model.TypeTask, Priority: 2, Branch: "main",
				Labels: []string{"backend"}, Anchors: []model.Anchor{path("x.go")},
			},
			steps: [][]model.Op{
				{model.AddCriterion{ID: "k1", Text: "tests pass", Script: "go test ./..."}, model.AddCriterion{ID: "k2", Text: "docs"}},
				{model.Claim{Assignee: "bob"}},
				{model.AddComment{Body: "starting"}},
				{model.Renew{}},
				{model.SetCriterionStatus{ID: "k1", Status: model.CriterionMet, Note: "ci green"}},
				{model.SetCriterionText{ID: "k2", Text: "docs updated"}, model.SetCriterionScript{ID: "k2", Script: "make docs"}},
				{model.RemoveCriterion{ID: "k1"}},
				{model.AddDep{ID: "dep1"}, model.AddDep{ID: "dep2"}},
				{model.RemoveDep{ID: "dep1"}, model.LinkCommit{SHA: "abc123"}},
				{model.Reclaim{Assignee: "carol", From: "bob", AfterLamport: 100}},
				{model.SetStatus{Status: model.StatusDone}},
				{model.SetStatus{Status: model.StatusInProgress}},
				{model.SetTitle{Title: "t2"}, model.SetDescription{Description: "d2"}, model.SetType{Type: model.TypeBug}, model.SetPriority{Priority: 1}},
				{model.SetAssignee{Assignee: "alice"}},
				{model.SetParent{Parent: "parent1"}, model.SetBranch{Branch: "feature"}},
				{model.SetSprint{Sprint: "s1"}, model.SetProject{Project: "p1"}, model.SetPlan{Plan: "pl1"}},
				{model.AddLabel{Label: "urgent"}, model.RemoveLabel{Label: "backend"}, model.AddAnchor{Anchor: dir("internal")}, model.RemoveAnchor{Anchor: path("x.go")}},
				{model.UnlinkCommit{SHA: "abc123"}},
				{model.Claim{Assignee: "dave"}},
				{model.SetStatus{Status: model.StatusCancelled}},
				{model.DeleteNote{}},
			},
		},
		{
			kind:   model.KindSprint,
			create: model.CreateSprint{Nonce: "n", Title: "S0", Description: "d0", Project: "p0", Labels: []string{"q3"}},
			steps: [][]model.Op{
				{model.SetSprintStatus{Status: model.SprintActive}},
				{model.SetStartDate{Date: 1_700_000_000}, model.SetEndDate{Date: 1_700_600_000}},
				{model.AddComment{Body: "kickoff"}},
				{model.LinkCommit{SHA: "abc123"}, model.LinkCommit{SHA: "def456"}},
				{model.AddLabel{Label: "infra"}, model.RemoveLabel{Label: "q3"}},
				{model.SetProject{Project: "p1"}},
				{model.SetTitle{Title: "S1"}, model.SetDescription{Description: "d1"}},
				{model.UnlinkCommit{SHA: "abc123"}},
				{model.SetSprintStatus{Status: model.SprintCompleted}},
				{model.SetSprintStatus{Status: model.SprintPlanned}},
				{model.AddComment{Body: "replanned"}},
				{model.SetSprintStatus{Status: model.SprintCancelled}},
				{model.DeleteNote{}},
			},
		},
		{
			kind:   model.KindProject,
			create: model.CreateProject{Nonce: "n", Title: "P0", Description: "d0", Labels: []string{"roadmap"}},
			steps: [][]model.Op{
				{model.AddComment{Body: "scoped"}},
				{model.LinkCommit{SHA: "abc123"}},
				{model.SetProjectStatus{Status: model.ProjectCompleted}},
				{model.SetProjectStatus{Status: model.ProjectActive}},
				{model.AddLabel{Label: "h2"}, model.RemoveLabel{Label: "roadmap"}},
				{model.SetTitle{Title: "P1"}, model.SetDescription{Description: "d1"}},
				{model.UnlinkCommit{SHA: "abc123"}, model.LinkCommit{SHA: "def456"}},
				{model.SetProjectStatus{Status: model.ProjectArchived}},
				{model.AddComment{Body: "archived"}},
				{model.SetProjectStatus{Status: model.ProjectCancelled}},
				{model.DeleteNote{}},
			},
		},
		{
			kind:   model.KindRunbook,
			create: model.CreateRunbook{Nonce: "n", Title: "R0", Description: "d0", Labels: []string{"deploy"}, Anchors: []model.Anchor{path("deploy.sh")}},
			steps: [][]model.Op{
				{model.AddStep{ID: "s1", Text: "build", Command: "make", Position: "b"}},
				{model.AddStep{ID: "s2", Text: "test", Position: "c"}, model.AddStep{ID: "s3", Text: "lint", Command: "golangci-lint run", Position: "a"}},
				{model.SetStepText{ID: "s2", Text: "unit tests"}, model.SetStepCommand{ID: "s2", Command: "go test ./..."}},
				{model.SetStepPosition{ID: "s1", Position: "d"}},
				{model.StartRun{ID: "r1", Task: "t1"}},
				{model.SetRunStepStatus{RunID: "r1", StepID: "s1", Status: model.StepDone, Note: "ok"}},
				{model.SetRunStepStatus{RunID: "r1", StepID: "s2", Status: model.StepSkipped}},
				{model.SetRunStepStatus{RunID: "r1", StepID: "s1", Status: model.StepFailed, Note: "flaky"}},
				{model.FinishRun{ID: "r1", Status: model.RunFailed}},
				{model.AddComment{Body: "retrying"}, model.StartRun{ID: "r2"}},
				{model.SetRunStepStatus{RunID: "r2", StepID: "s3", Status: model.StepDone}},
				{model.FinishRun{ID: "r2", Status: model.RunSucceeded}},
				{model.SetRunbookStatus{Status: model.RunbookArchived}},
				{model.SetRunbookStatus{Status: model.RunbookActive}},
				{model.RemoveStep{ID: "s3"}},
				{model.AddLabel{Label: "prod"}, model.RemoveLabel{Label: "deploy"}, model.AddAnchor{Anchor: branch("main")}, model.RemoveAnchor{Anchor: path("deploy.sh")}},
				{model.SetTitle{Title: "R1"}, model.SetDescription{Description: "d1"}},
				{model.DeleteNote{}},
			},
		},
		{
			kind: model.KindInvestigation,
			create: model.CreateInvestigation{
				Nonce: "n", Title: "I0", Premise: "sync is slow", Tags: []string{"perf"}, Anchors: []model.Anchor{path("internal/store/write.go")},
			},
			steps: [][]model.Op{
				{model.AppendEntry{Text: "profiled append", Model: "opus"}},
				{model.AddFinding{ID: "f1", Text: "ReadChain per append"}, model.AddFinding{ID: "f2", Text: "cache miss"}},
				{model.SetFindingText{ID: "f1", Text: "ReadChain decodes every pack"}},
				{model.SetFindingStatus{ID: "f2", Status: model.FindingCleared, Note: "cache hits"}},
				{model.SetFindingStatus{ID: "f1", Status: model.FindingConfirmed, Note: "pprof"}},
				{model.RemoveFinding{ID: "f2"}},
				{model.SetRootCause{Text: "quadratic chain reads"}, model.SetInvestigationStatus{Status: model.InvestigationRootCaused}},
				{model.AddFixCommit{SHA: "fix1"}, model.AddFixCommit{SHA: "fix2"}},
				{model.RemoveFixCommit{SHA: "fix2"}, model.SetInvestigationStatus{Status: model.InvestigationFixed}},
				{model.AddFollowUp{ID: "task9"}, model.AddFollowUp{ID: "task8"}},
				{model.RemoveFollowUp{ID: "task8"}, model.LinkCommit{SHA: "abc123"}},
				{model.AddTag{Tag: "sync"}, model.RemoveTag{Tag: "perf"}, model.AddAnchor{Anchor: branch("main")}},
				{model.AddAttachment{Name: "cpu.pprof", OID: "oid-p", Size: 4096}, model.AddSupersededBy{ID: "inv2"}},
				{model.SetInvestigationStatus{Status: model.InvestigationConfirmed}},
				{model.SetInvestigationStatus{Status: model.InvestigationOpen}, model.UnlinkCommit{SHA: "abc123"}},
				{model.RemoveAttachment{Name: "cpu.pprof"}, model.RemoveSupersededBy{ID: "inv2"}, model.RemoveAnchor{Anchor: path("internal/store/write.go")}},
				{model.SetTitle{Title: "I1"}, model.SetBody{Body: "fixed by fold.Extend"}},
				{model.DeleteNote{}},
			},
		},
		{
			kind: model.KindPlan,
			create: model.CreatePlan{
				Nonce: "n", Title: "PL0", Body: "steps", Status: model.PlanDraft, Labels: []string{"q4"}, Anchors: []model.Anchor{dir("internal/fold")},
			},
			steps: [][]model.Op{
				{model.SetPlanStatus{Status: model.PlanApproved}},
				{model.SetPlanStatus{Status: model.PlanExecuting}, model.AddComment{Body: "started"}},
				{model.SetPlanOutcome{Outcome: "shipped v1"}},
				{model.SetPlanStatus{Status: model.PlanDone}},
				{model.SetPlanStatus{Status: model.PlanExecuting}},
				{model.AddSupersededBy{ID: "plan2"}},
				{model.AddLabel{Label: "perf"}, model.RemoveLabel{Label: "q4"}},
				{model.AddAnchor{Anchor: branch("main")}, model.RemoveAnchor{Anchor: dir("internal/fold")}},
				{model.RemoveSupersededBy{ID: "plan2"}, model.SetTitle{Title: "PL1"}, model.SetBody{Body: "revised"}},
				{model.SetPlanStatus{Status: model.PlanAbandoned}},
				{model.AddComment{Body: "dropped"}},
				{model.DeleteNote{}},
			},
		},
		{
			kind: model.KindAnswer,
			create: model.CreateAnswer{
				Nonce: "n", Title: "Which cache dir?", Body: "folds-v1", Tags: []string{"cache"}, Anchors: []model.Anchor{path("internal/store/cache.go")},
			},
			steps: [][]model.Op{
				{model.VerifyNote{Witness: []model.AnchorWitness{{Anchor: path("internal/store/cache.go"), OID: "oid1"}}, VerifiedCommit: "c0ffee"}},
				{model.AddTag{Tag: "scope:durable"}},
				{model.MarkStale{Reason: "cache moved"}},
				{model.SetBody{Body: "folds-v2"}, model.VerifyNote{Witness: []model.AnchorWitness{{Anchor: path("internal/store/cache.go"), OID: "oid2"}}, VerifiedCommit: "beef"}},
				{model.AddAttachment{Name: "a.txt", OID: "oid-a", Size: 1}, model.AddAttachment{Name: "b.txt", OID: "oid-b", Size: 2}},
				{model.RemoveAttachment{Name: "a.txt"}},
				{model.AddSupersededBy{ID: "ans2"}, model.AddSupersededBy{ID: "ans3"}},
				{model.RemoveSupersededBy{ID: "ans3"}},
				{model.MarkStale{Reason: "superseded"}},
				{model.ClearStale{}},
				{model.SetTitle{Title: "Which fold cache dir?"}},
				{model.AddAnchor{Anchor: dir("internal/store")}, model.RemoveAnchor{Anchor: path("internal/store/cache.go")}},
				{model.RemoveTag{Tag: "cache"}},
				{model.VerifyNote{VerifiedCommit: "f00d"}},
				{model.DeleteNote{}},
			},
		},
		{
			kind: model.KindLedger,
			create: model.CreateLedger{
				Nonce: "n", Title: "lg", Description: "d0", Columns: []string{"state"}, Labels: []string{"ops"}, Anchors: []model.Anchor{path("a.go")},
			},
			steps: [][]model.Op{
				{model.UpsertRow{Key: "b", Fields: map[string]string{"state": "open", "owner": "bob"}, Position: "b"}},
				{model.UpsertRow{Key: "a", Fields: map[string]string{"state": "open"}}},
				{model.UpsertRow{Key: "b", Fields: map[string]string{"state": "review"}}},
				{model.UpsertRow{Key: "a", Position: "c"}},
				{model.UpsertRow{Key: "b", Fields: map[string]string{"result": "pass"}, Replace: true}},
				{model.UpsertRow{Key: "c"}},
				{model.UpsertRow{Key: "c", Fields: map[string]string{"state": "new"}}},
				{model.SetColumns{Columns: []string{"state", "owner", "result"}}},
				{model.AddComment{Body: "synced"}},
				{model.AddLabel{Label: "weekly"}, model.RemoveLabel{Label: "ops"}},
				{model.AddAnchor{Anchor: branch("main")}, model.RemoveAnchor{Anchor: path("a.go")}},
				{model.SetLedgerStatus{Status: model.LedgerArchived}},
				{model.UpsertRow{Key: "d", Fields: map[string]string{"state": "late"}, Position: "a"}, model.RemoveRow{Key: "a"}},
				{model.SetLedgerStatus{Status: model.LedgerActive}},
				{model.SetTitle{Title: "lg2"}, model.SetDescription{Description: "d1"}},
				{model.RemoveRow{Key: "zz"}},
				{model.RemoveAnchor{Anchor: branch("main")}},
				{model.UpsertRow{Key: "a", Fields: map[string]string{"state": "back"}, Position: "b"}},
				{model.UpsertRow{Key: "d", Replace: true, Position: "e"}},
				{model.AddComment{Body: "second"}},
				{model.DeleteNote{}},
			},
		},
	}
}

func appendSteps(chain []model.PackCommit, steps [][]model.Op) []model.PackCommit {
	for _, ops := range steps {
		k := len(chain)
		chain = append(chain, mk(fmt.Sprintf("c%02d", k), []string{string(chain[k-1].SHA)}, extendAuthors[k%len(extendAuthors)], int64(100+100*k), uint64(k+1), ops...))
	}
	return chain
}

func extendChain(create model.Op, steps [][]model.Op) []model.PackCommit {
	return appendSteps([]model.PackCommit{mk("c00", nil, "alice", 100, 1, create)}, steps)
}

func compactedChain(t *testing.T, create model.Op, steps [][]model.Op, split int) []model.PackCommit {
	t.Helper()
	prefix := extendChain(create, steps[:split])
	state, err := fold.Strict(prefix)
	if err != nil {
		t.Fatalf("Strict(prefix): %v", err)
	}
	covers := make([]string, len(prefix))
	for i, c := range prefix {
		covers[i] = string(c.SHA)
	}
	head := prefix[len(prefix)-1]
	k := len(prefix)
	checkpoint := codecCheckpoint(t, fmt.Sprintf("c%02d", k), string(head.SHA), "compactor", int64(100+100*k), uint64(k+1),
		withSeedSentinel(t, state), uint64(head.Pack.Lamport), covers...)
	return appendSteps(append(prefix, checkpoint), steps[split:])
}

func withSeedSentinel(t *testing.T, snap model.Snapshot) model.Snapshot {
	t.Helper()
	switch s := snap.(type) {
	case model.Note:
		s.Tags = append(slices.Clone(s.Tags), seedSentinel)
		return s
	case model.Doc:
		s.Tags = append(slices.Clone(s.Tags), seedSentinel)
		return s
	case model.Log:
		s.Tags = append(slices.Clone(s.Tags), seedSentinel)
		return s
	case model.Task:
		s.Labels = append(slices.Clone(s.Labels), seedSentinel)
		return s
	case model.Sprint:
		s.Labels = append(slices.Clone(s.Labels), seedSentinel)
		return s
	case model.Project:
		s.Labels = append(slices.Clone(s.Labels), seedSentinel)
		return s
	case model.Runbook:
		s.Labels = append(slices.Clone(s.Labels), seedSentinel)
		return s
	case model.Investigation:
		s.Tags = append(slices.Clone(s.Tags), seedSentinel)
		return s
	case model.Plan:
		s.Labels = append(slices.Clone(s.Labels), seedSentinel)
		return s
	case model.Answer:
		s.Tags = append(slices.Clone(s.Tags), seedSentinel)
		return s
	case model.Ledger:
		s.Labels = append(slices.Clone(s.Labels), seedSentinel)
		return s
	}
	t.Fatalf("withSeedSentinel: unhandled snapshot %T", snap)
	return nil
}

func roundTripped(t *testing.T, snap model.Snapshot) model.Snapshot {
	t.Helper()
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal %s snapshot: %v", snap.Meta().Kind, err)
	}
	decoded, err := snap.Meta().Kind.DecodeSnapshot(data)
	if err != nil {
		t.Fatalf("decode %s snapshot: %v", snap.Meta().Kind, err)
	}
	return decoded
}

func isCheckpoint(c model.PackCommit) bool {
	if len(c.Pack.Ops) != 1 {
		return false
	}
	_, ok := c.Pack.Ops[0].(model.Checkpoint)
	return ok
}

func requireExtendMatchesStrict(t *testing.T, chain []model.PackCommit) {
	t.Helper()
	for k := 1; k < len(chain); k++ {
		next := chain[k]
		base, err := fold.Strict(chain[:k])
		if err != nil {
			t.Fatalf("Strict(chain[:%d]): %v", k, err)
		}
		cached, pristine := roundTripped(t, base), roundTripped(t, base)
		if isCheckpoint(next) {
			if _, err := fold.Extend(cached, next); !errors.Is(err, fold.ErrExtendCheckpoint) {
				t.Fatalf("Extend by checkpoint %s error = %v, want %v", next.SHA, err, fold.ErrExtendCheckpoint)
			}
			continue
		}
		want, err := fold.Strict(chain[:k+1])
		if err != nil {
			t.Fatalf("Strict(chain[:%d]): %v", k+1, err)
		}
		got, err := fold.Extend(cached, next)
		if err != nil {
			t.Fatalf("Extend(cached Strict(chain[:%d]), %s): %v", k, next.SHA, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Extend(cached Strict(chain[:%d]), %s):\n got  %+v\n want %+v", k, next.SHA, got, want)
		}
		if !reflect.DeepEqual(cached, pristine) {
			t.Fatalf("Extend by %s mutated its base:\n got  %+v\n want %+v", next.SHA, cached, pristine)
		}
		got, err = fold.Extend(base, next)
		if err != nil {
			t.Fatalf("Extend(Strict(chain[:%d]), %s): %v", k, next.SHA, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Extend(Strict(chain[:%d]), %s):\n got  %+v\n want %+v", k, next.SHA, got, want)
		}
	}
}

func TestExtendMatchesStrict(t *testing.T) {
	covered := map[model.Kind]bool{}
	for _, tc := range extendCases() {
		covered[tc.kind] = true
		t.Run(string(tc.kind), func(t *testing.T) {
			chain := extendChain(tc.create, tc.steps)
			whole, err := fold.Strict(chain)
			if err != nil {
				t.Fatalf("Strict: %v", err)
			}
			if got := whole.Meta().Kind; got != tc.kind {
				t.Fatalf("chain folds as %s, want %s", got, tc.kind)
			}
			requireExtendMatchesStrict(t, chain)
		})
	}
	for _, k := range allKinds {
		if !covered[k] {
			t.Errorf("no Extend case for kind %s", k)
		}
	}
}

func TestExtendAfterCheckpointMatchesStrict(t *testing.T) {
	for _, tc := range extendCases() {
		for _, split := range []int{1, len(tc.steps) / 2, len(tc.steps) - 1} {
			t.Run(fmt.Sprintf("%s_split%d", tc.kind, split), func(t *testing.T) {
				chain := compactedChain(t, tc.create, tc.steps, split)
				requireExtendMatchesStrict(t, chain)
				whole, err := fold.Strict(chain)
				if err != nil {
					t.Fatalf("Strict: %v", err)
				}
				if labels := whole.Meta().Labels; !slices.Contains(labels, seedSentinel) {
					t.Fatalf("Strict did not seed from the checkpoint: labels = %v, want %q among them", labels, seedSentinel)
				}
			})
		}
	}
}

func TestExtendAfterMergeMatchesStrict(t *testing.T) {
	strictOf := func(commits ...model.PackCommit) model.Snapshot {
		t.Helper()
		s, err := fold.Strict(commits)
		if err != nil {
			t.Fatalf("Strict: %v", err)
		}
		return s
	}
	c0 := mk("c0", nil, "alice", 100, 1, model.CreateLedger{Nonce: "n", Title: "lg", Columns: []string{"state"}})
	c1 := mk("c1", []string{"c0"}, "alice", 150, 2, model.UpsertRow{Key: "a", Fields: map[string]string{"state": "open"}})
	a1 := mk("a1", []string{"c1"}, "bob", 200, 3,
		model.UpsertRow{Key: "a", Fields: map[string]string{"state": "from-a", "owner": "bob"}}, model.AddLabel{Label: "a"})
	b1 := mk("b1", []string{"c1"}, "carol", 210, 3,
		model.UpsertRow{Key: "a", Fields: map[string]string{"state": "from-b"}, Position: "z"}, model.AddComment{Body: "b"})
	ka := cp("ka", "a1", "bob", 250, 4, strictOf(c0, c1, a1), 3, "c0", "c1", "a1")
	kb := cp("kb", "b1", "carol", 260, 4, strictOf(c0, c1, b1), 3, "c0", "c1", "b1")
	cases := []struct {
		name    string
		history []model.PackCommit
	}{
		{"fork", []model.PackCommit{c0, c1, a1, b1, mk("m", []string{"a1", "b1"}, "alice", 300, 4)}},
		{"concurrent checkpoints", []model.PackCommit{c0, c1, a1, ka, b1, kb, mk("m", []string{"ka", "kb"}, "alice", 300, 5)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			merge := tc.history[len(tc.history)-1]
			next := mk("n", []string{string(merge.SHA)}, "dave", 400, uint64(merge.Pack.Lamport)+1,
				model.UpsertRow{Key: "a", Fields: map[string]string{"state": "after-merge"}}, model.RemoveLabel{Label: "a"})
			want := strictOf(append(slices.Clone(tc.history), next)...)
			got, err := fold.Extend(roundTripped(t, strictOf(tc.history...)), next)
			if err != nil {
				t.Fatalf("Extend: %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Extend after merge:\n got  %+v\n want %+v", got, want)
			}
		})
	}
}

func TestExtendRefuses(t *testing.T) {
	chain := extendChain(model.CreateLedger{Nonce: "n", Title: "lg", Columns: []string{"state"}}, [][]model.Op{
		{model.UpsertRow{Key: "a", Fields: map[string]string{"state": "open"}}},
		{model.AddComment{Body: "synced"}},
	})
	base, err := fold.Strict(chain)
	if err != nil {
		t.Fatalf("Strict: %v", err)
	}
	head := string(chain[len(chain)-1].SHA)
	lamport := uint64(len(chain) + 1)
	row := model.UpsertRow{Key: "b", Fields: map[string]string{"state": "new"}}
	skipped, err := fold.Fold(append(slices.Clone(chain), mk("cx", []string{head}, "bob", 900, lamport, model.Claim{Assignee: "bob"})))
	if err != nil {
		t.Fatalf("Fold: %v", err)
	}
	if n := skipped.Meta().SkippedOps; n != 1 {
		t.Fatalf("tolerant base SkippedOps = %d, want 1", n)
	}
	cases := []struct {
		name   string
		base   model.Snapshot
		commit model.PackCommit
		want   error
	}{
		{"unknown parent", base, mk("n1", []string{"zzz"}, "bob", 900, lamport, row), fold.ErrMissingParent},
		{"stale parent", base, mk("n1", []string{string(chain[0].SHA)}, "bob", 900, lamport, row), fold.ErrMissingParent},
		{"no parent", base, mk("n1", nil, "bob", 900, lamport, row), fold.ErrMissingParent},
		{"merge commit", base, mk("n1", []string{head, "other"}, "bob", 900, lamport, row), fold.ErrMissingParent},
		{"checkpoint commit", base, cp("n1", head, "compactor", 900, lamport, base, lamport-1, "c00", "c01", "c02"), fold.ErrExtendCheckpoint},
		{"base skipped ops", skipped, mk("n1", []string{"cx"}, "bob", 1000, lamport+1, row), fold.ErrKindMismatch},
		{"foreign op", base, mk("n1", []string{head}, "bob", 900, lamport, model.Claim{Assignee: "bob"}), fold.ErrKindMismatch},
		{"foreign op after an own op", base, mk("n1", []string{head}, "bob", 900, lamport, row, model.SetPriority{Priority: 1}), fold.ErrKindMismatch},
		{"own create op", base, mk("n1", []string{head}, "bob", 900, lamport, model.CreateLedger{Nonce: "m", Title: "again"}), fold.ErrDuplicateCreate},
		{"foreign create op", base, mk("n1", []string{head}, "bob", 900, lamport, model.CreateTask{Nonce: "m", Title: "t", Type: model.TypeTask, Branch: "main"}), fold.ErrDuplicateCreate},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := fold.Extend(tc.base, tc.commit); !errors.Is(err, tc.want) {
				t.Fatalf("Extend error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestExtendLeavesBaseUnchanged(t *testing.T) {
	chain := extendChain(model.CreateLedger{Nonce: "n", Title: "lg", Columns: []string{"state"}}, [][]model.Op{
		{model.UpsertRow{Key: "a", Fields: map[string]string{"state": "open", "owner": "bob"}, Position: "a"}},
		{model.UpsertRow{Key: "b", Fields: map[string]string{"state": "open"}, Position: "b"}},
		{model.AddComment{Body: "first"}},
	})
	base, err := fold.Strict(chain)
	if err != nil {
		t.Fatalf("Strict: %v", err)
	}
	want, err := fold.Strict(chain)
	if err != nil {
		t.Fatalf("Strict: %v", err)
	}
	next := mk("next", []string{string(chain[len(chain)-1].SHA)}, "carol", 900, uint64(len(chain)+1),
		model.UpsertRow{Key: "a", Fields: map[string]string{"state": "done", "extra": "x"}},
		model.UpsertRow{Key: "b", Fields: map[string]string{"state": "gone"}, Replace: true, Position: "0"},
		model.SetColumns{Columns: []string{"state", "extra"}},
		model.AddComment{Body: "second"},
		model.AddLabel{Label: "l"},
	)
	got, err := fold.Extend(base, next)
	if err != nil {
		t.Fatalf("Extend: %v", err)
	}
	if !reflect.DeepEqual(base, want) {
		t.Fatalf("Extend mutated base:\n got  %+v\n want %+v", base, want)
	}
	l, ok := got.(model.Ledger)
	if !ok {
		t.Fatalf("Extend returned %T, want model.Ledger", got)
	}
	if fields, wantFields := rowByKey(t, l, "a").Fields, map[string]string{"state": "done", "owner": "bob", "extra": "x"}; !reflect.DeepEqual(fields, wantFields) {
		t.Fatalf("row a fields = %v, want %v", fields, wantFields)
	}
	if fields, wantFields := rowByKey(t, l, "b").Fields, map[string]string{"state": "gone"}; !reflect.DeepEqual(fields, wantFields) {
		t.Fatalf("row b fields = %v, want %v", fields, wantFields)
	}
}
