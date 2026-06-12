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
	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
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
  "created_at": "2025-12-12T02:54:56Z",
  "updated_at": "2025-12-13T02:54:56Z",
  "started_at": "2025-12-12T03:10:00Z",
  "closed_at": null
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
		Author:    "Agent A <a@example.com>",
		CreatedAt: 1765508096,
		UpdatedAt: 1765594496,
		Head:      "ffff0000ffff0000ffff0000ffff0000ffff0000",
	}
}

func richTask() model.Task {
	return model.Task{
		ID:          "0123abcd4567ef890123abcd4567ef890123abcd",
		Branch:      "feature/login",
		Title:       "Wire the FUSE layer <urgently>",
		Description: "Render, parse, diff.\nNo kernel needed.",
		Type:        model.TypeBug,
		Status:      model.StatusInProgress,
		Priority:    1,
		Assignee:    "Agent A <a@example.com>",
		Labels:      []string{"fs", "render"},
		BlockedBy:   []model.EntityID{"9999aaaa9999aaaa9999aaaa9999aaaa9999aaaa"},
		Parent:      "8888bbbb8888bbbb8888bbbb8888bbbb8888bbbb",
		Comments: []model.Comment{
			{Author: "Agent B <b@example.com>", TS: 1765510000, Body: "On it.\nETA tonight."},
		},
		CreatedAt: 1765508096,
		UpdatedAt: 1765594496,
		StartedAt: 1765509000,
		Head:      "eeee0000eeee0000eeee0000eeee0000eeee0000",
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

func mustParseTask(t *testing.T, data []byte) fusefs.ParsedTask {
	t.Helper()
	p, err := fusefs.ParseTask(data)
	if err != nil {
		t.Fatalf("ParseTask(%q): %v", data, err)
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
				model.AddTag{Tag: "aa"}, model.AddTag{Tag: "bb"},
				model.RemoveTag{Tag: "bug"}, model.RemoveTag{Tag: "parser"},
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
	parentID := addTaskID(t, dir, "task", "add", "Parent epic", "--type", "epic")
	blockerID := addTaskID(t, dir, "task", "add", "Blocker")
	id := addTaskID(t, dir, "task", "add", "Cross-check & <rich> task",
		"--desc", "Multi-line\ndescription.", "--label", "render", "--label", "fs",
		"--priority", "1", "--parent", parentID, "--blocked-by", blockerID)
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
	snapshot, err := s.Load(t.Context(), refs.Task("main", model.EntityID(id)))
	if err != nil {
		t.Fatalf("load task: %v", err)
	}
	if got := fusefs.RenderTask(snapshot.(model.Task)); !bytes.Equal(got, want.Bytes()) {
		t.Errorf("RenderTask diverges from CLI --json:\n got %s\nwant %s", got, want.Bytes())
	}
}
