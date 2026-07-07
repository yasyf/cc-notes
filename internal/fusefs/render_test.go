// Render/parse/diff tests: golden bytes pin the rendered formats, a
// round-trip property pins diff(state, parse(render(state))) == no ops, and
// the CLI cross-check pins RenderTask to `task show --json` byte for byte.
package fusefs_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/fusefs"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

const goldenNote = "---\n" +
	"id: a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0\n" +
	"title: \"Fix the parser: edge-cases & \\U0001F984\"\n" +
	"tags: [bug, parser]\n" +
	"commits: [abc1234, def5678]\n" +
	"paths: [internal/cli/output.go]\n" +
	"branches: [feature/login]\n" +
	"author: Agent A <a@example.com>\n" +
	"created: \"2025-12-12T02:54:56Z\"\n" +
	"updated: \"2025-12-13T02:54:56Z\"\n" +
	"verified_at: \"2025-12-14T02:54:56Z\"\n" +
	"verified_by: Agent V <v@example.com>\n" +
	"verified_commit: aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111\n" +
	"witness:\n" +
	"  - kind: path\n" +
	"    value: internal/cli/output.go\n" +
	"    oid: 1234567890abcdef1234567890abcdef12345678\n" +
	"  - kind: commit\n" +
	"    value: abc1234\n" +
	"    oid: abcd1234abcd1234abcd1234abcd1234abcd1234\n" +
	"superseded_by: [cccc1111cccc1111cccc1111cccc1111cccc1111, dddd2222dddd2222dddd2222dddd2222dddd2222]\n" +
	"---\n" +
	"Long-form analysis.\n\nWith a second paragraph.\n"

const goldenMinimalNote = "---\n" +
	"id: b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1\n" +
	"title: \"07\"\n" +
	"tags: []\n" +
	"author: A <a@x>\n" +
	"created: \"1970-01-01T00:00:00Z\"\n" +
	"updated: \"1970-01-01T00:00:00Z\"\n" +
	"---\n"

const goldenDoc = "---\n" +
	"id: a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0\n" +
	"title: \"Fix the parser: edge-cases & \\U0001F984\"\n" +
	"when: before touching the auth flow\n" +
	"tags: [bug, parser]\n" +
	"commits: [abc1234, def5678]\n" +
	"paths: [internal/cli/output.go]\n" +
	"branches: [feature/login]\n" +
	"author: Agent A <a@example.com>\n" +
	"created: \"2025-12-12T02:54:56Z\"\n" +
	"updated: \"2025-12-13T02:54:56Z\"\n" +
	"verified_at: \"2025-12-14T02:54:56Z\"\n" +
	"verified_by: Agent V <v@example.com>\n" +
	"verified_commit: aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111\n" +
	"witness:\n" +
	"  - kind: path\n" +
	"    value: internal/cli/output.go\n" +
	"    oid: 1234567890abcdef1234567890abcdef12345678\n" +
	"  - kind: commit\n" +
	"    value: abc1234\n" +
	"    oid: abcd1234abcd1234abcd1234abcd1234abcd1234\n" +
	"superseded_by: [cccc1111cccc1111cccc1111cccc1111cccc1111, dddd2222dddd2222dddd2222dddd2222dddd2222]\n" +
	"---\n" +
	"Long-form analysis.\n\nWith a second paragraph.\n"

const goldenMinimalDoc = "---\n" +
	"id: b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1\n" +
	"title: \"07\"\n" +
	"when: \"\"\n" +
	"tags: []\n" +
	"author: A <a@x>\n" +
	"created: \"1970-01-01T00:00:00Z\"\n" +
	"updated: \"1970-01-01T00:00:00Z\"\n" +
	"---\n"

const goldenTask = `{
  "id": "0123abcd4567ef890123abcd4567ef890123abcd",
  "branch": "feature/login",
  "title": "Wire the FUSE layer \u003curgently\u003e",
  "description": "Render, parse, diff.\nNo kernel needed.",
  "type": "bug",
  "status": "in_progress",
  "priority": 1,
  "assignee": "Agent A \u003ca@example.com\u003e",
  "labels": [
    "fs",
    "render"
  ],
  "blocked_by": [
    "9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa"
  ],
  "blocks": [],
  "parent": "8888bbbb8888bbbb8888bbbb8888bbbb8888bbbb",
  "comments": [
    {
      "author": "Agent B \u003cb@example.com\u003e",
      "ts": "2025-12-12T03:26:40Z",
      "body": "On it.\nETA tonight."
    }
  ],
  "commits": [
    "cafe0000cafe0000cafe0000cafe0000cafe0000",
    "feed1111feed1111feed1111feed1111feed1111"
  ],
  "lease": {
    "holder": "Agent A \u003ca@example.com\u003e",
    "heartbeat": "2025-12-13T02:54:56Z"
  },
  "created_at": "2025-12-12T02:54:56Z",
  "updated_at": "2025-12-13T02:54:56Z",
  "started_at": "2025-12-12T03:10:00Z",
  "closed_at": null,
  "sprint": "7777cccc7777cccc7777cccc7777cccc7777cccc",
  "project": "6666dddd6666dddd6666dddd6666dddd6666dddd",
  "criteria": [
    {
      "id": "aaaa1111aaaa1111aaaa1111aaaa1111",
      "text": "Compiles clean",
      "script": "go build ./...",
      "status": "met"
    },
    {
      "id": "bbbb2222bbbb2222bbbb2222bbbb2222",
      "text": "Tests pass",
      "script": "",
      "status": "pending"
    }
  ],
  "closed_forced": false
}
`

const goldenSprint = `{
  "id": "5555aaaa5555aaaa5555aaaa5555aaaa5555aaaa",
  "project": "6666dddd6666dddd6666dddd6666dddd6666dddd",
  "title": "Sprint 7 core",
  "description": "Ship the FUSE layer.\nTwo lines.",
  "status": "active",
  "start_date": "2025-12-12T02:54:56Z",
  "end_date": "2025-12-19T02:54:56Z",
  "labels": [
    "core",
    "fs"
  ],
  "commits": [
    "cafe0000cafe0000cafe0000cafe0000cafe0000"
  ],
  "comments": [
    {
      "author": "Agent B b@example.com",
      "ts": "2025-12-12T03:26:40Z",
      "body": "Kickoff."
    }
  ],
  "author": "Agent A a@example.com",
  "created_at": "2025-12-12T02:54:56Z",
  "updated_at": "2025-12-13T02:54:56Z",
  "started_at": "2025-12-12T03:10:00Z",
  "closed_at": null,
  "tasks": []
}
`

const goldenProject = `{
  "id": "6666dddd6666dddd6666dddd6666dddd6666dddd",
  "title": "Platform v2",
  "description": "Long-lived effort.\nMany sprints.",
  "status": "active",
  "labels": [
    "core",
    "platform"
  ],
  "commits": [
    "feed1111feed1111feed1111feed1111feed1111"
  ],
  "comments": [
    {
      "author": "Agent C c@example.com",
      "ts": "2025-12-12T03:26:40Z",
      "body": "Charter approved."
    }
  ],
  "author": "Agent A a@example.com",
  "created_at": "2025-12-12T02:54:56Z",
  "updated_at": "2025-12-13T02:54:56Z",
  "closed_at": null,
  "sprints": [],
  "tasks": []
}
`

func richNote() model.Note {
	return model.Note{
		ID:    "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
		Title: "Fix the parser: edge-cases & 🦄",
		Body:  "Long-form analysis.\n\nWith a second paragraph.\n",
		Tags:  []string{"bug", "parser"},
		Anchors: []model.Anchor{
			{Kind: model.AnchorCommit, Value: "abc1234"},
			{Kind: model.AnchorCommit, Value: "def5678"},
			{Kind: model.AnchorPath, Value: "internal/cli/output.go"},
			{Kind: model.AnchorBranch, Value: "feature/login"},
		},
		Author:         "Agent A <a@example.com>",
		CreatedAt:      1765508096,
		UpdatedAt:      1765594496,
		VerifiedAt:     1765680896,
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
	}
}

func richDoc() model.Doc {
	return model.Doc{
		ID:    "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
		Title: "Fix the parser: edge-cases & 🦄",
		Body:  "Long-form analysis.\n\nWith a second paragraph.\n",
		When:  "before touching the auth flow",
		Tags:  []string{"bug", "parser"},
		Anchors: []model.Anchor{
			{Kind: model.AnchorCommit, Value: "abc1234"},
			{Kind: model.AnchorCommit, Value: "def5678"},
			{Kind: model.AnchorPath, Value: "internal/cli/output.go"},
			{Kind: model.AnchorBranch, Value: "feature/login"},
		},
		Author:         "Agent A <a@example.com>",
		CreatedAt:      1765508096,
		UpdatedAt:      1765594496,
		VerifiedAt:     1765680896,
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
	}
}

func richTask() model.Task {
	return model.Task{
		ID:               "0123abcd4567ef890123abcd4567ef890123abcd",
		Branch:           "feature/login",
		Title:            "Wire the FUSE layer <urgently>",
		Description:      "Render, parse, diff.\nNo kernel needed.",
		Type:             model.TypeBug,
		Status:           model.StatusInProgress,
		Priority:         1,
		Assignee:         "Agent A <a@example.com>",
		HeartbeatAt:      1765594496,
		HeartbeatLamport: 7,
		Labels:           []string{"fs", "render"},
		BlockedBy:        []model.EntityID{"9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa"},
		Parent:           "8888bbbb8888bbbb8888bbbb8888bbbb8888bbbb",
		Comments: []model.Comment{
			{Author: "Agent B <b@example.com>", TS: 1765510000, Body: "On it.\nETA tonight."},
		},
		Commits:   []model.SHA{"cafe0000cafe0000cafe0000cafe0000cafe0000", "feed1111feed1111feed1111feed1111feed1111"},
		CreatedAt: 1765508096,
		UpdatedAt: 1765594496,
		StartedAt: 1765509000,
		Sprint:    "7777cccc7777cccc7777cccc7777cccc7777cccc",
		Project:   "6666dddd6666dddd6666dddd6666dddd6666dddd",
		Criteria: []model.Criterion{
			{ID: "aaaa1111aaaa1111aaaa1111aaaa1111", Text: "Compiles clean", Script: "go build ./...", Status: model.CriterionMet},
			{ID: "bbbb2222bbbb2222bbbb2222bbbb2222", Text: "Tests pass", Status: model.CriterionPending},
		},
		Head: "eeee0000eeee0000eeee0000eeee0000eeee0000",
	}
}

func richSprint() model.Sprint {
	return model.Sprint{
		ID:          "5555aaaa5555aaaa5555aaaa5555aaaa5555aaaa",
		Project:     "6666dddd6666dddd6666dddd6666dddd6666dddd",
		Title:       "Sprint 7 core",
		Description: "Ship the FUSE layer.\nTwo lines.",
		Status:      model.SprintActive,
		StartDate:   1765508096,
		EndDate:     1766112896,
		Labels:      []string{"core", "fs"},
		Commits:     []model.SHA{"cafe0000cafe0000cafe0000cafe0000cafe0000"},
		Comments: []model.Comment{
			{Author: "Agent B b@example.com", TS: 1765510000, Body: "Kickoff."},
		},
		Author:    "Agent A a@example.com",
		CreatedAt: 1765508096,
		UpdatedAt: 1765594496,
		StartedAt: 1765509000,
		Head:      "dddd0000dddd0000dddd0000dddd0000dddd0000",
	}
}

func richProject() model.Project {
	return model.Project{
		ID:          "6666dddd6666dddd6666dddd6666dddd6666dddd",
		Title:       "Platform v2",
		Description: "Long-lived effort.\nMany sprints.",
		Status:      model.ProjectActive,
		Labels:      []string{"core", "platform"},
		Commits:     []model.SHA{"feed1111feed1111feed1111feed1111feed1111"},
		Comments: []model.Comment{
			{Author: "Agent C c@example.com", TS: 1765510000, Body: "Charter approved."},
		},
		Author:    "Agent A a@example.com",
		CreatedAt: 1765508096,
		UpdatedAt: 1765594496,
		Head:      "cccc0000cccc0000cccc0000cccc0000cccc0000",
	}
}

func set[T any](v T) fusefs.Field[T] { return fusefs.Field[T]{Set: true, Value: v} }

func null[T any]() fusefs.Field[T] { return fusefs.Field[T]{Set: true, Null: true} }

func mustParseNote(t *testing.T, data []byte) fusefs.ParsedNote {
	t.Helper()
	p, err := fusefs.ParseNote(data)
	if err != nil {
		t.Fatalf("ParseNote(%q): %v", data, err)
	}
	return p
}

func mustParseDoc(t *testing.T, data []byte) fusefs.ParsedDoc {
	t.Helper()
	p, err := fusefs.ParseDoc(data)
	if err != nil {
		t.Fatalf("ParseDoc(%q): %v", data, err)
	}
	return p
}

func mustParseTask(t *testing.T, data []byte) fusefs.ParsedTask {
	t.Helper()
	p, err := fusefs.ParseTask(data)
	if err != nil {
		t.Fatalf("ParseTask(%q): %v", data, err)
	}
	return p
}

func mustParseSprint(t *testing.T, data []byte) fusefs.ParsedSprint {
	t.Helper()
	p, err := fusefs.ParseSprint(data)
	if err != nil {
		t.Fatalf("ParseSprint(%q): %v", data, err)
	}
	return p
}

func mustParseProject(t *testing.T, data []byte) fusefs.ParsedProject {
	t.Helper()
	p, err := fusefs.ParseProject(data)
	if err != nil {
		t.Fatalf("ParseProject(%q): %v", data, err)
	}
	return p
}

func TestRenderNoteGolden(t *testing.T) {
	if got := string(fusefs.RenderNote(richNote())); got != goldenNote {
		t.Errorf("rich note render:\n got %q\nwant %q", got, goldenNote)
	}
	minimal := model.Note{
		ID:     "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1",
		Title:  "07",
		Author: "A <a@x>",
	}
	if got := string(fusefs.RenderNote(minimal)); got != goldenMinimalNote {
		t.Errorf("minimal note render:\n got %q\nwant %q", got, goldenMinimalNote)
	}
}

func TestRenderDocGolden(t *testing.T) {
	if got := string(fusefs.RenderDoc(richDoc())); got != goldenDoc {
		t.Errorf("rich doc render:\n got %q\nwant %q", got, goldenDoc)
	}
	minimal := model.Doc{
		ID:     "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1",
		Title:  "07",
		Author: "A <a@x>",
	}
	if got := string(fusefs.RenderDoc(minimal)); got != goldenMinimalDoc {
		t.Errorf("minimal doc render:\n got %q\nwant %q", got, goldenMinimalDoc)
	}
}

func TestDiffDocWhenEdit(t *testing.T) {
	base := richDoc()
	noop, err := fusefs.DiffDoc(base, mustParseDoc(t, fusefs.RenderDoc(base)))
	if err != nil {
		t.Fatalf("DiffDoc round trip: %v", err)
	}
	if len(noop) != 0 {
		t.Errorf("unchanged doc round trip produced ops %#v, want none", noop)
	}

	p := mustParseDoc(t, fusefs.RenderDoc(base))
	p.When = set("after the migration lands")
	ops, err := fusefs.DiffDoc(base, p)
	if err != nil {
		t.Fatalf("DiffDoc: %v", err)
	}
	want := []model.Op{model.SetWhen{When: "after the migration lands"}}
	if !reflect.DeepEqual(ops, want) {
		t.Errorf("edited when produced ops %#v, want %#v", ops, want)
	}
}

func TestNewTemplateGolden(t *testing.T) {
	// A zero-value template is the empty add --checkout buffer, byte for byte.
	const (
		docTemplate  = "---\ntitle: \"\"\nwhen: \"\"\ntags: []\n---\n"
		noteTemplate = "---\ntitle: \"\"\ntags: []\n---\n"
	)
	if got := string(fusefs.NewDocTemplate("", "", nil, nil)); got != docTemplate {
		t.Errorf("NewDocTemplate zero value:\n got %q\nwant %q", got, docTemplate)
	}
	if got := string(fusefs.NewNoteTemplate("", nil, nil)); got != noteTemplate {
		t.Errorf("NewNoteTemplate zero value:\n got %q\nwant %q", got, noteTemplate)
	}
}

func TestNewDocTemplateRoundTrip(t *testing.T) {
	anchors := []model.Anchor{
		{Kind: model.AnchorCommit, Value: "abc1234"},
		{Kind: model.AnchorPath, Value: "internal/cli/output.go"},
		{Kind: model.AnchorBranch, Value: "main"},
	}
	tmpl := fusefs.NewDocTemplate("Prefilled", "before the cutover", []string{"handoff", "auth"}, anchors)
	tmpl = append(tmpl, []byte("Body goes here.\n")...)

	ops, err := fusefs.NewDoc(mustParseDoc(t, tmpl))
	if err != nil {
		t.Fatalf("NewDoc: %v", err)
	}
	create := ops[0].(model.CreateDoc)
	if len(ops) != 1 || len(create.Nonce) != 32 {
		t.Fatalf("ops %#v, want one CreateDoc with a 32-char nonce", ops)
	}
	create.Nonce = ""
	want := model.CreateDoc{
		Title: "Prefilled",
		Body:  "Body goes here.\n",
		When:  "before the cutover",
		Tags:  []string{"auth", "handoff"},
		Anchors: []model.Anchor{
			{Kind: model.AnchorCommit, Value: "abc1234"},
			{Kind: model.AnchorPath, Value: "internal/cli/output.go"},
			{Kind: model.AnchorBranch, Value: "main"},
		},
	}
	if !reflect.DeepEqual(create, want) {
		t.Errorf("create %#v, want %#v", create, want)
	}
}

func TestNewNoteTemplateRoundTrip(t *testing.T) {
	anchors := []model.Anchor{
		{Kind: model.AnchorDir, Value: "internal/auth"},
		{Kind: model.AnchorCommit, Value: "ffff111"},
	}
	tmpl := fusefs.NewNoteTemplate("Fact", []string{"design"}, anchors)
	tmpl = append(tmpl, []byte("The captured fact.\n")...)

	ops, err := fusefs.NewNote(mustParseNote(t, tmpl))
	if err != nil {
		t.Fatalf("NewNote: %v", err)
	}
	create := ops[0].(model.CreateNote)
	create.Nonce = ""
	want := model.CreateNote{
		Title: "Fact",
		Body:  "The captured fact.\n",
		Tags:  []string{"design"},
		Anchors: []model.Anchor{
			{Kind: model.AnchorCommit, Value: "ffff111"},
			{Kind: model.AnchorDir, Value: "internal/auth"},
		},
	}
	if !reflect.DeepEqual(create, want) {
		t.Errorf("create %#v, want %#v", create, want)
	}
}

func TestRenderTaskGolden(t *testing.T) {
	if got := string(fusefs.RenderTask(richTask())); got != goldenTask {
		t.Errorf("rich task render:\n got %q\nwant %q", got, goldenTask)
	}
}

func TestNoteRoundTrip(t *testing.T) {
	base := richNote()
	cases := map[string]model.Note{
		"rich": base,
		"unicode title": {
			ID: base.ID, Title: "Étude — 多言語 🎉 melody", Author: base.Author,
			CreatedAt: 100, UpdatedAt: 200,
		},
		"empty note": {ID: base.ID, Author: base.Author},
		"empty body": {
			ID: base.ID, Title: "No body", Tags: []string{"x"}, Author: base.Author,
			CreatedAt: 100, UpdatedAt: 100,
		},
		"body without trailing newline": {
			ID: base.ID, Title: "Dangling", Body: "dangling line", Author: base.Author,
		},
		"body with delimiter lines": {
			ID: base.ID, Title: "Tricky", Body: "---\nfirst\n---\nlast\n", Author: base.Author,
		},
		"title that looks like yaml": {
			ID: base.ID, Title: "07: {flow} [seq] #comment --- 'quotes'", Author: base.Author,
		},
		"long title with double  spaces": {
			ID: base.ID, Title: strings.Repeat("word ", 25) + "tail  end", Author: base.Author,
		},
		"many anchors": {
			ID: base.ID, Title: "Anchored", Author: base.Author,
			Anchors: []model.Anchor{
				{Kind: model.AnchorCommit, Value: "1111111"},
				{Kind: model.AnchorCommit, Value: "2222222"},
				{Kind: model.AnchorCommit, Value: "3333333"},
				{Kind: model.AnchorPath, Value: "a/b.go"},
				{Kind: model.AnchorPath, Value: "c d/e.go"},
				{Kind: model.AnchorDir, Value: "internal/auth"},
				{Kind: model.AnchorDir, Value: "c d/sub"},
				{Kind: model.AnchorBranch, Value: "main"},
				{Kind: model.AnchorBranch, Value: "feature/x"},
			},
		},
	}
	for name, n := range cases {
		t.Run(name, func(t *testing.T) {
			ops, err := fusefs.DiffNote(n, mustParseNote(t, fusefs.RenderNote(n)))
			if err != nil {
				t.Fatalf("DiffNote: %v", err)
			}
			if len(ops) != 0 {
				t.Errorf("round trip produced ops %#v, want none", ops)
			}
		})
	}
}

func TestTaskRoundTrip(t *testing.T) {
	base := richTask()
	cases := map[string]model.Task{
		"rich claimed": base,
		"open unassigned": {
			ID: base.ID, Branch: "main", Title: "Plain", Type: model.TypeTask,
			Status: model.StatusOpen, Priority: 2, CreatedAt: 100, UpdatedAt: 100,
		},
		"done with closure": {
			ID: base.ID, Branch: "main", Title: "Finished", Type: model.TypeEpic,
			Status: model.StatusDone, Priority: 0, Assignee: "B <b@x>",
			CreatedAt: 100, UpdatedAt: 400, StartedAt: 200, ClosedAt: 400,
		},
		"cancelled unicode": {
			ID: base.ID, Branch: "feature/login", Title: "Запуск 🚀", Type: model.TypeQuestion,
			Status: model.StatusCancelled, Priority: 3, CreatedAt: 100, UpdatedAt: 300, ClosedAt: 300,
		},
	}
	for name, task := range cases {
		t.Run(name, func(t *testing.T) {
			ops, err := fusefs.DiffTask(task, mustParseTask(t, fusefs.RenderTask(task)))
			if err != nil {
				t.Fatalf("DiffTask: %v", err)
			}
			if len(ops) != 0 {
				t.Errorf("round trip produced ops %#v, want none", ops)
			}
		})
	}
}

func TestDiffNoteEditable(t *testing.T) {
	base := richNote()
	cases := []struct {
		name   string
		mutate func(*fusefs.ParsedNote)
		want   []model.Op
	}{
		{
			"title", func(p *fusefs.ParsedNote) { p.Title = set("New title") },
			[]model.Op{model.SetTitle{Title: "New title"}},
		},
		{
			"title removed entirely is untouched", func(p *fusefs.ParsedNote) { p.Title = fusefs.Field[string]{} },
			nil,
		},
		{
			"body", func(p *fusefs.ParsedNote) { p.Body = "rewritten\n" },
			[]model.Op{model.SetBody{Body: "rewritten\n"}},
		},
		{
			"add tag", func(p *fusefs.ParsedNote) { p.Tags.Value = append(p.Tags.Value, "urgent") },
			[]model.Op{model.AddTag{Tag: "urgent"}},
		},
		{
			"remove tag", func(p *fusefs.ParsedNote) { p.Tags = set([]string{"parser"}) },
			[]model.Op{model.RemoveTag{Tag: "bug"}},
		},
		{
			"swap tags adds before removes", func(p *fusefs.ParsedNote) { p.Tags = set([]string{"bb", "aa"}) },
			[]model.Op{
				model.AddTag{Tag: "aa"},
				model.AddTag{Tag: "bb"},
				model.RemoveTag{Tag: "bug"},
				model.RemoveTag{Tag: "parser"},
			},
		},
		{
			"tags null clears all", func(p *fusefs.ParsedNote) { p.Tags = null[[]string]() },
			[]model.Op{model.RemoveTag{Tag: "bug"}, model.RemoveTag{Tag: "parser"}},
		},
		{
			"add commit anchor", func(p *fusefs.ParsedNote) { p.Commits.Value = append(p.Commits.Value, "0099aab") },
			[]model.Op{model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorCommit, Value: "0099aab"}}},
		},
		{
			"remove path anchor", func(p *fusefs.ParsedNote) { p.Paths = set([]string{}) },
			[]model.Op{model.RemoveAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "internal/cli/output.go"}}},
		},
		{
			"replace branch anchor", func(p *fusefs.ParsedNote) { p.Branches = set([]string{"main"}) },
			[]model.Op{
				model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorBranch, Value: "main"}},
				model.RemoveAnchor{Anchor: model.Anchor{Kind: model.AnchorBranch, Value: "feature/login"}},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParseNote(t, fusefs.RenderNote(base))
			tc.mutate(&p)
			ops, err := fusefs.DiffNote(base, p)
			if err != nil {
				t.Fatalf("DiffNote: %v", err)
			}
			if !reflect.DeepEqual(ops, tc.want) {
				t.Errorf("ops %#v, want %#v", ops, tc.want)
			}
		})
	}
}

func TestDiffNoteImmutable(t *testing.T) {
	base := richNote()
	cases := []struct {
		field  string
		mutate func(*fusefs.ParsedNote)
	}{
		{"id", func(p *fusefs.ParsedNote) { p.ID = set("0000000000000000000000000000000000000000") }},
		{"author", func(p *fusefs.ParsedNote) { p.Author = set("Mallory <m@example.com>") }},
		{"created", func(p *fusefs.ParsedNote) { p.Created = set("2020-01-01T00:00:00Z") }},
		{"verified_at", func(p *fusefs.ParsedNote) { p.VerifiedAt = set("2020-01-01T00:00:00Z") }},
		{"verified_by", func(p *fusefs.ParsedNote) { p.VerifiedBy = set("Mallory <m@example.com>") }},
		{"verified_commit", func(p *fusefs.ParsedNote) { p.VerifiedCommit = set("0000000000000000000000000000000000000000") }},
		{"superseded_by", func(p *fusefs.ParsedNote) {
			p.SupersededBy.Value = append(p.SupersededBy.Value, "eeee3333eeee3333eeee3333eeee3333eeee3333")
		}},
		{"superseded_by", func(p *fusefs.ParsedNote) { p.SupersededBy = set([]string{}) }},
		{"witness", func(p *fusefs.ParsedNote) { p.Witness.Value[0].OID = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef" }},
		{"witness", func(p *fusefs.ParsedNote) { p.Witness = set([]fusefs.ParsedWitness{}) }},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			p := mustParseNote(t, fusefs.RenderNote(base))
			tc.mutate(&p)
			_, err := fusefs.DiffNote(base, p)
			if !errors.Is(err, fusefs.ErrImmutableField) {
				t.Fatalf("err %v, want ErrImmutableField", err)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("err %q does not name field %q", err, tc.field)
			}
		})
	}
}

func TestDiffNoteVerifiedSurvivesRoundTrip(t *testing.T) {
	base := richNote()
	p := mustParseNote(t, fusefs.RenderNote(base))
	if got, want := p.VerifiedAt.Value, "2025-12-14T02:54:56Z"; !p.VerifiedAt.Set || got != want {
		t.Errorf("verified_at = %q (set %v), want %q set", got, p.VerifiedAt.Set, want)
	}
	if got, want := p.VerifiedBy.Value, "Agent V <v@example.com>"; !p.VerifiedBy.Set || got != want {
		t.Errorf("verified_by = %q (set %v), want %q set", got, p.VerifiedBy.Set, want)
	}
	if got, want := p.VerifiedCommit.Value, "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111"; !p.VerifiedCommit.Set || got != want {
		t.Errorf("verified_commit = %q (set %v), want %q set", got, p.VerifiedCommit.Set, want)
	}
	wantWitness := []fusefs.ParsedWitness{
		{Kind: "path", Value: "internal/cli/output.go", OID: "1234567890abcdef1234567890abcdef12345678"},
		{Kind: "commit", Value: "abc1234", OID: "abcd1234abcd1234abcd1234abcd1234abcd1234"},
	}
	if !p.Witness.Set || !reflect.DeepEqual(p.Witness.Value, wantWitness) {
		t.Errorf("witness = %#v (set %v), want %#v in stored order", p.Witness.Value, p.Witness.Set, wantWitness)
	}
	wantSuperseded := []string{
		"cccc1111cccc1111cccc1111cccc1111cccc1111",
		"dddd2222dddd2222dddd2222dddd2222dddd2222",
	}
	if !p.SupersededBy.Set || !reflect.DeepEqual(p.SupersededBy.Value, wantSuperseded) {
		t.Errorf("superseded_by = %#v (set %v), want %#v", p.SupersededBy.Value, p.SupersededBy.Set, wantSuperseded)
	}
	ops, err := fusefs.DiffNote(base, p)
	if err != nil {
		t.Fatalf("DiffNote: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("verified-note round trip produced ops %#v, want none", ops)
	}
}

func TestDiffNoteUpdatedIsInformational(t *testing.T) {
	base := richNote()
	p := mustParseNote(t, fusefs.RenderNote(base))
	p.Updated = set("1999-12-31T23:59:59Z")
	ops, err := fusefs.DiffNote(base, p)
	if err != nil {
		t.Fatalf("DiffNote: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("stale updated produced ops %#v, want none", ops)
	}
}

func TestDiffTaskEditable(t *testing.T) {
	base := richTask()
	cases := []struct {
		name   string
		mutate func(*fusefs.ParsedTask)
		want   []model.Op
	}{
		{
			"title", func(p *fusefs.ParsedTask) { p.Title = set("Retitled") },
			[]model.Op{model.SetTitle{Title: "Retitled"}},
		},
		{
			"description", func(p *fusefs.ParsedTask) { p.Description = set("New body") },
			[]model.Op{model.SetDescription{Description: "New body"}},
		},
		{
			"status", func(p *fusefs.ParsedTask) { p.Status = set("done") },
			[]model.Op{model.SetStatus{Status: model.StatusDone}},
		},
		{
			"priority", func(p *fusefs.ParsedTask) { p.Priority = set(0) },
			[]model.Op{model.SetPriority{Priority: 0}},
		},
		{
			"add label", func(p *fusefs.ParsedTask) { p.Labels.Value = append(p.Labels.Value, "alpha") },
			[]model.Op{model.AddLabel{Label: "alpha"}},
		},
		{
			"remove label", func(p *fusefs.ParsedTask) { p.Labels = set([]string{"render"}) },
			[]model.Op{model.RemoveLabel{Label: "fs"}},
		},
		{
			"labels null clears all", func(p *fusefs.ParsedTask) { p.Labels = null[[]string]() },
			[]model.Op{model.RemoveLabel{Label: "fs"}, model.RemoveLabel{Label: "render"}},
		},
		{
			"several fields in fixed order",
			func(p *fusefs.ParsedTask) {
				p.Title = set("Retitled")
				p.Status = set("done")
				p.Priority = set(3)
				p.Labels = set([]string{"fs", "zz"})
			},
			[]model.Op{
				model.SetTitle{Title: "Retitled"},
				model.SetStatus{Status: model.StatusDone},
				model.SetPriority{Priority: 3},
				model.AddLabel{Label: "zz"},
				model.RemoveLabel{Label: "render"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParseTask(t, fusefs.RenderTask(base))
			tc.mutate(&p)
			ops, err := fusefs.DiffTask(base, p)
			if err != nil {
				t.Fatalf("DiffTask: %v", err)
			}
			if !reflect.DeepEqual(ops, tc.want) {
				t.Errorf("ops %#v, want %#v", ops, tc.want)
			}
		})
	}
}

func TestDiffTaskInvalidValues(t *testing.T) {
	base := richTask()
	cases := []struct {
		name   string
		mutate func(*fusefs.ParsedTask)
	}{
		{"bogus status", func(p *fusefs.ParsedTask) { p.Status = set("paused") }},
		{"null status", func(p *fusefs.ParsedTask) { p.Status = null[string]() }},
		{"priority above range", func(p *fusefs.ParsedTask) { p.Priority = set(4) }},
		{"priority below range", func(p *fusefs.ParsedTask) { p.Priority = set(-1) }},
		{"null priority", func(p *fusefs.ParsedTask) { p.Priority = null[int]() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParseTask(t, fusefs.RenderTask(base))
			tc.mutate(&p)
			if _, err := fusefs.DiffTask(base, p); !errors.Is(err, fusefs.ErrParse) {
				t.Fatalf("err %v, want ErrParse", err)
			}
		})
	}
}

func TestDiffTaskImmutable(t *testing.T) {
	base := richTask()
	cases := []struct {
		field  string
		mutate func(*fusefs.ParsedTask)
	}{
		{"id", func(p *fusefs.ParsedTask) { p.ID = set("0000000000000000000000000000000000000000") }},
		{"branch", func(p *fusefs.ParsedTask) { p.Branch = set("main") }},
		{"type", func(p *fusefs.ParsedTask) { p.Type = set("epic") }},
		{"assignee", func(p *fusefs.ParsedTask) { p.Assignee = set("Agent B <b@example.com>") }},
		{"assignee", func(p *fusefs.ParsedTask) { p.Assignee = null[string]() }},
		{"blocked_by", func(p *fusefs.ParsedTask) { p.BlockedBy.Value = append(p.BlockedBy.Value, "1111") }},
		{"blocked_by", func(p *fusefs.ParsedTask) { p.BlockedBy = set([]string{}) }},
		{"blocks", func(p *fusefs.ParsedTask) { p.Blocks = set([]string{"1111"}) }},
		{"parent", func(p *fusefs.ParsedTask) { p.Parent = null[string]() }},
		{"comments", func(p *fusefs.ParsedTask) { p.Comments.Value[0].Body = "edited" }},
		{"comments", func(p *fusefs.ParsedTask) { p.Comments = set([]fusefs.ParsedComment{}) }},
		{"created_at", func(p *fusefs.ParsedTask) { p.CreatedAt = set("2020-01-01T00:00:00Z") }},
		{"updated_at", func(p *fusefs.ParsedTask) { p.UpdatedAt = set("2020-01-01T00:00:00Z") }},
		{"started_at", func(p *fusefs.ParsedTask) { p.StartedAt = null[string]() }},
		{"closed_at", func(p *fusefs.ParsedTask) { p.ClosedAt = set("2026-01-01T00:00:00Z") }},
		{"sprint", func(p *fusefs.ParsedTask) { p.Sprint = set("0000000000000000000000000000000000000000") }},
		{"project", func(p *fusefs.ParsedTask) { p.Project = set("0000000000000000000000000000000000000000") }},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			p := mustParseTask(t, fusefs.RenderTask(base))
			tc.mutate(&p)
			_, err := fusefs.DiffTask(base, p)
			if !errors.Is(err, fusefs.ErrImmutableField) {
				t.Fatalf("err %v, want ErrImmutableField", err)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("err %q does not name field %q", err, tc.field)
			}
		})
	}
}

const (
	critA = "aaaa1111aaaa1111aaaa1111aaaa1111"
	critB = "bbbb2222bbbb2222bbbb2222bbbb2222"
)

func TestDiffTaskCriteriaEditable(t *testing.T) {
	base := richTask()
	cases := []struct {
		name   string
		mutate func(*fusefs.ParsedTask)
		want   []model.Op
	}{
		{
			"edit text", func(p *fusefs.ParsedTask) { p.Criteria.Value[0].Text = "Builds clean" },
			[]model.Op{model.SetCriterionText{ID: critA, Text: "Builds clean"}},
		},
		{
			"edit status", func(p *fusefs.ParsedTask) { p.Criteria.Value[1].Status = "met" },
			[]model.Op{model.SetCriterionStatus{ID: critB, Status: model.CriterionMet}},
		},
		{
			"edit script", func(p *fusefs.ParsedTask) { p.Criteria.Value[1].Script = "go test ./..." },
			[]model.Op{model.SetCriterionScript{ID: critB, Script: "go test ./..."}},
		},
		{
			"text status script in order for one id",
			func(p *fusefs.ParsedTask) {
				p.Criteria.Value[0].Text = "Builds"
				p.Criteria.Value[0].Status = "failed"
				p.Criteria.Value[0].Script = "make"
			},
			[]model.Op{
				model.SetCriterionText{ID: critA, Text: "Builds"},
				model.SetCriterionStatus{ID: critA, Status: model.CriterionFailed},
				model.SetCriterionScript{ID: critA, Script: "make"},
			},
		},
		{
			"remove omitted criterion",
			func(p *fusefs.ParsedTask) { p.Criteria.Value = p.Criteria.Value[:1] },
			[]model.Op{model.RemoveCriterion{ID: critB}},
		},
		{
			"null clears all, removes sorted by id",
			func(p *fusefs.ParsedTask) { p.Criteria = null[[]fusefs.ParsedCriterion]() },
			[]model.Op{model.RemoveCriterion{ID: critA}, model.RemoveCriterion{ID: critB}},
		},
		{
			"removes precede field updates",
			func(p *fusefs.ParsedTask) {
				p.Criteria.Value[1].Text = "Tests still pass"
				p.Criteria.Value = p.Criteria.Value[1:]
			},
			[]model.Op{
				model.RemoveCriterion{ID: critA},
				model.SetCriterionText{ID: critB, Text: "Tests still pass"},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParseTask(t, fusefs.RenderTask(base))
			tc.mutate(&p)
			ops, err := fusefs.DiffTask(base, p)
			if err != nil {
				t.Fatalf("DiffTask: %v", err)
			}
			if !reflect.DeepEqual(ops, tc.want) {
				t.Errorf("ops %#v, want %#v", ops, tc.want)
			}
		})
	}
}

func TestDiffTaskCriteriaAdd(t *testing.T) {
	base := richTask()
	t.Run("empty id appends with a nonce, pending start emits no status", func(t *testing.T) {
		p := mustParseTask(t, fusefs.RenderTask(base))
		p.Criteria.Value = append(p.Criteria.Value, fusefs.ParsedCriterion{Text: "New crit", Script: "scripty"})
		ops, err := fusefs.DiffTask(base, p)
		if err != nil {
			t.Fatalf("DiffTask: %v", err)
		}
		if len(ops) != 1 {
			t.Fatalf("ops %#v, want one AddCriterion", ops)
		}
		add, ok := ops[0].(model.AddCriterion)
		if !ok || len(add.ID) != 32 {
			t.Fatalf("op %#v, want AddCriterion with a 32-char nonce", ops[0])
		}
		add.ID = ""
		if want := (model.AddCriterion{Text: "New crit", Script: "scripty"}); add != want {
			t.Errorf("add %#v, want %#v", add, want)
		}
	})
	t.Run("non-pending new criterion emits a status op on the same nonce", func(t *testing.T) {
		p := mustParseTask(t, fusefs.RenderTask(base))
		p.Criteria.Value = append(p.Criteria.Value, fusefs.ParsedCriterion{Text: "Pre-met", Status: "met"})
		ops, err := fusefs.DiffTask(base, p)
		if err != nil {
			t.Fatalf("DiffTask: %v", err)
		}
		if len(ops) != 2 {
			t.Fatalf("ops %#v, want AddCriterion then SetCriterionStatus", ops)
		}
		add, ok := ops[0].(model.AddCriterion)
		if !ok || len(add.ID) != 32 {
			t.Fatalf("op[0] %#v, want AddCriterion with a 32-char nonce", ops[0])
		}
		st, ok := ops[1].(model.SetCriterionStatus)
		if !ok || st.ID != add.ID || st.Status != model.CriterionMet {
			t.Fatalf("op[1] %#v, want SetCriterionStatus{%q, met}", ops[1], add.ID)
		}
	})
}

func TestDiffTaskCriteriaErrors(t *testing.T) {
	base := richTask()
	cases := []struct {
		name   string
		mutate func(*fusefs.ParsedTask)
	}{
		{"invented id", func(p *fusefs.ParsedTask) { p.Criteria.Value[0].ID = "deadbeefdeadbeefdeadbeefdeadbeef" }},
		{"bad status on existing", func(p *fusefs.ParsedTask) { p.Criteria.Value[0].Status = "explode" }},
		{"bad status on new", func(p *fusefs.ParsedTask) {
			p.Criteria.Value = append(p.Criteria.Value, fusefs.ParsedCriterion{Text: "x", Status: "explode"})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParseTask(t, fusefs.RenderTask(base))
			tc.mutate(&p)
			if _, err := fusefs.DiffTask(base, p); !errors.Is(err, fusefs.ErrParse) {
				t.Fatalf("err %v, want ErrParse", err)
			}
		})
	}
}

func TestDiffTaskClosedForcedIgnored(t *testing.T) {
	base := richTask()
	p := mustParseTask(t, fusefs.RenderTask(base))
	p.ClosedForced = set(true)
	ops, err := fusefs.DiffTask(base, p)
	if err != nil {
		t.Fatalf("DiffTask: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("closed_forced flip produced ops %#v, want none", ops)
	}
}

func TestParseNoteErrors(t *testing.T) {
	cases := map[string]string{
		"no frontmatter":          "# Just markdown\n\nbody\n",
		"missing closing fence":   "---\ntitle: Open\n",
		"unknown key":             "---\ntitle: x\ncolor: red\n---\n",
		"body key is not known":   "---\nbody: inline\n---\n",
		"invalid yaml":            "---\ntitle: [unclosed\n---\n",
		"non-mapping frontmatter": "---\n- a\n- b\n---\n",
		"tags wrong shape":        "---\ntags: {a: 1}\n---\n",
		"empty file":              "",
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := fusefs.ParseNote([]byte(doc)); !errors.Is(err, fusefs.ErrParse) {
				t.Fatalf("err %v, want ErrParse", err)
			}
		})
	}
}

func TestParseNoteMinimalNewFile(t *testing.T) {
	p := mustParseNote(t, []byte("---\n---\n# Scratch heading\n\nnotes here\n"))
	if p.ID.Set || p.Title.Set || p.Tags.Set {
		t.Errorf("empty frontmatter parsed with set fields: %#v", p)
	}
	if want := "# Scratch heading\n\nnotes here\n"; p.Body != want {
		t.Errorf("body %q, want %q", p.Body, want)
	}
}

func TestParseTaskErrors(t *testing.T) {
	cases := map[string]string{
		"garbage":            "not json at all",
		"null document":      "null",
		"array document":     `[{"title": "x"}]`,
		"unknown key":        `{"title": "x", "bogus": 1}`,
		"unknown nested key": `{"comments": [{"author": "a", "ts": "t", "body": "b", "extra": 1}]}`,
		"wrong value type":   `{"priority": "high"}`,
		"trailing data":      `{"title": "x"} {"title": "y"}`,
		"empty file":         "",
		"truncated":          `{"title": "x"`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := fusefs.ParseTask([]byte(doc)); !errors.Is(err, fusefs.ErrParse) {
				t.Fatalf("err %v, want ErrParse", err)
			}
		})
	}
}

func TestNewNote(t *testing.T) {
	t.Run("frontmatter title", func(t *testing.T) {
		doc := "---\ntitle: Brand new\ntags: [b, a, b]\ncommits: [ffff111]\n---\nBody here.\n"
		ops, err := fusefs.NewNote(mustParseNote(t, []byte(doc)))
		if err != nil {
			t.Fatalf("NewNote: %v", err)
		}
		create := ops[0].(model.CreateNote)
		if len(ops) != 1 || len(create.Nonce) != 32 {
			t.Fatalf("ops %#v, want one CreateNote with a 32-char nonce", ops)
		}
		create.Nonce = ""
		want := model.CreateNote{
			Title:   "Brand new",
			Body:    "Body here.\n",
			Tags:    []string{"a", "b"},
			Anchors: []model.Anchor{{Kind: model.AnchorCommit, Value: "ffff111"}},
		}
		if !reflect.DeepEqual(create, want) {
			t.Errorf("create %#v, want %#v", create, want)
		}
	})
	t.Run("heading fallback", func(t *testing.T) {
		ops, err := fusefs.NewNote(mustParseNote(t, []byte("---\n---\n# Heading title\nrest\n")))
		if err != nil {
			t.Fatalf("NewNote: %v", err)
		}
		if got := ops[0].(model.CreateNote).Title; got != "Heading title" {
			t.Errorf("title %q, want %q", got, "Heading title")
		}
	})
	t.Run("no title anywhere", func(t *testing.T) {
		if _, err := fusefs.NewNote(mustParseNote(t, []byte("---\n---\nplain text\n"))); !errors.Is(err, fusefs.ErrParse) {
			t.Fatalf("err %v, want ErrParse", err)
		}
	})
	t.Run("id on a new note", func(t *testing.T) {
		doc := "---\nid: a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0\ntitle: Copied\n---\n"
		if _, err := fusefs.NewNote(mustParseNote(t, []byte(doc))); !errors.Is(err, fusefs.ErrParse) {
			t.Fatalf("err %v, want ErrParse", err)
		}
	})
}

func TestNewTask(t *testing.T) {
	t.Run("minimal defaults", func(t *testing.T) {
		ops, err := fusefs.NewTask(mustParseTask(t, []byte(`{"title": "New task"}`)), "main")
		if err != nil {
			t.Fatalf("NewTask: %v", err)
		}
		create := ops[0].(model.CreateTask)
		if len(ops) != 1 || len(create.Nonce) != 32 {
			t.Fatalf("ops %#v, want one CreateTask with a 32-char nonce", ops)
		}
		create.Nonce = ""
		want := model.CreateTask{Title: "New task", Type: model.TypeTask, Priority: 2, Branch: "main"}
		if !reflect.DeepEqual(create, want) {
			t.Errorf("create %#v, want %#v", create, want)
		}
	})
	t.Run("explicit fields", func(t *testing.T) {
		doc := `{"title": "Bug hunt", "description": "repro", "type": "bug", "priority": 0,
			"status": "open", "labels": ["z", "a", "z"], "branch": "main"}`
		ops, err := fusefs.NewTask(mustParseTask(t, []byte(doc)), "main")
		if err != nil {
			t.Fatalf("NewTask: %v", err)
		}
		create := ops[0].(model.CreateTask)
		create.Nonce = ""
		want := model.CreateTask{
			Title: "Bug hunt", Description: "repro", Type: model.TypeBug,
			Priority: 0, Branch: "main", Labels: []string{"a", "z"},
		}
		if !reflect.DeepEqual(create, want) {
			t.Errorf("create %#v, want %#v", create, want)
		}
	})
	errorCases := map[string]string{
		"missing title":    `{"description": "no title"}`,
		"non-open status":  `{"title": "x", "status": "done"}`,
		"invalid type":     `{"title": "x", "type": "chore"}`,
		"invalid priority": `{"title": "x", "priority": 9}`,
		"id set":           `{"title": "x", "id": "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0"}`,
		"branch mismatch":  `{"title": "x", "branch": "other"}`,
		"assignee set":     `{"title": "x", "assignee": "A <a@x>"}`,
		"blocked_by set":   `{"title": "x", "blocked_by": ["1111"]}`,
		"blocks set":       `{"title": "x", "blocks": ["1111"]}`,
		"parent set":       `{"title": "x", "parent": "1111"}`,
		"comments set":     `{"title": "x", "comments": [{"author": "a", "ts": "t", "body": "b"}]}`,
		"sprint set":       `{"title": "x", "sprint": "1111"}`,
		"project set":      `{"title": "x", "project": "1111"}`,
		"criteria set":     `{"title": "x", "criteria": [{"id": "1", "text": "t", "script": "", "status": "pending"}]}`,
		"closed_forced":    `{"title": "x", "closed_forced": true}`,
	}
	for name, doc := range errorCases {
		t.Run(name, func(t *testing.T) {
			if _, err := fusefs.NewTask(mustParseTask(t, []byte(doc)), "main"); !errors.Is(err, fusefs.ErrParse) {
				t.Fatalf("err %v, want ErrParse", err)
			}
		})
	}
	t.Run("timestamps ignored", func(t *testing.T) {
		doc := `{"title": "x", "created_at": "2020-01-01T00:00:00Z", "updated_at": "2020-01-01T00:00:00Z",
			"started_at": null, "closed_at": null, "assignee": null, "parent": null,
			"blocked_by": [], "blocks": [], "comments": []}`
		if _, err := fusefs.NewTask(mustParseTask(t, []byte(doc)), "main"); err != nil {
			t.Fatalf("NewTask: %v", err)
		}
	})
}

func TestNoteTextEditEndToEnd(t *testing.T) {
	base := richNote()
	text := string(fusefs.RenderNote(base))
	text = strings.Replace(text, "title: \"Fix the parser: edge-cases & \\U0001F984\"", "title: Retitled by hand", 1)
	text += "Appended line.\n"
	ops, err := fusefs.DiffNote(base, mustParseNote(t, []byte(text)))
	if err != nil {
		t.Fatalf("DiffNote: %v", err)
	}
	want := []model.Op{
		model.SetTitle{Title: "Retitled by hand"},
		model.SetBody{Body: base.Body + "Appended line.\n"},
	}
	if !reflect.DeepEqual(ops, want) {
		t.Errorf("ops %#v, want %#v", ops, want)
	}
}

func TestRenderSprintGolden(t *testing.T) {
	if got := string(fusefs.RenderSprint(richSprint())); got != goldenSprint {
		t.Errorf("rich sprint render:\n got %q\nwant %q", got, goldenSprint)
	}
}

func TestRenderProjectGolden(t *testing.T) {
	if got := string(fusefs.RenderProject(richProject())); got != goldenProject {
		t.Errorf("rich project render:\n got %q\nwant %q", got, goldenProject)
	}
}

func TestSprintRoundTrip(t *testing.T) {
	base := richSprint()
	cases := map[string]model.Sprint{
		"rich": base,
		"planned no project": {
			ID: base.ID, Title: "Planning", Status: model.SprintPlanned,
			CreatedAt: 100, UpdatedAt: 100,
		},
		"completed with dates": {
			ID: base.ID, Project: base.Project, Title: "Done", Status: model.SprintCompleted,
			StartDate: 100, EndDate: 200, CreatedAt: 100, UpdatedAt: 300, StartedAt: 100, ClosedAt: 300,
		},
	}
	for name, s := range cases {
		t.Run(name, func(t *testing.T) {
			ops, err := fusefs.DiffSprint(s, mustParseSprint(t, fusefs.RenderSprint(s)))
			if err != nil {
				t.Fatalf("DiffSprint: %v", err)
			}
			if len(ops) != 0 {
				t.Errorf("round trip produced ops %#v, want none", ops)
			}
		})
	}
}

func TestDiffSprintEditable(t *testing.T) {
	base := richSprint()
	cases := []struct {
		name   string
		mutate func(*fusefs.ParsedSprint)
		want   []model.Op
	}{
		{"title", func(p *fusefs.ParsedSprint) { p.Title = set("Renamed") }, []model.Op{model.SetTitle{Title: "Renamed"}}},
		{"description", func(p *fusefs.ParsedSprint) { p.Description = set("New body") }, []model.Op{model.SetDescription{Description: "New body"}}},
		{"status", func(p *fusefs.ParsedSprint) { p.Status = set("completed") }, []model.Op{model.SetSprintStatus{Status: model.SprintCompleted}}},
		{"start_date change", func(p *fusefs.ParsedSprint) { p.StartDate = set("2025-12-13T02:54:56Z") }, []model.Op{model.SetStartDate{Date: 1765594496}}},
		{"start_date clear via null", func(p *fusefs.ParsedSprint) { p.StartDate = null[string]() }, []model.Op{model.SetStartDate{Date: 0}}},
		{"end_date clear via empty", func(p *fusefs.ParsedSprint) { p.EndDate = set("") }, []model.Op{model.SetEndDate{Date: 0}}},
		{"add label", func(p *fusefs.ParsedSprint) { p.Labels.Value = append(p.Labels.Value, "urgent") }, []model.Op{model.AddLabel{Label: "urgent"}}},
		{"remove label", func(p *fusefs.ParsedSprint) { p.Labels = set([]string{"fs"}) }, []model.Op{model.RemoveLabel{Label: "core"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParseSprint(t, fusefs.RenderSprint(base))
			tc.mutate(&p)
			ops, err := fusefs.DiffSprint(base, p)
			if err != nil {
				t.Fatalf("DiffSprint: %v", err)
			}
			if !reflect.DeepEqual(ops, tc.want) {
				t.Errorf("ops %#v, want %#v", ops, tc.want)
			}
		})
	}
}

func TestDiffSprintInvalidValues(t *testing.T) {
	base := richSprint()
	cases := []struct {
		name   string
		mutate func(*fusefs.ParsedSprint)
	}{
		{"bogus status", func(p *fusefs.ParsedSprint) { p.Status = set("paused") }},
		{"null status", func(p *fusefs.ParsedSprint) { p.Status = null[string]() }},
		{"bad start_date", func(p *fusefs.ParsedSprint) { p.StartDate = set("not-a-date") }},
		{"bad end_date", func(p *fusefs.ParsedSprint) { p.EndDate = set("12/19/2025") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParseSprint(t, fusefs.RenderSprint(base))
			tc.mutate(&p)
			if _, err := fusefs.DiffSprint(base, p); !errors.Is(err, fusefs.ErrParse) {
				t.Fatalf("err %v, want ErrParse", err)
			}
		})
	}
}

func TestDiffSprintImmutable(t *testing.T) {
	base := richSprint()
	cases := []struct {
		field  string
		mutate func(*fusefs.ParsedSprint)
	}{
		{"id", func(p *fusefs.ParsedSprint) { p.ID = set("0000000000000000000000000000000000000000") }},
		{"project", func(p *fusefs.ParsedSprint) { p.Project = set("0000000000000000000000000000000000000000") }},
		{"project", func(p *fusefs.ParsedSprint) { p.Project = null[string]() }},
		{"commits", func(p *fusefs.ParsedSprint) { p.Commits = set([]string{}) }},
		{"comments", func(p *fusefs.ParsedSprint) { p.Comments.Value[0].Body = "edited" }},
		{"author", func(p *fusefs.ParsedSprint) { p.Author = set("Mallory m@example.com") }},
		{"created_at", func(p *fusefs.ParsedSprint) { p.CreatedAt = set("2020-01-01T00:00:00Z") }},
		{"updated_at", func(p *fusefs.ParsedSprint) { p.UpdatedAt = set("2020-01-01T00:00:00Z") }},
		{"started_at", func(p *fusefs.ParsedSprint) { p.StartedAt = null[string]() }},
		{"closed_at", func(p *fusefs.ParsedSprint) { p.ClosedAt = set("2026-01-01T00:00:00Z") }},
		{"tasks", func(p *fusefs.ParsedSprint) { p.Tasks = set([]string{"1111"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			p := mustParseSprint(t, fusefs.RenderSprint(base))
			tc.mutate(&p)
			_, err := fusefs.DiffSprint(base, p)
			if !errors.Is(err, fusefs.ErrImmutableField) {
				t.Fatalf("err %v, want ErrImmutableField", err)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("err %q does not name field %q", err, tc.field)
			}
		})
	}
}

func TestNewSprint(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		ops, err := fusefs.NewSprint(mustParseSprint(t, []byte(`{"title": "New sprint"}`)))
		if err != nil {
			t.Fatalf("NewSprint: %v", err)
		}
		create := ops[0].(model.CreateSprint)
		if len(ops) != 1 || len(create.Nonce) != 32 {
			t.Fatalf("ops %#v, want one CreateSprint with a 32-char nonce", ops)
		}
		create.Nonce = ""
		if want := (model.CreateSprint{Title: "New sprint"}); !reflect.DeepEqual(create, want) {
			t.Errorf("create %#v, want %#v", create, want)
		}
	})
	t.Run("description and labels", func(t *testing.T) {
		doc := `{"title": "Planned", "description": "scope", "labels": ["z", "a", "z"]}`
		ops, err := fusefs.NewSprint(mustParseSprint(t, []byte(doc)))
		if err != nil {
			t.Fatalf("NewSprint: %v", err)
		}
		create := ops[0].(model.CreateSprint)
		create.Nonce = ""
		want := model.CreateSprint{Title: "Planned", Description: "scope", Labels: []string{"a", "z"}}
		if !reflect.DeepEqual(create, want) {
			t.Errorf("create %#v, want %#v", create, want)
		}
	})
	errorCases := map[string]string{
		"missing title":      `{"description": "no title"}`,
		"non-planned status": `{"title": "x", "status": "active"}`,
		"id set":             `{"title": "x", "id": "5555aaaa5555aaaa5555aaaa5555aaaa5555aaaa"}`,
		"project set":        `{"title": "x", "project": "6666dddd6666dddd6666dddd6666dddd6666dddd"}`,
		"commits set":        `{"title": "x", "commits": ["cafe0000cafe0000cafe0000cafe0000cafe0000"]}`,
		"comments set":       `{"title": "x", "comments": [{"author": "a", "ts": "t", "body": "b"}]}`,
		"tasks set":          `{"title": "x", "tasks": ["1111"]}`,
	}
	for name, doc := range errorCases {
		t.Run(name, func(t *testing.T) {
			if _, err := fusefs.NewSprint(mustParseSprint(t, []byte(doc))); !errors.Is(err, fusefs.ErrParse) {
				t.Fatalf("err %v, want ErrParse", err)
			}
		})
	}
	t.Run("default status accepted", func(t *testing.T) {
		p := fusefs.ParsedSprint{Title: set("x"), Status: set("planned")}
		if _, err := fusefs.NewSprint(p); err != nil {
			t.Fatalf("NewSprint with planned status: %v", err)
		}
	})
	t.Run("timestamps ignored", func(t *testing.T) {
		doc := `{"title": "x", "created_at": "2020-01-01T00:00:00Z", "updated_at": "2020-01-01T00:00:00Z",
			"started_at": null, "closed_at": null, "start_date": null, "end_date": null}`
		if _, err := fusefs.NewSprint(mustParseSprint(t, []byte(doc))); err != nil {
			t.Fatalf("NewSprint: %v", err)
		}
	})
}

func TestProjectRoundTrip(t *testing.T) {
	base := richProject()
	cases := map[string]model.Project{
		"rich": base,
		"active minimal": {
			ID: base.ID, Title: "Active", Status: model.ProjectActive,
			CreatedAt: 100, UpdatedAt: 100,
		},
		"archived with closure": {
			ID: base.ID, Title: "Closed", Status: model.ProjectArchived,
			CreatedAt: 100, UpdatedAt: 400, ClosedAt: 400, Labels: []string{"x"},
		},
	}
	for name, pr := range cases {
		t.Run(name, func(t *testing.T) {
			ops, err := fusefs.DiffProject(pr, mustParseProject(t, fusefs.RenderProject(pr)))
			if err != nil {
				t.Fatalf("DiffProject: %v", err)
			}
			if len(ops) != 0 {
				t.Errorf("round trip produced ops %#v, want none", ops)
			}
		})
	}
}

func TestDiffProjectEditable(t *testing.T) {
	base := richProject()
	cases := []struct {
		name   string
		mutate func(*fusefs.ParsedProject)
		want   []model.Op
	}{
		{"title", func(p *fusefs.ParsedProject) { p.Title = set("Renamed") }, []model.Op{model.SetTitle{Title: "Renamed"}}},
		{"description", func(p *fusefs.ParsedProject) { p.Description = set("New body") }, []model.Op{model.SetDescription{Description: "New body"}}},
		{"status", func(p *fusefs.ParsedProject) { p.Status = set("archived") }, []model.Op{model.SetProjectStatus{Status: model.ProjectArchived}}},
		{"add label", func(p *fusefs.ParsedProject) { p.Labels.Value = append(p.Labels.Value, "urgent") }, []model.Op{model.AddLabel{Label: "urgent"}}},
		{"remove label", func(p *fusefs.ParsedProject) { p.Labels = set([]string{"core"}) }, []model.Op{model.RemoveLabel{Label: "platform"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParseProject(t, fusefs.RenderProject(base))
			tc.mutate(&p)
			ops, err := fusefs.DiffProject(base, p)
			if err != nil {
				t.Fatalf("DiffProject: %v", err)
			}
			if !reflect.DeepEqual(ops, tc.want) {
				t.Errorf("ops %#v, want %#v", ops, tc.want)
			}
		})
	}
}

func TestDiffProjectInvalidValues(t *testing.T) {
	base := richProject()
	cases := []struct {
		name   string
		mutate func(*fusefs.ParsedProject)
	}{
		{"bogus status", func(p *fusefs.ParsedProject) { p.Status = set("paused") }},
		{"null status", func(p *fusefs.ParsedProject) { p.Status = null[string]() }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParseProject(t, fusefs.RenderProject(base))
			tc.mutate(&p)
			if _, err := fusefs.DiffProject(base, p); !errors.Is(err, fusefs.ErrParse) {
				t.Fatalf("err %v, want ErrParse", err)
			}
		})
	}
}

func TestDiffProjectImmutable(t *testing.T) {
	base := richProject()
	cases := []struct {
		field  string
		mutate func(*fusefs.ParsedProject)
	}{
		{"id", func(p *fusefs.ParsedProject) { p.ID = set("0000000000000000000000000000000000000000") }},
		{"commits", func(p *fusefs.ParsedProject) { p.Commits = set([]string{}) }},
		{"comments", func(p *fusefs.ParsedProject) { p.Comments.Value[0].Body = "edited" }},
		{"author", func(p *fusefs.ParsedProject) { p.Author = set("Mallory m@example.com") }},
		{"created_at", func(p *fusefs.ParsedProject) { p.CreatedAt = set("2020-01-01T00:00:00Z") }},
		{"updated_at", func(p *fusefs.ParsedProject) { p.UpdatedAt = set("2020-01-01T00:00:00Z") }},
		{"closed_at", func(p *fusefs.ParsedProject) { p.ClosedAt = set("2026-01-01T00:00:00Z") }},
		{"sprints", func(p *fusefs.ParsedProject) { p.Sprints = set([]string{"1111"}) }},
		{"tasks", func(p *fusefs.ParsedProject) { p.Tasks = set([]string{"1111"}) }},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			p := mustParseProject(t, fusefs.RenderProject(base))
			tc.mutate(&p)
			_, err := fusefs.DiffProject(base, p)
			if !errors.Is(err, fusefs.ErrImmutableField) {
				t.Fatalf("err %v, want ErrImmutableField", err)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("err %q does not name field %q", err, tc.field)
			}
		})
	}
}

func TestNewProject(t *testing.T) {
	t.Run("minimal", func(t *testing.T) {
		ops, err := fusefs.NewProject(mustParseProject(t, []byte(`{"title": "New project"}`)))
		if err != nil {
			t.Fatalf("NewProject: %v", err)
		}
		create := ops[0].(model.CreateProject)
		if len(ops) != 1 || len(create.Nonce) != 32 {
			t.Fatalf("ops %#v, want one CreateProject with a 32-char nonce", ops)
		}
		create.Nonce = ""
		if want := (model.CreateProject{Title: "New project"}); !reflect.DeepEqual(create, want) {
			t.Errorf("create %#v, want %#v", create, want)
		}
	})
	t.Run("description and labels", func(t *testing.T) {
		doc := `{"title": "Platform", "description": "scope", "labels": ["z", "a", "z"]}`
		ops, err := fusefs.NewProject(mustParseProject(t, []byte(doc)))
		if err != nil {
			t.Fatalf("NewProject: %v", err)
		}
		create := ops[0].(model.CreateProject)
		create.Nonce = ""
		want := model.CreateProject{Title: "Platform", Description: "scope", Labels: []string{"a", "z"}}
		if !reflect.DeepEqual(create, want) {
			t.Errorf("create %#v, want %#v", create, want)
		}
	})
	errorCases := map[string]string{
		"missing title":     `{"description": "no title"}`,
		"non-active status": `{"title": "x", "status": "completed"}`,
		"id set":            `{"title": "x", "id": "6666dddd6666dddd6666dddd6666dddd6666dddd"}`,
		"commits set":       `{"title": "x", "commits": ["feed1111feed1111feed1111feed1111feed1111"]}`,
		"comments set":      `{"title": "x", "comments": [{"author": "a", "ts": "t", "body": "b"}]}`,
		"sprints set":       `{"title": "x", "sprints": ["1111"]}`,
		"tasks set":         `{"title": "x", "tasks": ["1111"]}`,
	}
	for name, doc := range errorCases {
		t.Run(name, func(t *testing.T) {
			if _, err := fusefs.NewProject(mustParseProject(t, []byte(doc))); !errors.Is(err, fusefs.ErrParse) {
				t.Fatalf("err %v, want ErrParse", err)
			}
		})
	}
	t.Run("default status accepted", func(t *testing.T) {
		p := fusefs.ParsedProject{Title: set("x"), Status: set("active")}
		if _, err := fusefs.NewProject(p); err != nil {
			t.Fatalf("NewProject with active status: %v", err)
		}
	})
	t.Run("timestamps ignored", func(t *testing.T) {
		doc := `{"title": "x", "created_at": "2020-01-01T00:00:00Z", "updated_at": "2020-01-01T00:00:00Z", "closed_at": null}`
		if _, err := fusefs.NewProject(mustParseProject(t, []byte(doc))); err != nil {
			t.Fatalf("NewProject: %v", err)
		}
	})
}

// gitEnv pins git to a hermetic config so host state cannot leak in.
func gitEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("CC_NOTES_ACTOR", "Agent A <a@example.com>")
}

func initRepo(t *testing.T) string {
	t.Helper()
	gitEnv(t)
	dir := t.TempDir()
	//nolint:gosec // G204: test shells out to git with fixed argv[0] and literal init args.
	out, err := exec.Command("git", "-C", dir, "init", "-q", "-b", "main").CombinedOutput()
	if err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	return dir
}

func runCLI(t *testing.T, dir string, args ...string) string {
	t.Helper()
	t.Chdir(dir)
	root := cli.NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(args)
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("cc-notes %s: %v (stderr %q)", strings.Join(args, " "), err, stderr.String())
	}
	return stdout.String()
}

func addTaskID(t *testing.T, dir string, args ...string) string {
	t.Helper()
	var added struct {
		ID string `json:"id"`
	}
	raw := runCLI(t, dir, append(args, "--json")...)
	if err := json.Unmarshal([]byte(raw), &added); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	return added.ID
}

// TestRenderTaskMatchesCLIJSON pins the byte-compatibility contract between
// RenderTask and the CLI: the rendered file must equal `task show --json`
// pretty-printed, for a task exercising every DTO field.
func TestRenderTaskMatchesCLIJSON(t *testing.T) {
	dir := initRepo(t)
	parentID := addTaskID(t, dir, "task", "add", "Parent epic", "--type", "epic", "--no-validation-criteria")
	blockerID := addTaskID(t, dir, "task", "add", "Blocker", "--no-validation-criteria")
	id := addTaskID(t, dir, "task", "add", "Cross-check & <rich> task",
		"--desc", "Multi-line\ndescription.", "--label", "render", "--label", "fs",
		"--priority", "1", "--parent", parentID, "--blocked-by", blockerID, "--no-validation-criteria")
	runCLI(t, dir, "task", "claim", id)
	runCLI(t, dir, "task", "comment", id, "First comment\nwith a newline")

	raw := runCLI(t, dir, "task", "show", id, "--json")
	var want bytes.Buffer
	if err := json.Indent(&want, []byte(strings.TrimSuffix(raw, "\n")), "", "  "); err != nil {
		t.Fatalf("indent CLI output %q: %v", raw, err)
	}
	want.WriteByte('\n')

	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	snapshot, err := s.Load(t.Context(), refs.Task(model.EntityID(id)))
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if got := fusefs.RenderTask(snapshot.(model.Task)); !bytes.Equal(got, want.Bytes()) {
		t.Errorf("RenderTask diverges from CLI --json:\n got %s\nwant %s", got, want.Bytes())
	}
}

// TestRenderSprintMatchesCLIJSON pins the byte-compatibility contract between
// RenderSprint and the CLI: the rendered file must equal `sprint show --json`
// pretty-printed, for a sprint exercising every DTO field. The sprint has no
// member tasks, so the tasks reverse index is empty on both sides (like the
// task test keeps blocks empty).
func TestRenderSprintMatchesCLIJSON(t *testing.T) {
	dir := initRepo(t)
	projectID := addTaskID(t, dir, "project", "add", "Umbrella project", "--desc", "Holds the sprint.")
	id := addTaskID(t, dir, "sprint", "add", "Sprint 7 <core>",
		"--desc", "Ship the FUSE layer.\nTwo lines.", "--project", projectID,
		"--label", "fs", "--label", "core", "--start", "2025-12-12", "--end", "2025-12-19")
	runCLI(t, dir, "sprint", "start", id)
	runCLI(t, dir, "sprint", "comment", id, "Kickoff comment\nwith a newline")

	raw := runCLI(t, dir, "sprint", "show", id, "--json")
	var want bytes.Buffer
	if err := json.Indent(&want, []byte(strings.TrimSuffix(raw, "\n")), "", "  "); err != nil {
		t.Fatalf("indent CLI output %q: %v", raw, err)
	}
	want.WriteByte('\n')

	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	snapshot, err := s.Load(t.Context(), refs.Sprint(model.EntityID(id)))
	if err != nil {
		t.Fatalf("load sprint: %v", err)
	}
	if got := fusefs.RenderSprint(snapshot.(model.Sprint)); !bytes.Equal(got, want.Bytes()) {
		t.Errorf("RenderSprint diverges from CLI --json:\n got %s\nwant %s", got, want.Bytes())
	}
}

// TestRenderProjectMatchesCLIJSON pins the byte-compatibility contract between
// RenderProject and the CLI: the rendered file must equal `project show --json`
// pretty-printed, for a project exercising every DTO field. The project has no
// member sprints or tasks, so both reverse indexes are empty on both sides.
func TestRenderProjectMatchesCLIJSON(t *testing.T) {
	dir := initRepo(t)
	id := addTaskID(t, dir, "project", "add", "Platform <v2>",
		"--desc", "Long-lived effort.\nMany sprints.", "--label", "platform", "--label", "core")
	runCLI(t, dir, "project", "complete", id)
	runCLI(t, dir, "project", "comment", id, "Charter approved\nfinal.")

	raw := runCLI(t, dir, "project", "show", id, "--json")
	var want bytes.Buffer
	if err := json.Indent(&want, []byte(strings.TrimSuffix(raw, "\n")), "", "  "); err != nil {
		t.Fatalf("indent CLI output %q: %v", raw, err)
	}
	want.WriteByte('\n')

	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	snapshot, err := s.Load(t.Context(), refs.Project(model.EntityID(id)))
	if err != nil {
		t.Fatalf("load project: %v", err)
	}
	if got := fusefs.RenderProject(snapshot.(model.Project)); !bytes.Equal(got, want.Bytes()) {
		t.Errorf("RenderProject diverges from CLI --json:\n got %s\nwant %s", got, want.Bytes())
	}
}

// goldenLog pins the rich-log render byte for byte. The second entry carries a
// markdown heading, a list, and a code fence to prove the full-line HTML-comment
// fence is an unambiguous split key — none of that text can be mistaken for a
// delimiter.
const goldenLog = "---\n" +
	"id: a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0\n" +
	"title: Auth rollout timeline\n" +
	"tags: [ops, rollout]\n" +
	"paths: [internal/auth]\n" +
	"branches: [main]\n" +
	"author: Agent A <a@example.com>\n" +
	"created: \"2025-12-12T02:54:56Z\"\n" +
	"updated: \"2025-12-13T02:54:56Z\"\n" +
	"---\n" +
	"<!-- cc-notes:entry author=\"Agent A <a@example.com>\" ts=\"2025-12-12T02:54:56Z\" -->\n" +
	"flipped to 5%\n" +
	"<!-- cc-notes:entry author=\"Agent B <b@example.com>\" ts=\"2025-12-13T02:54:56Z\" -->\n" +
	"# Heading inside an entry\n- a list item\n- another\n\n```go\nfunc main() {}\n```\nDone.\n"

const goldenMinimalLog = "---\n" +
	"id: b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1\n" +
	"title: \"07\"\n" +
	"tags: []\n" +
	"author: A <a@x>\n" +
	"created: \"1970-01-01T00:00:00Z\"\n" +
	"updated: \"1970-01-01T00:00:00Z\"\n" +
	"---\n"

func richLog() model.Log {
	return model.Log{
		ID:    "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
		Title: "Auth rollout timeline",
		Entries: []model.LogEntry{
			{Author: "Agent A <a@example.com>", TS: 1765508096, Text: "flipped to 5%\n"},
			{
				Author: "Agent B <b@example.com>", TS: 1765594496,
				Text: "# Heading inside an entry\n- a list item\n- another\n\n```go\nfunc main() {}\n```\nDone.\n",
			},
		},
		Tags: []string{"ops", "rollout"},
		Anchors: []model.Anchor{
			{Kind: model.AnchorPath, Value: "internal/auth"},
			{Kind: model.AnchorBranch, Value: "main"},
		},
		Author:    "Agent A <a@example.com>",
		CreatedAt: 1765508096,
		UpdatedAt: 1765594496,
		Head:      "ffff0000ffff0000ffff0000ffff0000ffff0000",
	}
}

func mustParseLog(t *testing.T, data []byte) fusefs.ParsedLog {
	t.Helper()
	p, err := fusefs.ParseLog(data)
	if err != nil {
		t.Fatalf("ParseLog(%q): %v", data, err)
	}
	return p
}

func TestRenderLogGolden(t *testing.T) {
	if got := string(fusefs.RenderLog(richLog())); got != goldenLog {
		t.Errorf("rich log render:\n got %q\nwant %q", got, goldenLog)
	}
	minimal := model.Log{
		ID:     "b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1",
		Title:  "07",
		Author: "A <a@x>",
	}
	if got := string(fusefs.RenderLog(minimal)); got != goldenMinimalLog {
		t.Errorf("minimal log render:\n got %q\nwant %q", got, goldenMinimalLog)
	}
}

// TestRenderLogCLIEntriesParseable proves RenderLog terminates CLI-created
// entry text — stored verbatim with no trailing newline — so a multi-entry log
// renders with every fence anchored at a line start and ParseLog recovers the
// entries instead of failing on a glued-together fence.
func TestRenderLogCLIEntriesParseable(t *testing.T) {
	l := model.Log{
		ID:     "a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0",
		Title:  "Rollout",
		Author: "A <a@x>",
		Entries: []model.LogEntry{
			{Author: "A <a@x>", TS: 100, Text: "flipped to 5%"},
			{Author: "B <b@x>", TS: 200, Text: "rolled back to 0%"},
		},
	}
	rendered := string(fusefs.RenderLog(l))
	// The second entry's text must end at a newline so the following fence
	// opens a fresh line — never "...5%<!-- cc-notes:entry".
	if !strings.Contains(rendered, "flipped to 5%\n<!-- cc-notes:entry") {
		t.Errorf("second fence not anchored at line start:\n%s", rendered)
	}
	p := mustParseLog(t, []byte(rendered))
	if len(p.Entries) != 2 {
		t.Fatalf("parsed %d entries, want 2", len(p.Entries))
	}
	if got, want := p.Entries[0].Text, "flipped to 5%\n"; got != want {
		t.Errorf("entry 0 text %q, want %q", got, want)
	}
	if got, want := p.Entries[1].Text, "rolled back to 0%\n"; got != want {
		t.Errorf("entry 1 text %q, want %q", got, want)
	}
}

func TestLogRoundTrip(t *testing.T) {
	base := richLog()
	cases := map[string]model.Log{
		"rich":      base,
		"empty log": {ID: base.ID, Title: "Empty", Author: base.Author, CreatedAt: 100, UpdatedAt: 100},
		"no trailing newline in last entry": {
			ID: base.ID, Title: "Dangling", Author: base.Author,
			Entries: []model.LogEntry{{Author: "A <a@x>", TS: 100, Text: "dangling line"}},
		},
		"entry text with delimiter lines": {
			ID: base.ID, Title: "Tricky", Author: base.Author,
			Entries: []model.LogEntry{{Author: "A <a@x>", TS: 100, Text: "---\nfirst\n---\nlast\n"}},
		},
		"entry text with blank lines": {
			ID: base.ID, Title: "Spaced", Author: base.Author,
			Entries: []model.LogEntry{
				{Author: "A <a@x>", TS: 100, Text: "line one\n\nline three\n"},
				{Author: "B <b@x>", TS: 200, Text: "next entry\n"},
			},
		},
		// The CLI stores entry text verbatim with no trailing newline (log
		// append/-m/--entry); a 2+ entry log in that shape must still render to
		// a parseable doc and diff back to no ops.
		"cli-shaped entries without trailing newlines": {
			ID: base.ID, Title: "Rollout", Author: base.Author,
			Entries: []model.LogEntry{
				{Author: "A <a@x>", TS: 100, Text: "flipped to 5%"},
				{Author: "B <b@x>", TS: 200, Text: "rolled back to 0%"},
			},
		},
		"many anchors": {
			ID: base.ID, Title: "Anchored", Author: base.Author,
			Anchors: []model.Anchor{
				{Kind: model.AnchorCommit, Value: "1111111"},
				{Kind: model.AnchorPath, Value: "a/b.go"},
				{Kind: model.AnchorDir, Value: "internal/auth"},
				{Kind: model.AnchorBranch, Value: "main"},
			},
		},
	}
	for name, l := range cases {
		t.Run(name, func(t *testing.T) {
			ops, err := fusefs.DiffLog(l, mustParseLog(t, fusefs.RenderLog(l)))
			if err != nil {
				t.Fatalf("DiffLog: %v", err)
			}
			if len(ops) != 0 {
				t.Errorf("round trip produced ops %#v, want none", ops)
			}
		})
	}
}

func TestDiffLogAppendEntry(t *testing.T) {
	base := richLog()
	text := string(fusefs.RenderLog(base))
	text += "<!-- cc-notes:entry author=\"ignored\" ts=\"1999-01-01T00:00:00Z\" -->\n"
	text += "rolled back to 0%\nincident closed.\n"
	ops, err := fusefs.DiffLog(base, mustParseLog(t, []byte(text)))
	if err != nil {
		t.Fatalf("DiffLog: %v", err)
	}
	want := []model.Op{model.AppendEntry{Text: "rolled back to 0%\nincident closed.\n"}}
	if !reflect.DeepEqual(ops, want) {
		t.Errorf("ops %#v, want %#v", ops, want)
	}
}

func TestDiffLogFrontmatterEdits(t *testing.T) {
	base := richLog()
	cases := []struct {
		name   string
		mutate func(*fusefs.ParsedLog)
		want   []model.Op
	}{
		{
			"title", func(p *fusefs.ParsedLog) { p.Title = set("Renamed timeline") },
			[]model.Op{model.SetTitle{Title: "Renamed timeline"}},
		},
		{
			"add tag", func(p *fusefs.ParsedLog) { p.Tags = set([]string{"ops", "rollout", "urgent"}) },
			[]model.Op{model.AddTag{Tag: "urgent"}},
		},
		{
			"remove tag", func(p *fusefs.ParsedLog) { p.Tags = set([]string{"ops"}) },
			[]model.Op{model.RemoveTag{Tag: "rollout"}},
		},
		{
			"add anchor", func(p *fusefs.ParsedLog) { p.Paths = set([]string{"internal/auth", "internal/auth/oauth.go"}) },
			[]model.Op{model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorPath, Value: "internal/auth/oauth.go"}}},
		},
		{
			"remove anchor", func(p *fusefs.ParsedLog) { p.Branches = set([]string{}) },
			[]model.Op{model.RemoveAnchor{Anchor: model.Anchor{Kind: model.AnchorBranch, Value: "main"}}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParseLog(t, fusefs.RenderLog(base))
			tc.mutate(&p)
			ops, err := fusefs.DiffLog(base, p)
			if err != nil {
				t.Fatalf("DiffLog: %v", err)
			}
			if !reflect.DeepEqual(ops, tc.want) {
				t.Errorf("ops %#v, want %#v", ops, tc.want)
			}
		})
	}
}

func TestDiffLogAppendOnly(t *testing.T) {
	base := richLog()
	cases := []struct {
		name   string
		mutate func(p *fusefs.ParsedLog)
	}{
		{"modified entry text", func(p *fusefs.ParsedLog) { p.Entries[0].Text = "rewritten\n" }},
		{"modified entry author", func(p *fusefs.ParsedLog) { p.Entries[0].Author = "Mallory <m@x>" }},
		{"modified entry ts", func(p *fusefs.ParsedLog) { p.Entries[0].TS = "2000-01-01T00:00:00Z" }},
		{"reordered entries", func(p *fusefs.ParsedLog) { p.Entries[0], p.Entries[1] = p.Entries[1], p.Entries[0] }},
		{"removed entry", func(p *fusefs.ParsedLog) { p.Entries = p.Entries[:1] }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := mustParseLog(t, fusefs.RenderLog(base))
			tc.mutate(&p)
			_, err := fusefs.DiffLog(base, p)
			if !errors.Is(err, fusefs.ErrImmutableField) {
				t.Fatalf("err %v, want ErrImmutableField", err)
			}
		})
	}
}

func TestDiffLogImmutableFrontmatter(t *testing.T) {
	base := richLog()
	cases := []struct {
		field  string
		mutate func(*fusefs.ParsedLog)
	}{
		{"id", func(p *fusefs.ParsedLog) { p.ID = set("0000000000000000000000000000000000000000") }},
		{"author", func(p *fusefs.ParsedLog) { p.Author = set("Mallory <m@example.com>") }},
		{"created", func(p *fusefs.ParsedLog) { p.Created = set("2020-01-01T00:00:00Z") }},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			p := mustParseLog(t, fusefs.RenderLog(base))
			tc.mutate(&p)
			_, err := fusefs.DiffLog(base, p)
			if !errors.Is(err, fusefs.ErrImmutableField) {
				t.Fatalf("err %v, want ErrImmutableField", err)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("err %q does not name field %q", err, tc.field)
			}
		})
	}
}

func TestDiffLogUpdatedIsInformational(t *testing.T) {
	base := richLog()
	p := mustParseLog(t, fusefs.RenderLog(base))
	p.Updated = set("1999-12-31T23:59:59Z")
	ops, err := fusefs.DiffLog(base, p)
	if err != nil {
		t.Fatalf("DiffLog: %v", err)
	}
	if len(ops) != 0 {
		t.Errorf("stale updated produced ops %#v, want none", ops)
	}
}

func TestParseLogSentinelCollision(t *testing.T) {
	// An entry whose own text carries the fence sentinel is ambiguous and must
	// be rejected, not silently re-split.
	body := "---\ntitle: Bad\nauthor: A <a@x>\n---\n" +
		"<!-- cc-notes:entry author=\"A <a@x>\" ts=\"2025-12-12T02:54:56Z\" -->\n" +
		"text mentioning <!-- cc-notes:entry author=\"forged\" ts=\"x\" --> inline\n"
	if _, err := fusefs.ParseLog([]byte(body)); !errors.Is(err, fusefs.ErrParse) {
		t.Fatalf("err %v, want ErrParse", err)
	}
}

func TestParseLogTextBeforeFirstEntry(t *testing.T) {
	body := "---\ntitle: Bad\nauthor: A <a@x>\n---\n" +
		"loose text with no fence\n" +
		"<!-- cc-notes:entry author=\"A <a@x>\" ts=\"2025-12-12T02:54:56Z\" -->\n" +
		"entry text\n"
	if _, err := fusefs.ParseLog([]byte(body)); !errors.Is(err, fusefs.ErrParse) {
		t.Fatalf("err %v, want ErrParse", err)
	}
}

func TestNewLog(t *testing.T) {
	t.Run("frontmatter title with entries", func(t *testing.T) {
		doc := "---\ntitle: Brand new\ntags: [b, a, b]\npaths: [internal/auth]\n---\n" +
			"<!-- cc-notes:entry author=\"ignored\" ts=\"2025-12-12T02:54:56Z\" -->\nfirst entry\n"
		ops, err := fusefs.NewLog(mustParseLog(t, []byte(doc)))
		if err != nil {
			t.Fatalf("NewLog: %v", err)
		}
		create, ok := ops[0].(model.CreateLog)
		if !ok || len(create.Nonce) != 32 {
			t.Fatalf("ops[0] %#v, want CreateLog with a 32-char nonce", ops[0])
		}
		create.Nonce = ""
		wantCreate := model.CreateLog{
			Title:   "Brand new",
			Tags:    []string{"a", "b"},
			Anchors: []model.Anchor{{Kind: model.AnchorPath, Value: "internal/auth"}},
		}
		if !reflect.DeepEqual(create, wantCreate) {
			t.Errorf("create %#v, want %#v", create, wantCreate)
		}
		wantRest := []model.Op{model.AppendEntry{Text: "first entry\n"}}
		if !reflect.DeepEqual(ops[1:], wantRest) {
			t.Errorf("entry ops %#v, want %#v", ops[1:], wantRest)
		}
	})
	t.Run("heading fallback from first entry", func(t *testing.T) {
		doc := "---\n---\n" +
			"<!-- cc-notes:entry author=\"A <a@x>\" ts=\"2025-12-12T02:54:56Z\" -->\n# Heading title\nrest\n"
		ops, err := fusefs.NewLog(mustParseLog(t, []byte(doc)))
		if err != nil {
			t.Fatalf("NewLog: %v", err)
		}
		if got := ops[0].(model.CreateLog).Title; got != "Heading title" {
			t.Errorf("title %q, want %q", got, "Heading title")
		}
	})
	t.Run("no title anywhere", func(t *testing.T) {
		doc := "---\n---\n" +
			"<!-- cc-notes:entry author=\"A <a@x>\" ts=\"2025-12-12T02:54:56Z\" -->\nplain text\n"
		if _, err := fusefs.NewLog(mustParseLog(t, []byte(doc))); !errors.Is(err, fusefs.ErrParse) {
			t.Fatalf("err %v, want ErrParse", err)
		}
	})
	t.Run("id on a new log", func(t *testing.T) {
		doc := "---\nid: a1b2c3d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0\ntitle: Copied\n---\n"
		if _, err := fusefs.NewLog(mustParseLog(t, []byte(doc))); !errors.Is(err, fusefs.ErrParse) {
			t.Fatalf("err %v, want ErrParse", err)
		}
	})
}
