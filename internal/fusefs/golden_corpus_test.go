// Golden corpus: freezes the current rendered byte format of every entity kind
// so the descriptor-driven render/parse/diff rewrite cannot silently change the
// on-disk contract. Rendered file bytes are the contract — TestGoldenRender pins
// them against committed fixtures, TestGoldenRoundTrip pins render->parse->diff
// == no ops for every writable kind, and TestGoldenEdits pins the exact op
// sequence a targeted byte edit produces. Regenerate the committed fixtures with
//
//	go test ./internal/fusefs -run Golden -update
//
// After this lands, any refactor that dirties testdata/golden is a format break.
package fusefs_test

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/model"
)

// updateGolden regenerates the corpus fixtures instead of asserting against
// them. It is fusefs-local; the viz package owns its own -update flag in a
// separate test binary.
var updateGolden = flag.Bool("update", false, "regenerate the fusefs golden corpus")

// Shared fixture timestamps. The unix values map to the RFC3339 stamps the
// renderer emits (2025-12-12T02:54:56Z and neighbours); pinning them here keeps
// every fixture on the same clock.
const (
	cCreated  = int64(1765508096) // 2025-12-12T02:54:56Z
	cUpdated  = int64(1765594496) // 2025-12-13T02:54:56Z
	cVerified = int64(1765680896) // 2025-12-14T02:54:56Z
	cStarted  = int64(1765509000) // 2025-12-12T03:10:00Z
	cClosed   = int64(1765767296) // 2025-12-15T02:54:56Z
	cComment  = int64(1765510000) // 2025-12-12T03:26:40Z
	cStepA    = int64(1765508196) // 2025-12-12T02:56:36Z
	cStepB    = int64(1765508296) // 2025-12-12T02:58:16Z
	cEnd      = int64(1766112896) // 2025-12-19T02:54:56Z
)

// corpusEntry names one fixture and the snapshot it renders from. name is the
// golden file basename; the extension follows the render format (markdown for
// note/doc/log/runbook, JSON for task/sprint/project).
type corpusEntry struct {
	name string
	snap model.Snapshot
}

// corpus returns every fixture in the frozen corpus, rebuilt fresh on each call
// so no test can mutate another's snapshot.
func corpus() []corpusEntry {
	return concat(noteCorpus(), docCorpus(), logCorpus(), taskCorpus(), sprintCorpus(), projectCorpus(), runbookCorpus())
}

func concat(groups ...[]corpusEntry) []corpusEntry {
	var out []corpusEntry
	for _, g := range groups {
		out = append(out, g...)
	}
	return out
}

func noteCorpus() []corpusEntry {
	return []corpusEntry{
		{"note_rich", model.Note{
			ID:    "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
			Title: "Deploy notes",
			Body:  "Long-form analysis.\n\nWith a second paragraph.\n",
			Tags:  []string{"bug", "parser"},
			Anchors: []model.Anchor{
				{Kind: model.AnchorCommit, Value: "abc1234"},
				{Kind: model.AnchorCommit, Value: "def5678"},
				{Kind: model.AnchorPath, Value: "internal/cli/output.go"},
				{Kind: model.AnchorDir, Value: "internal/auth"},
				{Kind: model.AnchorBranch, Value: "feature/login"},
			},
			Author:         "Agent A <a@example.com>",
			CreatedAt:      cCreated,
			UpdatedAt:      cUpdated,
			VerifiedAt:     cVerified,
			VerifiedBy:     "Agent V <v@example.com>",
			VerifiedCommit: "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111",
			Witness: []model.AnchorWitness{
				{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "internal/cli/output.go"}, OID: "1234567890abcdef1234567890abcdef12345678"},
				{Anchor: model.Anchor{Kind: model.AnchorCommit, Value: "abc1234"}, OID: "abcd1234abcd1234abcd1234abcd1234abcd1234"},
			},
			SupersededBy: []model.EntityID{
				"cccc1111cccc1111cccc1111cccc1111cccc1111",
				"dddd2222dddd2222dddd2222dddd2222dddd2222",
			},
			Head: "ffff0000ffff0000ffff0000ffff0000ffff0000",
		}},
		{"note_minimal", model.Note{
			ID:     "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1",
			Title:  "Bare note",
			Author: "A <a@x>",
		}},
		{"note_stale", model.Note{
			ID:          "c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2",
			Title:       "Superseded fact",
			Body:        "This claim drifted.\n",
			Tags:        []string{"auth"},
			Anchors:     []model.Anchor{{Kind: model.AnchorPath, Value: "internal/auth/token.go"}},
			Author:      "Agent A <a@example.com>",
			CreatedAt:   cCreated,
			UpdatedAt:   cUpdated,
			VerifiedAt:  cVerified,
			VerifiedBy:  "Agent V <v@example.com>",
			StaleAt:     cUpdated,
			StaleBy:     "Agent S <s@example.com>",
			StaleReason: "token flow rewritten",
			Head:        "eeee0000eeee0000eeee0000eeee0000eeee0000",
		}},
		{"note_title_colon", model.Note{
			ID:        "d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3",
			Title:     `Fix the parser: edge-cases & "quotes" — 多言語 🦄`,
			Author:    "A <a@x>",
			CreatedAt: cCreated,
			UpdatedAt: cCreated,
		}},
		{"note_title_numberish", model.Note{
			ID:        "e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4",
			Title:     "07",
			Author:    "A <a@x>",
			CreatedAt: cCreated,
			UpdatedAt: cCreated,
		}},
		{"note_title_special_lead", model.Note{
			ID:        "f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5",
			Title:     "- [ ] TODO: {flow} [seq] #hash | pipe > gt 'q'",
			Author:    "A <a@x>",
			CreatedAt: cCreated,
			UpdatedAt: cCreated,
		}},
		{"note_body_delimiter", model.Note{
			ID:        "a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6",
			Title:     "Dangling body",
			Body:      "---\nfirst\n---\nlast, no trailing newline",
			Author:    "A <a@x>",
			CreatedAt: cCreated,
			UpdatedAt: cCreated,
		}},
	}
}

func docCorpus() []corpusEntry {
	return []corpusEntry{
		{"doc_rich", model.Doc{
			ID:    "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
			Title: "Auth architecture",
			Body:  "How the token refresh loop works.\n\nSecond paragraph.\n",
			When:  "before touching the auth flow",
			Tags:  []string{"auth", "reference"},
			Anchors: []model.Anchor{
				{Kind: model.AnchorCommit, Value: "abc1234"},
				{Kind: model.AnchorPath, Value: "internal/auth/token.go"},
				{Kind: model.AnchorDir, Value: "internal/auth"},
				{Kind: model.AnchorBranch, Value: "main"},
			},
			Author:         "Agent A <a@example.com>",
			CreatedAt:      cCreated,
			UpdatedAt:      cUpdated,
			VerifiedAt:     cVerified,
			VerifiedBy:     "Agent V <v@example.com>",
			VerifiedCommit: "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111",
			Witness: []model.AnchorWitness{
				{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "internal/auth/token.go"}, OID: "1234567890abcdef1234567890abcdef12345678"},
			},
			SupersededBy: []model.EntityID{"cccc1111cccc1111cccc1111cccc1111cccc1111"},
			Head:         "ffff0000ffff0000ffff0000ffff0000ffff0000",
		}},
		{"doc_minimal", model.Doc{
			ID:     "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1",
			Title:  "Bare doc",
			Author: "A <a@x>",
		}},
		{"doc_when_hostile", model.Doc{
			ID:        "c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2",
			Title:     `Doc: "quoted" & 🦄`,
			When:      "when: the flag flips [staged] #now",
			Author:    "A <a@x>",
			CreatedAt: cCreated,
			UpdatedAt: cCreated,
		}},
		{"doc_stale", model.Doc{
			ID:          "d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3",
			Title:       "Drifted guidance",
			Body:        "Out of date.\n",
			When:        "never — superseded",
			Author:      "Agent A <a@example.com>",
			CreatedAt:   cCreated,
			UpdatedAt:   cUpdated,
			StaleAt:     cUpdated,
			StaleBy:     "Agent S <s@example.com>",
			StaleReason: "replaced by the new guide",
			Head:        "eeee0000eeee0000eeee0000eeee0000eeee0000",
		}},
	}
}

func logCorpus() []corpusEntry {
	return []corpusEntry{
		{"log_rich", model.Log{
			ID:    "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
			Title: "Auth rollout",
			Tags:  []string{"ops"},
			Anchors: []model.Anchor{
				{Kind: model.AnchorDir, Value: "internal/auth"},
				{Kind: model.AnchorBranch, Value: "main"},
			},
			Entries: []model.LogEntry{
				{Author: "Agent A <a@example.com>", TS: cCreated, Text: "flipped to 5%"},
				{Author: "Agent B <b@example.com>", TS: cUpdated, Text: "# Heading-like line\n- looks like a list\nstill entry two\n"},
			},
			Author:    "Agent A <a@example.com>",
			CreatedAt: cCreated,
			UpdatedAt: cUpdated,
			Head:      "ffff0000ffff0000ffff0000ffff0000ffff0000",
		}},
		{"log_minimal", model.Log{
			ID:     "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1",
			Title:  "Empty log",
			Author: "A <a@x>",
		}},
		{"log_title_hostile", model.Log{
			ID:    "c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2",
			Title: `Log: "run" & 🦄`,
			Entries: []model.LogEntry{
				{Author: "A <a@x>", TS: cCreated, Text: "single entry\n"},
			},
			Author:    "A <a@x>",
			CreatedAt: cCreated,
			UpdatedAt: cCreated,
		}},
		{"log_model", model.Log{
			ID:    "a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5e6f7a8b9",
			Title: "papercuts",
			Tags:  []string{"papercut"},
			Entries: []model.LogEntry{
				{Author: "Agent A <a@example.com>", TS: cCreated, Text: "unquoted globs broke rg\n", Model: "claude-fable-5"},
				{Author: "Agent B <b@example.com>", TS: cUpdated, Text: "no model on this one\n"},
			},
			Author:    "Agent A <a@example.com>",
			CreatedAt: cCreated,
			UpdatedAt: cUpdated,
			Head:      "ffff0000ffff0000ffff0000ffff0000ffff0000",
		}},
	}
}

func taskCorpus() []corpusEntry {
	return []corpusEntry{
		{"task_rich", model.Task{
			ID:               "0123abcd4567ef890123abcd4567ef890123abcd",
			Branch:           "feature/login",
			Title:            "Wire the FUSE layer <urgently>",
			Description:      "Render, parse, diff.\nNo kernel needed.",
			Type:             model.TypeBug,
			Status:           model.StatusInProgress,
			Priority:         1,
			Assignee:         "Agent A <a@example.com>",
			HeartbeatAt:      cUpdated,
			HeartbeatLamport: 7,
			Labels:           []string{"fs", "render"},
			BlockedBy:        []model.EntityID{"9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa"},
			Parent:           "8888bbbb8888bbbb8888bbbb8888bbbb8888bbbb",
			Comments: []model.Comment{
				{Author: "Agent B <b@example.com>", TS: cComment, Body: "On it.\nETA tonight."},
			},
			Commits:   []model.SHA{"cafe0000cafe0000cafe0000cafe0000cafe0000", "feed1111feed1111feed1111feed1111feed1111"},
			CreatedAt: cCreated,
			UpdatedAt: cUpdated,
			StartedAt: cStarted,
			Sprint:    "7777cccc7777cccc7777cccc7777cccc7777cccc",
			Project:   "6666dddd6666dddd6666dddd6666dddd6666dddd",
			Criteria: []model.Criterion{
				{ID: "aaaa1111aaaa1111aaaa1111aaaa1111", Text: "Compiles clean", Script: "go build ./...", Status: model.CriterionMet},
				{ID: "bbbb2222bbbb2222bbbb2222bbbb2222", Text: "Tests pass", Status: model.CriterionPending},
			},
			Head: "eeee0000eeee0000eeee0000eeee0000eeee0000",
		}},
		{"task_minimal_open", model.Task{
			ID:        "1234abcd5678ef901234abcd5678ef901234abcd",
			Branch:    "main",
			Title:     "Plain task",
			Type:      model.TypeTask,
			Status:    model.StatusOpen,
			Priority:  2,
			CreatedAt: cCreated,
			UpdatedAt: cCreated,
		}},
		{"task_done_forced", model.Task{
			ID:        "2345abcd6789ef012345abcd6789ef012345abcd",
			Branch:    "main",
			Title:     "Force-closed epic",
			Type:      model.TypeEpic,
			Status:    model.StatusDone,
			Priority:  0,
			CreatedAt: cCreated,
			UpdatedAt: cClosed,
			StartedAt: cStarted,
			ClosedAt:  cClosed,
			Criteria: []model.Criterion{
				{ID: "cccc3333cccc3333cccc3333cccc3333", Text: "Docs updated", Status: model.CriterionMet},
				{ID: "dddd4444dddd4444dddd4444dddd4444", Text: "Perf benchmark", Script: "go test -bench=.", Status: model.CriterionFailed},
			},
			Head: "dddd0000dddd0000dddd0000dddd0000dddd0000",
		}},
		{"task_cancelled", model.Task{
			ID:        "3456abcd7890ef013456abcd7890ef013456abcd",
			Branch:    "feature/login",
			Title:     "Запуск 🚀",
			Type:      model.TypeQuestion,
			Status:    model.StatusCancelled,
			Priority:  3,
			CreatedAt: cCreated,
			UpdatedAt: cClosed,
			ClosedAt:  cClosed,
			Head:      "cccc0000cccc0000cccc0000cccc0000cccc0000",
		}},
		{"task_criteria_matrix", model.Task{
			ID:        "4567abcd8901ef014567abcd8901ef014567abcd",
			Branch:    "main",
			Title:     "Criteria matrix",
			Type:      model.TypeTask,
			Status:    model.StatusInProgress,
			Priority:  2,
			Assignee:  "Agent A <a@example.com>",
			CreatedAt: cCreated,
			UpdatedAt: cUpdated,
			StartedAt: cStarted,
			Criteria: []model.Criterion{
				{ID: "1111aaaa1111aaaa1111aaaa1111aaaa", Text: "pending no script", Status: model.CriterionPending},
				{ID: "2222bbbb2222bbbb2222bbbb2222bbbb", Text: "met with script", Script: "go vet ./...", Status: model.CriterionMet},
				{ID: "3333cccc3333cccc3333cccc3333cccc", Text: "failed with script", Script: "exit 1", Status: model.CriterionFailed},
			},
			Head: "bbbb0000bbbb0000bbbb0000bbbb0000bbbb0000",
		}},
	}
}

func sprintCorpus() []corpusEntry {
	return []corpusEntry{
		{"sprint_rich", model.Sprint{
			ID:          "5555aaaa5555aaaa5555aaaa5555aaaa5555aaaa",
			Project:     "6666dddd6666dddd6666dddd6666dddd6666dddd",
			Title:       "Sprint 7 core",
			Description: "Ship the FUSE layer.\nTwo lines.",
			Status:      model.SprintActive,
			StartDate:   cCreated,
			EndDate:     cEnd,
			Labels:      []string{"core", "fs"},
			Commits:     []model.SHA{"cafe0000cafe0000cafe0000cafe0000cafe0000"},
			Comments: []model.Comment{
				{Author: "Agent B <b@example.com>", TS: cComment, Body: "Kickoff."},
			},
			Author:    "Agent A <a@example.com>",
			CreatedAt: cCreated,
			UpdatedAt: cUpdated,
			StartedAt: cStarted,
			Head:      "dddd0000dddd0000dddd0000dddd0000dddd0000",
		}},
		{"sprint_minimal", model.Sprint{
			ID:        "6666aaaa6666aaaa6666aaaa6666aaaa6666aaaa",
			Title:     "Planning sprint",
			Status:    model.SprintPlanned,
			Author:    "A <a@x>",
			CreatedAt: cCreated,
			UpdatedAt: cCreated,
		}},
		{"sprint_completed", model.Sprint{
			ID:          "7777aaaa7777aaaa7777aaaa7777aaaa7777aaaa",
			Title:       "Finished sprint",
			Description: "Done.",
			Status:      model.SprintCompleted,
			StartDate:   cCreated,
			EndDate:     cEnd,
			Author:      "A <a@x>",
			CreatedAt:   cCreated,
			UpdatedAt:   cClosed,
			StartedAt:   cStarted,
			ClosedAt:    cClosed,
			Head:        "aaaa0000aaaa0000aaaa0000aaaa0000aaaa0000",
		}},
		{"sprint_cancelled", model.Sprint{
			ID:        "8888aaaa8888aaaa8888aaaa8888aaaa8888aaaa",
			Title:     "Dropped sprint",
			Status:    model.SprintCancelled,
			StartDate: cCreated,
			Author:    "A <a@x>",
			CreatedAt: cCreated,
			UpdatedAt: cClosed,
			ClosedAt:  cClosed,
			Head:      "9999000099990000999900009999000099990000",
		}},
	}
}

func projectCorpus() []corpusEntry {
	return []corpusEntry{
		{"project_rich", model.Project{
			ID:          "6666dddd6666dddd6666dddd6666dddd6666dddd",
			Title:       "Platform v2",
			Description: "Long-lived effort.\nMany sprints.",
			Status:      model.ProjectActive,
			Labels:      []string{"core", "platform"},
			Commits:     []model.SHA{"feed1111feed1111feed1111feed1111feed1111"},
			Comments: []model.Comment{
				{Author: "Agent C <c@example.com>", TS: cComment, Body: "Charter approved."},
			},
			Author:    "Agent A <a@example.com>",
			CreatedAt: cCreated,
			UpdatedAt: cUpdated,
			Head:      "cccc0000cccc0000cccc0000cccc0000cccc0000",
		}},
		{"project_minimal", model.Project{
			ID:        "7777dddd7777dddd7777dddd7777dddd7777dddd",
			Title:     "Bare project",
			Status:    model.ProjectActive,
			Author:    "A <a@x>",
			CreatedAt: cCreated,
			UpdatedAt: cCreated,
		}},
		{"project_completed", model.Project{
			ID:        "8888dddd8888dddd8888dddd8888dddd8888dddd",
			Title:     "Shipped project",
			Status:    model.ProjectCompleted,
			Author:    "A <a@x>",
			CreatedAt: cCreated,
			UpdatedAt: cClosed,
			ClosedAt:  cClosed,
			Head:      "8888000088880000888800008888000088880000",
		}},
		{"project_archived", model.Project{
			ID:        "9999dddd9999dddd9999dddd9999dddd9999dddd",
			Title:     "Shelved project",
			Status:    model.ProjectArchived,
			Author:    "A <a@x>",
			CreatedAt: cCreated,
			UpdatedAt: cClosed,
			ClosedAt:  cClosed,
			Head:      "7777000077770000777700007777000077770000",
		}},
		{"project_cancelled", model.Project{
			ID:        "aaaadddaaaadddaaaadddaaaadddaaaaddd00000",
			Title:     "Killed project",
			Status:    model.ProjectCancelled,
			Author:    "A <a@x>",
			CreatedAt: cCreated,
			UpdatedAt: cClosed,
			ClosedAt:  cClosed,
			Head:      "6666000066660000666600006666000066660000",
		}},
	}
}

func runbookCorpus() []corpusEntry {
	return []corpusEntry{
		{"runbook_rich", model.Runbook{
			ID:          "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
			Title:       "Deploy the service",
			Description: "Roll a new build to production.",
			Status:      model.RunbookActive,
			Steps: []model.RunbookStep{
				{ID: "1111111aaaabbbbccccddddeeeeffff0", Text: "Pull the latest image", Command: "docker pull myapp:latest", Position: "a"},
				{ID: "2222222aaaabbbbccccddddeeeeffff0", Text: "Restart the service", Position: "b"},
			},
			Runs: []model.RunbookRun{
				{
					ID:         "3333333aaaabbbbccccddddeeeeffff0",
					Task:       "ffff0000ffff0000ffff0000ffff0000ffff0000",
					Status:     model.RunSucceeded,
					Runner:     "Agent A <a@example.com>",
					StartedAt:  cCreated,
					FinishedAt: cUpdated,
					Results: []model.RunbookStepResult{
						{StepID: "1111111aaaabbbbccccddddeeeeffff0", Status: model.StepDone, Actor: "Agent A <a@example.com>", TS: cStepA},
						{StepID: "2222222aaaabbbbccccddddeeeeffff0", Status: model.StepSkipped, Actor: "Agent A <a@example.com>", TS: cStepB},
					},
				},
				{
					ID:        "4444444aaaabbbbccccddddeeeeffff0",
					Status:    model.RunRunning,
					Runner:    "Agent B <b@example.com>",
					StartedAt: cVerified,
				},
			},
			Labels:    []string{"deploy", "ops"},
			Author:    "Agent A <a@example.com>",
			CreatedAt: cCreated,
			UpdatedAt: cUpdated,
			Head:      "ffff0000ffff0000ffff0000ffff0000ffff0000",
		}},
		{"runbook_minimal", model.Runbook{
			ID:     "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1",
			Title:  "Rollback",
			Status: model.RunbookActive,
			Author: "A <a@x>",
		}},
		{"runbook_archived", model.Runbook{
			ID:          "c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2",
			Title:       "Retired procedure",
			Description: "No longer used.",
			Status:      model.RunbookArchived,
			Steps: []model.RunbookStep{
				{ID: "5555555aaaabbbbccccddddeeeeffff0", Text: "Do the thing", Command: "echo done", Position: "a"},
			},
			Runs: []model.RunbookRun{
				{
					ID:         "6666666aaaabbbbccccddddeeeeffff0",
					Status:     model.RunSucceeded,
					Runner:     "A <a@x>",
					StartedAt:  cCreated,
					FinishedAt: cUpdated,
					Results: []model.RunbookStepResult{
						{StepID: "5555555aaaabbbbccccddddeeeeffff0", Status: model.StepDone, Actor: "A <a@x>", TS: cStepA},
					},
				},
			},
			Labels:     []string{"legacy"},
			Author:     "A <a@x>",
			CreatedAt:  cCreated,
			UpdatedAt:  cUpdated,
			ArchivedAt: cClosed,
			Head:       "eeee0000eeee0000eeee0000eeee0000eeee0000",
		}},
		{"runbook_run_matrix", model.Runbook{
			ID:          "d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3",
			Title:       "Run status matrix",
			Description: "Exercises every run and step-result status.",
			Status:      model.RunbookActive,
			Steps: []model.RunbookStep{
				{ID: "aaaaaaa1111bbbbccccddddeeeeffff0", Text: "Build", Command: "go build ./...", Position: "a"},
				{ID: "bbbbbbb2222bbbbccccddddeeeeffff0", Text: "Deploy", Position: "b"},
			},
			Runs: []model.RunbookRun{
				{
					ID:         "run1succeededaabbccddeeeeffff0",
					Task:       "ffff0000ffff0000ffff0000ffff0000ffff0000",
					Status:     model.RunSucceeded,
					Runner:     "Agent A <a@example.com>",
					StartedAt:  cCreated,
					FinishedAt: cUpdated,
					Results: []model.RunbookStepResult{
						{StepID: "aaaaaaa1111bbbbccccddddeeeeffff0", Status: model.StepDone, Actor: "Agent A <a@example.com>", TS: cStepA},
						{StepID: "bbbbbbb2222bbbbccccddddeeeeffff0", Status: model.StepSkipped, Actor: "Agent A <a@example.com>", TS: cStepB},
					},
				},
				{
					ID:         "run2failedaabbccddeeeeffff00000",
					Status:     model.RunFailed,
					Runner:     "Agent B <b@example.com>",
					StartedAt:  cCreated,
					FinishedAt: cUpdated,
					Results: []model.RunbookStepResult{
						{StepID: "aaaaaaa1111bbbbccccddddeeeeffff0", Status: model.StepFailed, Actor: "Agent B <b@example.com>", TS: cStepA},
					},
				},
				{
					ID:        "run3runningaabbccddeeeeffff0000",
					Status:    model.RunRunning,
					Runner:    "Agent C <c@example.com>",
					StartedAt: cVerified,
				},
				{
					ID:         "run4abandonedaabbccddeeeeffff00",
					Status:     model.RunAbandoned,
					Runner:     "Agent D <d@example.com>",
					StartedAt:  cCreated,
					FinishedAt: cUpdated,
				},
			},
			Labels:    []string{"ci"},
			Author:    "Agent A <a@example.com>",
			CreatedAt: cCreated,
			UpdatedAt: cUpdated,
			Head:      "dddd0000dddd0000dddd0000dddd0000dddd0000",
		}},
		{"runbook_short_ids", model.Runbook{
			ID:        "e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4",
			Title:     "Foreign short ids",
			Status:    model.RunbookActive,
			CreatedAt: cCreated,
			UpdatedAt: cCreated,
			Steps:     []model.RunbookStep{{ID: "s1", Text: "build", Position: "a"}},
			Runs: []model.RunbookRun{{
				ID:        "r12",
				Status:    model.RunRunning,
				Runner:    "Agent B <b@example.com>",
				StartedAt: cVerified,
			}},
		}},
	}
}

// renderSnapshot dispatches to the concrete render function for snap's kind.
func renderSnapshot(snap model.Snapshot) []byte {
	switch v := snap.(type) {
	case model.Note:
		return fusefs.RenderNote(v)
	case model.Doc:
		return fusefs.RenderDoc(v)
	case model.Log:
		return fusefs.RenderLog(v)
	case model.Task:
		return fusefs.RenderTask(v)
	case model.Sprint:
		return fusefs.RenderSprint(v)
	case model.Project:
		return fusefs.RenderProject(v)
	case model.Runbook:
		return fusefs.RenderRunbook(v)
	}
	panic("golden corpus: unknown snapshot kind")
}

// goldenExt is the on-disk extension for snap's render format.
func goldenExt(snap model.Snapshot) string {
	switch snap.(type) {
	case model.Task, model.Sprint, model.Project:
		return ".json"
	default:
		return ".md"
	}
}

func goldenPath(e corpusEntry) string {
	return filepath.Join("testdata", "golden", e.name+goldenExt(e.snap))
}

// diffSnapshot parses edited and diffs it against snap, returning the reproduced
// ops. It reports writable=false for the read-only runbook kind.
func diffSnapshot(t *testing.T, snap model.Snapshot, edited []byte) (ops []model.Op, writable bool) {
	t.Helper()
	switch v := snap.(type) {
	case model.Note:
		p, err := fusefs.ParseNote(edited)
		if err != nil {
			t.Fatalf("ParseNote: %v", err)
		}
		ops, err = fusefs.DiffNote(v, p)
		if err != nil {
			t.Fatalf("DiffNote: %v", err)
		}
		return ops, true
	case model.Doc:
		p, err := fusefs.ParseDoc(edited)
		if err != nil {
			t.Fatalf("ParseDoc: %v", err)
		}
		ops, err = fusefs.DiffDoc(v, p)
		if err != nil {
			t.Fatalf("DiffDoc: %v", err)
		}
		return ops, true
	case model.Log:
		p, err := fusefs.ParseLog(edited)
		if err != nil {
			t.Fatalf("ParseLog: %v", err)
		}
		ops, err = fusefs.DiffLog(v, p)
		if err != nil {
			t.Fatalf("DiffLog: %v", err)
		}
		return ops, true
	case model.Task:
		p, err := fusefs.ParseTask(edited)
		if err != nil {
			t.Fatalf("ParseTask: %v", err)
		}
		ops, err = fusefs.DiffTask(v, p)
		if err != nil {
			t.Fatalf("DiffTask: %v", err)
		}
		return ops, true
	case model.Sprint:
		p, err := fusefs.ParseSprint(edited)
		if err != nil {
			t.Fatalf("ParseSprint: %v", err)
		}
		ops, err = fusefs.DiffSprint(v, p)
		if err != nil {
			t.Fatalf("DiffSprint: %v", err)
		}
		return ops, true
	case model.Project:
		p, err := fusefs.ParseProject(edited)
		if err != nil {
			t.Fatalf("ParseProject: %v", err)
		}
		ops, err = fusefs.DiffProject(v, p)
		if err != nil {
			t.Fatalf("DiffProject: %v", err)
		}
		return ops, true
	case model.Runbook:
		return nil, false
	}
	panic("golden corpus: unknown snapshot kind")
}

// TestGoldenRender pins every fixture's rendered bytes against its committed
// golden file. Run with -update to regenerate the corpus from current code.
func TestGoldenRender(t *testing.T) {
	for _, e := range corpus() {
		t.Run(e.name, func(t *testing.T) {
			got := renderSnapshot(e.snap)
			path := goldenPath(e)
			if *updateGolden {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("mkdir golden dir: %v", err)
				}
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden (regenerate with -update): %v", err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("render mismatch for %s (regenerate with -update):\n got %q\nwant %q", path, got, want)
			}
		})
	}
}

// TestGoldenRoundTrip proves that for every writable kind the committed golden
// bytes parse and diff back to the source snapshot with no ops — the round-trip
// property render->parse->diff == identity. The read-only runbook has no parse
// path and is skipped.
func TestGoldenRoundTrip(t *testing.T) {
	for _, e := range corpus() {
		if _, ok := e.snap.(model.Runbook); ok {
			continue
		}
		t.Run(e.name, func(t *testing.T) {
			want, err := os.ReadFile(goldenPath(e))
			if err != nil {
				t.Fatalf("read golden (regenerate with -update): %v", err)
			}
			ops, writable := diffSnapshot(t, e.snap, want)
			if !writable {
				t.Fatalf("kind reported read-only but was not skipped")
			}
			if len(ops) != 0 {
				t.Errorf("round trip produced ops %#v, want none", ops)
			}
		})
	}
}

// goldenEdit is one byte-level mutation of a rendered golden: old must occur
// exactly once and is replaced by repl, then parse+diff must reproduce want
// (after normalizeOps blanks server-assigned nonces).
type goldenEdit struct {
	name string
	old  string
	repl string
	want []model.Op
}

// goldenEdits maps a writable fixture to the edits exercised against its golden
// bytes. Each entry pins the exact op sequence a targeted edit reproduces.
var goldenEdits = map[string][]goldenEdit{
	"note_rich": {
		{
			"title", "title: Deploy notes", "title: Renamed notes",
			[]model.Op{model.SetTitle{Title: "Renamed notes"}},
		},
		{
			"body", "Long-form analysis.", "Rewritten analysis.",
			[]model.Op{model.SetBody{Body: "Rewritten analysis.\n\nWith a second paragraph.\n"}},
		},
		{
			"add tag", "tags: [bug, parser]", "tags: [bug, parser, urgent]",
			[]model.Op{model.AddTag{Tag: "urgent"}},
		},
		{
			"add commit anchor", "commits: [abc1234, def5678]", "commits: [abc1234, def5678, 0099aab]",
			[]model.Op{model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorCommit, Value: "0099aab"}}},
		},
		{
			"clear dir anchors", "dirs: [internal/auth]", "dirs: []",
			[]model.Op{model.RemoveAnchor{Anchor: model.Anchor{Kind: model.AnchorDir, Value: "internal/auth"}}},
		},
	},
	"doc_rich": {
		{
			"when", "when: before touching the auth flow", "when: after the migration",
			[]model.Op{model.SetWhen{When: "after the migration"}},
		},
	},
	"log_rich": {
		{
			"append entry", "still entry two\n",
			"still entry two\n<!-- cc-notes:entry author=\"ci\" ts=\"2025-01-01T00:00:00Z\" -->\nnew rollout note\n",
			[]model.Op{model.AppendEntry{Text: "new rollout note\n"}},
		},
	},
	"log_model": {
		{
			"append entry with model", "no model on this one\n",
			"no model on this one\n<!-- cc-notes:entry author=\"ci\" ts=\"2025-01-01T00:00:00Z\" model=\"claude-fable-5\" -->\nanother complaint\n",
			[]model.Op{model.AppendEntry{Text: "another complaint\n", Model: "claude-fable-5"}},
		},
	},
	"task_rich": {
		{
			"flip status", `"status": "in_progress"`, `"status": "done"`,
			[]model.Op{model.SetStatus{Status: model.StatusDone}},
		},
		{
			"set priority", `"priority": 1`, `"priority": 3`,
			[]model.Op{model.SetPriority{Priority: 3}},
		},
		{
			"add label", "    \"render\"\n  ],", "    \"render\",\n    \"urgent\"\n  ],",
			[]model.Op{model.AddLabel{Label: "urgent"}},
		},
		{
			"edit criterion text", `"text": "Tests pass"`, `"text": "Tests still pass"`,
			[]model.Op{model.SetCriterionText{ID: "bbbb2222bbbb2222bbbb2222bbbb2222", Text: "Tests still pass"}},
		},
		{
			"flip criterion status", `"status": "pending"`, `"status": "met"`,
			[]model.Op{model.SetCriterionStatus{ID: "bbbb2222bbbb2222bbbb2222bbbb2222", Status: model.CriterionMet}},
		},
		{
			"add criterion",
			"      \"status\": \"pending\"\n    }\n  ],",
			"      \"status\": \"pending\"\n    },\n    {\n      \"id\": \"\",\n      \"text\": \"New criterion\",\n      \"script\": \"\",\n      \"status\": \"pending\"\n    }\n  ],",
			[]model.Op{model.AddCriterion{ID: normalizedNonce, Text: "New criterion", Script: ""}},
		},
	},
	"sprint_rich": {
		{
			"flip status", `"status": "active"`, `"status": "completed"`,
			[]model.Op{model.SetSprintStatus{Status: model.SprintCompleted}},
		},
		{
			"clear end date", `"end_date": "2025-12-19T02:54:56Z"`, `"end_date": null`,
			[]model.Op{model.SetEndDate{Date: 0}},
		},
	},
	"project_rich": {
		{
			"flip status", `"status": "active"`, `"status": "archived"`,
			[]model.Op{model.SetProjectStatus{Status: model.ProjectArchived}},
		},
		{
			"title", `"title": "Platform v2"`, `"title": "Platform v3"`,
			[]model.Op{model.SetTitle{Title: "Platform v3"}},
		},
	},
}

// normalizedNonce stands in for the random id AddCriterion assigns a new
// criterion; normalizeOps rewrites the live nonce to it before comparison.
const normalizedNonce = "NONCE"

// normalizeOps blanks server-assigned nonces (currently only AddCriterion.ID)
// so an edit's op sequence compares against a fixed expectation.
func normalizeOps(ops []model.Op) []model.Op {
	out := make([]model.Op, len(ops))
	for i, op := range ops {
		if c, ok := op.(model.AddCriterion); ok {
			c.ID = normalizedNonce
			out[i] = c
			continue
		}
		out[i] = op
	}
	return out
}

func fixtureByName(t *testing.T, name string) corpusEntry {
	t.Helper()
	for _, e := range corpus() {
		if e.name == name {
			return e
		}
	}
	t.Fatalf("no fixture named %q", name)
	return corpusEntry{}
}

// TestGoldenEdits applies a targeted byte edit to each writable fixture's golden
// and asserts the exact op sequence parse+diff reproduces, proving the
// filesystem-edit contract field by field.
func TestGoldenEdits(t *testing.T) {
	for name, edits := range goldenEdits {
		e := fixtureByName(t, name)
		golden, err := os.ReadFile(goldenPath(e))
		if err != nil {
			t.Fatalf("read golden (regenerate with -update): %v", err)
		}
		for _, ed := range edits {
			t.Run(name+"/"+ed.name, func(t *testing.T) {
				if n := strings.Count(string(golden), ed.old); n != 1 {
					t.Fatalf("anchor %q occurs %d times in %s, want exactly 1", ed.old, n, e.name)
				}
				edited := []byte(strings.Replace(string(golden), ed.old, ed.repl, 1))
				ops, writable := diffSnapshot(t, e.snap, edited)
				if !writable {
					t.Fatalf("edit targeted a read-only kind")
				}
				got := normalizeOps(ops)
				if !reflect.DeepEqual(got, ed.want) {
					t.Errorf("ops %#v, want %#v", got, ed.want)
				}
			})
		}
	}
}
