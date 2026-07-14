package notes_test

import (
	"errors"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func stepTexts(rb model.Runbook) []string {
	out := make([]string, len(rb.Steps))
	for i, st := range rb.Steps {
		out[i] = st.Text
	}
	return out
}

func stepIDByText(t *testing.T, rb model.Runbook, text string) string {
	t.Helper()
	for _, st := range rb.Steps {
		if st.Text == text {
			return st.ID
		}
	}
	t.Fatalf("no step with text %q in %+v", text, rb.Steps)
	return ""
}

func eqStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestCreateRunbookWithSteps(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	rb, reused, err := c.CreateRunbook(ctx, notes.RunbookSpec{
		Title:       "Deploy",
		Description: "how to deploy",
		Labels:      []string{"ops"},
		Steps:       []string{"build", "test", "ship"},
	})
	if err != nil {
		t.Fatalf("CreateRunbook: %v", err)
	}
	if reused {
		t.Fatal("first CreateRunbook reported reused")
	}
	if rb.Status != model.RunbookActive {
		t.Errorf("status = %q, want active", rb.Status)
	}
	if want := []string{"build", "test", "ship"}; !eqStrings(stepTexts(rb), want) {
		t.Fatalf("steps = %v, want %v", stepTexts(rb), want)
	}
	if rb.Steps[0].Position >= rb.Steps[1].Position || rb.Steps[1].Position >= rb.Steps[2].Position {
		t.Errorf("positions not strictly increasing: %q %q %q", rb.Steps[0].Position, rb.Steps[1].Position, rb.Steps[2].Position)
	}
}

func TestRunbookStepPlacementAndMove(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	rb, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "R"})
	if err != nil {
		t.Fatalf("CreateRunbook: %v", err)
	}
	id := rb.ID

	rb, err = c.AddStep(ctx, id, "first", "", notes.Placement{Anchor: notes.PlaceLast})
	if err != nil {
		t.Fatalf("AddStep first: %v", err)
	}
	firstID := stepIDByText(t, rb, "first")
	if rb, err = c.AddStep(ctx, id, "third", "", notes.Placement{Anchor: notes.PlaceLast}); err != nil {
		t.Fatalf("AddStep third: %v", err)
	}
	rb, err = c.AddStep(ctx, id, "second", "", notes.Placement{Anchor: notes.PlaceAfter, Step: firstID})
	if err != nil {
		t.Fatalf("AddStep second --after first: %v", err)
	}
	if want := []string{"first", "second", "third"}; !eqStrings(stepTexts(rb), want) {
		t.Fatalf("after --after = %v, want %v", stepTexts(rb), want)
	}
	if rb, err = c.AddStep(ctx, id, "zero", "", notes.Placement{Anchor: notes.PlaceFirst}); err != nil {
		t.Fatalf("AddStep zero --first: %v", err)
	}
	if want := []string{"zero", "first", "second", "third"}; !eqStrings(stepTexts(rb), want) {
		t.Fatalf("after --first = %v, want %v", stepTexts(rb), want)
	}

	moved, err := c.MoveStep(ctx, id, firstID, notes.Placement{Anchor: notes.PlaceLast})
	if err != nil {
		t.Fatalf("MoveStep: %v", err)
	}
	if want := []string{"zero", "second", "third", "first"}; !eqStrings(stepTexts(moved), want) {
		t.Fatalf("after move --last = %v, want %v", stepTexts(moved), want)
	}

	// Self-relative placement is refused before any write.
	if _, err := c.MoveStep(ctx, id, firstID, notes.Placement{Anchor: notes.PlaceBefore, Step: firstID}); err == nil {
		t.Fatal("MoveStep before itself succeeded, want an error")
	}
}

func TestRunbookStepEditRemove(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	rb, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "R", Steps: []string{"a"}})
	if err != nil {
		t.Fatalf("CreateRunbook: %v", err)
	}
	id := rb.ID
	stepID := rb.Steps[0].ID

	// Empty step edit is refused.
	if _, err := c.EditStep(ctx, id, stepID, notes.StepEdit{}); !errors.Is(err, notes.ErrEmptyEdit) {
		t.Fatalf("empty EditStep = %v, want ErrEmptyEdit", err)
	}

	text, command := "A2", "echo hi"
	edited, err := c.EditStep(ctx, id, stepID, notes.StepEdit{Text: &text, Command: &command})
	if err != nil {
		t.Fatalf("EditStep: %v", err)
	}
	if edited.Steps[0].Text != "A2" || edited.Steps[0].Command != "echo hi" {
		t.Fatalf("edited step = %+v, want text A2 command 'echo hi'", edited.Steps[0])
	}

	cleared, err := c.EditStep(ctx, id, stepID, notes.StepEdit{Command: ptr("")})
	if err != nil {
		t.Fatalf("EditStep clear command: %v", err)
	}
	if cleared.Steps[0].Command != "" {
		t.Fatalf("command = %q, want cleared", cleared.Steps[0].Command)
	}

	removed, err := c.RemoveStep(ctx, id, stepID)
	if err != nil {
		t.Fatalf("RemoveStep: %v", err)
	}
	if len(removed.Steps) != 0 {
		t.Fatalf("steps after rm = %v, want none", removed.Steps)
	}
}

func TestRunbookRunLifecycleAndResolution(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	rb, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "R", Steps: []string{"a", "b"}})
	if err != nil {
		t.Fatalf("CreateRunbook: %v", err)
	}
	id := rb.ID
	stepA := stepIDByText(t, rb, "a")
	stepB := stepIDByText(t, rb, "b")

	// Zero running runs is a conflict.
	_, err = c.SetRunStep(ctx, id, "", stepA, model.StepDone, "built")
	var conflict *notes.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("SetRunStep with no running run = %v, want *ConflictError", err)
	}

	started, err := c.StartRun(ctx, id, "")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if len(started.Runs) != 1 || started.Runs[0].Status != model.RunRunning {
		t.Fatalf("runs = %+v, want one running", started.Runs)
	}

	if _, err := c.SetRunStep(ctx, id, "", stepA, model.StepDone, "built"); err != nil {
		t.Fatalf("SetRunStep a done: %v", err)
	}
	afterSkip, err := c.SetRunStep(ctx, id, "", stepB, model.StepSkipped, "")
	if err != nil {
		t.Fatalf("SetRunStep b skip: %v", err)
	}
	run := afterSkip.Runs[0]
	if run.Results[0].Status != model.StepDone || run.Results[0].Note != "built" {
		t.Fatalf("step a result = %+v, want done/built", run.Results[0])
	}

	// Two running runs make an empty-run target ambiguous.
	if _, err := c.StartRun(ctx, id, ""); err != nil {
		t.Fatalf("second StartRun: %v", err)
	}
	if _, err := c.SetRunStep(ctx, id, "", stepA, model.StepDone, ""); !errors.Is(err, notes.ErrAmbiguous) {
		t.Fatalf("SetRunStep with two running = %v, want ErrAmbiguous", err)
	}

	// A run id prefix disambiguates the target.
	runID := afterSkip.Runs[0].ID
	if _, err := c.SetRunStep(ctx, id, runID, stepB, model.StepDone, ""); err != nil {
		t.Fatalf("SetRunStep with --run: %v", err)
	}
}

func TestRunbookFinishConflictAndDerived(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	rb, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "R", Steps: []string{"a"}})
	if err != nil {
		t.Fatalf("CreateRunbook: %v", err)
	}
	id := rb.ID
	stepA := rb.Steps[0].ID

	rb, err = c.StartRun(ctx, id, "")
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	runID := rb.Runs[0].ID

	rb, err = c.SetRunStep(ctx, id, "", stepA, model.StepFailed, "boom")
	if err != nil {
		t.Fatalf("SetRunStep fail: %v", err)
	}
	// DerivedRunStatus over the run with a failed step is failed.
	if got := notes.DerivedRunStatus(rb.Runs[0]); got != model.RunFailed {
		t.Fatalf("DerivedRunStatus = %q, want failed", got)
	}

	finished, err := c.FinishRun(ctx, id, "", notes.DerivedRunStatus(rb.Runs[0]))
	if err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	if finished.Runs[0].Status != model.RunFailed || finished.Runs[0].FinishedAt == 0 {
		t.Fatalf("finished run = %+v, want failed with a finish stamp", finished.Runs[0])
	}

	// Re-finishing the same run conflicts.
	if _, err := c.FinishRun(ctx, id, runID, model.RunSucceeded); err == nil {
		t.Fatal("re-finish succeeded, want a *ConflictError")
	} else {
		var conflict *notes.ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("re-finish = %v, want *ConflictError", err)
		}
	}
}

func TestRunbookStatusAndGating(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	rb, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "R", Steps: []string{"a"}})
	if err != nil {
		t.Fatalf("CreateRunbook: %v", err)
	}
	id := rb.ID

	archived, err := c.ArchiveRunbook(ctx, id)
	if err != nil {
		t.Fatalf("ArchiveRunbook: %v", err)
	}
	if archived.Status != model.RunbookArchived || archived.ArchivedAt == 0 {
		t.Fatalf("archived = %+v, want archived with a stamp", archived)
	}

	// Archiving again is a same-status no-op conflict.
	if _, err := c.ArchiveRunbook(ctx, id); err == nil {
		t.Fatal("double archive succeeded, want *ConflictError")
	} else {
		var conflict *notes.ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("double archive = %v, want *ConflictError", err)
		}
	}

	// Every write but activate is gated while archived.
	title := "X"
	for name, fn := range map[string]func() error{
		"edit":    func() error { _, e := c.EditRunbook(ctx, id, notes.RunbookEdit{Title: &title}); return e },
		"comment": func() error { _, e := c.CommentRunbook(ctx, id, "hi"); return e },
		"step":    func() error { _, e := c.AddStep(ctx, id, "b", "", notes.Placement{Anchor: notes.PlaceLast}); return e },
		"run":     func() error { _, e := c.StartRun(ctx, id, ""); return e },
	} {
		err := fn()
		var conflict *notes.ConflictError
		if !errors.As(err, &conflict) {
			t.Fatalf("%s on archived = %v, want *ConflictError", name, err)
		}
	}

	// Reactivating an archived runbook is allowed and lifts the gate.
	if _, err := c.ActivateRunbook(ctx, id); err != nil {
		t.Fatalf("ActivateRunbook: %v", err)
	}
	if _, err := c.AddStep(ctx, id, "b", "", notes.Placement{Anchor: notes.PlaceLast}); err != nil {
		t.Fatalf("AddStep after reactivate: %v", err)
	}

	// Activating an already-active runbook is a same-status conflict.
	if _, err := c.ActivateRunbook(ctx, id); err == nil {
		t.Fatal("double activate succeeded, want *ConflictError")
	}
}

func TestRunbooksIncludeArchived(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	a, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "A"})
	if err != nil {
		t.Fatalf("CreateRunbook A: %v", err)
	}
	b, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "B"})
	if err != nil {
		t.Fatalf("CreateRunbook B: %v", err)
	}
	if _, err := c.ArchiveRunbook(ctx, b.ID); err != nil {
		t.Fatalf("ArchiveRunbook: %v", err)
	}

	active, err := c.Runbooks(ctx, false)
	if err != nil {
		t.Fatalf("Runbooks: %v", err)
	}
	if len(active) != 1 || active[0].ID != a.ID {
		t.Fatalf("Runbooks(false) = %d, want only active A", len(active))
	}
	all, err := c.Runbooks(ctx, true)
	if err != nil {
		t.Fatalf("Runbooks(true): %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("Runbooks(true) = %d, want 2", len(all))
	}
}

func TestRunbookEditMaskAndEmpty(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	rb, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "R", Labels: []string{"keep", "drop"}})
	if err != nil {
		t.Fatalf("CreateRunbook: %v", err)
	}
	id := rb.ID

	if _, err := c.EditRunbook(ctx, id, notes.RunbookEdit{}); !errors.Is(err, notes.ErrEmptyEdit) {
		t.Fatalf("empty EditRunbook = %v, want ErrEmptyEdit", err)
	}

	title, desc := "R2", "desc"
	edited, err := c.EditRunbook(ctx, id, notes.RunbookEdit{
		Title:        &title,
		Description:  &desc,
		AddLabels:    []string{"new"},
		RemoveLabels: []string{"drop"},
	})
	if err != nil {
		t.Fatalf("EditRunbook: %v", err)
	}
	if edited.Title != "R2" || edited.Description != "desc" {
		t.Fatalf("edited = title %q desc %q", edited.Title, edited.Description)
	}
	if want := []string{"keep", "new"}; !eqStrings(edited.Labels, want) {
		t.Fatalf("labels = %v, want %v", edited.Labels, want)
	}
}

func ptr[T any](v T) *T { return &v }

func TestRunbookAnchors(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	rb, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{
		Title:   "Deploy",
		Anchors: notes.AnchorSpec{Paths: []string{"scripts/deploy.sh"}, Branches: []string{"main"}},
	})
	if err != nil {
		t.Fatalf("CreateRunbook: %v", err)
	}
	want := []model.Anchor{
		{Kind: model.AnchorBranch, Value: "main"},
		{Kind: model.AnchorPath, Value: "scripts/deploy.sh"},
	}
	if !reflect.DeepEqual(rb.Anchors, want) {
		t.Fatalf("Anchors = %+v, want %+v", rb.Anchors, want)
	}

	edited, err := c.EditRunbook(ctx, rb.ID, notes.RunbookEdit{
		AddAnchors:    notes.AnchorSpec{Dirs: []string{"internal/auth"}},
		RemoveAnchors: notes.AnchorSpec{Branches: []string{"main"}},
	})
	if err != nil {
		t.Fatalf("EditRunbook anchors: %v", err)
	}
	want = []model.Anchor{
		{Kind: model.AnchorDir, Value: "internal/auth"},
		{Kind: model.AnchorPath, Value: "scripts/deploy.sh"},
	}
	if !reflect.DeepEqual(edited.Anchors, want) {
		t.Fatalf("edited Anchors = %+v, want %+v", edited.Anchors, want)
	}

	// A bad commit revision refuses before any write.
	if _, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{
		Title:   "Bad",
		Anchors: notes.AnchorSpec{Commits: []string{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}},
	}); !errors.Is(err, notes.ErrNotFound) {
		t.Errorf("CreateRunbook(bad commit) = %v, want ErrNotFound", err)
	}
	if _, err := c.EditRunbook(ctx, rb.ID, notes.RunbookEdit{
		AddAnchors: notes.AnchorSpec{Commits: []string{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}},
	}); !errors.Is(err, notes.ErrNotFound) {
		t.Errorf("EditRunbook(bad commit) = %v, want ErrNotFound", err)
	}

	// An anchor-less runbook folds nil Anchors.
	plain, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "Plain"})
	if err != nil {
		t.Fatalf("CreateRunbook plain: %v", err)
	}
	if plain.Anchors != nil {
		t.Errorf("anchor-less Anchors = %+v, want nil", plain.Anchors)
	}
}

func TestRemoveRunbook(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	keep, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "Keep"})
	if err != nil {
		t.Fatalf("CreateRunbook keep: %v", err)
	}
	drop, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "Drop"})
	if err != nil {
		t.Fatalf("CreateRunbook drop: %v", err)
	}

	removed, err := c.RemoveRunbook(ctx, drop.ID)
	if err != nil {
		t.Fatalf("RemoveRunbook: %v", err)
	}
	if !removed.Deleted {
		t.Fatal("removed.Deleted = false, want true")
	}

	list, err := c.Runbooks(ctx, true)
	if err != nil {
		t.Fatalf("Runbooks: %v", err)
	}
	if len(list) != 1 || list[0].ID != keep.ID {
		t.Fatalf("Runbooks after remove = %+v, want only %s", list, keep.ID)
	}

	// The tombstoned runbook still resolves by id.
	shown, err := c.Runbook(ctx, drop.ID)
	if err != nil {
		t.Fatalf("Runbook(removed): %v", err)
	}
	if !shown.Deleted {
		t.Error("shown.Deleted = false, want true")
	}
}

func TestSearchRunbooks(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	byTitle, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "Deploy service"})
	if err != nil {
		t.Fatalf("CreateRunbook byTitle: %v", err)
	}
	byLabel, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "Rollback", Labels: []string{"deploy"}})
	if err != nil {
		t.Fatalf("CreateRunbook byLabel: %v", err)
	}
	byStep, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "Incident", Steps: []string{"deploy the fix"}})
	if err != nil {
		t.Fatalf("CreateRunbook byStep: %v", err)
	}
	if _, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "Unrelated"}); err != nil {
		t.Fatalf("CreateRunbook unrelated: %v", err)
	}
	archived, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "Deploy archived"})
	if err != nil {
		t.Fatalf("CreateRunbook archived: %v", err)
	}
	if _, err := c.ArchiveRunbook(ctx, archived.ID); err != nil {
		t.Fatalf("ArchiveRunbook: %v", err)
	}
	deleted, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "Deploy deleted"})
	if err != nil {
		t.Fatalf("CreateRunbook deleted: %v", err)
	}
	if _, err := c.RemoveRunbook(ctx, deleted.ID); err != nil {
		t.Fatalf("RemoveRunbook: %v", err)
	}

	got, err := c.SearchRunbooks(ctx, "deploy", notes.SearchFilter{Limit: -1})
	if err != nil {
		t.Fatalf("SearchRunbooks: %v", err)
	}
	wantIDs := []model.EntityID{byTitle.ID, byLabel.ID, byStep.ID}
	gotIDs := make([]model.EntityID, len(got))
	for i, rb := range got {
		gotIDs[i] = rb.ID
	}
	if !reflect.DeepEqual(gotIDs, wantIDs) {
		t.Fatalf("SearchRunbooks ids = %v, want %v (title tier over label over step)", gotIDs, wantIDs)
	}

	if none, err := c.SearchRunbooks(ctx, "nomatch", notes.SearchFilter{Limit: -1}); err != nil || len(none) != 0 {
		t.Fatalf("SearchRunbooks(nomatch) = %v/%v, want empty/nil", none, err)
	}
}
