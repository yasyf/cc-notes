package eval

import (
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func openRepo(t *testing.T) *notes.Client {
	t.Helper()
	dir := gittest.InitRepo(t)
	t.Setenv("CC_NOTES_ACTOR", "Test User <test@example.com>")
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
	c, err := notes.Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	return c
}

func TestLoadCorpusEmptyRepo(t *testing.T) {
	got, err := LoadCorpus(t.Context(), openRepo(t))
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("LoadCorpus on a cc-notes-free repo = %d entities, want 0", len(got))
	}
}

func TestLoadCorpusEveryKind(t *testing.T) {
	c := openRepo(t)
	ctx := t.Context()

	old, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "fold order", Body: "lamport wins", Tags: []string{"crdt"}})
	if err != nil {
		t.Fatalf("CreateNote old: %v", err)
	}
	fresh, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "fold order v2", Body: "author time breaks ties"})
	if err != nil {
		t.Fatalf("CreateNote fresh: %v", err)
	}
	if _, err := c.SupersedeNote(ctx, old.ID, fresh.ID); err != nil {
		t.Fatalf("SupersedeNote: %v", err)
	}
	doc, _, err := c.CreateDoc(ctx, notes.DocSpec{Title: "ship guide", Body: "run ship", When: "before pushing"})
	if err != nil {
		t.Fatalf("CreateDoc: %v", err)
	}
	lg, _, err := c.CreateLog(ctx, notes.LogSpec{Title: "rollout", Entry: "canary green"})
	if err != nil {
		t.Fatalf("CreateLog: %v", err)
	}
	task, err := c.CreateTask(ctx, notes.TaskSpec{Title: "wire the ranker", Description: "graph lane"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	proj, _, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "knowledge graph"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	sprint, _, err := c.CreateSprint(ctx, notes.SprintSpec{Title: "harness sprint"})
	if err != nil {
		t.Fatalf("CreateSprint: %v", err)
	}
	rb, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "release", Description: "tag and watch"})
	if err != nil {
		t.Fatalf("CreateRunbook: %v", err)
	}
	inv, _, err := c.CreateInvestigation(ctx, notes.InvestigationSpec{Title: "flaky sync", Premise: "union merge drops refs"})
	if err != nil {
		t.Fatalf("CreateInvestigation: %v", err)
	}

	corpus, err := LoadCorpus(ctx, c)
	if err != nil {
		t.Fatalf("LoadCorpus: %v", err)
	}
	if len(corpus) != 9 {
		t.Fatalf("LoadCorpus = %d entities, want 9: %+v", len(corpus), corpus)
	}
	if !slices.IsSortedFunc(corpus, func(a, b Entity) int { return strings.Compare(string(a.ID), string(b.ID)) }) {
		t.Errorf("LoadCorpus is not sorted by id: %+v", corpus)
	}

	byID := map[model.EntityID]Entity{}
	for _, e := range corpus {
		byID[e.ID] = e
	}
	wantKinds := map[model.EntityID]model.Kind{
		old.ID:       model.KindNote,
		fresh.ID:     model.KindNote,
		doc.ID:       model.KindDoc,
		lg.ID:        model.KindLog,
		task.Task.ID: model.KindTask,
		proj.ID:      model.KindProject,
		sprint.ID:    model.KindSprint,
		rb.ID:        model.KindRunbook,
		inv.ID:       model.KindInvestigation,
	}
	for id, kind := range wantKinds {
		e, ok := byID[id]
		if !ok {
			t.Fatalf("entity %s missing from the corpus", id)
		}
		if e.Kind != kind {
			t.Errorf("entity %s kind = %s, want %s", id, e.Kind, kind)
		}
	}
	if got := byID[old.ID].SupersededBy; !slices.Equal(got, []model.EntityID{fresh.ID}) {
		t.Errorf("superseded note SupersededBy = %v, want [%s]", got, fresh.ID)
	}
	if got := byID[fresh.ID].SupersededBy; len(got) != 0 {
		t.Errorf("live note SupersededBy = %v, want none", got)
	}

	wantText := map[model.EntityID][]string{
		old.ID:       {"fold order", "lamport wins", "crdt"},
		doc.ID:       {"ship guide", "before pushing", "run ship"},
		lg.ID:        {"rollout", "canary green"},
		task.Task.ID: {"wire the ranker", "graph lane"},
		rb.ID:        {"release", "tag and watch"},
		inv.ID:       {"flaky sync", "union merge drops refs"},
	}
	for id, wants := range wantText {
		text := byID[id].Text()
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("entity %s text %q does not contain %q", id, text, want)
			}
		}
	}
}

func TestTokenize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"case folded", "Fold ORDER", []string{"fold", "order"}},
		{"punctuation split", "refs/cc-notes/*", []string{"refs", "cc", "notes"}},
		{"digits kept", "go 1.26.5", []string{"go", "1", "26", "5"}},
		{"newlines and tabs", "a\nb\tc", []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Tokenize(tc.in); !slices.Equal(got, tc.want) {
				t.Errorf("Tokenize(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
