package notes_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func sharedCommit(t *testing.T, dir, path, content string) model.SHA {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	gittest.Git(t, dir, "add", path)
	gittest.Git(t, dir, "commit", "-q", "-m", "commit "+path)
	return model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD"))
}

func sharedBind(t *testing.T, thin, source string) {
	t.Helper()
	if _, err := store.Bind(t.Context(), thin, source); err != nil {
		t.Fatalf("Bind(%s, %s): %v", thin, source, err)
	}
}

func sharedOpen(t *testing.T, dir string) *notes.Client {
	t.Helper()
	c, err := notes.Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	return c
}

func sharedRecordTips(t *testing.T, dir string) []string {
	t.Helper()
	out := gittest.Git(t, dir, "for-each-ref", "--format=%(objectname)", "refs/cc-notes/")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

func sharedHasObject(dir, sha string) bool {
	//nolint:gosec // G204: a fixed git verb over a sha the test itself produced.
	return exec.Command("git", "-C", dir, "cat-file", "-e", sha+"^{commit}").Run() == nil
}

func sortedIDs[T any](items []T, id func(T) model.EntityID) []model.EntityID {
	ids := make([]model.EntityID, len(items))
	for i, item := range items {
		ids[i] = id(item)
	}
	slices.Sort(ids)
	return ids
}

func noteID(n model.Note) model.EntityID { return n.ID }

type sharedView struct {
	notes       []model.EntityID
	noteTitle   string
	noteBody    string
	searchNotes []model.EntityID
	searchDocs  []model.EntityID
	searchTasks []model.EntityID
	taskTitle   string
	statusNotes notes.SummaryCount
	statusDocs  notes.SummaryCount
}

func (v sharedView) equal(o sharedView) bool {
	return slices.Equal(v.notes, o.notes) && v.noteTitle == o.noteTitle && v.noteBody == o.noteBody &&
		slices.Equal(v.searchNotes, o.searchNotes) && slices.Equal(v.searchDocs, o.searchDocs) &&
		slices.Equal(v.searchTasks, o.searchTasks) && v.taskTitle == o.taskTitle &&
		v.statusNotes == o.statusNotes && v.statusDocs == o.statusDocs
}

func readSharedView(t *testing.T, c *notes.Client, note, task model.EntityID) sharedView {
	t.Helper()
	ctx := t.Context()
	listed, err := c.Notes(ctx, notes.DocumentFilter{})
	if err != nil {
		t.Fatalf("Notes: %v", err)
	}
	shown, err := c.Note(ctx, note)
	if err != nil {
		t.Fatalf("Note(%s): %v", note, err)
	}
	foundNotes, err := c.SearchNotes(ctx, "quasar", notes.SearchFilter{Limit: 10})
	if err != nil {
		t.Fatalf("SearchNotes: %v", err)
	}
	foundDocs, err := c.SearchDocs(ctx, "quasar", notes.SearchFilter{Limit: 10})
	if err != nil {
		t.Fatalf("SearchDocs: %v", err)
	}
	foundTasks, err := c.SearchTasks(ctx, "quasar", notes.SearchFilter{Limit: 10})
	if err != nil {
		t.Fatalf("SearchTasks: %v", err)
	}
	shownTask, err := c.Task(ctx, task)
	if err != nil {
		t.Fatalf("Task(%s): %v", task, err)
	}
	report, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return sharedView{
		notes:       sortedIDs(listed, noteID),
		noteTitle:   shown.Title,
		noteBody:    shown.Body,
		searchNotes: sortedIDs(foundNotes, noteID),
		searchDocs:  sortedIDs(foundDocs, func(d model.Doc) model.EntityID { return d.ID }),
		searchTasks: sortedIDs(foundTasks, func(t model.Task) model.EntityID { return t.ID }),
		taskTitle:   shownTask.Title,
		statusNotes: report.Notes,
		statusDocs:  report.Docs,
	}
}

// TestSharedStorageListShowSearchParity reads one source corpus through two
// linked worktrees of a bound depth-1 clone; writes land only in the source.
func TestSharedStorageListShowSearchParity(t *testing.T) {
	source := gittest.InitRepo(t)
	t.Setenv("CC_NOTES_ACTOR", "Test User <test@example.com>")
	sharedCommit(t, source, "a.go", "package a\n")
	src := sharedOpen(t, source)
	ctx := t.Context()
	note, _, err := src.CreateNote(ctx, notes.NoteSpec{Title: "Shared fact", Body: "the quasar invariant"})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	doc, _, err := src.CreateDoc(ctx, notes.DocSpec{Title: "Shared guide", Body: "read the quasar guide", When: "before editing"})
	if err != nil {
		t.Fatalf("CreateDoc: %v", err)
	}
	created, err := src.CreateTask(ctx, notes.TaskSpec{Title: "Shared quasar task"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	task := created.Task.ID

	thin := gittest.ShallowClone(t, source, 1)
	sharedBind(t, thin, source)
	worktreeA := gittest.AddWorktree(t, thin, "wt-a")
	worktreeB := gittest.AddWorktree(t, thin, "wt-b")
	a, b := sharedOpen(t, worktreeA), sharedOpen(t, worktreeB)

	want := sharedView{
		notes:       []model.EntityID{note.ID},
		noteTitle:   "Shared fact",
		noteBody:    "the quasar invariant",
		searchNotes: []model.EntityID{note.ID},
		searchDocs:  []model.EntityID{doc.ID},
		searchTasks: []model.EntityID{task},
		taskTitle:   "Shared quasar task",
		statusNotes: notes.SummaryCount{Total: 1},
		statusDocs:  notes.SummaryCount{Total: 1},
	}
	views := []struct {
		name string
		c    *notes.Client
	}{{"source", src}, {"worktree A", a}, {"worktree B", b}}
	for _, view := range views {
		if got := readSharedView(t, view.c, note.ID, task); !got.equal(want) {
			t.Fatalf("%s view = %+v, want %+v", view.name, got, want)
		}
	}

	second, _, err := b.CreateNote(ctx, notes.NoteSpec{Title: "Written from B", Body: "second quasar"})
	if err != nil {
		t.Fatalf("CreateNote from B: %v", err)
	}
	wantIDs := []model.EntityID{note.ID, second.ID}
	slices.Sort(wantIDs)
	views[2].name, views[2].c = "fresh source", sharedOpen(t, source)
	for _, view := range views {
		listed, err := view.c.Notes(ctx, notes.DocumentFilter{})
		if err != nil {
			t.Fatalf("%s Notes: %v", view.name, err)
		}
		if got := sortedIDs(listed, noteID); !slices.Equal(got, wantIDs) {
			t.Fatalf("%s notes after B's write = %v, want %v", view.name, got, wantIDs)
		}
		shown, err := view.c.Note(ctx, second.ID)
		if err != nil {
			t.Fatalf("%s Note(%s): %v", view.name, second.ID, err)
		}
		if shown.Title != "Written from B" {
			t.Fatalf("%s Note(%s).Title = %q, want %q", view.name, second.ID, shown.Title, "Written from B")
		}
	}

	tips := sharedRecordTips(t, source)
	if len(tips) != 4 {
		t.Fatalf("source refs/cc-notes/ tips = %v, want 4 (note, doc, task, B's note)", tips)
	}
	for _, dir := range []string{thin, worktreeA, worktreeB} {
		if got := sharedRecordTips(t, dir); got != nil {
			t.Fatalf("%s refs/cc-notes/ = %v, want none", dir, got)
		}
	}
	for _, tip := range tips {
		if !sharedHasObject(source, tip) {
			t.Fatalf("records tip %s missing from the source", tip)
		}
		if sharedHasObject(thin, tip) {
			t.Fatalf("records tip %s leaked into the clone's object database", tip)
		}
	}
}

// TestSharedStorageContextComesFromThinCheckout pins every context read of a
// bound clone (HEAD, witness, drift, branch, script cwd, author) to the clone.
func TestSharedStorageContextComesFromThinCheckout(t *testing.T) {
	source := gittest.InitRepo(t)
	gittest.Git(t, source, "config", "user.name", "Source User")
	gittest.Git(t, source, "config", "user.email", "source@example.com")
	sharedCommit(t, source, "a.go", "source v1\n")
	thin := gittest.ShallowClone(t, source, 1)
	gittest.Git(t, thin, "config", "user.name", "Thin User")
	gittest.Git(t, thin, "config", "user.email", "thin@example.com")
	gittest.Git(t, thin, "checkout", "-q", "-b", "feature")
	thinHead := sharedCommit(t, thin, "a.go", "thin only\n")
	sourceHead := sharedCommit(t, source, "a.go", "source later\n")
	thinBlob := model.SHA(gittest.Git(t, thin, "rev-parse", "HEAD:a.go"))
	if sourceBlob := model.SHA(gittest.Git(t, source, "rev-parse", "HEAD:a.go")); sourceBlob == thinBlob {
		t.Fatalf("fixture invalid: a.go is %s in both repositories", thinBlob)
	}
	sharedBind(t, thin, source)
	if sharedHasObject(source, string(thinHead)) {
		t.Fatalf("fixture invalid: the clone-only commit %s is in the source", thinHead)
	}
	c := sharedOpen(t, thin)
	ctx := t.Context()

	actor, err := c.Actor(ctx)
	if err != nil {
		t.Fatalf("Actor: %v", err)
	}
	if want := model.Actor("Thin User <thin@example.com>"); actor != want {
		t.Fatalf("Actor = %q, want the clone's identity %q", actor, want)
	}

	note, _, err := c.CreateNote(ctx, notes.NoteSpec{
		Title:   "Thin fact",
		Body:    "anchored in the clone",
		Anchors: notes.AnchorSpec{Commits: []string{"HEAD"}, Paths: []string{"a.go"}},
	})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	wantAnchors := []model.Anchor{{Kind: model.AnchorCommit, Value: string(thinHead)}, {Kind: model.AnchorPath, Value: "a.go"}}
	if !slices.Equal(note.Anchors, wantAnchors) {
		t.Fatalf("note anchors = %+v, want %+v (source HEAD is %s)", note.Anchors, wantAnchors, sourceHead)
	}
	if note.VerifiedCommit != thinHead {
		t.Fatalf("VerifiedCommit = %s, want the clone's HEAD %s", note.VerifiedCommit, thinHead)
	}
	wantWitness := []model.AnchorWitness{{Anchor: wantAnchors[0], OID: thinHead}, {Anchor: wantAnchors[1], OID: thinBlob}}
	if !slices.Equal(note.Witness, wantWitness) {
		t.Fatalf("note witness = %+v, want %+v", note.Witness, wantWitness)
	}
	if note.Author != actor {
		t.Fatalf("note author = %q, want %q", note.Author, actor)
	}
	stored, err := sharedOpen(t, source).Note(ctx, note.ID)
	if err != nil {
		t.Fatalf("source Note(%s): %v", note.ID, err)
	}
	if !slices.Equal(stored.Anchors, wantAnchors) {
		t.Fatalf("source-read anchors = %+v, want the clone-only commit kept as a value %+v", stored.Anchors, wantAnchors)
	}
	if sharedHasObject(source, string(thinHead)) {
		t.Fatalf("anchoring the clone-only commit %s copied it into the source", thinHead)
	}

	if verdict, err := c.NoteVerdict(ctx, note, time.Hour, true); err != nil || verdict != "" {
		t.Fatalf("NoteVerdict before the edit = %q, %v; want fresh", verdict, err)
	}
	if err := os.WriteFile(filepath.Join(thin, "a.go"), []byte("edited in the clone\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if verdict, err := c.NoteVerdict(ctx, note, time.Hour, true); err != nil || verdict != notes.VerdictDrifted {
		t.Fatalf("NoteVerdict after a clone worktree edit = %q, %v; want %q", verdict, err, notes.VerdictDrifted)
	}

	created, err := c.CreateTask(ctx, notes.TaskSpec{Title: "Thin task", Criteria: []string{"runs in the clone"}})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.Task.Branch != "feature" {
		t.Fatalf("task branch = %q, want the clone's feature", created.Task.Branch)
	}
	criteria := slices.Clone(created.Task.Criteria)
	if len(criteria) != 1 {
		t.Fatalf("task criteria = %+v, want one", criteria)
	}
	criteria[0].Script = "pwd > out"
	validated, err := c.ValidateTask(ctx, created.Task.ID, criteria, time.Minute, nil)
	if err != nil {
		t.Fatalf("ValidateTask: %v", err)
	}
	if got := validated.Criteria[0].Status; got != model.CriterionMet {
		t.Fatalf("criterion status = %q, want %q", got, model.CriterionMet)
	}
	//nolint:gosec // G304: reads the file the test's own script wrote.
	raw, err := os.ReadFile(filepath.Join(thin, "out"))
	if err != nil {
		t.Fatalf("read the script's output in the clone: %v", err)
	}
	gotDir, err := filepath.EvalSymlinks(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	wantDir, err := filepath.EvalSymlinks(thin)
	if err != nil {
		t.Fatal(err)
	}
	if gotDir != wantDir {
		t.Fatalf("validation script ran in %s, want the clone %s", gotDir, wantDir)
	}
	if _, err := os.Stat(filepath.Join(source, "out")); !os.IsNotExist(err) {
		t.Fatalf("validation script wrote into the source (stat err %v)", err)
	}

	if got := sharedRecordTips(t, thin); got != nil {
		t.Fatalf("clone refs/cc-notes/ = %v, want none", got)
	}
	if got := len(sharedRecordTips(t, source)); got != 2 {
		t.Fatalf("source holds %d records refs, want 2 (the note and the task)", got)
	}
}
