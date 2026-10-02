package notes_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitobj"
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

// TestSharedStorageDeepenInvalidatesHistoryUnavailable rescores a cached
// HISTORY-UNAVAILABLE verdict after an explicit unshallow of the bound clone.
func TestSharedStorageDeepenInvalidatesHistoryUnavailable(t *testing.T) {
	t.Cleanup(notes.SetRelevantRacyWindow(0))
	source := gittest.InitRepo(t)
	t.Setenv("CC_NOTES_ACTOR", "Test User <test@example.com>")
	old := sharedCommit(t, source, "old.go", "package old\n")
	sharedCommit(t, source, "a.go", "package a\n")
	sharedCommit(t, source, "b.go", "package b\n")
	note, _, err := sharedOpen(t, source).CreateNote(t.Context(), notes.NoteSpec{
		Title:   "Old decision",
		Body:    "anchored before the clone's depth",
		Anchors: notes.AnchorSpec{Commits: []string{string(old)}, Paths: []string{"a.go"}},
	})
	if err != nil {
		t.Fatalf("CreateNote: %v", err)
	}
	thin := gittest.ShallowClone(t, source, 1)
	sharedBind(t, thin, source)
	if sharedHasObject(thin, string(old)) {
		t.Fatalf("fixture invalid: the clone holds %s", old)
	}
	c := sharedOpen(t, thin)

	renders := 0
	render := func(entries []notes.RelevantEntry) ([]byte, error) {
		renders++
		var b strings.Builder
		for _, e := range entries {
			b.WriteString(string(e.Note.ID) + " [" + string(e.Verdict) + "]\n")
		}
		return []byte(b.String()), nil
	}
	relevant := func(step, want string, wantRenders int) {
		t.Helper()
		out, err := c.RelevantCached(t.Context(), "a.go", notes.RelevantFilter{}, "verdicts", render)
		if err != nil {
			t.Fatalf("%s: RelevantCached: %v", step, err)
		}
		if string(out) != want {
			t.Fatalf("%s: RelevantCached = %q, want %q", step, out, want)
		}
		if renders != wantRenders {
			t.Fatalf("%s: renders = %d, want %d", step, renders, wantRenders)
		}
	}

	unavailable := string(note.ID) + " [" + string(notes.VerdictHistoryUnavailable) + "]\n"
	relevant("cold", unavailable, 1)
	relevant("warm", unavailable, 1)
	gittest.Git(t, thin, "fetch", "-q", "--unshallow", "origin")
	if !sharedHasObject(thin, string(old)) {
		t.Fatalf("fixture invalid: the unshallow did not fetch %s", old)
	}
	relevant("after deepen", string(note.ID)+" []\n", 2)
}

func sharedNote(t *testing.T, c *notes.Client, title string) model.EntityID {
	t.Helper()
	note, _, err := c.CreateNote(t.Context(), notes.NoteSpec{Title: title, Body: title, Anchors: notes.AnchorSpec{Paths: []string{"a.go"}}})
	if err != nil {
		t.Fatalf("CreateNote(%s): %v", title, err)
	}
	return note.ID
}

func sharedIDs(ids ...model.EntityID) string {
	lines := make([]string, len(ids))
	for i, id := range ids {
		lines[i] = string(id)
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

type sharedRelevance struct {
	t       *testing.T
	c       *notes.Client
	git     gitCounter
	renders int
	last    string
}

func (r *sharedRelevance) render(entries []notes.RelevantEntry) ([]byte, error) {
	r.renders++
	ids := make([]model.EntityID, len(entries))
	for i, e := range entries {
		ids[i] = e.Note.ID
	}
	return []byte(sharedIDs(ids...)), nil
}

func (r *sharedRelevance) call() (cacheTier, error) {
	r.t.Helper()
	calls, renders := r.git.calls(r.t), r.renders
	out, err := r.c.RelevantCached(r.t.Context(), "a.go", notes.RelevantFilter{}, "ids", r.render)
	if err != nil {
		return 0, err
	}
	r.last = string(out)
	switch {
	case r.renders > renders:
		return tierRebuild, nil
	case r.git.calls(r.t) > calls:
		return tierRevalidate, nil
	}
	return tierHit, nil
}

func (r *sharedRelevance) expect(step string, want cacheTier, wantOut string) {
	r.t.Helper()
	got, err := r.call()
	if err != nil {
		r.t.Fatalf("%s: RelevantCached: %v", step, err)
	}
	if got != want {
		r.t.Fatalf("%s: served by %v, want %v", step, got, want)
	}
	if r.last != wantOut {
		r.t.Fatalf("%s: RelevantCached = %q, want %q", step, r.last, wantOut)
	}
}

func (r *sharedRelevance) settle(step string, wantOut string) notes.RelevantCacheProbe {
	r.t.Helper()
	if _, err := r.call(); err != nil {
		r.t.Fatalf("%s (promotion): RelevantCached: %v", step, err)
	}
	r.expect(step, tierHit, wantOut)
	probe, ok, err := notes.RelevantCacheProbeOf(r.c, "a.go", notes.RelevantFilter{}, "ids")
	if err != nil {
		r.t.Fatalf("%s: RelevantCacheProbeOf: %v", step, err)
	}
	if !ok {
		r.t.Fatalf("%s: no cache entry persisted", step)
	}
	return probe
}

// TestSharedStorageRelevantWarmHitIsFixedWork pins a bound clone's warm hit
// to zero git and exactly two stamps over an unbound clone, invalidated by
// backend entity writes and clone HEAD moves, never by source branch work.
func TestSharedStorageRelevantWarmHitIsFixedWork(t *testing.T) {
	t.Cleanup(notes.SetRelevantRacyWindow(0))
	source := gittest.InitRepo(t)
	t.Setenv("CC_NOTES_ACTOR", "Test User <test@example.com>")
	sharedCommit(t, source, "a.go", "package a\n")
	src := sharedOpen(t, source)
	first := sharedNote(t, src, "First")
	thin := gittest.ShallowClone(t, source, 1)
	sharedBind(t, thin, source)
	unbound := gittest.ShallowClone(t, source, 1)
	git := installCountingGit(t, countingGitScript)
	bound := &sharedRelevance{t: t, c: sharedOpen(t, thin), git: git}
	plain := &sharedRelevance{t: t, c: sharedOpen(t, unbound), git: git}

	bound.expect("bound cold", tierRebuild, sharedIDs(first))
	boundStamps := bound.settle("bound warm", sharedIDs(first)).Stamps
	plain.expect("unbound cold", tierRebuild, "")
	plainStamps := plain.settle("unbound warm", "").Stamps
	if len(boundStamps) != len(plainStamps)+2 {
		t.Fatalf("bound clone stamps = %d, unbound clone stamps = %d; want exactly two more\nbound   %v\nunbound %v", len(boundStamps), len(plainStamps), boundStamps, plainStamps)
	}
	s, err := store.Open(thin)
	if err != nil {
		t.Fatalf("store.Open(%s): %v", thin, err)
	}
	backend, isBound := s.Binding()
	if !isBound {
		t.Fatalf("store.Open(%s): not bound", thin)
	}
	for _, want := range []string{filepath.Join(backend.CommonDir, "packed-refs"), backend.CommonDir} {
		if !slices.Contains(boundStamps, want) {
			t.Fatalf("bound clone stamps %v lack %s", boundStamps, want)
		}
	}
	for _, stamp := range plainStamps {
		if strings.HasPrefix(stamp, backend.CommonDir) {
			t.Fatalf("unbound clone stamps the source: %s", stamp)
		}
	}

	second := sharedNote(t, src, "Second")
	bound.expect("backend note written", tierRebuild, sharedIDs(first, second))
	if got := bound.settle("warm after the backend write", sharedIDs(first, second)).Stamps; len(got) != len(boundStamps) {
		t.Fatalf("stamps after a second entity = %d, want %d: %v", len(got), len(boundStamps), got)
	}

	gittest.Git(t, source, "checkout", "-q", "-b", "side")
	sharedCommit(t, source, "side.go", "package side\n")
	bound.expect("source branch and commit", tierHit, sharedIDs(first, second))
	var updates strings.Builder
	for i := range 200 {
		fmt.Fprintf(&updates, "update refs/heads/unrelated-%03d HEAD\n", i)
	}
	refs := exec.CommandContext(t.Context(), "git", "-C", source, "update-ref", "--stdin")
	refs.Stdin = strings.NewReader(updates.String())
	if out, err := refs.CombinedOutput(); err != nil {
		t.Fatalf("create unrelated source refs: %v\n%s", err, out)
	}
	bound.expect("unrelated source refs", tierHit, sharedIDs(first, second))

	sharedCommit(t, thin, "b.go", "package b\n")
	bound.expect("clone commit", tierRebuild, sharedIDs(first, second))
	bound.settle("warm after the clone commit", sharedIDs(first, second))
	gittest.Git(t, thin, "checkout", "-q", "-b", "feature")
	bound.expect("clone branch switch", tierRebuild, sharedIDs(first, second))
}

func sharedRename(t *testing.T, from, to string) {
	t.Helper()
	if err := os.Rename(from, to); err != nil {
		t.Fatal(err)
	}
}

func sharedBindingTo(t *testing.T, commonDir string) store.Binding {
	t.Helper()
	info, err := os.Stat(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	device, inode := gitobj.FileID(info)
	return store.Binding{CommonDir: commonDir, Device: device, Inode: inode}
}

// TestSharedStorageRelevantBackendFailureNeverServesCache breaks the backend
// under a warm entry: the pre-opened client fails with a *store.BindingError
// and a fresh client never sees the stale output.
func TestSharedStorageRelevantBackendFailureNeverServesCache(t *testing.T) {
	t.Cleanup(notes.SetRelevantRacyWindow(0))
	for _, tc := range []struct {
		name      string
		breakIt   func(t *testing.T, thin, backend string)
		want      error
		freshOpen error
	}{
		{
			name:      "backend removed",
			breakIt:   func(t *testing.T, _, backend string) { sharedRename(t, backend, backend+".gone") },
			want:      store.ErrBackendUnavailable,
			freshOpen: store.ErrBackendUnavailable,
		},
		{
			name: "backend replaced",
			breakIt: func(t *testing.T, _, backend string) {
				sharedRename(t, backend, backend+".old")
				if out, err := exec.CommandContext(t.Context(), "cp", "-R", backend+".old", backend).CombinedOutput(); err != nil {
					t.Fatalf("cp -R: %v\n%s", err, out)
				}
			},
			want:      store.ErrBackendReplaced,
			freshOpen: store.ErrBackendReplaced,
		},
		{
			name: "backend redirects",
			breakIt: func(t *testing.T, _, backend string) {
				if err := os.WriteFile(filepath.Join(backend, "commondir"), []byte("../elsewhere\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want:      store.ErrBackendRedirects,
			freshOpen: store.ErrBackendRedirects,
		},
		{
			name: "context rebound",
			breakIt: func(t *testing.T, thin, _ string) {
				other := sharedBindingTo(t, filepath.Join(gittest.InitRepo(t), ".git"))
				gittest.Git(t, thin, "config", "cc-notes.storage", other.String())
			},
			want: store.ErrBindingChanged,
		},
		{
			name:    "binding removed",
			breakIt: func(t *testing.T, thin, _ string) { gittest.Git(t, thin, "config", "--unset", "cc-notes.storage") },
			want:    store.ErrBindingChanged,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source := gittest.InitRepo(t)
			t.Setenv("CC_NOTES_ACTOR", "Test User <test@example.com>")
			sharedCommit(t, source, "a.go", "package a\n")
			note := sharedNote(t, sharedOpen(t, source), "Shared")
			thin := gittest.ShallowClone(t, source, 1)
			sharedBind(t, thin, source)
			s, err := store.Open(thin)
			if err != nil {
				t.Fatalf("store.Open(%s): %v", thin, err)
			}
			backend, isBound := s.Binding()
			if !isBound {
				t.Fatalf("store.Open(%s): not bound", thin)
			}
			warm := &sharedRelevance{t: t, c: sharedOpen(t, thin), git: installCountingGit(t, countingGitScript)}
			warm.expect("cold", tierRebuild, sharedIDs(note))
			warm.settle("warm", sharedIDs(note))

			tc.breakIt(t, thin, backend.CommonDir)
			var bindingErr *store.BindingError
			if _, err := warm.call(); !errors.As(err, &bindingErr) || !errors.Is(err, tc.want) {
				t.Fatalf("pre-opened client after %s: RelevantCached err = %v, want a *store.BindingError wrapping %v", tc.name, err, tc.want)
			}
			fresh, err := notes.Open(thin)
			if tc.freshOpen != nil {
				if !errors.As(err, &bindingErr) || !errors.Is(err, tc.freshOpen) {
					t.Fatalf("fresh Open after %s: err = %v, want a *store.BindingError wrapping %v", tc.name, err, tc.freshOpen)
				}
				return
			}
			if err != nil {
				t.Fatalf("fresh Open after %s: %v", tc.name, err)
			}
			out, err := fresh.RelevantCached(t.Context(), "a.go", notes.RelevantFilter{}, "ids", warm.render)
			if err != nil {
				t.Fatalf("fresh client after %s: RelevantCached: %v", tc.name, err)
			}
			if string(out) != "" {
				t.Fatalf("fresh client after %s: RelevantCached = %q, want the new records repository's empty corpus", tc.name, out)
			}
		})
	}
}

// TestSharedStorageLocalEntitiesReadRecords pins the local policy through a
// bound client: SetLocal secludes in the records repository, LocalEntities
// lists from the records refs, and the thin repository gains neither.
func TestSharedStorageLocalEntitiesReadRecords(t *testing.T) {
	ctx := t.Context()
	source := gittest.InitRepo(t)
	sharedCommit(t, source, "a.go", "package a\n")
	thin := gittest.ShallowClone(t, source, 1)
	sharedBind(t, thin, source)
	c := sharedOpen(t, thin)
	id := sharedNote(t, c, "brief")

	wantReason := "label " + store.LocalLabel
	if reason, err := c.SetLocal(ctx, model.KindNote, id, true); err != nil || reason != wantReason {
		t.Fatalf("SetLocal = %q, %v; want %q", reason, err, wantReason)
	}
	got, err := c.LocalEntities(ctx)
	if err != nil {
		t.Fatalf("LocalEntities: %v", err)
	}
	want := []notes.LocalEntity{{Kind: model.KindNote, ID: id, Title: "brief", Reason: wantReason, Secluded: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LocalEntities = %+v, want %+v", got, want)
	}
	_, sourceCommon := gittest.Dirs(t, source)
	_, thinCommon := gittest.Dirs(t, thin)
	if _, err := os.Stat(filepath.Join(sourceCommon, store.LocalPushInclude)); err != nil {
		t.Fatalf("records push include: %v", err)
	}
	if _, err := os.Stat(filepath.Join(thinCommon, store.LocalPushInclude)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("thin push include: stat = %v, want not exist", err)
	}
	if out := gittest.Git(t, thin, "for-each-ref", "--format=%(refname)", "refs/cc-notes/"); out != "" {
		t.Fatalf("thin repository holds records refs:\n%s", out)
	}
}

// TestSharedStorageOrphanHeadProvesDrift pins the review's orphan-branch
// scenario, bound and unbound: a depth-1 clone lacks the anchor commit, but its
// HEAD is an orphan commit whose complete history never meets the shallow
// boundary, so the anchor is proven unreachable and the note is DRIFTED, the
// verdict a full clone reports. Fetching the anchor's object alone moves no
// relevance stamp, so the warm cached answer must already equal a fresh one.
// Back on the branch whose walk does reach the boundary, the same anchor is
// genuinely undecidable and stays HISTORY-UNAVAILABLE.
func TestSharedStorageOrphanHeadProvesDrift(t *testing.T) {
	for _, mode := range []string{"bound", "unbound"} {
		t.Run(mode, func(t *testing.T) {
			t.Cleanup(notes.SetRelevantRacyWindow(0))
			source := gittest.InitRepo(t)
			t.Setenv("CC_NOTES_ACTOR", "Test User <test@example.com>")
			anchor := sharedCommit(t, source, "a.go", "package a\n")
			sharedCommit(t, source, "b.go", "package b\n")
			note, _, err := sharedOpen(t, source).CreateNote(t.Context(), notes.NoteSpec{
				Title:   "Anchored decision",
				Body:    "anchored past the clone's depth",
				Anchors: notes.AnchorSpec{Commits: []string{string(anchor)}, Paths: []string{"a.go"}},
			})
			if err != nil {
				t.Fatalf("CreateNote: %v", err)
			}
			thin := gittest.ShallowClone(t, source, 1)
			branch := gittest.Git(t, thin, "branch", "--show-current")
			gittest.Git(t, thin, "checkout", "-q", "--orphan", "orphan")
			gittest.Git(t, thin, "commit", "-q", "-m", "orphan")
			if mode == "bound" {
				sharedBind(t, thin, source)
			} else {
				gittest.Git(t, thin, "fetch", "-q", "origin", "+refs/cc-notes/*:refs/cc-notes/*")
			}
			if sharedHasObject(thin, string(anchor)) {
				t.Fatalf("fixture invalid: the clone holds %s", anchor)
			}
			c := sharedOpen(t, thin)

			lines := func(entries []notes.RelevantEntry) string {
				var b strings.Builder
				for _, e := range entries {
					b.WriteString(string(e.Note.ID) + " [" + string(e.Verdict) + "]\n")
				}
				return b.String()
			}
			render := func(entries []notes.RelevantEntry) ([]byte, error) { return []byte(lines(entries)), nil }
			check := func(step string, want notes.Verdict) {
				t.Helper()
				n, err := c.Note(t.Context(), note.ID)
				if err != nil {
					t.Fatalf("%s: Note: %v", step, err)
				}
				got, err := c.NoteVerdict(t.Context(), n, time.Hour, false)
				if err != nil {
					t.Fatalf("%s: NoteVerdict: %v", step, err)
				}
				if got != want {
					t.Errorf("%s: NoteVerdict = %q, want %q", step, got, want)
				}
				cached, err := c.RelevantCached(t.Context(), "a.go", notes.RelevantFilter{}, "verdicts", render)
				if err != nil {
					t.Fatalf("%s: RelevantCached: %v", step, err)
				}
				fresh, err := c.Relevant(t.Context(), "a.go", notes.RelevantFilter{})
				if err != nil {
					t.Fatalf("%s: Relevant: %v", step, err)
				}
				wantLines := string(note.ID) + " [" + string(want) + "]\n"
				if string(cached) != wantLines || lines(fresh) != wantLines {
					t.Errorf("%s: RelevantCached = %q, fresh Relevant = %q; want both %q", step, cached, lines(fresh), wantLines)
				}
			}

			check("orphan head, cold", notes.VerdictDrifted)
			check("orphan head, warm", notes.VerdictDrifted)
			shallowFile := filepath.Join(thin, ".git", "shallow")
			before, err := os.ReadFile(shallowFile)
			if err != nil {
				t.Fatalf("read shallow: %v", err)
			}
			gittest.Git(t, thin, "fetch", "-q", "--no-tags", "--no-write-fetch-head", "origin", string(anchor))
			after, err := os.ReadFile(shallowFile)
			if err != nil {
				t.Fatalf("read shallow: %v", err)
			}
			if !sharedHasObject(thin, string(anchor)) || string(after) != string(before) {
				t.Fatalf("fixture invalid: fetching %s must add its object and leave the shallow file %q unchanged, got %q", anchor, before, after)
			}
			check("orphan head, anchor object fetched", notes.VerdictDrifted)

			gittest.Git(t, thin, "checkout", "-q", branch)
			check("boundary head", notes.VerdictHistoryUnavailable)
		})
	}
}
