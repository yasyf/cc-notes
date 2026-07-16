package cli_test

import (
	"encoding/json"
	"fmt"
	"regexp"
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
	Session string           `json:"session"`
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

func unmarshalHistory(t *testing.T, raw string) []histEntryJSON {
	t.Helper()
	var entries []histEntryJSON
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		t.Fatalf("unmarshal history: %v\n%s", err, raw)
	}
	return entries
}

// firstChange returns the field's delta from the newest entry that carries it,
// scanning the newest-first trail.
func firstChange(entries []histEntryJSON, field string) (histChangeJSON, bool) {
	for _, e := range entries {
		if c, ok := changeByField(e, field); ok {
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

// TestHistorySession checks that history renders the short writing session in
// text, preserves the full id in JSON, and omits both surfaces when unknown.
func TestHistorySession(t *testing.T) {
	const sessionID = "0b5c9b3a-7e2f-4c1d-9a8b-2f3e4d5c6b7a"

	t.Run("stamped", func(t *testing.T) {
		dir := initRepo(t)
		t.Setenv("CC_NOTES_SESSION_ID", sessionID)
		id := histID(t, mustRun(t, dir, "note", "add", "session note", "--json"))
		mustRun(t, dir, "note", "edit", id, "--title", "session note edited")

		out := mustRun(t, dir, "history", id)
		if !strings.Contains(out, "session:0b5c9b3a") {
			t.Errorf("history text missing short session %q:\n%s", "session:0b5c9b3a", out)
		}

		raw := mustRun(t, dir, "history", id, "--json")
		entries := unmarshalHistory(t, raw)
		if len(entries) < 2 {
			t.Fatalf("history entries = %d, want at least 2 (add + edit)", len(entries))
		}
		for i, entry := range entries {
			if entry.Session != sessionID {
				t.Errorf("history entry %d session = %q, want %q", i, entry.Session, sessionID)
			}
		}
	})

	t.Run("unknown", func(t *testing.T) {
		dir := initRepo(t)
		id := histID(t, mustRun(t, dir, "note", "add", "no session note", "--json"))
		mustRun(t, dir, "note", "edit", id, "--title", "no session note edited")

		out := mustRun(t, dir, "history", id)
		if strings.Contains(out, "session:") {
			t.Errorf("history text contains session marker with no session env:\n%s", out)
		}
		raw := mustRun(t, dir, "history", id, "--json")
		if strings.Contains(raw, `"session"`) {
			t.Errorf("history JSON contains session key with no session env:\n%s", raw)
		}
	})
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

// TestHistoryElementFormats pins the exact human rendering of every set-element
// format the CLI emits: log entries, task comments (with %q quote-escaping),
// task criteria and their status transition, note anchors, and the
// deterministic sorted order of a simple-set field. A changed format verb in
// renderChangeLines/formatTrailElement breaks a case here.
func TestHistoryElementFormats(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, dir string) string // returns the entity id to trail
		want   []string
		absent []string
	}{
		{
			name: "log entry",
			setup: func(t *testing.T, dir string) string {
				id := histID(t, mustRun(t, dir, "log", "add", "Rollout", "--json"))
				mustRun(t, dir, "log", "append", id, "flipped to 5%")
				return id
			},
			want: []string{`entries: +entry by ` + actorA + `: "flipped to 5%"`},
		},
		{
			name: "task comment quotes the body",
			setup: func(t *testing.T, dir string) string {
				id := histID(t, mustRun(t, dir, "task", "add", "ship", "--no-validation-criteria", "--json"))
				mustRun(t, dir, "task", "comment", id, `he said "ship it"`)
				return id
			},
			// %q escapes the embedded quotes; %s would drop the surrounding quotes.
			want: []string{`comments: +comment by ` + actorA + `: "he said \"ship it\""`},
		},
		{
			name: "task criterion and its status transition",
			setup: func(t *testing.T, dir string) string {
				id := histID(t, mustRun(t, dir, "task", "add", "build", "--no-validation-criteria", "--json"))
				task := mustJSON[taskJSON](t, mustRun(t, dir, "task", "criterion", "add", id, "tests pass", "--json"))
				mustRun(t, dir, "task", "criterion", "met", id, task.Criteria[0].ID)
				return id
			},
			// The add edit renders the pending criterion; the met edit replaces the
			// pending element with a met one (identity-keyed set diff).
			want: []string{
				`criteria: +"tests pass" [pending]`,
				`criteria: +"tests pass" [met]`,
				`criteria: -"tests pass" [pending]`,
			},
		},
		{
			name: "note anchor",
			setup: func(t *testing.T, dir string) string {
				commitFile(t, dir, "scripts/vm/setup.sh", "echo hi\n")
				return histID(t, mustRun(t, dir, "note", "add", "anchored", "--dir", "scripts/vm", "--json"))
			},
			want: []string{"anchors: +dir:scripts/vm"},
		},
		{
			name: "simple set renders sorted",
			setup: func(t *testing.T, dir string) string {
				id := histID(t, mustRun(t, dir, "task", "add", "sortme", "--no-validation-criteria", "--json"))
				mustRun(t, dir, "task", "edit", id, "--add-label", "zebra", "--add-label", "alpha")
				return id
			},
			// formatTrailSet sorts the formatted strings, so input order never leaks.
			want:   []string{"labels: +alpha +zebra"},
			absent: []string{"labels: +zebra +alpha"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := initRepo(t)
			id := tc.setup(t, dir)
			out := mustRun(t, dir, "history", id)
			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("history missing %q\n--- got ---\n%s", want, out)
				}
			}
			for _, absent := range tc.absent {
				if strings.Contains(out, absent) {
					t.Errorf("history contains unexpected %q\n--- got ---\n%s", absent, out)
				}
			}
		})
	}
}

// TestHistoryAttachmentJSONFallback pins the compact-JSON rendering an
// attachment element falls back to (no dedicated formatTrailElement case): a
// sorted-key object with the name, oid, and integer size.
func TestHistoryAttachmentJSONFallback(t *testing.T) {
	dir := initRepo(t)
	content := []byte("attachment payload bytes")
	path, oid := writeAttachable(t, "report.txt", content)
	id := histID(t, mustRun(t, dir, "note", "add", "with file", "--attach", path, "--json"))

	out := mustRun(t, dir, "history", id)
	want := fmt.Sprintf(`attachments: +{"name":"report.txt","oid":%q,"size":%d}`, oid, len(content))
	if !strings.Contains(out, want) {
		t.Errorf("history missing attachment fallback %q\n--- got ---\n%s", want, out)
	}
}

// TestHistoryVerifiedAtRFC3339 pins that a time-valued scalar renders as
// RFC3339 UTC, not the raw unix seconds the snapshot stores. A note is
// born-verified, so its second commit sets verified_at.
func TestHistoryVerifiedAtRFC3339(t *testing.T) {
	dir := initRepo(t)
	id := histID(t, mustRun(t, dir, "note", "add", "n", "--json"))

	out := mustRun(t, dir, "history", id)
	rfc := regexp.MustCompile(`verified_at: \d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z`)
	if !rfc.MatchString(out) {
		t.Errorf("verified_at not rendered as RFC3339 UTC\n--- got ---\n%s", out)
	}
	if raw := regexp.MustCompile(`verified_at: \d+(\s|$)`); raw.MatchString(out) {
		t.Errorf("verified_at rendered as a raw unix int, want RFC3339\n--- got ---\n%s", out)
	}
}

// TestHistoryElementJSON pins the --json DTO's changes[].added / .removed
// strings for the set-element cases the text renderer shares them with: log
// entries and task criteria, including the identity-keyed pending→met swap.
func TestHistoryElementJSON(t *testing.T) {
	dir := initRepo(t)

	logID := histID(t, mustRun(t, dir, "log", "add", "Rollout", "--json"))
	mustRun(t, dir, "log", "append", logID, "flipped to 5%")
	entries := unmarshalHistory(t, mustRun(t, dir, "history", logID, "--json"))
	ch, ok := firstChange(entries, "entries")
	if !ok {
		t.Fatalf("no entries change in log history: %+v", entries)
	}
	wantEntry := `entry by ` + actorA + `: "flipped to 5%"`
	if len(ch.Added) != 1 || ch.Added[0] != wantEntry {
		t.Errorf("entries added = %#v, want [%q]", ch.Added, wantEntry)
	}

	taskID := histID(t, mustRun(t, dir, "task", "add", "build", "--no-validation-criteria", "--json"))
	task := mustJSON[taskJSON](t, mustRun(t, dir, "task", "criterion", "add", taskID, "tests pass", "--json"))
	mustRun(t, dir, "task", "criterion", "met", taskID, task.Criteria[0].ID)
	tEntries := unmarshalHistory(t, mustRun(t, dir, "history", taskID, "--json"))

	var addOnly, transition *histChangeJSON
	for i := range tEntries {
		if c, ok := changeByField(tEntries[i], "criteria"); ok {
			cc := c
			if len(cc.Removed) == 0 {
				addOnly = &cc
			} else {
				transition = &cc
			}
		}
	}
	if addOnly == nil || len(addOnly.Added) != 1 || addOnly.Added[0] != `"tests pass" [pending]` {
		t.Errorf("criterion add change = %#v, want added [\"tests pass\" [pending]]", addOnly)
	}
	if transition == nil {
		t.Fatalf("no criteria status transition in task history: %+v", tEntries)
		return
	}
	if len(transition.Added) != 1 || transition.Added[0] != `"tests pass" [met]` {
		t.Errorf("transition added = %#v, want [\"tests pass\" [met]]", transition.Added)
	}
	if len(transition.Removed) != 1 || transition.Removed[0] != `"tests pass" [pending]` {
		t.Errorf("transition removed = %#v, want [\"tests pass\" [pending]]", transition.Removed)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
