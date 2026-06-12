// Integration tests: every test drives a real bare remote plus real clones
// in t.TempDir() with the git environment scrubbed. The engineered
// non-fast-forward race in TestTwoCloneConvergence injects a remote move
// between fetch and push through a PATH-stubbed git.
package sync_test

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
)

const (
	ccFetchRefspec = "+refs/cc-notes/*:refs/cc-notes/*"
	ccPushRefspec  = "refs/cc-notes/*:refs/cc-notes/*"
	defaultFetch   = "+refs/heads/*:refs/remotes/origin/*"
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
		"GIT_EDITOR", "EMAIL", "CC_NOTES_ACTOR",
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

func initBare(t *testing.T) string {
	t.Helper()
	scrubGitEnv(t)
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "--bare")
	return dir
}

func clone(t *testing.T, bare, name, email string) *store.Store {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	mustGit(t, dir, "remote", "add", "origin", bare)
	mustGit(t, dir, "config", "user.name", name)
	mustGit(t, dir, "config", "user.email", email)
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	return s
}

func createNote(t *testing.T, s *store.Store, title string) model.Note {
	t.Helper()
	snapshot, err := s.Create(t.Context(), []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: title}})
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	return snapshot.(model.Note)
}

func createTask(t *testing.T, s *store.Store, title string, branch model.Branch) model.Task {
	t.Helper()
	ops := []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: title, Type: model.TypeTask, Branch: branch}}
	snapshot, err := s.Create(t.Context(), ops)
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return snapshot.(model.Task)
}

func appendOps(t *testing.T, s *store.Store, ref string, ops ...model.Op) model.Snapshot {
	t.Helper()
	snapshot, err := s.Append(t.Context(), ref, ops)
	if err != nil {
		t.Fatalf("append to %s: %v", ref, err)
	}
	return snapshot
}

func sync(t *testing.T, s *store.Store) ccsync.Report {
	t.Helper()
	report, err := ccsync.Sync(t.Context(), s.Git.Dir, "origin")
	if err != nil {
		t.Fatalf("Sync(%s): %v", s.Git.Dir, err)
	}
	return report
}

func configAll(t *testing.T, s *store.Store, key string) []string {
	t.Helper()
	values, err := s.Git.ConfigGetAll(t.Context(), key)
	if err != nil {
		t.Fatalf("ConfigGetAll(%s): %v", key, err)
	}
	return values
}

// ccRefs maps every refs/cc-notes/ ref in dir to its tip.
func ccRefs(t *testing.T, dir string) map[string]string {
	t.Helper()
	tips := map[string]string{}
	out := mustGit(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/cc-notes/")
	for line := range strings.Lines(out) {
		if name, sha, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			tips[name] = sha
		}
	}
	return tips
}

func loadTask(t *testing.T, s *store.Store, ref string) model.Task {
	t.Helper()
	snapshot, err := s.Load(t.Context(), ref)
	if err != nil {
		t.Fatalf("Load(%s): %v", ref, err)
	}
	return snapshot.(model.Task)
}

func listTasks(t *testing.T, s *store.Store, branch model.Branch) []model.Task {
	t.Helper()
	tasks, err := s.ListTasks(t.Context(), branch)
	if err != nil {
		t.Fatalf("ListTasks(%s): %v", branch, err)
	}
	return tasks
}

func TestInstallIdempotent(t *testing.T) {
	bare := initBare(t)
	s := clone(t, bare, "Alice", "alice@example.com")
	for run := range 2 {
		if err := ccsync.Install(t.Context(), s.Git, "origin"); err != nil {
			t.Fatalf("Install run %d: %v", run, err)
		}
		if got, want := configAll(t, s, "remote.origin.fetch"), []string{defaultFetch, ccFetchRefspec}; !slices.Equal(got, want) {
			t.Errorf("run %d: fetch lines = %q, want %q", run, got, want)
		}
		if got, want := configAll(t, s, "remote.origin.push"), []string{"HEAD", ccPushRefspec}; !slices.Equal(got, want) {
			t.Errorf("run %d: push lines = %q, want %q", run, got, want)
		}
		if got, want := configAll(t, s, "core.logAllRefUpdates"), []string{"always"}; !slices.Equal(got, want) {
			t.Errorf("run %d: core.logAllRefUpdates = %q, want %q", run, got, want)
		}
	}
}

func TestInstallPreservesExistingPushRefspec(t *testing.T) {
	bare := initBare(t)
	s := clone(t, bare, "Alice", "alice@example.com")
	existing := "refs/heads/main:refs/heads/main"
	mustGit(t, s.Git.Dir, "config", "remote.origin.push", existing)
	for run := range 2 {
		if err := ccsync.Install(t.Context(), s.Git, "origin"); err != nil {
			t.Fatalf("Install run %d: %v", run, err)
		}
		if got, want := configAll(t, s, "remote.origin.push"), []string{existing, ccPushRefspec}; !slices.Equal(got, want) {
			t.Errorf("run %d: push lines = %q, want %q (no HEAD line)", run, got, want)
		}
	}
}

func TestInstallUnknownRemote(t *testing.T) {
	bare := initBare(t)
	s := clone(t, bare, "Alice", "alice@example.com")
	err := ccsync.Install(t.Context(), s.Git, "upstream")
	if !errors.Is(err, ccsync.ErrRemoteNotFound) {
		t.Fatalf("Install unknown remote: got %v, want ErrRemoteNotFound", err)
	}
}

func TestPlainGitCarry(t *testing.T) {
	bare := initBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	mustGit(t, a.Git.Dir, "commit", "-q", "--allow-empty", "-m", "init")
	head := mustGit(t, a.Git.Dir, "rev-parse", "HEAD")
	if err := ccsync.Install(t.Context(), a.Git, "origin"); err != nil {
		t.Fatalf("Install A: %v", err)
	}
	note := createNote(t, a, "carried note")
	task := createTask(t, a, "carried task", "main")
	noteRef, taskRef := refs.Note(note.ID), refs.Task("main", task.ID)

	mustGit(t, a.Git.Dir, "push", "-q", "origin")

	if got := mustGit(t, bare, "rev-parse", "refs/heads/main"); got != head {
		t.Errorf("plain push: remote main at %s, want %s", got, head)
	}
	if got := mustGit(t, bare, "rev-parse", noteRef); got != string(note.Head) {
		t.Errorf("plain push: remote %s at %s, want %s", noteRef, got, note.Head)
	}
	if got := mustGit(t, bare, "rev-parse", taskRef); got != string(task.Head) {
		t.Errorf("plain push: remote %s at %s, want %s", taskRef, got, task.Head)
	}

	b := clone(t, bare, "Bob", "bob@example.com")
	if err := ccsync.Install(t.Context(), b.Git, "origin"); err != nil {
		t.Fatalf("Install B: %v", err)
	}
	mustGit(t, b.Git.Dir, "fetch", "-q", "origin")

	if got := mustGit(t, b.Git.Dir, "rev-parse", noteRef); got != string(note.Head) {
		t.Errorf("plain fetch: local %s at %s, want %s", noteRef, got, note.Head)
	}
	loaded, err := b.Load(t.Context(), taskRef)
	if err != nil {
		t.Fatalf("Load fetched task in B: %v", err)
	}
	if !reflect.DeepEqual(loaded, model.Snapshot(task)) {
		t.Errorf("fetched task = %+v, want %+v", loaded, task)
	}
}

// writeGitStub puts a fake git first on PATH that, on the first push it
// sees, moves the bare remote's ref to tip before running the real push —
// the remote-moved-between-fetch-and-push race, made deterministic.
func writeGitStub(t *testing.T, bare, ref, tip string) (marker string) {
	t.Helper()
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("LookPath(git): %v", err)
	}
	dir := t.TempDir()
	marker = filepath.Join(dir, "injected")
	script := fmt.Sprintf(`#!/bin/sh
if [ ! -e %q ]; then
  for arg in "$@"; do
    if [ "$arg" = push ]; then
      : > %q
      %q -C %q update-ref %q %q
      break
    fi
  done
fi
exec %q "$@"
`, marker, marker, realGit, bare, ref, tip, realGit)
	stub := filepath.Join(dir, "git")
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("write git stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

func TestTwoCloneConvergence(t *testing.T) {
	bare := initBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	note := createNote(t, a, "shared note")
	task := createTask(t, a, "shared task", "main")
	noteRef, taskRef := refs.Note(note.ID), refs.Task("main", task.ID)

	if got, want := sync(t, a), (ccsync.Report{Pushed: 2, Rounds: 1}); got != want {
		t.Fatalf("A first sync report = %+v, want %+v", got, want)
	}
	if got, want := sync(t, b), (ccsync.Report{Created: 2, Rounds: 1}); got != want {
		t.Fatalf("B first sync report = %+v, want %+v", got, want)
	}
	for _, ref := range []string{noteRef, taskRef} {
		got, err := b.Load(t.Context(), ref)
		if err != nil {
			t.Fatalf("B Load(%s): %v", ref, err)
		}
		want, err := a.Load(t.Context(), ref)
		if err != nil {
			t.Fatalf("A Load(%s): %v", ref, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("after first sync, %s folds = %+v on B, want %+v", ref, got, want)
		}
	}

	appendOps(t, b, taskRef, model.SetStatus{Status: model.StatusInProgress})
	appendOps(t, a, taskRef, model.AddLabel{Label: "urgent"})

	sync(t, b)
	bTip := mustGit(t, b.Git.Dir, "rev-parse", taskRef)
	mustGit(t, bare, "update-ref", taskRef, string(task.Head))
	marker := writeGitStub(t, bare, taskRef, bTip)

	if got, want := sync(t, a), (ccsync.Report{Merged: 1, Pushed: 1, Rounds: 2}); got != want {
		t.Fatalf("A contended sync report = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("git stub never injected the remote move: %v", err)
	}
	if got, want := sync(t, b), (ccsync.Report{FastForwarded: 1, Rounds: 1}); got != want {
		t.Fatalf("B final sync report = %+v, want %+v", got, want)
	}

	tipsA, tipsB := ccRefs(t, a.Git.Dir), ccRefs(t, b.Git.Dir)
	if len(tipsA) != 2 || !reflect.DeepEqual(tipsA, tipsB) {
		t.Errorf("tips diverge: A = %v, B = %v", tipsA, tipsB)
	}
	for _, ref := range []string{noteRef, taskRef} {
		got, err := b.Load(t.Context(), ref)
		if err != nil {
			t.Fatalf("B Load(%s): %v", ref, err)
		}
		want, err := a.Load(t.Context(), ref)
		if err != nil {
			t.Fatalf("A Load(%s): %v", ref, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("converged %s folds = %+v on B, want %+v", ref, got, want)
		}
	}
	merged := loadTask(t, a, taskRef)
	if merged.Status != model.StatusInProgress {
		t.Errorf("merged Status = %s, want in_progress", merged.Status)
	}
	if want := []string{"urgent"}; !slices.Equal(merged.Labels, want) {
		t.Errorf("merged Labels = %v, want %v", merged.Labels, want)
	}
	for _, dir := range []string{a.Git.Dir, b.Git.Dir} {
		if got := mustGit(t, dir, "rev-list", "--merges", "--count", taskRef); got != "1" {
			t.Errorf("merge commits in %s chain = %s, want 1", dir, got)
		}
	}
}

func TestConcurrentSameFieldLWW(t *testing.T) {
	bare := initBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	task := createTask(t, a, "contested", "main")
	taskRef := refs.Task("main", task.ID)
	sync(t, a)
	sync(t, b)

	appendOps(t, a, taskRef, model.SetPriority{Priority: 1})
	appendOps(t, b, taskRef, model.SetPriority{Priority: 2})
	sync(t, b)
	sync(t, a)
	sync(t, b)

	taskA, taskB := loadTask(t, a, taskRef), loadTask(t, b, taskRef)
	if !reflect.DeepEqual(taskA, taskB) {
		t.Fatalf("clones diverge: A = %+v, B = %+v", taskA, taskB)
	}

	// Re-derive the LWW winner from the commit metadata: the set_priority
	// commit that linearizes last — greatest (lamport, author time, sha) —
	// must have won on both clones.
	chain, err := a.Repo.ReadChain(t.Context(), taskA.Head)
	if err != nil {
		t.Fatalf("ReadChain: %v", err)
	}
	var writes []model.PackCommit
	for _, c := range chain {
		if slices.ContainsFunc(c.Pack.Ops, func(op model.Op) bool {
			_, ok := op.(model.SetPriority)
			return ok
		}) {
			writes = append(writes, c)
		}
	}
	if len(writes) != 2 {
		t.Fatalf("found %d set_priority commits, want 2", len(writes))
	}
	slices.SortFunc(writes, func(x, y model.PackCommit) int {
		if c := cmp.Compare(x.Pack.Lamport, y.Pack.Lamport); c != 0 {
			return c
		}
		if c := cmp.Compare(x.AuthorTime, y.AuthorTime); c != 0 {
			return c
		}
		return cmp.Compare(x.SHA, y.SHA)
	})
	want := writes[1].Pack.Ops[0].(model.SetPriority).Priority
	if taskA.Priority != want {
		t.Errorf("Priority = %d, want LWW winner %d", taskA.Priority, want)
	}
}

func TestPromoteStaleClone(t *testing.T) {
	bare := initBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	from, to := model.Branch("feature/x"), model.Branch("main")
	task := createTask(t, a, "promoted", from)
	oldRef, liveRef := refs.Task(from, task.ID), refs.Task(to, task.ID)
	sync(t, a)
	sync(t, b)

	if err := ccsync.Promote(t.Context(), a, from, to, []model.EntityID{task.ID}); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	promoted := mustGit(t, a.Git.Dir, "rev-parse", oldRef)
	if got := mustGit(t, a.Git.Dir, "rev-parse", liveRef); got != promoted {
		t.Fatalf("dest ref at %s, want promote tip %s", got, promoted)
	}
	if got := listTasks(t, a, from); len(got) != 0 {
		t.Fatalf("A ListTasks(%s) after promote = %+v, want empty", from, got)
	}
	sync(t, a)

	// B is stale: it still appends to the old ref.
	appendOps(t, b, oldRef, model.AddComment{Body: "stale comment"})
	if got, want := sync(t, b), (ccsync.Report{Created: 1, FastForwarded: 1, Merged: 1, Pushed: 2, Rounds: 1}); got != want {
		t.Fatalf("B stale sync report = %+v, want %+v", got, want)
	}
	sync(t, a)

	for name, s := range map[string]*store.Store{"A": a, "B": b} {
		live := listTasks(t, s, to)
		if len(live) != 1 || live[0].ID != task.ID {
			t.Fatalf("%s ListTasks(%s) = %+v, want the promoted task", name, to, live)
		}
		if len(live[0].Comments) != 1 || live[0].Comments[0].Body != "stale comment" {
			t.Errorf("%s promoted task comments = %+v, want the stale comment", name, live[0].Comments)
		}
		if got := listTasks(t, s, from); len(got) != 0 {
			t.Errorf("%s ListTasks(%s) = %+v, want empty", name, from, got)
		}
		mustGit(t, s.Git.Dir, "rev-parse", "--verify", oldRef)
	}
	mustGit(t, bare, "rev-parse", "--verify", oldRef)

	// A third sync round on each side must change nothing: no resurrection.
	tipsA, tipsB := ccRefs(t, a.Git.Dir), ccRefs(t, b.Git.Dir)
	if got, want := sync(t, a), (ccsync.Report{Rounds: 1}); got != want {
		t.Errorf("A settle sync report = %+v, want %+v", got, want)
	}
	if got, want := sync(t, b), (ccsync.Report{Rounds: 1}); got != want {
		t.Errorf("B settle sync report = %+v, want %+v", got, want)
	}
	if got := ccRefs(t, a.Git.Dir); !reflect.DeepEqual(got, tipsA) {
		t.Errorf("A tips moved on settle sync: %v -> %v", tipsA, got)
	}
	if got := ccRefs(t, b.Git.Dir); !reflect.DeepEqual(got, tipsB) {
		t.Errorf("B tips moved on settle sync: %v -> %v", tipsB, got)
	}
	if !reflect.DeepEqual(ccRefs(t, a.Git.Dir), ccRefs(t, b.Git.Dir)) {
		t.Errorf("clones diverge after settle: A = %v, B = %v", ccRefs(t, a.Git.Dir), ccRefs(t, b.Git.Dir))
	}
}

func TestPromoteFromDeadRef(t *testing.T) {
	bare := initBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	task := createTask(t, a, "moved on", "alpha")
	if err := ccsync.Promote(t.Context(), a, "alpha", "bravo", []model.EntityID{task.ID}); err != nil {
		t.Fatalf("Promote: %v", err)
	}

	err := ccsync.Promote(t.Context(), a, "alpha", "charlie", []model.EntityID{task.ID})
	if !errors.Is(err, ccsync.ErrNotLive) {
		t.Fatalf("re-promote from dead ref: got %v, want ErrNotLive", err)
	}
	if got := loadTask(t, a, refs.Task("alpha", task.ID)).Branch; got != "bravo" {
		t.Errorf("dead chain folds to %q after rejected promote, want %q untouched", got, "bravo")
	}
}

// syncUntilQuiescent alternates syncs across the clones until every clone
// reports a clean pass — nothing created, fast-forwarded, merged, or pushed —
// failing the test when they never settle. The terminating pass doubles as
// proof that a further sync is a no-op.
func syncUntilQuiescent(t *testing.T, stores ...*store.Store) {
	t.Helper()
	quiet := ccsync.Report{Rounds: 1}
	for range 8 {
		settled := true
		for _, s := range stores {
			if sync(t, s) != quiet {
				settled = false
			}
		}
		if settled {
			return
		}
	}
	t.Fatal("clones never quiesced")
}

// liveBranches returns the branches whose task ref for id folds live: the
// folded branch equals the branch encoded in the ref path.
func liveBranches(t *testing.T, s *store.Store, id model.EntityID) []model.Branch {
	t.Helper()
	var live []model.Branch
	for name := range ccRefs(t, s.Git.Dir) {
		parsed, err := refs.Parse(name)
		if err != nil {
			t.Fatalf("Parse(%s): %v", name, err)
		}
		if parsed.Kind != refs.KindTask || parsed.ID != id {
			continue
		}
		if loadTask(t, s, name).Branch == parsed.Branch {
			live = append(live, parsed.Branch)
		}
	}
	return live
}

// TestRacingPromotesConverge pins the repair for racing promotes: A promotes
// alpha→bravo while B promotes alpha→charlie. Consolidation must converge
// every sibling ref — not just the fold winner's — to the union history;
// otherwise the loser's destination ref contains only its own promote, folds
// to its own namespace, and stays live forever on every replica.
func TestRacingPromotesConverge(t *testing.T) {
	bare := initBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	task := createTask(t, a, "contested promote", "alpha")
	sync(t, a)
	sync(t, b)

	if err := ccsync.Promote(t.Context(), a, "alpha", "bravo", []model.EntityID{task.ID}); err != nil {
		t.Fatalf("A Promote: %v", err)
	}
	if err := ccsync.Promote(t.Context(), b, "alpha", "charlie", []model.EntityID{task.ID}); err != nil {
		t.Fatalf("B Promote: %v", err)
	}
	syncUntilQuiescent(t, a, b)

	liveA, liveB := liveBranches(t, a, task.ID), liveBranches(t, b, task.ID)
	if len(liveA) != 1 || len(liveB) != 1 {
		t.Fatalf("live refs: A = %v, B = %v, want exactly one each", liveA, liveB)
	}
	if liveA[0] != liveB[0] {
		t.Fatalf("clones disagree on winner: A = %q, B = %q", liveA[0], liveB[0])
	}
	winner := liveA[0]
	if winner != "bravo" && winner != "charlie" {
		t.Fatalf("winner = %q, want a promote destination", winner)
	}
	for name, s := range map[string]*store.Store{"A": a, "B": b} {
		for _, branch := range []model.Branch{"alpha", "bravo", "charlie"} {
			mustGit(t, s.Git.Dir, "rev-parse", "--verify", refs.Task(branch, task.ID))
			got := listTasks(t, s, branch)
			switch {
			case branch == winner && (len(got) != 1 || got[0].ID != task.ID):
				t.Errorf("%s ListTasks(%s) = %+v, want the promoted task", name, branch, got)
			case branch != winner && len(got) != 0:
				t.Errorf("%s ListTasks(%s) = %+v, want empty", name, branch, got)
			}
		}
	}

	tipsA, tipsB := ccRefs(t, a.Git.Dir), ccRefs(t, b.Git.Dir)
	if !reflect.DeepEqual(tipsA, tipsB) {
		t.Errorf("tips diverge after quiescence: A = %v, B = %v", tipsA, tipsB)
	}
	if got, want := sync(t, a), (ccsync.Report{Rounds: 1}); got != want {
		t.Errorf("A settle sync report = %+v, want %+v", got, want)
	}
	if got := ccRefs(t, a.Git.Dir); !reflect.DeepEqual(got, tipsA) {
		t.Errorf("A tips moved on settle sync: %v -> %v", tipsA, got)
	}
}

func TestSyncNoRemote(t *testing.T) {
	scrubGitEnv(t)
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	_, err := ccsync.Sync(t.Context(), dir, "origin")
	if !errors.Is(err, ccsync.ErrRemoteNotFound) {
		t.Fatalf("Sync without remote: got %v, want ErrRemoteNotFound", err)
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("error %q does not name the remote", err)
	}
}
