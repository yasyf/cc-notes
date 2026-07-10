package cli

import (
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func sampleRunbook() model.Runbook {
	return model.Runbook{
		ID:          "deadbeefcafe1234",
		Title:       "Deploy",
		Description: "steps to deploy",
		Status:      model.RunbookActive,
		Labels:      []string{"ops"},
		Comments:    []model.Comment{{Author: "Bob <bob@x>", TS: 1735689600, Body: "lgtm"}},
		CreatedAt:   1735689600,
		UpdatedAt:   1735689600,
		Steps: []model.RunbookStep{
			{ID: "step111aaaaaaaa", Text: "build", Position: "i"},
			{ID: "step222bbbbbbbb", Text: "ship", Command: "make ship", Position: "n"},
		},
		Runs: []model.RunbookRun{{
			ID:         "run111cccccccc",
			Status:     model.RunSucceeded,
			Runner:     "Ada <ada@x>",
			StartedAt:  1735689600,
			FinishedAt: 1735693200,
			Results: []model.RunbookStepResult{
				{StepID: "step111aaaaaaaa", Status: model.StepDone, Note: "ok"},
			},
		}},
	}
}

func TestLeanRunbookLine(t *testing.T) {
	rb := sampleRunbook()
	if got, want := leanRunbookLine(rb), "deadbee\tactive\tDeploy"; got != want {
		t.Fatalf("leanRunbookLine = %q, want %q", got, want)
	}
}

func TestLeanRunLine(t *testing.T) {
	rb := sampleRunbook()
	if got, want := leanRunLine(rb, rb.Runs[0]), "run111c\tsucceeded\tAda <ada@x>\t2025-01-01\t1/2"; got != want {
		t.Fatalf("leanRunLine = %q, want %q", got, want)
	}
}

func TestNewRunbookDTO(t *testing.T) {
	rb := sampleRunbook()
	dto := newRunbookDTO(rb)

	if dto.ID != "deadbeefcafe1234" || dto.Status != "active" {
		t.Fatalf("id/status = %q/%q", dto.ID, dto.Status)
	}
	if dto.ArchivedAt != nil {
		t.Fatalf("ArchivedAt = %v on active runbook, want nil", dto.ArchivedAt)
	}
	if len(dto.Steps) != 2 || dto.Steps[1].Command != "make ship" || dto.Steps[0].Position != "i" {
		t.Fatalf("steps = %+v", dto.Steps)
	}
	if len(dto.Runs) != 1 {
		t.Fatalf("runs = %d, want 1", len(dto.Runs))
	}
	run := dto.Runs[0]
	if run.Task != nil {
		t.Fatalf("Task = %v, want nil (no cite)", run.Task)
	}
	if run.FinishedAt == nil || *run.FinishedAt != "2025-01-01T01:00:00Z" {
		t.Fatalf("FinishedAt = %v", run.FinishedAt)
	}
	if len(run.Steps) != 2 {
		t.Fatalf("run steps = %d, want 2 (one per current step)", len(run.Steps))
	}
	if run.Steps[0].Status != "done" || run.Steps[0].Note != "ok" || run.Steps[0].Step != "step111aaaaaaaa" {
		t.Fatalf("run step[0] = %+v, want done/ok/full-id", run.Steps[0])
	}
	if run.Steps[1].Status != "pending" || run.Steps[1].Note != "" {
		t.Fatalf("run step[1] = %+v, want pending with no note", run.Steps[1])
	}
	if len(dto.Comments) != 1 || dto.Comments[0].Author != "Bob <bob@x>" || dto.Comments[0].Body != "lgtm" || dto.Comments[0].TS != "2025-01-01T00:00:00Z" {
		t.Fatalf("comments = %+v, want one lgtm comment", dto.Comments)
	}

	archived := sampleRunbook()
	archived.Status = model.RunbookArchived
	archived.ArchivedAt = 1735693200
	adto := newRunbookDTO(archived)
	if adto.ArchivedAt == nil || *adto.ArchivedAt != "2025-01-01T01:00:00Z" {
		t.Fatalf("archived ArchivedAt = %v, want the stamp", adto.ArchivedAt)
	}

	empty := newRunbookDTO(model.Runbook{ID: "0000000aa"})
	if empty.Steps == nil || empty.Runs == nil || empty.Labels == nil || empty.Comments == nil {
		t.Fatalf("empty runbook slices must be non-nil: steps=%v runs=%v labels=%v comments=%v", empty.Steps, empty.Runs, empty.Labels, empty.Comments)
	}
	if len(empty.Comments) != 0 {
		t.Fatalf("empty runbook has phantom comments: %+v", empty.Comments)
	}
	if len(empty.Steps) != 0 || len(empty.Runs) != 0 {
		t.Fatalf("empty runbook has phantom steps/runs: %+v", empty)
	}
}

func TestRenderRunbookShow(t *testing.T) {
	rb := sampleRunbook()
	got := renderRunbookShow(rb)

	wantHeader := "id: deadbeefcafe1234\ntitle: Deploy\nstatus: active\nlabels: ops\ncreated: 2025-01-01T00:00:00Z\nupdated: 2025-01-01T00:00:00Z\narchived: -\n"
	if !strings.HasPrefix(got, wantHeader) {
		t.Fatalf("header block wrong:\n%s", got)
	}
	if !strings.Contains(got, "\nsteps to deploy\n") {
		t.Fatalf("description not rendered:\n%s", got)
	}
	if !strings.Contains(got, "\n-- Bob <bob@x> 2025-01-01T00:00:00Z\nlgtm\n") {
		t.Fatalf("comment not rendered:\n%s", got)
	}
	// Comments sit between the description and the steps block, mirroring sprint.
	if strings.Index(got, "lgtm") > strings.Index(got, "\nsteps:\n") {
		t.Fatalf("comment must render before steps:\n%s", got)
	}
	// The renderTaskShow-gotcha regression: steps and runs are the primary
	// content and MUST render in plain text.
	if !strings.Contains(got, "steps:\n  1. [step111] build\n  2. [step222] ship\n     $ make ship\n") {
		t.Fatalf("steps block missing or malformed:\n%s", got)
	}
	if !strings.Contains(got, "runs:\n-- run111c succeeded by Ada <ada@x> 2025-01-01T00:00:00Z → 2025-01-01T01:00:00Z (1 done, 0 skipped, 0 failed / 2) task -\n") {
		t.Fatalf("runs block missing or malformed:\n%s", got)
	}
}

func TestRenderRunbookShowRunCap(t *testing.T) {
	rb := sampleRunbook()
	rb.Runs = nil
	rb.Comments = nil
	for i := 0; i < 7; i++ {
		rb.Runs = append(rb.Runs, model.RunbookRun{ID: string(rune('a'+i)) + "unxxxxxxxxx", Status: model.RunSucceeded, Runner: "r", StartedAt: 1735689600})
	}
	got := renderRunbookShow(rb)
	if strings.Count(got, "\n-- ") != 5 {
		t.Fatalf("want exactly 5 run lines, got %d:\n%s", strings.Count(got, "\n-- "), got)
	}
	if !strings.Contains(got, "(+2 older — use run list)\n") {
		t.Fatalf("older-runs trailer missing:\n%s", got)
	}
	// Newest first: the last-appended run (index 6, "g...") heads the list.
	firstRunLine := got[strings.Index(got, "\n-- ")+4:]
	if !strings.HasPrefix(firstRunLine, "gunxxxx ") {
		t.Fatalf("runs not newest-first; first line = %q", firstRunLine[:20])
	}
}

func TestRenderRunShow(t *testing.T) {
	rb := sampleRunbook()
	got := renderRunShow(rb, rb.Runs[0])

	if !strings.HasPrefix(got, "run: run111cccccccc\nrunbook: deadbeefcafe1234\nstatus: succeeded\nrunner: Ada <ada@x>\nstarted: 2025-01-01T00:00:00Z\nfinished: 2025-01-01T01:00:00Z\ntask: -\n") {
		t.Fatalf("run header wrong:\n%s", got)
	}
	if !strings.Contains(got, "steps:\n  step111 done build\n     note: ok\n  step222 pending ship\n") {
		t.Fatalf("per-step lines wrong:\n%s", got)
	}
}

func TestFinishStatus(t *testing.T) {
	base := model.RunbookRun{Results: []model.RunbookStepResult{{StepID: "a", Status: model.StepDone}}}
	failing := model.RunbookRun{Results: []model.RunbookStepResult{{StepID: "a", Status: model.StepFailed}}}
	for _, tc := range []struct {
		name            string
		run             model.RunbookRun
		failed, abandon bool
		want            model.RunStatus
	}{
		{"clean default succeeds", base, false, false, model.RunSucceeded},
		{"a failed step defaults failed", failing, false, false, model.RunFailed},
		{"explicit failed overrides", base, true, false, model.RunFailed},
		{"explicit abandoned overrides a failed step", failing, false, true, model.RunAbandoned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := finishStatus(tc.run, tc.failed, tc.abandon); got != tc.want {
				t.Fatalf("finishStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRenderForeignShortIDs guards the render paths against a pack synced from
// another client whose step/run wire ids are shorter than the 7 chars our own
// nonces guarantee: the renderers must clamp, not slice-panic, and echo the
// short id verbatim.
func TestRenderForeignShortIDs(t *testing.T) {
	rb := model.Runbook{
		ID:        "deadbeefcafe1234",
		Title:     "Foreign",
		Status:    model.RunbookActive,
		CreatedAt: 1735689600,
		UpdatedAt: 1735689600,
		Steps:     []model.RunbookStep{{ID: "s1", Text: "build", Position: "i"}},
		Runs: []model.RunbookRun{{
			ID:        "r12",
			Status:    model.RunRunning,
			Runner:    "Ada <ada@x>",
			StartedAt: 1735689600,
		}},
	}

	if got := leanRunLine(rb, rb.Runs[0]); !strings.HasPrefix(got, "r12\t") {
		t.Fatalf("leanRunLine = %q, want short run id prefix %q", got, "r12\t")
	}

	show := renderRunbookShow(rb)
	if !strings.Contains(show, "  1. [s1] build\n") {
		t.Fatalf("renderRunbookShow lost short step id:\n%s", show)
	}
	if !strings.Contains(show, "-- r12 running by Ada <ada@x>") {
		t.Fatalf("renderRunbookShow lost short run id:\n%s", show)
	}

	run := renderRunShow(rb, rb.Runs[0])
	if !strings.HasPrefix(run, "run: r12\n") {
		t.Fatalf("renderRunShow header lost short run id:\n%s", run)
	}
	if !strings.Contains(run, "  s1 pending build\n") {
		t.Fatalf("renderRunShow lost short step id:\n%s", run)
	}
}
