package viz

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// TestAPIEntitiesAllKinds drives /api/entities over a repo holding every kind,
// asserting per-kind counts and that superseded entities stay in (flagged)
// while tombstoned ones drop out, plus that full-snapshot fields — a log's
// entries and a task's criteria — round-trip into the JSON.
func TestAPIEntitiesAllKinds(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	ctx := t.Context()
	s := r.openStore()

	// Two notes: keep is the superseding target (live); old is superseded by it
	// (must appear, flagged); gone is tombstoned (must not appear).
	keepSnap, err := s.Create(ctx, []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "keep note", Body: "current"}})
	if err != nil {
		t.Fatalf("create keep note: %v", err)
	}
	keep := keepSnap.(model.Note)
	oldSnap, err := s.Create(ctx, []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "old note", Body: "stale"}})
	if err != nil {
		t.Fatalf("create old note: %v", err)
	}
	old := oldSnap.(model.Note)
	if _, err := s.Append(ctx, refs.Note(old.ID), []model.Op{model.AddSupersededBy{ID: keep.ID}}); err != nil {
		t.Fatalf("supersede old note: %v", err)
	}
	goneSnap, err := s.Create(ctx, []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "gone note"}})
	if err != nil {
		t.Fatalf("create gone note: %v", err)
	}
	gone := goneSnap.(model.Note)
	if _, err := s.Append(ctx, refs.Note(gone.ID), []model.Op{model.DeleteNote{}}); err != nil {
		t.Fatalf("delete gone note: %v", err)
	}

	if _, err := s.Create(ctx, []model.Op{model.CreateDoc{Nonce: model.NewNonce(), Title: "a doc", Body: "doc body"}}); err != nil {
		t.Fatalf("create doc: %v", err)
	}

	logSnap, err := s.Create(ctx, []model.Op{model.CreateLog{Nonce: model.NewNonce(), Title: "a log"}})
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	logID := logSnap.(model.Log).ID
	const entryText = "deploy rolled out to us-east-1"
	if _, err := s.Append(ctx, refs.Log(logID), []model.Op{model.AppendEntry{Text: entryText}}); err != nil {
		t.Fatalf("append log entry: %v", err)
	}

	taskSnap, err := s.Create(ctx, []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: "a task", Type: model.TypeTask, Branch: "main"}})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	taskID := taskSnap.(model.Task).ID
	const criterionText = "binary builds with CGO disabled"
	if _, err := s.Append(ctx, refs.Task(taskID), []model.Op{model.AddCriterion{ID: model.NewNonce(), Text: criterionText}}); err != nil {
		t.Fatalf("add criterion: %v", err)
	}

	if _, err := s.Create(ctx, []model.Op{model.CreateSprint{Nonce: model.NewNonce(), Title: "a sprint"}}); err != nil {
		t.Fatalf("create sprint: %v", err)
	}
	if _, err := s.Create(ctx, []model.Op{model.CreateProject{Nonce: model.NewNonce(), Title: "a project"}}); err != nil {
		t.Fatalf("create project: %v", err)
	}

	const stepText = "restart the service"
	rbSnap, err := s.Create(ctx, []model.Op{
		model.CreateRunbook{Nonce: model.NewNonce(), Title: "a runbook"},
		model.AddStep{ID: model.NewNonce(), Text: stepText, Position: model.PositionBetween("", "")},
	})
	if err != nil {
		t.Fatalf("create runbook: %v", err)
	}
	rb := rbSnap.(model.Runbook)
	runID := model.NewNonce()
	if _, err := s.Append(ctx, refs.Runbook(rb.ID), []model.Op{
		model.StartRun{ID: runID},
		model.SetRunStepStatus{RunID: runID, StepID: rb.Steps[0].ID, Status: model.StepDone},
	}); err != nil {
		t.Fatalf("start run: %v", err)
	}

	ts, _, _ := newVizServer(t, r)
	code, body := getBody(t, ts.URL+"/api/entities")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", code, body)
	}
	var resp stateResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}

	if len(resp.Notes) != 2 {
		t.Errorf("notes = %d, want 2 (keep + superseded old); got %+v", len(resp.Notes), resp.Notes)
	}
	if len(resp.Docs) != 1 {
		t.Errorf("docs = %d, want 1", len(resp.Docs))
	}
	if len(resp.Logs) != 1 {
		t.Errorf("logs = %d, want 1", len(resp.Logs))
	}
	if len(resp.Tasks) != 1 {
		t.Errorf("tasks = %d, want 1", len(resp.Tasks))
	}
	if len(resp.Sprints) != 1 {
		t.Errorf("sprints = %d, want 1", len(resp.Sprints))
	}
	if len(resp.Projects) != 1 {
		t.Errorf("projects = %d, want 1", len(resp.Projects))
	}
	if len(resp.Runbooks) != 1 {
		t.Errorf("runbooks = %d, want 1", len(resp.Runbooks))
	}

	byID := make(map[model.EntityID]model.Note, len(resp.Notes))
	for _, n := range resp.Notes {
		byID[n.ID] = n
	}
	if _, present := byID[gone.ID]; present {
		t.Errorf("tombstoned note %s appears in the payload", gone.ID)
	}
	superseded, present := byID[old.ID]
	if !present {
		t.Fatalf("superseded note %s missing from the payload", old.ID)
	}
	if len(superseded.SupersededBy) != 1 || superseded.SupersededBy[0] != keep.ID {
		t.Errorf("superseded_by = %v, want [%s]", superseded.SupersededBy, keep.ID)
	}

	if got := resp.Logs[0].Entries; len(got) != 1 || got[0].Text != entryText {
		t.Errorf("log entries = %+v, want one entry %q", got, entryText)
	}
	if got := resp.Tasks[0].Criteria; len(got) != 1 || got[0].Text != criterionText {
		t.Errorf("task criteria = %+v, want one criterion %q", got, criterionText)
	}
	if got := resp.Runbooks[0].Steps; len(got) != 1 || got[0].Text != stepText {
		t.Errorf("runbook steps = %+v, want one step %q", got, stepText)
	}
	if got := resp.Runbooks[0].Runs; len(got) != 1 || len(got[0].Results) != 1 || got[0].Results[0].Status != model.StepDone {
		t.Errorf("runbook runs = %+v, want one run with a done step result", got)
	}
}
