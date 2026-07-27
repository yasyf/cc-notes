package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// Fixed timestamps chosen distinct so each RFC3339 field renders differently in
// the goldens; every value is UTC and deterministic.
const (
	dtoCreated   = int64(1700000000)
	dtoUpdated   = int64(1700003600)
	dtoVerified  = int64(1700007200)
	dtoStale     = int64(1700010800)
	dtoStarted   = int64(1700014400)
	dtoClosed    = int64(1700018000)
	dtoHeartbeat = int64(1700021600)
	dtoStartDate = int64(1690000000)
	dtoEndDate   = int64(1698000000)
)

// TestDTOGoldens freezes the JSON shape of every entity-kind DTO the CLI emits,
// full and summary alike. Each kind gets a fully-populated snapshot and a
// minimal one, projected through the real DTO conversion and marshaled; the
// goldens pin key order, the bodies only a full DTO carries, and the absence of
// every field sitting at its zero value.
func TestDTOGoldens(t *testing.T) {
	fullAnchor := model.Anchor{Kind: model.AnchorDir, Value: "internal/api"}
	bareAnchor := model.Anchor{Kind: model.AnchorPath, Value: "internal/cli/output.go"}
	ada := model.Actor("ada <ada@example.com>")
	bob := model.Actor("bob <bob@example.com>")

	noteFull := model.Note{
		ID:             "note-full-000000000000000000000000000000",
		Title:          "verified superseded note",
		Body:           "the note body",
		Tags:           []string{"auth", "handoff"},
		Anchors:        []model.Anchor{fullAnchor, bareAnchor},
		Author:         ada,
		CreatedAt:      dtoCreated,
		UpdatedAt:      dtoUpdated,
		Deleted:        true,
		VerifiedAt:     dtoVerified,
		VerifiedBy:     ada,
		VerifiedCommit: "c0ffee0000000000000000000000000000000000",
		Witness:        []model.AnchorWitness{{Anchor: fullAnchor, OID: "f00ba12"}},
		SupersededBy:   []model.EntityID{"note-newer-0001", "note-newer-0002"},
		StaleAt:        dtoStale,
		StaleBy:        bob,
		StaleReason:    "rewritten",
	}
	noteAtts := []attachmentDTO{{Name: "dump.log", OID: "abc123", Size: 42, Present: true}}
	noteSupersedes := []model.EntityID{"note-older-0001"}
	noteLiveHead := []model.EntityID{"note-head-00001", "note-head-00002"}
	noteMin := model.Note{ID: "note-min-0000000000000000000000000000000", Title: "bare", Author: ada, CreatedAt: dtoCreated, UpdatedAt: dtoUpdated}

	docFull := model.Doc{
		ID:             "doc-full-0000000000000000000000000000000",
		Title:          "auth migration handoff",
		Body:           "the doc body",
		When:           "resuming the auth cutover",
		Tags:           []string{"auth"},
		Anchors:        []model.Anchor{fullAnchor, bareAnchor},
		Author:         ada,
		CreatedAt:      dtoCreated,
		UpdatedAt:      dtoUpdated,
		Deleted:        true,
		VerifiedAt:     dtoVerified,
		VerifiedBy:     ada,
		VerifiedCommit: "c0ffee0000000000000000000000000000000000",
		Witness:        []model.AnchorWitness{{Anchor: fullAnchor, OID: "f00ba12"}},
		SupersededBy:   []model.EntityID{"doc-newer-0001"},
		StaleAt:        dtoStale,
		StaleBy:        bob,
		StaleReason:    "rewritten",
	}
	docSupersedes := []model.EntityID{"doc-older-0001"}
	docLiveHead := []model.EntityID{"doc-head-000001"}
	docMin := model.Doc{ID: "doc-min-00000000000000000000000000000000", Title: "bare", Author: ada, CreatedAt: dtoCreated, UpdatedAt: dtoUpdated}

	logFull := model.Log{
		ID:    "log-full-0000000000000000000000000000000",
		Title: "rollout log",
		Entries: []model.LogEntry{
			{Author: ada, TS: dtoCreated, Text: "flipped to 5%"},
			{Author: bob, TS: dtoUpdated, Text: "flipped to 100%"},
		},
		Tags:      []string{"rollout"},
		Anchors:   []model.Anchor{fullAnchor},
		Author:    ada,
		CreatedAt: dtoCreated,
		UpdatedAt: dtoUpdated,
	}
	logAtts := []attachmentDTO{{Name: "run.log", OID: "deadbeef", Size: 1024, Present: false}}
	logMin := model.Log{ID: "log-min-00000000000000000000000000000000", Title: "bare", Author: ada, CreatedAt: dtoCreated, UpdatedAt: dtoUpdated}

	taskFull := model.Task{
		ID:          "task-full-000000000000000000000000000000",
		Branch:      "feature/x",
		Title:       "implement the thing",
		Description: "do the thing",
		Type:        model.TypeBug,
		Status:      model.StatusDone,
		Priority:    2,
		Assignee:    "alice <alice@example.com>",
		HeartbeatAt: dtoHeartbeat,
		Labels:      []string{"backend", "urgent"},
		BlockedBy:   []model.EntityID{"task-block-0001", "task-block-0002"},
		Parent:      "task-parent-001",
		Comments: []model.Comment{
			{Author: "alice <alice@example.com>", TS: dtoCreated, Body: "starting"},
			{Author: bob, TS: dtoUpdated, Body: "done"},
		},
		CreatedAt: dtoCreated,
		UpdatedAt: dtoUpdated,
		StartedAt: dtoStarted,
		ClosedAt:  dtoClosed,
		Commits:   []model.SHA{"c0ffee0000000000000000000000000000000000", "d00d000000000000000000000000000000000000"},
		Sprint:    "sprint-0000001",
		Project:   "project-000001",
		Criteria: []model.Criterion{
			{ID: "c1", Text: "tests pass", Script: "go test ./...", Status: model.CriterionMet, Note: "go test: 12 passed"},
			{ID: "c2", Text: "reviewed", Script: "", Status: model.CriterionPending},
		},
	}
	taskBlocks := []model.EntityID{"task-blocks-001", "task-blocks-002"}
	taskChildren := []model.EntityID{"task-child-0001", "task-child-0002"}
	taskRuns := []notes.TaskRun{
		{Runbook: "runbook-of-run1", Run: model.RunbookRun{ID: "run00001", Task: "task-full-000000000000000000000000000000", Status: model.RunSucceeded, StartedAt: dtoStarted, FinishedAt: dtoClosed}},
		{Runbook: "runbook-of-run2", Run: model.RunbookRun{ID: "run00002", Task: "task-full-000000000000000000000000000000", Status: model.RunRunning, StartedAt: dtoHeartbeat}},
	}
	taskMin := model.Task{ID: "task-min-0000000000000000000000000000000", Title: "bare", CreatedAt: dtoCreated, UpdatedAt: dtoUpdated}

	sprintFull := model.Sprint{
		ID:          "sprint-full-00000000000000000000000000000",
		Project:     "project-000001",
		Title:       "q3 hardening",
		Description: "hardening sprint",
		Status:      model.SprintActive,
		StartDate:   dtoStartDate,
		EndDate:     dtoEndDate,
		Labels:      []string{"hardening"},
		Commits:     []model.SHA{"c0ffee0000000000000000000000000000000000"},
		Comments:    []model.Comment{{Author: ada, TS: dtoCreated, Body: "kickoff"}},
		Author:      ada,
		CreatedAt:   dtoCreated,
		UpdatedAt:   dtoUpdated,
		StartedAt:   dtoStarted,
		ClosedAt:    dtoClosed,
	}
	sprintTasks := []model.EntityID{"task-in-sprint1", "task-in-sprint2"}
	sprintMin := model.Sprint{ID: "sprint-min-000000000000000000000000000000", Title: "bare", Author: ada, CreatedAt: dtoCreated, UpdatedAt: dtoUpdated}

	projectFull := model.Project{
		ID:          "project-full-0000000000000000000000000000",
		Title:       "platform",
		Description: "the platform project",
		Status:      model.ProjectArchived,
		Labels:      []string{"infra"},
		Commits:     []model.SHA{"c0ffee0000000000000000000000000000000000"},
		Comments:    []model.Comment{{Author: ada, TS: dtoCreated, Body: "note"}},
		Author:      ada,
		CreatedAt:   dtoCreated,
		UpdatedAt:   dtoUpdated,
		ClosedAt:    dtoClosed,
	}
	projectSprints := []model.EntityID{"sprint-000000001"}
	projectTasks := []model.EntityID{"task-in-proj001", "task-in-proj002"}
	projectMin := model.Project{ID: "project-min-00000000000000000000000000000", Title: "bare", Author: ada, CreatedAt: dtoCreated, UpdatedAt: dtoUpdated}

	runbookFull := model.Runbook{
		ID:          "runbook-full-0000000000000000000000000000",
		Title:       "deploy",
		Description: "deploy runbook",
		Status:      model.RunbookActive,
		Steps: []model.RunbookStep{
			{ID: "step0001", Text: "build image", Command: "make build", Position: "a"},
			{ID: "step0002", Text: "roll out", Command: "", Position: "b"},
		},
		Runs: []model.RunbookRun{{
			ID:         "run00001",
			Task:       "task-of-run0001",
			Status:     model.RunSucceeded,
			Runner:     "ci <ci@example.com>",
			StartedAt:  dtoStarted,
			FinishedAt: dtoClosed,
			Results:    []model.RunbookStepResult{{StepID: "step0001", Status: model.StepDone, Note: "ok", Actor: "ci <ci@example.com>", TS: dtoStarted}},
		}},
		Labels:     []string{"deploy"},
		Anchors:    []model.Anchor{fullAnchor, bareAnchor},
		Comments:   []model.Comment{{Author: ada, TS: dtoCreated, Body: "comment"}},
		Author:     ada,
		CreatedAt:  dtoCreated,
		UpdatedAt:  dtoUpdated,
		ArchivedAt: dtoClosed,
		Deleted:    true,
	}
	runbookMin := model.Runbook{ID: "runbook-min-00000000000000000000000000000", Title: "bare", Status: model.RunbookActive, Author: ada, CreatedAt: dtoCreated, UpdatedAt: dtoUpdated}
	runMin := model.RunbookRun{ID: "run00002", Status: model.RunRunning, StartedAt: dtoStarted}

	invFull := model.Investigation{
		ID:        "inv-full-0000000000000000000000000000000",
		Title:     "flaky sync",
		Premise:   "the union merge drops task branches",
		Body:      "the investigation body",
		Status:    model.InvestigationFixed,
		RootCause: "relocate ran before the fold",
		Findings: []model.Finding{
			{ID: "f1", Text: "merge order", Status: model.FindingConfirmed, Note: "reproduced"},
			{ID: "f2", Text: "clock skew", Status: model.FindingCleared},
			{ID: "f3", Text: "cache staleness", Status: model.FindingOpen},
		},
		Entries: []model.LogEntry{
			{Author: ada, TS: dtoCreated, Text: "repro on a clean clone"},
			{Author: bob, TS: dtoUpdated, Text: "patched the fold"},
		},
		FollowUps:    []model.EntityID{"task-followup01"},
		FixCommits:   []model.SHA{"c0ffee0000000000000000000000000000000000"},
		Commits:      []model.SHA{"d00d000000000000000000000000000000000000"},
		Tags:         []string{"sync"},
		Anchors:      []model.Anchor{fullAnchor, bareAnchor},
		SupersededBy: []model.EntityID{"inv-newer-00001"},
		Author:       ada,
		CreatedAt:    dtoCreated,
		UpdatedAt:    dtoUpdated,
		ClosedAt:     dtoClosed,
		ClosedBy:     bob,
		Deleted:      true,
	}
	invMin := model.Investigation{ID: "inv-min-00000000000000000000000000000000", Title: "bare", Status: model.InvestigationOpen, Author: ada, CreatedAt: dtoCreated, UpdatedAt: dtoUpdated}

	cases := []struct {
		name string
		dto  any
	}{
		{"note_full", newNoteDTO(noteFull, "DRIFTED", noteSupersedes, noteLiveHead, noteAtts)},
		{"note_empty", newNoteDTO(noteMin, "", nil, nil, []attachmentDTO{})},
		{"doc_full", newDocDTO(docFull, "DRIFTED", docSupersedes, docLiveHead, noteAtts)},
		{"doc_empty", newDocDTO(docMin, "", nil, nil, []attachmentDTO{})},
		{"log_full", newLogDTO(logFull, logAtts)},
		{"log_empty", newLogDTO(logMin, []attachmentDTO{})},
		{"task_full", newTaskDTO(taskFull, taskBlocks, taskChildren, taskRuns)},
		{"task_empty", newTaskDTO(taskMin, nil, nil, nil)},
		{"sprint_full", newSprintDTO(sprintFull, sprintTasks)},
		{"sprint_empty", newSprintDTO(sprintMin, nil)},
		{"project_full", newProjectDTO(projectFull, projectSprints, projectTasks)},
		{"project_empty", newProjectDTO(projectMin, nil, nil)},
		{"runbook_full", newRunbookDTO(runbookFull)},
		{"runbook_empty", newRunbookDTO(runbookMin)},
		{"investigation_full", newInvestigationDTO(invFull, noteAtts)},
		{"investigation_empty", newInvestigationDTO(invMin, []attachmentDTO{})},
		{"note_summary_full", newNoteSummaryDTO(noteFull, "DRIFTED")},
		{"note_summary_empty", newNoteSummaryDTO(noteMin, "")},
		{"doc_summary_full", newDocSummaryDTO(docFull, "DRIFTED")},
		{"doc_summary_empty", newDocSummaryDTO(docMin, "")},
		{"log_summary_full", newLogSummaryDTO(logFull)},
		{"log_summary_empty", newLogSummaryDTO(logMin)},
		{"task_summary_full", newTaskSummaryDTO(taskFull, taskBlocks)},
		{"task_summary_empty", newTaskSummaryDTO(taskMin, nil)},
		{"sprint_summary_full", newSprintSummaryDTO(sprintFull)},
		{"sprint_summary_empty", newSprintSummaryDTO(sprintMin)},
		{"project_summary_full", newProjectSummaryDTO(projectFull)},
		{"project_summary_empty", newProjectSummaryDTO(projectMin)},
		{"runbook_summary_full", newRunbookSummaryDTO(runbookFull)},
		{"runbook_summary_empty", newRunbookSummaryDTO(runbookMin)},
		{"run_summary_full", newRunbookRunSummaryDTO(runbookFull, runbookFull.Runs[0])},
		{"run_summary_empty", newRunbookRunSummaryDTO(runbookMin, runMin)},
		{"investigation_summary_full", newInvestigationSummaryDTO(invFull)},
		{"investigation_summary_empty", newInvestigationSummaryDTO(invMin)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.MarshalIndent(tc.dto, "", "  ")
			if err != nil {
				t.Fatalf("marshal %s: %v", tc.name, err)
			}
			got = append(got, '\n')

			golden := filepath.Join("testdata", "dto", tc.name+".json")
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(golden), 0o750); err != nil {
					t.Fatalf("mkdir testdata/dto: %v", err)
				}
				if err := os.WriteFile(golden, got, 0o600); err != nil {
					t.Fatalf("write golden: %v", err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden %s (regenerate with -update): %v", golden, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s DTO shape mismatch (regenerate with -update):\n%s", tc.name, got)
			}
		})
	}
}
