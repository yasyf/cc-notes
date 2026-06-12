// White-box tests: living in the package lets them freeze the Store clock,
// which the deterministic-id (contended create) tests need. Every test runs
// against a real git repository in t.TempDir().
package store

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
)

const (
	testName  = "Test User"
	testEmail = "test@example.com"
	testActor = model.Actor("Test User <test@example.com>")
)

// scrubGitEnv clears every git environment knob that could leak host state
// into a test and pins global/system config to /dev/null. t.Setenv with the
// original value registers the restore before os.Unsetenv removes the key.
func scrubGitEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_NAMESPACE", "GIT_CEILING_DIRECTORIES",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE",
		"GIT_EDITOR", "EMAIL", actorEnv,
	} {
		if value, ok := os.LookupEnv(key); ok {
			t.Setenv(key, value)
			os.Unsetenv(key)
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func initStore(t *testing.T) *Store {
	t.Helper()
	scrubGitEnv(t)
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	mustGit(t, dir, "config", "user.name", testName)
	mustGit(t, dir, "config", "user.email", testEmail)
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	return s
}

func noteOps(title string) []model.Op {
	return []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: title}}
}

func taskOps(title string, branch model.Branch) []model.Op {
	return []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: title, Type: model.TypeTask, Branch: branch}}
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

	if msg := mustGit(t, s.Git.Dir, "log", "-1", "--format=%s", ref); msg != "cc-notes: note create" {
		t.Errorf("commit message = %q, want %q", msg, "cc-notes: note create")
	}

	list, err := s.ListNotes(t.Context(), false)
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

	ref := refs.Task(branch, task.ID)
	loaded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(loaded, snapshot) {
		t.Errorf("Load = %+v, want Create snapshot %+v", loaded, snapshot)
	}
	if msg := mustGit(t, s.Git.Dir, "log", "-1", "--format=%s", ref); msg != "cc-notes: task create" {
		t.Errorf("commit message = %q, want %q", msg, "cc-notes: task create")
	}

	list, err := s.ListTasks(t.Context(), branch)
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	if want := []model.Task{task}; !reflect.DeepEqual(list, want) {
		t.Errorf("ListTasks(%s) = %+v, want %+v", branch, list, want)
	}

	parent, err := s.ListTasks(t.Context(), "feat")
	if err != nil {
		t.Fatalf("ListTasks(feat): %v", err)
	}
	if len(parent) != 0 {
		t.Errorf("ListTasks(feat) leaked sub-branch tasks: %+v", parent)
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
		{name: "task with empty branch", ops: []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: "x", Type: model.TypeTask}}},
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

func TestCreateContended(t *testing.T) {
	s := initStore(t)
	s.now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	ops := []model.Op{model.CreateNote{Nonce: strings.Repeat("ab", 16), Title: "dup"}}

	if _, err := s.Create(t.Context(), ops); err != nil {
		t.Fatalf("first create: %v", err)
	}
	if _, err := s.Create(t.Context(), ops); !errors.Is(err, gitcmd.ErrCASMismatch) {
		t.Fatalf("second create = %v, want ErrCASMismatch", err)
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

	if msg := mustGit(t, s.Git.Dir, "log", "-1", "--format=%s", ref); msg != "cc-notes: set_title add_tag" {
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
	mustGit(t, s.Git.Dir, "config", "core.filesRefLockTimeout", "3000")
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
	mustGit(t, blocked, "init", "-q", "-b", "main")
	mustGit(t, blocked, "config", "user.name", testName)
	mustGit(t, blocked, "config", "user.email", testEmail)
	alternates := filepath.Join(blocked, ".git", "objects", "info", "alternates")
	if err := os.WriteFile(alternates, []byte(filepath.Join(s.Git.Dir, ".git", "objects")+"\n"), 0o644); err != nil {
		t.Fatalf("write alternates: %v", err)
	}
	mustGit(t, blocked, "update-ref", ref, string(decoy.ID))

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

	live, err := s.ListNotes(t.Context(), false)
	if err != nil {
		t.Fatalf("ListNotes(false): %v", err)
	}
	if want := []model.Note{keep}; !reflect.DeepEqual(live, want) {
		t.Errorf("ListNotes(false) = %+v, want %+v", live, want)
	}

	all, err := s.ListNotes(t.Context(), true)
	if err != nil {
		t.Fatalf("ListNotes(true): %v", err)
	}
	want := []model.Note{keep, deleted}
	slices.SortFunc(want, chronoNotes)
	if !reflect.DeepEqual(all, want) {
		t.Errorf("ListNotes(true) = %+v, want %+v", all, want)
	}
}

func TestListTasksLiveness(t *testing.T) {
	s := initStore(t)
	promoted := create(t, s, taskOps("promote me", "main")).(model.Task)
	live := create(t, s, taskOps("stays", "main")).(model.Task)
	ref := refs.Task("main", promoted.ID)

	if _, err := s.Append(t.Context(), ref, []model.Op{model.Promote{From: "main", To: "release"}}); err != nil {
		t.Fatalf("Append promote: %v", err)
	}

	list, err := s.ListTasks(t.Context(), "main")
	if err != nil {
		t.Fatalf("ListTasks(main): %v", err)
	}
	if want := []model.Task{live}; !reflect.DeepEqual(list, want) {
		t.Errorf("ListTasks(main) = %+v, want only the live task %+v", list, want)
	}

	loaded, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load dead ref: %v", err)
	}
	if got := loaded.(model.Task).Branch; got != "release" {
		t.Errorf("dead ref folds to branch %q, want release", got)
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

	got, err := s.Resolve(ctx, refs.KindNote, "", unique)
	if err != nil {
		t.Fatalf("Resolve unique: %v", err)
	}
	if want := refs.Note(a.ID); got != want {
		t.Errorf("Resolve(%q) = %q, want %q", unique, got, want)
	}

	got, err = s.Resolve(ctx, refs.KindNote, "", strings.ToUpper(unique))
	if err != nil {
		t.Fatalf("Resolve uppercase: %v", err)
	}
	if want := refs.Note(a.ID); got != want {
		t.Errorf("Resolve(upper %q) = %q, want %q", unique, got, want)
	}

	if _, err := s.Resolve(ctx, refs.KindNote, "", "zzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve(zzz) = %v, want ErrNotFound", err)
	}

	got, err = s.Resolve(ctx, refs.KindTask, "main", string(task.ID))
	if err != nil {
		t.Fatalf("Resolve task: %v", err)
	}
	if want := refs.Task("main", task.ID); got != want {
		t.Errorf("Resolve task = %q, want %q", got, want)
	}

	if _, err := s.Resolve(ctx, refs.KindTask, "release", string(task.ID)); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve task on wrong branch = %v, want ErrNotFound", err)
	}
	if _, err := s.Resolve(ctx, refs.KindTask, "main", string(a.ID)); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve note id as task = %v, want ErrNotFound", err)
	}
	if _, err := s.Resolve(ctx, refs.KindTask, "", string(task.ID)); err == nil || errors.Is(err, ErrNotFound) {
		t.Errorf("Resolve task with empty branch = %v, want plain error", err)
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
	_, err := s.Resolve(t.Context(), refs.KindNote, "", prefix)
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
