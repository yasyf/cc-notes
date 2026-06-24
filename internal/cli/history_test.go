package cli_test

import (
	"encoding/json"
	"strings"
	"testing"
)

type histChangeJSON struct {
	Field   string   `json:"field"`
	From    *string  `json:"from"`
	To      *string  `json:"to"`
	Added   []string `json:"added"`
	Removed []string `json:"removed"`
}

type histEntryJSON struct {
	SHA     string           `json:"sha"`
	Author  string           `json:"author"`
	Kind    string           `json:"kind"`
	Covers  int              `json:"covers"`
	Changes []histChangeJSON `json:"changes"`
}

func histID(t *testing.T, raw string) string {
	t.Helper()
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("unmarshal id %q: %v", raw, err)
	}
	return v.ID
}

func changeByField(e histEntryJSON, field string) (histChangeJSON, bool) {
	for _, c := range e.Changes {
		if c.Field == field {
			return c, true
		}
	}
	return histChangeJSON{}, false
}

// TestHistoryTaskText drives a task through its lifecycle and checks the
// rendered audit trail: the create header, the claim's field deltas (attributed
// to the claiming actor), the multi-field edit, the close, and that the no-op
// second commit task-start emits is suppressed.
func TestHistoryTaskText(t *testing.T) {
	dir := initRepo(t)
	id := histID(t, mustRun(t, dir, "task", "add", "ship history", "--label", "backend", "--no-validation-criteria", "--json"))
	t.Setenv("CC_NOTES_ACTOR", actorB)
	mustRun(t, dir, "task", "start", id)
	t.Setenv("CC_NOTES_ACTOR", actorA)
	mustRun(t, dir, "task", "edit", id, "--priority", "3", "--add-label", "urgent")
	mustRun(t, dir, "task", "done", id)

	out := mustRun(t, dir, "history", id, "--reverse")
	wantLines := []string{
		"created task",
		"title: ship history",
		"priority: 2",
		"status: open → in_progress",
		"assignee: " + actorB,
		"labels: +urgent",
		"priority: 2 → 3",
		"status: in_progress → done",
	}
	for _, want := range wantLines {
		if !strings.Contains(out, want) {
			t.Errorf("history output missing %q\n--- got ---\n%s", want, out)
		}
	}
	// The create entry renders a numeric default as a plain set, not "0 → 2".
	if strings.Contains(out, "priority: 0 → 2") {
		t.Errorf("create entry rendered priority as an edit:\n%s", out)
	}
}

// TestHistoryTaskJSON checks the structured trail: newest-first order, the
// suppressed no-op commit (so exactly four entries), and the create/edit/close
// deltas.
func TestHistoryTaskJSON(t *testing.T) {
	dir := initRepo(t)
	id := histID(t, mustRun(t, dir, "task", "add", "ship", "--no-validation-criteria", "--json"))
	mustRun(t, dir, "task", "start", id)
	mustRun(t, dir, "task", "edit", id, "--priority", "3")
	mustRun(t, dir, "task", "done", id)

	var entries []histEntryJSON
	raw := mustRun(t, dir, "history", id, "--json")
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatalf("unmarshal history: %v\n%s", err, raw)
	}
	if len(entries) != 4 {
		t.Fatalf("entries = %d, want 4 (create, claim, edit, done; no-op start suppressed)", len(entries))
	}
	// Default order is newest-first.
	if entries[0].Kind != "edit" || entries[len(entries)-1].Kind != "create" {
		t.Fatalf("kinds = first %q last %q, want first edit last create", entries[0].Kind, entries[len(entries)-1].Kind)
	}
	done := entries[0]
	if c, ok := changeByField(done, "status"); !ok || c.From == nil || *c.From != "in_progress" || c.To == nil || *c.To != "done" {
		t.Errorf("done entry status change = %+v, want in_progress → done", c)
	}
	create := entries[len(entries)-1]
	if create.Kind != "create" {
		t.Fatalf("last entry kind = %q, want create", create.Kind)
	}
	if c, ok := changeByField(create, "title"); !ok || c.From != nil || c.To == nil || *c.To != "ship" {
		t.Errorf("create title change = %+v, want from nil to \"ship\"", c)
	}
	if c, ok := changeByField(create, "priority"); !ok || c.From != nil || c.To == nil || *c.To != "2" {
		t.Errorf("create priority change = %+v, want from nil to \"2\"", c)
	}
}

// TestHistoryKindScoping checks that the top-level history resolves any kind,
// the noun-scoped history resolves only its kind, and a wrong-kind id fails.
func TestHistoryKindScoping(t *testing.T) {
	dir := initRepo(t)
	noteID := histID(t, mustRun(t, dir, "note", "add", "a note", "--json"))
	taskID := histID(t, mustRun(t, dir, "task", "add", "a task", "--no-validation-criteria", "--json"))

	top := mustRun(t, dir, "history", noteID)
	scoped := mustRun(t, dir, "note", "history", noteID)
	if top != scoped {
		t.Errorf("top-level and note-scoped history differ:\n top:\n%s\n scoped:\n%s", top, scoped)
	}
	if !strings.Contains(scoped, "created note") {
		t.Errorf("note history missing create header:\n%s", scoped)
	}

	if _, _, err := runCLI(t, dir, "note", "history", taskID); err == nil {
		t.Errorf("note history of a task id: error = nil, want not-found")
	}
	if _, _, err := runCLI(t, dir, "task", "history", noteID); err == nil {
		t.Errorf("task history of a note id: error = nil, want not-found")
	}
}

// TestHistoryReverseAndLimit checks ordering and truncation.
func TestHistoryReverseAndLimit(t *testing.T) {
	dir := initRepo(t)
	id := histID(t, mustRun(t, dir, "note", "add", "n", "--json"))
	mustRun(t, dir, "note", "edit", id, "--title", "n2")
	mustRun(t, dir, "note", "edit", id, "--body", "b3")

	newestFirst := mustRun(t, dir, "history", id)
	chronological := mustRun(t, dir, "history", id, "--reverse")
	if firstLine(newestFirst) == firstLine(chronological) {
		t.Errorf("default and --reverse share a first line: %q", firstLine(newestFirst))
	}
	if !strings.Contains(firstLine(chronological), "created note") {
		t.Errorf("--reverse should lead with the create entry, got %q", firstLine(chronological))
	}

	limited := mustRun(t, dir, "history", id, "--limit", "1")
	if strings.Contains(limited, "created note") {
		t.Errorf("--limit 1 should show only the newest entry, not the create:\n%s", limited)
	}
}

// TestHistoryCheckpointMarker compacts an entity and confirms the checkpoint
// commit renders as a marker, not a phantom edit, while the real edits remain.
func TestHistoryCheckpointMarker(t *testing.T) {
	dir := initRepo(t)
	id := histID(t, mustRun(t, dir, "note", "add", "n", "--json"))
	mustRun(t, dir, "note", "edit", id, "--title", "n2")
	mustRun(t, dir, "compact", id)

	out := mustRun(t, dir, "history", id)
	if !strings.Contains(out, "compacted (covers") {
		t.Errorf("history missing checkpoint marker:\n%s", out)
	}
	if !strings.Contains(out, "title: n → n2") {
		t.Errorf("history lost the real edit after compaction:\n%s", out)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
