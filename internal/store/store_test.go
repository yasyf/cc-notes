// White-box tests: living in the package lets them freeze the Store clock,
// which the deterministic-id (contended create) tests need. Every test runs
// against a real git repository in t.TempDir().
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

const (
	testName  = "Test User"
	testEmail = "test@example.com"
	testActor = model.Actor("Test User <test@example.com>")
)

func initStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(gittest.InitRepo(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return s
}

func noteOps(title string) []model.Op {
	return []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: title}}
}

func taskOps(title string, branch model.Branch) []model.Op {
	return []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: title, Type: model.TypeTask, Branch: branch}}
}

func docOps(title string) []model.Op {
	return []model.Op{model.CreateDoc{Nonce: model.NewNonce(), Title: title}}
}

func logOps(title string) []model.Op {
	return []model.Op{model.CreateLog{Nonce: model.NewNonce(), Title: title}}
}

func create(t *testing.T, s *Store, ops []model.Op) model.Snapshot {
	t.Helper()
	snapshot, err := s.Create(t.Context(), ops)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return snapshot
}

func chronoNotes(a, b model.Note) int {
	if c := a.CreatedAt - b.CreatedAt; c != 0 {
		return int(c)
	}
	return strings.Compare(string(a.ID), string(b.ID))
}

func chronoDocs(a, b model.Doc) int {
	if c := a.CreatedAt - b.CreatedAt; c != 0 {
		return int(c)
	}
	return strings.Compare(string(a.ID), string(b.ID))
}

func sprintOps(title string) []model.Op {
	return []model.Op{model.CreateSprint{Nonce: model.NewNonce(), Title: title}}
}

func projectOps(title string) []model.Op {
	return []model.Op{model.CreateProject{Nonce: model.NewNonce(), Title: title}}
}

func chronoSprints(a, b model.Sprint) int {
	if c := a.CreatedAt - b.CreatedAt; c != 0 {
		return int(c)
	}
	return strings.Compare(string(a.ID), string(b.ID))
}

func chronoProjects(a, b model.Project) int {
	if c := a.CreatedAt - b.CreatedAt; c != 0 {
		return int(c)
	}
	return strings.Compare(string(a.ID), string(b.ID))
}

func TestCreateNoteRoundTrip(t *testing.T) {
	s := initStore(t)
	ops := []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "hello", Body: "world", Tags: []string{"b", "a"}}}
	snapshot := create(t, s, ops)
	note, ok := snapshot.(model.Note)
	if !ok {
		t.Fatalf("Create returned %T, want model.Note", snapshot)
	}

	if note.Title != "hello" || note.Body != "world" {
		t.Errorf("note = %q/%q, want hello/world", note.Title, note.Body)
	}
	if want := []string{"a", "b"}; !slices.Equal(note.Tags, want) {
		t.Errorf("Tags = %v, want %v", note.Tags, want)
	}
	if note.Author != testActor {
		t.Errorf("Author = %q, want %q", note.Author, testActor)
	}
	if note.Deleted {
		t.Error("fresh note is Deleted")
	}
	if note.Head != model.SHA(note.ID) {
		t.Errorf("Head = %s, want root %s", note.Head, note.ID)
	}
	if note.CreatedAt == 0 || note.UpdatedAt != note.CreatedAt {
		t.Errorf("timestamps = %d/%d, want equal non-zero", note.CreatedAt, note.UpdatedAt)
	}

	ref := refs.Note(note.ID)
	loaded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded, snapshot) {
		t.Errorf("Load = %+v, want Create snapshot %+v", loaded, snapshot)
	}

	if msg := gittest.Git(t, s.Git.Dir, "log", "-1", "--format=%s", ref); msg != "cc-notes: note create" {
		t.Errorf("commit message = %q, want %q", msg, "cc-notes: note create")
	}

	list, err := s.ListNotes(t.Context(), false, false)
	if err != nil {
		t.Fatalf("ListNotes: %v", err)
	}
	if want := []model.Note{note}; !reflect.DeepEqual(list, want) {
		t.Errorf("ListNotes = %+v, want %+v", list, want)
	}
}

func TestCreateTaskRoundTrip(t *testing.T) {
	s := initStore(t)
	branch := model.Branch("feat/x")
	snapshot := create(t, s, taskOps("ship it", branch))
	task, ok := snapshot.(model.Task)
	if !ok {
		t.Fatalf("Create returned %T, want model.Task", snapshot)
	}

	if task.Title != "ship it" || task.Branch != branch || task.Status != model.StatusOpen || task.Type != model.TypeTask {
		t.Errorf("task = %+v, want title/branch/status/type = ship it/%s/open/task", task, branch)
	}

	ref := refs.Task(task.ID)
	loaded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded, snapshot) {
		t.Errorf("Load = %+v, want Create snapshot %+v", loaded, snapshot)
	}
	if msg := gittest.Git(t, s.Git.Dir, "log", "-1", "--format=%s", ref); msg != "cc-notes: task create" {
		t.Errorf("commit message = %q, want %q", msg, "cc-notes: task create")
	}

	list, err := s.ListTasks(t.Context())
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if want := []model.Task{task}; !reflect.DeepEqual(list, want) {
		t.Errorf("ListTasks = %+v, want %+v", list, want)
	}
	if list[0].Branch != branch {
		t.Errorf("task folds branch %q, want %q", list[0].Branch, branch)
	}
}

func TestCreateRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		ops  []model.Op
		want error
	}{
		{name: "no ops", ops: nil},
		{name: "first op not a create", ops: []model.Op{model.SetTitle{Title: "x"}}},
		{name: "invalid enum rejected by codec", ops: []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: "x", Type: "bogus", Branch: "main"}}, want: model.ErrInvalidValue},
		{name: "kind mismatch rejected by fold", ops: []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: "x"}, model.SetStatus{Status: model.StatusDone}}, want: fold.ErrKindMismatch},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := initStore(t)
			_, err := s.Create(t.Context(), tc.ops)
			if err == nil {
				t.Fatal("Create succeeded, want error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("Create = %v, want %v", err, tc.want)
			}
			tips, err := s.Repo.ListPrefix(t.Context(), "refs/cc-notes/")
			if err != nil {
				t.Fatalf("ListPrefix: %v", err)
			}
			if len(tips) != 0 {
				t.Errorf("rejected create published refs: %v", tips)
			}
		})
	}
}

func TestCreateDedupesSameContent(t *testing.T) {
	s := initStore(t)
	ops := []model.Op{model.CreateNote{Nonce: strings.Repeat("ab", 16), Title: "dup"}}

	first, err := s.Create(t.Context(), ops)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	_, err = s.Create(t.Context(), ops)
	var dup *DuplicateError
	if !errors.As(err, &dup) || !errors.Is(err, ErrDuplicate) {
		t.Fatalf("second create err = %v, want *DuplicateError matching ErrDuplicate", err)
	}
	if dup.Kind != refs.KindNote {
		t.Errorf("dup kind = %s, want note", dup.Kind)
	}
	if dup.Existing.EntityID() != first.EntityID() {
		t.Errorf("reused id = %s, want existing %s", dup.Existing.EntityID(), first.EntityID())
	}
	tips, err := s.Repo.ListPrefix(t.Context(), refs.NotesPrefix)
	if err != nil {
		t.Fatalf("ListPrefix: %v", err)
	}
	if len(tips) != 1 {
		t.Errorf("note refs = %d, want 1 (dedupe wrote nothing)", len(tips))
	}
}

func TestAppendRoundTrip(t *testing.T) {
	s := initStore(t)
	note := create(t, s, noteOps("v1")).(model.Note)
	ref := refs.Note(note.ID)

	snapshot, err := s.Append(t.Context(), ref, []model.Op{model.SetTitle{Title: "v2"}, model.AddTag{Tag: "x"}})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	updated := snapshot.(model.Note)
	if updated.Title != "v2" {
		t.Errorf("Title = %q, want v2", updated.Title)
	}
	if want := []string{"x"}; !slices.Equal(updated.Tags, want) {
		t.Errorf("Tags = %v, want %v", updated.Tags, want)
	}
	if updated.ID != note.ID {
		t.Errorf("ID changed across append: %s -> %s", note.ID, updated.ID)
	}

	loaded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded, snapshot) {
		t.Errorf("Load = %+v, want Append snapshot %+v", loaded, snapshot)
	}

	if msg := gittest.Git(t, s.Git.Dir, "log", "-1", "--format=%s", ref); msg != "cc-notes: set_title add_tag" {
		t.Errorf("commit message = %q, want %q", msg, "cc-notes: set_title add_tag")
	}
	chain, err := s.Repo.ReadChain(t.Context(), updated.Head)
	if err != nil {
		t.Fatalf("ReadChain: %v", err)
	}
	if chain[0].Pack.Lamport != 2 {
		t.Errorf("tip lamport = %d, want 2", chain[0].Pack.Lamport)
	}
}

func TestAppendConcurrent(t *testing.T) {
	s := initStore(t)
	gittest.Git(t, s.Git.Dir, "config", "core.filesRefLockTimeout", "3000")
	note := create(t, s, noteOps("contended")).(model.Note)
	ref := refs.Note(note.ID)

	tags := []string{"a", "b"}
	errs := make([]error, len(tags))
	var wg sync.WaitGroup
	for i, tag := range tags {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = s.Append(t.Context(), ref, []model.Op{model.AddTag{Tag: tag}})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}

	final, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := final.(model.Note).Tags; !slices.Equal(got, tags) {
		t.Errorf("Tags = %v, want %v", got, tags)
	}

	chain, err := s.Repo.ReadChain(t.Context(), final.(model.Note).Head)
	if err != nil {
		t.Fatalf("ReadChain: %v", err)
	}
	lamports := map[model.Lamport]int{}
	for _, c := range chain {
		lamports[c.Pack.Lamport]++
	}
	if want := map[model.Lamport]int{1: 1, 2: 1, 3: 1}; !reflect.DeepEqual(lamports, want) {
		t.Errorf("lamports = %v, want %v (distinct per commit)", lamports, want)
	}
}

func TestAppendMissingRef(t *testing.T) {
	s := initStore(t)
	ref := refs.Note(model.EntityID(strings.Repeat("ab", 20)))
	if _, err := s.Append(t.Context(), ref, []model.Op{model.AddTag{Tag: "x"}}); !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Fatalf("Append = %v, want ErrRefNotFound", err)
	}
}

func TestAppendRejectsDuplicateCreate(t *testing.T) {
	s := initStore(t)
	note := create(t, s, noteOps("v1")).(model.Note)
	ref := refs.Note(note.ID)

	if _, err := s.Append(t.Context(), ref, noteOps("again")); !errors.Is(err, fold.ErrDuplicateCreate) {
		t.Fatalf("Append = %v, want ErrDuplicateCreate", err)
	}
	tip, err := s.Repo.Tip(t.Context(), ref)
	if err != nil {
		t.Fatalf("Tip: %v", err)
	}
	if tip != note.Head {
		t.Errorf("rejected append moved ref to %s, want %s", tip, note.Head)
	}
}

// TestAppendContended pins the retry-exhaustion path: the doctored store
// reads tips from the real repository but CASes refs in a second repository
// (sharing objects via an alternates link) whose ref permanently disagrees,
// so every attempt loses and Append must wrap ErrContended.
func TestAppendContended(t *testing.T) {
	s := initStore(t)
	note := create(t, s, noteOps("victim")).(model.Note)
	decoy := create(t, s, noteOps("decoy")).(model.Note)
	ref := refs.Note(note.ID)

	blocked := t.TempDir()
	gittest.Git(t, blocked, "init", "-q", "-b", "main")
	gittest.Git(t, blocked, "config", "user.name", testName)
	gittest.Git(t, blocked, "config", "user.email", testEmail)
	alternates := filepath.Join(blocked, ".git", "objects", "info", "alternates")
	if err := os.WriteFile(alternates, []byte(filepath.Join(s.Git.Dir, ".git", "objects")+"\n"), 0o600); err != nil {
		t.Fatalf("write alternates: %v", err)
	}
	gittest.Git(t, blocked, "update-ref", ref, string(decoy.ID))

	doctored := &Store{Repo: s.Repo, Git: gitcmd.Git{Dir: blocked}, now: time.Now}
	_, err := doctored.Append(t.Context(), ref, []model.Op{model.AddTag{Tag: "x"}})
	if !errors.Is(err, ErrContended) {
		t.Fatalf("Append = %v, want ErrContended", err)
	}
	if !errors.Is(err, gitcmd.ErrCASMismatch) {
		t.Errorf("Append = %v, want wrapped ErrCASMismatch", err)
	}
}

func TestLoadMissingRef(t *testing.T) {
	s := initStore(t)
	ref := refs.Note(model.EntityID(strings.Repeat("ab", 20)))
	if _, err := s.Load(t.Context(), ref); !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Fatalf("Load = %v, want ErrRefNotFound", err)
	}
}

func TestListNotesDeleted(t *testing.T) {
	s := initStore(t)
	keep := create(t, s, noteOps("keep")).(model.Note)
	gone := create(t, s, noteOps("gone")).(model.Note)

	snapshot, err := s.Append(t.Context(), refs.Note(gone.ID), []model.Op{model.DeleteNote{}})
	if err != nil {
		t.Fatalf("Append delete: %v", err)
	}
	deleted := snapshot.(model.Note)
	if !deleted.Deleted {
		t.Fatal("snapshot after delete_note is not Deleted")
	}

	live, err := s.ListNotes(t.Context(), false, false)
	if err != nil {
		t.Fatalf("ListNotes(false): %v", err)
	}
	if want := []model.Note{keep}; !reflect.DeepEqual(live, want) {
		t.Errorf("ListNotes(false) = %+v, want %+v", live, want)
	}

	all, err := s.ListNotes(t.Context(), true, false)
	if err != nil {
		t.Fatalf("ListNotes(true): %v", err)
	}
	want := []model.Note{keep, deleted}
	slices.SortFunc(want, chronoNotes)
	if !reflect.DeepEqual(all, want) {
		t.Errorf("ListNotes(true) = %+v, want %+v", all, want)
	}
}

func TestListNotesSuperseded(t *testing.T) {
	s := initStore(t)
	keep := create(t, s, noteOps("keep")).(model.Note)
	old := create(t, s, noteOps("old")).(model.Note)

	snapshot, err := s.Append(t.Context(), refs.Note(old.ID), []model.Op{model.AddSupersededBy{ID: keep.ID}})
	if err != nil {
		t.Fatalf("Append supersede: %v", err)
	}
	superseded := snapshot.(model.Note)
	if want := []model.EntityID{keep.ID}; !slices.Equal(superseded.SupersededBy, want) {
		t.Fatalf("SupersededBy = %v, want %v", superseded.SupersededBy, want)
	}

	live, err := s.ListNotes(t.Context(), false, false)
	if err != nil {
		t.Fatalf("ListNotes(false, false): %v", err)
	}
	if want := []model.Note{keep}; !reflect.DeepEqual(live, want) {
		t.Errorf("ListNotes(false, false) = %+v, want only keep", live)
	}

	all, err := s.ListNotes(t.Context(), false, true)
	if err != nil {
		t.Fatalf("ListNotes(false, true): %v", err)
	}
	want := []model.Note{keep, superseded}
	slices.SortFunc(want, chronoNotes)
	if !reflect.DeepEqual(all, want) {
		t.Errorf("ListNotes(false, true) = %+v, want both", all)
	}
}

func TestVerifyNoteKeepsEntityID(t *testing.T) {
	s := initStore(t)
	note := create(t, s, noteOps("design")).(model.Note)
	rootID := note.ID
	if note.VerifiedAt != 0 {
		t.Fatalf("fresh create VerifiedAt = %d, want 0", note.VerifiedAt)
	}

	witness := []model.AnchorWitness{{Anchor: model.Anchor{Kind: model.AnchorCommit, Value: "abc1234"}, OID: "abc1234"}}
	snapshot, err := s.Append(t.Context(), refs.Note(rootID), []model.Op{model.VerifyNote{Witness: witness, VerifiedCommit: "deadbeef"}})
	if err != nil {
		t.Fatalf("Append verify: %v", err)
	}
	verified := snapshot.(model.Note)
	if verified.ID != rootID {
		t.Errorf("verify changed id: %s, want root %s", verified.ID, rootID)
	}
	if verified.VerifiedAt == 0 || verified.VerifiedBy != testActor || verified.VerifiedCommit != "deadbeef" {
		t.Errorf("verify fields = %d/%q/%q, want set/%q/deadbeef", verified.VerifiedAt, verified.VerifiedBy, verified.VerifiedCommit, testActor)
	}
	if !reflect.DeepEqual(verified.Witness, witness) {
		t.Errorf("Witness = %+v, want %+v", verified.Witness, witness)
	}
	if verified.Head == model.SHA(rootID) {
		t.Errorf("Head = %s, want the second commit, not the root", verified.Head)
	}
}

func TestListTasksReturnsAll(t *testing.T) {
	s := initStore(t)
	create(t, s, taskOps("on main", "main"))
	create(t, s, taskOps("on release", "release"))
	create(t, s, taskOps("backlog", ""))

	list, err := s.ListTasks(t.Context())
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("ListTasks returned %d tasks, want 3", len(list))
	}
	branches := map[model.Branch]bool{}
	for _, task := range list {
		branches[task.Branch] = true
	}
	for _, want := range []model.Branch{"main", "release", ""} {
		if !branches[want] {
			t.Errorf("ListTasks missing a task on branch %q: %+v", want, list)
		}
	}
}

func TestResolve(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	a := create(t, s, noteOps("alpha")).(model.Note)
	b := create(t, s, noteOps("beta")).(model.Note)
	task := create(t, s, taskOps("gamma", "main")).(model.Task)

	shared := 0
	for shared < len(a.ID) && a.ID[shared] == b.ID[shared] {
		shared++
	}
	unique := string(a.ID)[:shared+1]

	got, err := s.Resolve(ctx, refs.KindNote, unique)
	if err != nil {
		t.Fatalf("Resolve unique: %v", err)
	}
	if want := refs.Note(a.ID); got != want {
		t.Errorf("Resolve(%q) = %q, want %q", unique, got, want)
	}

	got, err = s.Resolve(ctx, refs.KindNote, strings.ToUpper(unique))
	if err != nil {
		t.Fatalf("Resolve uppercase: %v", err)
	}
	if want := refs.Note(a.ID); got != want {
		t.Errorf("Resolve(upper %q) = %q, want %q", unique, got, want)
	}

	if _, err := s.Resolve(ctx, refs.KindNote, "zzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(zzz) = %v, want ErrNotFound", err)
	}

	got, err = s.Resolve(ctx, refs.KindTask, string(task.ID))
	if err != nil {
		t.Fatalf("Resolve task: %v", err)
	}
	if want := refs.Task(task.ID); got != want {
		t.Errorf("Resolve task = %q, want %q", got, want)
	}

	if _, err := s.Resolve(ctx, refs.KindTask, string(a.ID)); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve note id as task = %v, want ErrNotFound", err)
	}
}

func TestResolveAmbiguous(t *testing.T) {
	s := initStore(t)
	titles := map[model.EntityID]string{}
	buckets := map[byte][]model.EntityID{}
	var shared []model.EntityID
	// 16 hex buckets: the pigeonhole principle guarantees a first-character
	// collision within 17 creates.
	for i := 0; len(shared) == 0; i++ {
		if i > 17 {
			t.Fatal("no shared 1-char prefix after 17 creates")
		}
		title := fmt.Sprintf("note-%d", i)
		note := create(t, s, noteOps(title)).(model.Note)
		titles[note.ID] = title
		first := note.ID[0]
		buckets[first] = append(buckets[first], note.ID)
		if len(buckets[first]) == 2 {
			shared = buckets[first]
		}
	}

	prefix := string(shared[0])[:1]
	_, err := s.Resolve(t.Context(), refs.KindNote, prefix)
	if !errors.Is(err, ErrAmbiguous) {
		t.Fatalf("Resolve(%q) = %v, want ErrAmbiguous", prefix, err)
	}
	var ambiguous *AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("Resolve(%q) = %v, want *AmbiguousError", prefix, err)
	}
	if ambiguous.Kind != refs.KindNote || ambiguous.Prefix != prefix {
		t.Errorf("AmbiguousError = %+v, want kind note prefix %q", ambiguous, prefix)
	}

	slices.Sort(shared)
	want := []Candidate{
		{ID: shared[0], Title: titles[shared[0]]},
		{ID: shared[1], Title: titles[shared[1]]},
	}
	if !reflect.DeepEqual(ambiguous.Candidates, want) {
		t.Errorf("Candidates = %+v, want %+v", ambiguous.Candidates, want)
	}
	for _, c := range want {
		if !strings.Contains(ambiguous.Error(), c.ID.Short()) || !strings.Contains(ambiguous.Error(), c.Title) {
			t.Errorf("Error() = %q, missing candidate %s %q", ambiguous.Error(), c.ID.Short(), c.Title)
		}
	}
}

func TestCreateDocRoundTrip(t *testing.T) {
	s := initStore(t)
	ops := []model.Op{model.CreateDoc{Nonce: model.NewNonce(), Title: "hello", Body: "world", When: "before refactoring auth", Tags: []string{"b", "a"}}}
	snapshot := create(t, s, ops)
	doc, ok := snapshot.(model.Doc)
	if !ok {
		t.Fatalf("Create returned %T, want model.Doc", snapshot)
	}

	if doc.Title != "hello" || doc.Body != "world" {
		t.Errorf("doc = %q/%q, want hello/world", doc.Title, doc.Body)
	}
	if doc.When != "before refactoring auth" {
		t.Errorf("When = %q, want %q", doc.When, "before refactoring auth")
	}
	if want := []string{"a", "b"}; !slices.Equal(doc.Tags, want) {
		t.Errorf("Tags = %v, want %v", doc.Tags, want)
	}
	if doc.Author != testActor {
		t.Errorf("Author = %q, want %q", doc.Author, testActor)
	}
	if doc.Deleted {
		t.Error("fresh doc is Deleted")
	}
	if doc.Head != model.SHA(doc.ID) {
		t.Errorf("Head = %s, want root %s", doc.Head, doc.ID)
	}
	if doc.CreatedAt == 0 || doc.UpdatedAt != doc.CreatedAt {
		t.Errorf("timestamps = %d/%d, want equal non-zero", doc.CreatedAt, doc.UpdatedAt)
	}

	ref := refs.Doc(doc.ID)
	loaded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded, snapshot) {
		t.Errorf("Load = %+v, want Create snapshot %+v", loaded, snapshot)
	}

	if msg := gittest.Git(t, s.Git.Dir, "log", "-1", "--format=%s", ref); msg != "cc-notes: doc create" {
		t.Errorf("commit message = %q, want %q", msg, "cc-notes: doc create")
	}

	list, err := s.ListDocs(t.Context(), false, false)
	if err != nil {
		t.Fatalf("ListDocs: %v", err)
	}
	if want := []model.Doc{doc}; !reflect.DeepEqual(list, want) {
		t.Errorf("ListDocs = %+v, want %+v", list, want)
	}
}

func TestSetWhenUpdatesDoc(t *testing.T) {
	s := initStore(t)
	doc := create(t, s, docOps("trigger")).(model.Doc)
	ref := refs.Doc(doc.ID)

	snapshot, err := s.Append(t.Context(), ref, []model.Op{model.SetWhen{When: "after deploy fails"}})
	if err != nil {
		t.Fatalf("Append SetWhen: %v", err)
	}
	updated := snapshot.(model.Doc)
	if updated.When != "after deploy fails" {
		t.Errorf("When = %q, want %q", updated.When, "after deploy fails")
	}
	if updated.ID != doc.ID {
		t.Errorf("ID changed across SetWhen: %s -> %s", doc.ID, updated.ID)
	}
}

func TestListDocsDeleted(t *testing.T) {
	s := initStore(t)
	keep := create(t, s, docOps("keep")).(model.Doc)
	gone := create(t, s, docOps("gone")).(model.Doc)

	snapshot, err := s.Append(t.Context(), refs.Doc(gone.ID), []model.Op{model.DeleteNote{}})
	if err != nil {
		t.Fatalf("Append delete: %v", err)
	}
	deleted := snapshot.(model.Doc)
	if !deleted.Deleted {
		t.Fatal("snapshot after delete_note is not Deleted")
	}

	live, err := s.ListDocs(t.Context(), false, false)
	if err != nil {
		t.Fatalf("ListDocs(false): %v", err)
	}
	if want := []model.Doc{keep}; !reflect.DeepEqual(live, want) {
		t.Errorf("ListDocs(false) = %+v, want %+v", live, want)
	}

	all, err := s.ListDocs(t.Context(), true, false)
	if err != nil {
		t.Fatalf("ListDocs(true): %v", err)
	}
	want := []model.Doc{keep, deleted}
	slices.SortFunc(want, chronoDocs)
	if !reflect.DeepEqual(all, want) {
		t.Errorf("ListDocs(true) = %+v, want %+v", all, want)
	}
}

func TestListDocsSuperseded(t *testing.T) {
	s := initStore(t)
	keep := create(t, s, docOps("keep")).(model.Doc)
	old := create(t, s, docOps("old")).(model.Doc)

	snapshot, err := s.Append(t.Context(), refs.Doc(old.ID), []model.Op{model.AddSupersededBy{ID: keep.ID}})
	if err != nil {
		t.Fatalf("Append supersede: %v", err)
	}
	superseded := snapshot.(model.Doc)
	if want := []model.EntityID{keep.ID}; !slices.Equal(superseded.SupersededBy, want) {
		t.Fatalf("SupersededBy = %v, want %v", superseded.SupersededBy, want)
	}

	live, err := s.ListDocs(t.Context(), false, false)
	if err != nil {
		t.Fatalf("ListDocs(false, false): %v", err)
	}
	if want := []model.Doc{keep}; !reflect.DeepEqual(live, want) {
		t.Errorf("ListDocs(false, false) = %+v, want only keep", live)
	}

	all, err := s.ListDocs(t.Context(), false, true)
	if err != nil {
		t.Fatalf("ListDocs(false, true): %v", err)
	}
	want := []model.Doc{keep, superseded}
	slices.SortFunc(want, chronoDocs)
	if !reflect.DeepEqual(all, want) {
		t.Errorf("ListDocs(false, true) = %+v, want both", all)
	}
}

func TestListDocsSorted(t *testing.T) {
	s := initStore(t)
	s.now = ticker(200, 200, 100)
	a := create(t, s, docOps("a")).(model.Doc)
	b := create(t, s, docOps("b")).(model.Doc)
	c := create(t, s, docOps("c")).(model.Doc)

	if c.CreatedAt != 100 || a.CreatedAt != 200 || b.CreatedAt != 200 {
		t.Fatalf("CreatedAt = %d/%d/%d, want 200/200/100", a.CreatedAt, b.CreatedAt, c.CreatedAt)
	}

	list, err := s.ListDocs(t.Context(), false, false)
	if err != nil {
		t.Fatalf("ListDocs: %v", err)
	}
	want := []model.Doc{a, b, c}
	slices.SortFunc(want, chronoDocs)
	if !reflect.DeepEqual(list, want) {
		t.Errorf("ListDocs = %+v, want %+v (CreatedAt then id)", list, want)
	}
	if list[0].ID != c.ID {
		t.Errorf("first = %s, want earliest %s", list[0].ID, c.ID)
	}
}

func TestResolveDoc(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	a := create(t, s, docOps("alpha")).(model.Doc)
	b := create(t, s, docOps("beta")).(model.Doc)
	note := create(t, s, noteOps("gamma")).(model.Note)

	shared := 0
	for shared < len(a.ID) && a.ID[shared] == b.ID[shared] {
		shared++
	}
	unique := string(a.ID)[:shared+1]

	got, err := s.Resolve(ctx, refs.KindDoc, unique)
	if err != nil {
		t.Fatalf("Resolve unique: %v", err)
	}
	if want := refs.Doc(a.ID); got != want {
		t.Errorf("Resolve(%q) = %q, want %q", unique, got, want)
	}

	got, err = s.Resolve(ctx, refs.KindDoc, strings.ToUpper(unique))
	if err != nil {
		t.Fatalf("Resolve uppercase: %v", err)
	}
	if want := refs.Doc(a.ID); got != want {
		t.Errorf("Resolve(upper %q) = %q, want %q", unique, got, want)
	}

	got, err = s.Resolve(ctx, refs.KindDoc, string(b.ID))
	if err != nil {
		t.Fatalf("Resolve full id: %v", err)
	}
	if want := refs.Doc(b.ID); got != want {
		t.Errorf("Resolve(%q) = %q, want %q", b.ID, got, want)
	}

	if _, err := s.Resolve(ctx, refs.KindDoc, "zzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(zzz) = %v, want ErrNotFound", err)
	}

	if _, err := s.Resolve(ctx, refs.KindDoc, string(note.ID)); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve note id as doc = %v, want ErrNotFound", err)
	}
}

func chronoLogs(a, b model.Log) int {
	if c := a.CreatedAt - b.CreatedAt; c != 0 {
		return int(c)
	}
	return strings.Compare(string(a.ID), string(b.ID))
}

func TestCreateLogRoundTrip(t *testing.T) {
	s := initStore(t)
	ops := []model.Op{model.CreateLog{Nonce: model.NewNonce(), Title: "rollout", Tags: []string{"b", "a"}}}
	snapshot := create(t, s, ops)
	log, ok := snapshot.(model.Log)
	if !ok {
		t.Fatalf("Create returned %T, want model.Log", snapshot)
	}

	if log.Title != "rollout" {
		t.Errorf("Title = %q, want rollout", log.Title)
	}
	if want := []string{"a", "b"}; !slices.Equal(log.Tags, want) {
		t.Errorf("Tags = %v, want %v", log.Tags, want)
	}
	if log.Author != testActor {
		t.Errorf("Author = %q, want %q", log.Author, testActor)
	}
	if log.Deleted {
		t.Error("fresh log is Deleted")
	}
	if log.Entries == nil || len(log.Entries) != 0 {
		t.Errorf("Entries = %+v, want non-nil empty", log.Entries)
	}
	if log.Head != model.SHA(log.ID) {
		t.Errorf("Head = %s, want root %s", log.Head, log.ID)
	}
	if log.CreatedAt == 0 || log.UpdatedAt != log.CreatedAt {
		t.Errorf("timestamps = %d/%d, want equal non-zero", log.CreatedAt, log.UpdatedAt)
	}

	ref := refs.Log(log.ID)
	if msg := gittest.Git(t, s.Git.Dir, "log", "-1", "--format=%s", ref); msg != "cc-notes: log create" {
		t.Errorf("commit message = %q, want %q", msg, "cc-notes: log create")
	}

	// Two appended entries take their author and timestamp from the carrying
	// commit, in linearization order.
	if _, err := s.Append(t.Context(), ref, []model.Op{model.AppendEntry{Text: "flipped to 5%"}}); err != nil {
		t.Fatalf("Append first entry: %v", err)
	}
	snapshot, err := s.Append(t.Context(), ref, []model.Op{model.AppendEntry{Text: "flipped to 50%"}})
	if err != nil {
		t.Fatalf("Append second entry: %v", err)
	}
	appended := snapshot.(model.Log)
	if appended.ID != log.ID {
		t.Errorf("ID changed across appends: %s -> %s", log.ID, appended.ID)
	}
	if len(appended.Entries) != 2 {
		t.Fatalf("Entries len = %d, want 2", len(appended.Entries))
	}
	if appended.Entries[0].Text != "flipped to 5%" || appended.Entries[1].Text != "flipped to 50%" {
		t.Errorf("entry order = %q/%q, want flipped to 5%%/flipped to 50%%", appended.Entries[0].Text, appended.Entries[1].Text)
	}
	for i, e := range appended.Entries {
		if e.Author != testActor {
			t.Errorf("Entries[%d].Author = %q, want %q", i, e.Author, testActor)
		}
		if e.TS == 0 {
			t.Errorf("Entries[%d].TS = 0, want non-zero commit time", i)
		}
	}

	loaded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded, snapshot) {
		t.Errorf("Load = %+v, want Append snapshot %+v", loaded, snapshot)
	}

	list, err := s.ListLogs(t.Context(), false)
	if err != nil {
		t.Fatalf("ListLogs: %v", err)
	}
	if want := []model.Log{appended}; !reflect.DeepEqual(list, want) {
		t.Errorf("ListLogs = %+v, want %+v", list, want)
	}
}

func TestListLogsDeleted(t *testing.T) {
	s := initStore(t)
	keep := create(t, s, logOps("keep")).(model.Log)
	gone := create(t, s, logOps("gone")).(model.Log)

	snapshot, err := s.Append(t.Context(), refs.Log(gone.ID), []model.Op{model.DeleteNote{}})
	if err != nil {
		t.Fatalf("Append delete: %v", err)
	}
	deleted := snapshot.(model.Log)
	if !deleted.Deleted {
		t.Fatal("snapshot after delete_note is not Deleted")
	}

	live, err := s.ListLogs(t.Context(), false)
	if err != nil {
		t.Fatalf("ListLogs(false): %v", err)
	}
	if want := []model.Log{keep}; !reflect.DeepEqual(live, want) {
		t.Errorf("ListLogs(false) = %+v, want %+v", live, want)
	}

	all, err := s.ListLogs(t.Context(), true)
	if err != nil {
		t.Fatalf("ListLogs(true): %v", err)
	}
	want := []model.Log{keep, deleted}
	slices.SortFunc(want, chronoLogs)
	if !reflect.DeepEqual(all, want) {
		t.Errorf("ListLogs(true) = %+v, want %+v", all, want)
	}
}

func TestResolveLog(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	a := create(t, s, logOps("alpha")).(model.Log)
	b := create(t, s, logOps("beta")).(model.Log)
	note := create(t, s, noteOps("gamma")).(model.Note)

	shared := 0
	for shared < len(a.ID) && a.ID[shared] == b.ID[shared] {
		shared++
	}
	unique := string(a.ID)[:shared+1]

	got, err := s.Resolve(ctx, refs.KindLog, unique)
	if err != nil {
		t.Fatalf("Resolve unique: %v", err)
	}
	if want := refs.Log(a.ID); got != want {
		t.Errorf("Resolve(%q) = %q, want %q", unique, got, want)
	}

	got, err = s.Resolve(ctx, refs.KindLog, string(b.ID))
	if err != nil {
		t.Fatalf("Resolve full id: %v", err)
	}
	if want := refs.Log(b.ID); got != want {
		t.Errorf("Resolve(%q) = %q, want %q", b.ID, got, want)
	}

	if _, err := s.Resolve(ctx, refs.KindLog, "zzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(zzz) = %v, want ErrNotFound", err)
	}

	if _, err := s.Resolve(ctx, refs.KindLog, string(note.ID)); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve note id as log = %v, want ErrNotFound", err)
	}
}

func TestActorOverride(t *testing.T) {
	s := initStore(t)
	t.Setenv(actorEnv, "Robo Agent <robo@example.com>")

	note := create(t, s, noteOps("by robot")).(model.Note)
	if want := model.Actor("Robo Agent <robo@example.com>"); note.Author != want {
		t.Errorf("Author = %q, want %q", note.Author, want)
	}

	loaded, err := s.Load(t.Context(), refs.Note(note.ID))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.(model.Note).Author != note.Author {
		t.Errorf("persisted Author = %q, want %q", loaded.(model.Note).Author, note.Author)
	}
}

func TestActorMalformed(t *testing.T) {
	for _, value := range []string{"", "garbage", "<robo@example.com>", "Robo <>", "Robo <robo@example.com> tail"} {
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			s := initStore(t)
			t.Setenv(actorEnv, value)
			_, err := s.Create(t.Context(), noteOps("x"))
			if err == nil || !strings.Contains(err.Error(), actorEnv) {
				t.Fatalf("Create = %v, want loud %s parse error", err, actorEnv)
			}
		})
	}
}

func TestMerge(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	note := create(t, s, noteOps("base")).(model.Note)
	ref := refs.Note(note.ID)

	snapshot, err := s.Append(ctx, ref, []model.Op{model.SetTitle{Title: "ours"}})
	if err != nil {
		t.Fatalf("Append: %v", err)
	}
	ours := snapshot.(model.Note).Head

	sig := gitobj.Signature{Name: testName, Email: testEmail, When: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	theirs, err := s.Repo.WriteOpsCommit(ctx, []model.SHA{model.SHA(note.ID)}, sig, "cc-notes: add_tag",
		model.Pack{Lamport: 2, Ops: []model.Op{model.AddTag{Tag: "remote"}}})
	if err != nil {
		t.Fatalf("WriteOpsCommit theirs: %v", err)
	}

	merge, err := s.Merge(ctx, ref, ours, theirs)
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	tip, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		t.Fatalf("Tip: %v", err)
	}
	if tip != merge {
		t.Errorf("ref at %s, want merge %s", tip, merge)
	}

	loaded, err := s.Load(ctx, ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	merged := loaded.(model.Note)
	if merged.Title != "ours" {
		t.Errorf("Title = %q, want ours", merged.Title)
	}
	if want := []string{"remote"}; !slices.Equal(merged.Tags, want) {
		t.Errorf("Tags = %v, want %v", merged.Tags, want)
	}

	chain, err := s.Repo.ReadChain(ctx, merge)
	if err != nil {
		t.Fatalf("ReadChain: %v", err)
	}
	head := chain[0]
	if head.SHA != merge {
		t.Fatalf("chain head = %s, want merge %s", head.SHA, merge)
	}
	if want := []model.SHA{ours, theirs}; !slices.Equal(head.Parents, want) {
		t.Errorf("merge parents = %v, want %v", head.Parents, want)
	}
	if head.Pack.Lamport != 3 || len(head.Pack.Ops) != 0 {
		t.Errorf("merge pack = lamport %d with %d ops, want lamport 3 with 0 ops", head.Pack.Lamport, len(head.Pack.Ops))
	}

	if _, err := s.Merge(ctx, ref, ours, theirs); !errors.Is(err, gitcmd.ErrCASMismatch) {
		t.Errorf("stale Merge = %v, want ErrCASMismatch", err)
	}
}

func TestCreateSprintRoundTrip(t *testing.T) {
	s := initStore(t)
	ops := []model.Op{model.CreateSprint{Nonce: model.NewNonce(), Title: "Q3", Description: "third quarter", Labels: []string{"b", "a"}}}
	snapshot := create(t, s, ops)
	sprint, ok := snapshot.(model.Sprint)
	if !ok {
		t.Fatalf("Create returned %T, want model.Sprint", snapshot)
	}

	if sprint.Title != "Q3" || sprint.Description != "third quarter" {
		t.Errorf("sprint = %q/%q, want Q3/third quarter", sprint.Title, sprint.Description)
	}
	if sprint.Status != model.SprintPlanned {
		t.Errorf("Status = %q, want %q", sprint.Status, model.SprintPlanned)
	}
	if want := []string{"a", "b"}; !slices.Equal(sprint.Labels, want) {
		t.Errorf("Labels = %v, want %v", sprint.Labels, want)
	}
	if sprint.Author != testActor {
		t.Errorf("Author = %q, want %q", sprint.Author, testActor)
	}
	if sprint.Head != model.SHA(sprint.ID) {
		t.Errorf("Head = %s, want root %s", sprint.Head, sprint.ID)
	}
	if sprint.CreatedAt == 0 || sprint.UpdatedAt != sprint.CreatedAt {
		t.Errorf("timestamps = %d/%d, want equal non-zero", sprint.CreatedAt, sprint.UpdatedAt)
	}

	ref := refs.Sprint(sprint.ID)
	if got := gittest.Git(t, s.Git.Dir, "rev-parse", ref); got != string(sprint.ID) {
		t.Errorf("ref %s -> %s, want %s", ref, got, sprint.ID)
	}
	loaded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded, snapshot) {
		t.Errorf("Load = %+v, want Create snapshot %+v", loaded, snapshot)
	}
	if msg := gittest.Git(t, s.Git.Dir, "log", "-1", "--format=%s", ref); msg != "cc-notes: sprint create" {
		t.Errorf("commit message = %q, want %q", msg, "cc-notes: sprint create")
	}

	list, err := s.ListSprints(t.Context())
	if err != nil {
		t.Fatalf("ListSprints: %v", err)
	}
	if want := []model.Sprint{sprint}; !reflect.DeepEqual(list, want) {
		t.Errorf("ListSprints = %+v, want %+v", list, want)
	}
}

func TestCreateProjectRoundTrip(t *testing.T) {
	s := initStore(t)
	ops := []model.Op{model.CreateProject{Nonce: model.NewNonce(), Title: "Platform", Description: "infra work", Labels: []string{"y", "x"}}}
	snapshot := create(t, s, ops)
	project, ok := snapshot.(model.Project)
	if !ok {
		t.Fatalf("Create returned %T, want model.Project", snapshot)
	}

	if project.Title != "Platform" || project.Description != "infra work" {
		t.Errorf("project = %q/%q, want Platform/infra work", project.Title, project.Description)
	}
	if project.Status != model.ProjectActive {
		t.Errorf("Status = %q, want %q", project.Status, model.ProjectActive)
	}
	if want := []string{"x", "y"}; !slices.Equal(project.Labels, want) {
		t.Errorf("Labels = %v, want %v", project.Labels, want)
	}
	if project.Author != testActor {
		t.Errorf("Author = %q, want %q", project.Author, testActor)
	}
	if project.Head != model.SHA(project.ID) {
		t.Errorf("Head = %s, want root %s", project.Head, project.ID)
	}
	if project.CreatedAt == 0 || project.UpdatedAt != project.CreatedAt {
		t.Errorf("timestamps = %d/%d, want equal non-zero", project.CreatedAt, project.UpdatedAt)
	}

	ref := refs.Project(project.ID)
	if got := gittest.Git(t, s.Git.Dir, "rev-parse", ref); got != string(project.ID) {
		t.Errorf("ref %s -> %s, want %s", ref, got, project.ID)
	}
	loaded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded, snapshot) {
		t.Errorf("Load = %+v, want Create snapshot %+v", loaded, snapshot)
	}
	if msg := gittest.Git(t, s.Git.Dir, "log", "-1", "--format=%s", ref); msg != "cc-notes: project create" {
		t.Errorf("commit message = %q, want %q", msg, "cc-notes: project create")
	}

	list, err := s.ListProjects(t.Context())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if want := []model.Project{project}; !reflect.DeepEqual(list, want) {
		t.Errorf("ListProjects = %+v, want %+v", list, want)
	}
}

// ticker returns a clock that yields ts[i] on the i-th call, pinning the
// per-create CreatedAt so list ordering is deterministic. The first two ticks
// collide to exercise the id tiebreaker.
func ticker(ts ...int64) func() time.Time {
	i := 0
	return func() time.Time {
		t := time.Unix(ts[i], 0).UTC()
		i++
		return t
	}
}

func TestListSprintsSorted(t *testing.T) {
	s := initStore(t)
	s.now = ticker(200, 200, 100)
	a := create(t, s, sprintOps("a")).(model.Sprint)
	b := create(t, s, sprintOps("b")).(model.Sprint)
	c := create(t, s, sprintOps("c")).(model.Sprint)

	if c.CreatedAt != 100 || a.CreatedAt != 200 || b.CreatedAt != 200 {
		t.Fatalf("CreatedAt = %d/%d/%d, want 200/200/100", a.CreatedAt, b.CreatedAt, c.CreatedAt)
	}

	list, err := s.ListSprints(t.Context())
	if err != nil {
		t.Fatalf("ListSprints: %v", err)
	}
	want := []model.Sprint{a, b, c}
	slices.SortFunc(want, chronoSprints)
	if !reflect.DeepEqual(list, want) {
		t.Errorf("ListSprints = %+v, want %+v (CreatedAt then id)", list, want)
	}
	if list[0].ID != c.ID {
		t.Errorf("first = %s, want earliest %s", list[0].ID, c.ID)
	}
}

func TestListProjectsSorted(t *testing.T) {
	s := initStore(t)
	s.now = ticker(200, 200, 100)
	a := create(t, s, projectOps("a")).(model.Project)
	b := create(t, s, projectOps("b")).(model.Project)
	c := create(t, s, projectOps("c")).(model.Project)

	if c.CreatedAt != 100 || a.CreatedAt != 200 || b.CreatedAt != 200 {
		t.Fatalf("CreatedAt = %d/%d/%d, want 200/200/100", a.CreatedAt, b.CreatedAt, c.CreatedAt)
	}

	list, err := s.ListProjects(t.Context())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	want := []model.Project{a, b, c}
	slices.SortFunc(want, chronoProjects)
	if !reflect.DeepEqual(list, want) {
		t.Errorf("ListProjects = %+v, want %+v (CreatedAt then id)", list, want)
	}
	if list[0].ID != c.ID {
		t.Errorf("first = %s, want earliest %s", list[0].ID, c.ID)
	}
}

func TestResolveSprint(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	titles := map[model.EntityID]string{}
	buckets := map[byte][]model.EntityID{}
	var shared []model.EntityID
	for i := 0; len(shared) == 0; i++ {
		if i > 17 {
			t.Fatal("no shared 1-char prefix after 17 creates")
		}
		title := fmt.Sprintf("sprint-%d", i)
		sprint := create(t, s, sprintOps(title)).(model.Sprint)
		titles[sprint.ID] = title
		first := sprint.ID[0]
		buckets[first] = append(buckets[first], sprint.ID)
		if len(buckets[first]) == 2 {
			shared = buckets[first]
		}
	}

	full := shared[0]
	got, err := s.Resolve(ctx, refs.KindSprint, string(full))
	if err != nil {
		t.Fatalf("Resolve(%q): %v", full, err)
	}
	if want := refs.Sprint(full); got != want {
		t.Errorf("Resolve(%q) = %q, want %q", full, got, want)
	}

	if _, err := s.Resolve(ctx, refs.KindSprint, "zzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(zzz) = %v, want ErrNotFound", err)
	}

	prefix := string(shared[0])[:1]
	_, err = s.Resolve(ctx, refs.KindSprint, prefix)
	var ambiguous *AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("Resolve(%q) = %v, want *AmbiguousError", prefix, err)
	}
	if ambiguous.Kind != refs.KindSprint || ambiguous.Prefix != prefix {
		t.Errorf("AmbiguousError = %+v, want kind sprint prefix %q", ambiguous, prefix)
	}
	slices.Sort(shared)
	want := []Candidate{
		{ID: shared[0], Title: titles[shared[0]]},
		{ID: shared[1], Title: titles[shared[1]]},
	}
	if !reflect.DeepEqual(ambiguous.Candidates, want) {
		t.Errorf("Candidates = %+v, want %+v", ambiguous.Candidates, want)
	}
}

func TestResolveProject(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	titles := map[model.EntityID]string{}
	buckets := map[byte][]model.EntityID{}
	var shared []model.EntityID
	for i := 0; len(shared) == 0; i++ {
		if i > 17 {
			t.Fatal("no shared 1-char prefix after 17 creates")
		}
		title := fmt.Sprintf("project-%d", i)
		project := create(t, s, projectOps(title)).(model.Project)
		titles[project.ID] = title
		first := project.ID[0]
		buckets[first] = append(buckets[first], project.ID)
		if len(buckets[first]) == 2 {
			shared = buckets[first]
		}
	}

	full := shared[0]
	got, err := s.Resolve(ctx, refs.KindProject, string(full))
	if err != nil {
		t.Fatalf("Resolve(%q): %v", full, err)
	}
	if want := refs.Project(full); got != want {
		t.Errorf("Resolve(%q) = %q, want %q", full, got, want)
	}

	if _, err := s.Resolve(ctx, refs.KindProject, "zzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(zzz) = %v, want ErrNotFound", err)
	}

	prefix := string(shared[0])[:1]
	_, err = s.Resolve(ctx, refs.KindProject, prefix)
	var ambiguous *AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("Resolve(%q) = %v, want *AmbiguousError", prefix, err)
	}
	if ambiguous.Kind != refs.KindProject || ambiguous.Prefix != prefix {
		t.Errorf("AmbiguousError = %+v, want kind project prefix %q", ambiguous, prefix)
	}
	slices.Sort(shared)
	want := []Candidate{
		{ID: shared[0], Title: titles[shared[0]]},
		{ID: shared[1], Title: titles[shared[1]]},
	}
	if !reflect.DeepEqual(ambiguous.Candidates, want) {
		t.Errorf("Candidates = %+v, want %+v", ambiguous.Candidates, want)
	}
}
