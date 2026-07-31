package cli

import (
	"testing"
)

// TestSearchFindsTaskByDescription pins tasks as members of the top-level search
// corpus. The query phrase lives only in the task's description — no title, no
// label — so the hit proves the body reaches the ranker, and the kind tag proves
// it merges alongside the other kinds rather than shadowing one.
func TestSearchFindsTaskByDescription(t *testing.T) {
	dir := spInitRepo(t)
	taskID := spID(t, spMust(t, dir, "task", "add", "Tighten the client",
		"--body", "the retry backoff caps at 30s",
		"--no-validation-criteria", "--json"))
	noteID := spID(t, spMust(t, dir, "note", "add", "retry backoff rationale", "--body", "why 30s", "--json"))

	hits := spJSON[[]searchDTO](t, spMust(t, dir, "search", "caps at 30s", "--json"))
	if len(hits) != 1 {
		t.Fatalf("search = %+v, want only the task whose description carries the phrase", hits)
	}
	hit := hits[0]
	if hit.Kind != "task" || hit.Task == nil || hit.Task.ID != taskID {
		t.Fatalf("hit = %+v, want task %s", hit, taskID)
	}
	if hit.Note != nil || hit.Doc != nil || hit.Log != nil || hit.Runbook != nil || hit.Investigation != nil || hit.Plan != nil {
		t.Errorf("task hit populates a foreign entity field: %+v", hit)
	}
	if got, want := spMust(t, dir, "search", "caps at 30s"), "task\t"+taskID[:7]+"\topen\tP2\t-\tTighten the client\n"; got != want {
		t.Errorf("lean search = %q, want %q", got, want)
	}

	// A query matching both kinds merges them, title tier first.
	both := spJSON[[]searchDTO](t, spMust(t, dir, "search", "retry backoff", "--json"))
	if len(both) != 2 {
		t.Fatalf("search = %+v, want the note and the task", both)
	}
	if both[0].Kind != "note" || both[0].Note == nil || both[0].Note.ID != noteID {
		t.Errorf("hit[0] = %+v, want the title-tier note %s", both[0], noteID)
	}
	if both[1].Kind != "task" || both[1].Task == nil || both[1].Task.ID != taskID {
		t.Errorf("hit[1] = %+v, want the body-tier task %s", both[1], taskID)
	}
}

// TestSearchFindsDocByWhenTrigger pins the When trigger as searchable text: the
// field a reader selects a doc on was invisible to the ranker.
func TestSearchFindsDocByWhenTrigger(t *testing.T) {
	dir := spInitRepo(t)
	docID := spID(t, spMust(t, dir, "doc", "add", "Token refresh loop",
		"--body", "how the gateway verifies",
		"--when", "resuming the auth cutover", "--json"))

	hits := spJSON[[]searchDTO](t, spMust(t, dir, "search", "auth cutover", "--json"))
	if len(hits) != 1 || hits[0].Kind != "doc" || hits[0].Doc == nil || hits[0].Doc.ID != docID {
		t.Fatalf("search = %+v, want the doc %s matched on its when trigger", hits, docID)
	}
}
