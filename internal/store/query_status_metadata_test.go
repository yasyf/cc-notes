package store

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func TestStatusMetadataFreshnessInputs(t *testing.T) {
	anchors := []model.Anchor{{Kind: model.AnchorPath, Value: "src/file.go"}}
	witness := []model.AnchorWitness{{Anchor: anchors[0], OID: "witness-oid"}}
	targets := []model.EntityID{"replacement"}
	for _, snapshot := range []model.Snapshot{
		model.Note{ID: "note", Anchors: anchors, Witness: witness, VerifiedAt: 13, StaleAt: 17, SupersededBy: targets},
		model.Doc{ID: "doc", Anchors: anchors, Witness: witness, VerifiedAt: 13, StaleAt: 17, SupersededBy: targets},
		model.Answer{ID: "answer", Anchors: anchors, Witness: witness, VerifiedAt: 13, StaleAt: 17, SupersededBy: targets},
	} {
		t.Run(string(snapshot.Meta().Kind), func(t *testing.T) {
			row := projectQueryMetadata("binding", "tip", snapshot)
			if !slices.Equal(row.Anchors, anchors) || !slices.Equal(row.Witness, witness) || !slices.Equal(row.SupersededBy, targets) || row.VerifiedAt != 13 || row.StaleAt != 17 {
				t.Fatalf("freshness inputs = %#v", row)
			}
			data, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			var restored queryMetadata
			if err := json.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(row, restored) {
				t.Fatalf("freshness round trip = %#v, want %#v", restored, row)
			}
		})
	}
}

func TestStatusMetadataTaskInputs(t *testing.T) {
	snapshot := model.Task{
		ID: "task", Branch: "branch", Status: model.StatusInProgress,
		Priority: 3, Assignee: "assignee", BlockedBy: []model.EntityID{"done", "missing"},
		CreatedAt: 19, Deleted: true, SkippedOps: 5,
	}
	row := projectQueryMetadata("binding", "tip", snapshot)
	if row.TaskStatus != model.StatusInProgress || row.Priority != 3 || row.Assignee != "assignee" || row.Branch != "branch" || row.CreatedAt != 19 || !row.Deleted || row.SkippedOps != 5 || !slices.Equal(row.BlockedBy, []model.EntityID{"done", "missing"}) {
		t.Fatalf("task status inputs = %#v", row)
	}
	snapshot.BlockedBy[0] = "mutated"
	if row.BlockedBy[0] != "done" {
		t.Fatalf("metadata aliases task dependencies: %#v", row)
	}
}

func TestStatusMetadataRunSelection(t *testing.T) {
	cases := []struct {
		name string
		runs []model.RunbookRun
		want bool
	}{
		{"empty", nil, false},
		{"terminal", []model.RunbookRun{{Status: model.RunSucceeded}, {Status: model.RunFailed}, {Status: model.RunAbandoned}}, false},
		{"running_after_terminal", []model.RunbookRun{{Status: model.RunSucceeded}, {Status: model.RunRunning}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := projectQueryMetadata("binding", "tip", model.Runbook{ID: "runbook", Status: model.RunbookArchived, Runs: tc.runs})
			if row.HasRunningRun != tc.want {
				t.Fatalf("has running run = %t, want %t", row.HasRunningRun, tc.want)
			}
		})
	}
}

func TestStatusMetadataCategoryInputs(t *testing.T) {
	log := projectQueryMetadata("binding", "tip", model.Log{ID: "log", Tags: []string{"papercut"}, Entries: make([]model.LogEntry, 3)})
	if log.LogEntryCount != 3 || !slices.Equal(log.Labels, []string{"papercut"}) {
		t.Fatalf("log status inputs = %#v", log)
	}
	for _, status := range []model.InvestigationStatus{model.InvestigationOpen, model.InvestigationRootCaused, model.InvestigationFixed, model.InvestigationConfirmed, model.InvestigationExonerated, model.InvestigationAbandoned} {
		row := projectQueryMetadata("binding", "tip", model.Investigation{
			ID: "investigation", Status: status,
			Findings: []model.Finding{{Status: model.FindingOpen}, {Status: model.FindingConfirmed}, {Status: model.FindingCleared}, {Status: model.FindingOpen}},
		})
		if row.InvestigationStatus != status || row.OpenFindingCount != 2 {
			t.Fatalf("investigation status inputs = %#v", row)
		}
	}
	for _, status := range []model.PlanStatus{model.PlanDraft, model.PlanApproved, model.PlanExecuting, model.PlanDone, model.PlanAbandoned} {
		row := projectQueryMetadata("binding", "tip", model.Plan{ID: "plan", Status: status})
		if row.PlanStatus != status {
			t.Fatalf("plan status = %q, want %q", row.PlanStatus, status)
		}
	}
}

func TestStatusMetadataOwnsWitnessAndIgnoresPayload(t *testing.T) {
	snapshot := model.Doc{
		ID: "doc", Title: "title", Body: "body", When: "trigger", StaleReason: "reason",
		VerifiedAt: 13, StaleAt: 17,
		Witness: []model.AnchorWitness{{Anchor: model.Anchor{Kind: model.AnchorCommit, Value: "commit"}, OID: "oid"}},
	}
	before := projectQueryMetadata("binding", "tip", snapshot)
	snapshot.Title = strings.Repeat("title", 1<<15)
	snapshot.Body = strings.Repeat("body", 1<<18)
	snapshot.When = strings.Repeat("trigger", 1<<15)
	snapshot.StaleReason = strings.Repeat("reason", 1<<15)
	after := projectQueryMetadata("binding", "tip", snapshot)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("status metadata depends on unused payload: before=%#v after=%#v", before, after)
	}
	snapshot.Witness[0].OID = "mutated"
	if before.Witness[0].OID != "oid" {
		t.Fatalf("status metadata aliases witness: %#v", before)
	}
}
