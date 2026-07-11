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

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
)

const (
	// ccFetchRefspec is the prune-safe fetch refspec Install writes: it mirrors
	// the remote's entity refs into the per-remote tracking namespace, never the
	// canonical one, so fetch.prune can never delete an unsynced canonical ref.
	ccFetchRefspec = "+refs/cc-notes/*:refs/cc-notes-sync/origin/*"
	// oldCCFetchRefspec is the pre-fix same-namespace force-mirror Install now
	// rewrites away on upgrade.
	oldCCFetchRefspec = "+refs/cc-notes/*:refs/cc-notes/*"
	ccPushRefspec     = "refs/cc-notes/*:refs/cc-notes/*"
	defaultFetch      = "+refs/heads/*:refs/remotes/origin/*"
)

// clone creates a fresh repo wired to bare as origin under a local identity,
// on top of the scrubbed environment gittest.InitBare already established.
func clone(t *testing.T, bare, name, email string) *store.Store {
	t.Helper()
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	gittest.Git(t, dir, "remote", "add", "origin", bare)
	gittest.Git(t, dir, "config", "user.name", name)
	gittest.Git(t, dir, "config", "user.email", email)
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
	return syncScope(t, s, false)
}

func syncFull(t *testing.T, s *store.Store) ccsync.Report {
	t.Helper()
	return syncScope(t, s, true)
}

func syncScope(t *testing.T, s *store.Store, full bool) ccsync.Report {
	t.Helper()
	report, err := ccsync.Sync(t.Context(), s.Git.Dir, "origin", full)
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
	out := gittest.Git(t, dir, "for-each-ref", "--format=%(refname) %(objectname)", "refs/cc-notes/")
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
	all, err := s.ListTasks(t.Context())
	if err != nil {
		t.Fatalf("ListTasks: %v", err)
	}
	var tasks []model.Task
	for _, task := range all {
		if task.Branch == branch {
			tasks = append(tasks, task)
		}
	}
	return tasks
}

func TestInstallIdempotent(t *testing.T) {
	bare := gittest.InitBare(t)
	s := clone(t, bare, "Alice", "alice@example.com")
	wantAdded := []string{
		"remote.origin.fetch=" + ccFetchRefspec,
		"remote.origin.push=HEAD",
		"remote.origin.push=" + ccPushRefspec,
		"core.logAllRefUpdates=always",
	}
	for run := range 2 {
		report, err := ccsync.Install(t.Context(), s.Git, "origin")
		if err != nil {
			t.Fatalf("Install run %d: %v", run, err)
		}
		if run == 0 && (!slices.Equal(report.Added, wantAdded) || !report.HeadPushAdded) {
			t.Errorf("run 0 report = %+v, want Added %q with HeadPushAdded", report, wantAdded)
		}
		if run > 0 && (len(report.Added) != 0 || report.HeadPushAdded) {
			t.Errorf("run %d report = %+v, want empty no-op report", run, report)
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
	bare := gittest.InitBare(t)
	s := clone(t, bare, "Alice", "alice@example.com")
	existing := "refs/heads/main:refs/heads/main"
	gittest.Git(t, s.Git.Dir, "config", "remote.origin.push", existing)
	for run := range 2 {
		report, err := ccsync.Install(t.Context(), s.Git, "origin")
		if err != nil {
			t.Fatalf("Install run %d: %v", run, err)
		}
		if report.HeadPushAdded {
			t.Errorf("run %d report = %+v, want HeadPushAdded false with an existing push refspec", run, report)
		}
		if got, want := configAll(t, s, "remote.origin.push"), []string{existing, ccPushRefspec}; !slices.Equal(got, want) {
			t.Errorf("run %d: push lines = %q, want %q (no HEAD line)", run, got, want)
		}
	}
}

func TestInstallUnknownRemote(t *testing.T) {
	bare := gittest.InitBare(t)
	s := clone(t, bare, "Alice", "alice@example.com")
	_, err := ccsync.Install(t.Context(), s.Git, "upstream")
	if !errors.Is(err, ccsync.ErrRemoteNotFound) {
		t.Fatalf("Install unknown remote: got %v, want ErrRemoteNotFound", err)
	}
}

// TestInstallUpgradesOldFetchRefspec pins the upgrade path: a repo wired before
// the prune-safety fix carries the same-namespace force-mirror
// +refs/cc-notes/*:refs/cc-notes/*, which a plain fetch --prune uses to delete
// unsynced canonical refs. Install rewrites it in place to the tracking-namespace
// refspec, leaving the default heads refspec and its order untouched, and a rerun
// is a no-op. Push, HEAD, and the reflog are pre-wired so the only change the
// upgrade run makes is the fetch rewrite.
func TestInstallUpgradesOldFetchRefspec(t *testing.T) {
	bare := gittest.InitBare(t)
	s := clone(t, bare, "Alice", "alice@example.com")
	gittest.Git(t, s.Git.Dir, "config", "--add", "remote.origin.fetch", oldCCFetchRefspec)
	gittest.Git(t, s.Git.Dir, "config", "--add", "remote.origin.push", "HEAD")
	gittest.Git(t, s.Git.Dir, "config", "--add", "remote.origin.push", ccPushRefspec)
	gittest.Git(t, s.Git.Dir, "config", "core.logAllRefUpdates", "always")

	for run := range 2 {
		report, err := ccsync.Install(t.Context(), s.Git, "origin")
		if err != nil {
			t.Fatalf("Install run %d: %v", run, err)
		}
		if run == 0 {
			if want := []string{"remote.origin.fetch=" + ccFetchRefspec}; !slices.Equal(report.Added, want) || report.HeadPushAdded {
				t.Errorf("upgrade run report = %+v, want Added %q without HeadPushAdded", report, want)
			}
		}
		if run > 0 && (len(report.Added) != 0 || report.HeadPushAdded) {
			t.Errorf("run %d report = %+v, want empty no-op report", run, report)
		}
		if got, want := configAll(t, s, "remote.origin.fetch"), []string{defaultFetch, ccFetchRefspec}; !slices.Equal(got, want) {
			t.Errorf("run %d: fetch lines = %q, want %q (old rewritten, order kept)", run, got, want)
		}
		if slices.Contains(configAll(t, s, "remote.origin.fetch"), oldCCFetchRefspec) {
			t.Errorf("run %d: old same-namespace fetch refspec still present", run)
		}
	}
}

// TestInstallSweepsEveryRemoteOldFetchRefspec pins the multi-remote upgrade: a
// repo wired before the prune-safety fix on two remotes carries the killer
// same-namespace refspec on both, so a plain git fetch --all --prune would
// delete unsynced canonical refs through whichever remote install did not
// touch. Installing for one remote must rewrite the killer refspec in place on
// every configured remote — each to its own tracking namespace — so the
// unsynced task survives the exact fetch --all --prune that used to erase it.
// The upgrade is idempotent and reported in write order: the wired remote fully
// first, then the swept remote's fetch rewrite.
func TestInstallSweepsEveryRemoteOldFetchRefspec(t *testing.T) {
	bare := gittest.InitBare(t)
	s := clone(t, bare, "Alice", "alice@example.com")
	upstreamBare := gittest.InitBare(t)
	gittest.Git(t, s.Git.Dir, "remote", "add", "upstream", upstreamBare)
	gittest.Git(t, s.Git.Dir, "config", "--add", "remote.origin.fetch", oldCCFetchRefspec)
	gittest.Git(t, s.Git.Dir, "config", "--add", "remote.upstream.fetch", oldCCFetchRefspec)

	const (
		upstreamDefaultFetch = "+refs/heads/*:refs/remotes/upstream/*"
		upstreamFetchRefspec = "+refs/cc-notes/*:refs/cc-notes-sync/upstream/*"
	)
	for run := range 2 {
		report, err := ccsync.Install(t.Context(), s.Git, "origin")
		if err != nil {
			t.Fatalf("Install run %d: %v", run, err)
		}
		if run == 0 {
			wantAdded := []string{
				"remote.origin.fetch=" + ccFetchRefspec,
				"remote.origin.push=HEAD",
				"remote.origin.push=" + ccPushRefspec,
				"core.logAllRefUpdates=always",
				"remote.upstream.fetch=" + upstreamFetchRefspec,
			}
			if !slices.Equal(report.Added, wantAdded) || len(report.Removed) != 0 || !report.HeadPushAdded {
				t.Errorf("upgrade run report = %+v, want Added %q with HeadPushAdded and no Removed", report, wantAdded)
			}
		}
		if run > 0 && (len(report.Added) != 0 || len(report.Removed) != 0 || report.HeadPushAdded) {
			t.Errorf("run %d report = %+v, want empty no-op report", run, report)
		}
		if got, want := configAll(t, s, "remote.origin.fetch"), []string{defaultFetch, ccFetchRefspec}; !slices.Equal(got, want) {
			t.Errorf("run %d: origin fetch = %q, want %q", run, got, want)
		}
		if got, want := configAll(t, s, "remote.upstream.fetch"), []string{upstreamDefaultFetch, upstreamFetchRefspec}; !slices.Equal(got, want) {
			t.Errorf("run %d: upstream fetch = %q, want %q", run, got, want)
		}
		for _, key := range []string{"remote.origin.fetch", "remote.upstream.fetch"} {
			if slices.Contains(configAll(t, s, key), oldCCFetchRefspec) {
				t.Errorf("run %d: %s still carries the killer refspec", run, key)
			}
		}
	}

	task := createTask(t, s, "unsynced", "main")
	taskRef := refs.For(model.KindTask, task.ID)
	gittest.Git(t, s.Git.Dir, "-c", "fetch.prune=true", "-c", "fetch.pruneTags=true", "fetch", "--all", "-q")
	if got := gittest.Git(t, s.Git.Dir, "rev-parse", taskRef); got != string(task.Head) {
		t.Fatalf("after git fetch --all --prune across both upgraded remotes, %s = %s, want the unsynced task to survive at %s", taskRef, got, task.Head)
	}
}

// TestUpgradedFetchPruneKeepsUnsyncedTask makes the upgrade's survival contract
// end-to-end: a repo wired pre-fix carries the same-namespace killer refspec,
// Install rewrites it to the tracking namespace, and then the exact plain
// git fetch --prune that used to delete an unsynced canonical ref leaves it
// intact — proving the rewrite, not just the config line, closes the data loss.
func TestUpgradedFetchPruneKeepsUnsyncedTask(t *testing.T) {
	bare := gittest.InitBare(t)
	s := clone(t, bare, "Alice", "alice@example.com")
	gittest.Git(t, s.Git.Dir, "config", "--add", "remote.origin.fetch", oldCCFetchRefspec)
	if _, err := ccsync.Install(t.Context(), s.Git, "origin"); err != nil {
		t.Fatalf("Install upgrade: %v", err)
	}
	if slices.Contains(configAll(t, s, "remote.origin.fetch"), oldCCFetchRefspec) {
		t.Fatalf("upgrade left the killer refspec in place")
	}
	task := createTask(t, s, "unsynced", "main")
	taskRef := refs.For(model.KindTask, task.ID)
	gittest.Git(t, s.Git.Dir, "-c", "fetch.prune=true", "-c", "fetch.pruneTags=true", "fetch", "-q", "origin")
	if got := gittest.Git(t, s.Git.Dir, "rev-parse", taskRef); got != string(task.Head) {
		t.Fatalf("after upgrade then plain fetch --prune, %s = %s, want the unsynced task to survive at %s", taskRef, got, task.Head)
	}
}

// TestInstallDropsRedundantOldFetchRefspec pins the dedupe: when an old binary
// re-added the killer refspec alongside the new tracking one, install drops the
// redundant old line rather than rewriting it into a second identical new line.
// The old line is not inert — git still applies it on a plain fetch --prune, so
// dropping it is a data-loss fix: an unsynced task survives the fetch --prune
// afterward. Push and the reflog are pre-wired so the only config change is the
// drop, reported under Removed; a rerun is a clean no-op.
func TestInstallDropsRedundantOldFetchRefspec(t *testing.T) {
	bare := gittest.InitBare(t)
	s := clone(t, bare, "Alice", "alice@example.com")
	gittest.Git(t, s.Git.Dir, "config", "--add", "remote.origin.fetch", oldCCFetchRefspec)
	gittest.Git(t, s.Git.Dir, "config", "--add", "remote.origin.fetch", ccFetchRefspec)
	gittest.Git(t, s.Git.Dir, "config", "--add", "remote.origin.push", "HEAD")
	gittest.Git(t, s.Git.Dir, "config", "--add", "remote.origin.push", ccPushRefspec)
	gittest.Git(t, s.Git.Dir, "config", "core.logAllRefUpdates", "always")

	for run := range 2 {
		report, err := ccsync.Install(t.Context(), s.Git, "origin")
		if err != nil {
			t.Fatalf("Install run %d: %v", run, err)
		}
		if run == 0 {
			if want := []string{"remote.origin.fetch=" + oldCCFetchRefspec}; !slices.Equal(report.Removed, want) {
				t.Errorf("run 0 Removed = %q, want %q", report.Removed, want)
			}
			if len(report.Added) != 0 || report.HeadPushAdded {
				t.Errorf("run 0 report = %+v, want no Added and no HeadPushAdded (only the redundant drop)", report)
			}
		}
		if run > 0 && (len(report.Added) != 0 || len(report.Removed) != 0 || report.HeadPushAdded) {
			t.Errorf("run %d report = %+v, want empty no-op report", run, report)
		}
		if got, want := configAll(t, s, "remote.origin.fetch"), []string{defaultFetch, ccFetchRefspec}; !slices.Equal(got, want) {
			t.Errorf("run %d: fetch lines = %q, want %q (redundant old dropped, new kept)", run, got, want)
		}
		if slices.Contains(configAll(t, s, "remote.origin.fetch"), oldCCFetchRefspec) {
			t.Errorf("run %d: old killer refspec still present after dedupe", run)
		}
	}

	// The drop is a data-loss fix, not cosmetic: with the old line still present
	// a plain fetch --prune deletes an unsynced canonical ref, so prove the task
	// survives now that only the tracking-namespace refspec remains.
	task := createTask(t, s, "unsynced", "main")
	taskRef := refs.For(model.KindTask, task.ID)
	gittest.Git(t, s.Git.Dir, "-c", "fetch.prune=true", "-c", "fetch.pruneTags=true", "fetch", "-q", "origin")
	if got := gittest.Git(t, s.Git.Dir, "rev-parse", taskRef); got != string(task.Head) {
		t.Fatalf("after dropping the redundant old refspec, %s = %s, want the unsynced task to survive at %s", taskRef, got, task.Head)
	}
}

// TestPlainGitCarry pins the two halves of the plain-git contract after the
// prune-safety fix. Plain push is unchanged: it publishes canonical entity refs
// straight to the remote alongside the branch. Plain fetch now mirrors the
// remote's entity refs into the per-remote tracking namespace only — never the
// canonical one — so a fresh clone's plain fetch pre-populates exactly the
// tracking refs Sync folds; a following sync surfaces them as canonical.
func TestPlainGitCarry(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	gittest.Git(t, a.Git.Dir, "commit", "-q", "--allow-empty", "-m", "init")
	head := gittest.Git(t, a.Git.Dir, "rev-parse", "HEAD")
	if _, err := ccsync.Install(t.Context(), a.Git, "origin"); err != nil {
		t.Fatalf("Install A: %v", err)
	}
	note := createNote(t, a, "carried note")
	task := createTask(t, a, "carried task", "main")
	noteRef, taskRef := refs.For(model.KindNote, note.ID), refs.For(model.KindTask, task.ID)

	gittest.Git(t, a.Git.Dir, "push", "-q", "origin")

	if got := gittest.Git(t, bare, "rev-parse", "refs/heads/main"); got != head {
		t.Errorf("plain push: remote main at %s, want %s", got, head)
	}
	if got := gittest.Git(t, bare, "rev-parse", noteRef); got != string(note.Head) {
		t.Errorf("plain push: remote %s at %s, want %s", noteRef, got, note.Head)
	}
	if got := gittest.Git(t, bare, "rev-parse", taskRef); got != string(task.Head) {
		t.Errorf("plain push: remote %s at %s, want %s", taskRef, got, task.Head)
	}

	b := clone(t, bare, "Bob", "bob@example.com")
	if _, err := ccsync.Install(t.Context(), b.Git, "origin"); err != nil {
		t.Fatalf("Install B: %v", err)
	}
	gittest.Git(t, b.Git.Dir, "fetch", "-q", "origin")

	// Plain fetch lands the remote tips in the tracking namespace, not canonical.
	trackNote := "refs/cc-notes-sync/origin/notes/" + string(note.ID)
	if got := gittest.Git(t, b.Git.Dir, "rev-parse", trackNote); got != string(note.Head) {
		t.Errorf("plain fetch: tracking %s at %s, want %s", trackNote, got, note.Head)
	}
	if refs := gittest.Git(t, b.Git.Dir, "for-each-ref", "refs/cc-notes/"); refs != "" {
		t.Errorf("plain fetch populated canonical refs %q, want tracking only", refs)
	}

	// Sync folds the pre-populated tracking refs into canonical and loads them.
	if _, err := ccsync.Sync(t.Context(), b.Git.Dir, "origin", false); err != nil {
		t.Fatalf("Sync B: %v", err)
	}
	if got := gittest.Git(t, b.Git.Dir, "rev-parse", noteRef); got != string(note.Head) {
		t.Errorf("after sync: local %s at %s, want %s", noteRef, got, note.Head)
	}
	loaded, err := b.Load(t.Context(), taskRef)
	if err != nil {
		t.Fatalf("Load synced task in B: %v", err)
	}
	if !reflect.DeepEqual(loaded, model.Snapshot(task)) {
		t.Errorf("synced task = %+v, want %+v", loaded, task)
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
	//nolint:gosec // G306: the git stub must be executable (0o755) for the test's PATH override to run it.
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatalf("write git stub: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return marker
}

func TestTwoCloneConvergence(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	note := createNote(t, a, "shared note")
	task := createTask(t, a, "shared task", "main")
	noteRef, taskRef := refs.For(model.KindNote, note.ID), refs.For(model.KindTask, task.ID)

	if got, want := sync(t, a), (ccsync.Report{Pushed: 2, Rounds: 1}); got != want {
		t.Fatalf("A first sync report = %+v, want %+v", got, want)
	}
	if got, want := sync(t, b), (ccsync.Report{Created: 2, Reconciled: 2, Rounds: 1}); got != want {
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
	bTip := gittest.Git(t, b.Git.Dir, "rev-parse", taskRef)
	gittest.Git(t, bare, "update-ref", taskRef, string(task.Head))
	marker := writeGitStub(t, bare, taskRef, bTip)

	if got, want := sync(t, a), (ccsync.Report{Merged: 1, Pushed: 1, Reconciled: 3, Rounds: 2}); got != want {
		t.Fatalf("A contended sync report = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("git stub never injected the remote move: %v", err)
	}
	if got, want := sync(t, b), (ccsync.Report{FastForwarded: 1, Reconciled: 1, Rounds: 1}); got != want {
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
		if got := gittest.Git(t, dir, "rev-list", "--merges", "--count", taskRef); got != "1" {
			t.Errorf("merge commits in %s chain = %s, want 1", dir, got)
		}
	}
}

func TestConcurrentSameFieldLWW(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	task := createTask(t, a, "contested", "main")
	taskRef := refs.For(model.KindTask, task.ID)
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

// TestSymmetricMergeRace pins an accepted tradeoff: two clones that each
// union-merge the same divergence locally before either pushes mint mirrored
// merge commits — parents [a,b] on one clone, [b,a] on the other — so the
// tips cannot be byte-equal until sync joins them, possibly via a merge of
// merges. What is pinned is fold equality and quiescence, never byte tips
// mid-flight.
func TestSymmetricMergeRace(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	task := createTask(t, a, "mirrored", "main")
	taskRef := refs.For(model.KindTask, task.ID)
	sync(t, a)
	sync(t, b)

	tipA := appendOps(t, a, taskRef, model.AddLabel{Label: "from-a"}).(model.Task).Head
	tipB := appendOps(t, b, taskRef, model.SetStatus{Status: model.StatusInProgress}).(model.Task).Head

	// Exchange objects clone-to-clone without moving refs, then mirror-merge
	// the same divergence on both sides before either clone pushes.
	gittest.Git(t, a.Git.Dir, "fetch", "-q", b.Git.Dir, taskRef)
	gittest.Git(t, b.Git.Dir, "fetch", "-q", a.Git.Dir, taskRef)
	if _, err := a.Merge(t.Context(), taskRef, tipA, tipB); err != nil {
		t.Fatalf("A Merge: %v", err)
	}
	if _, err := b.Merge(t.Context(), taskRef, tipB, tipA); err != nil {
		t.Fatalf("B Merge: %v", err)
	}
	syncUntilQuiescent(t, a, b)

	taskA, taskB := loadTask(t, a, taskRef), loadTask(t, b, taskRef)
	if !reflect.DeepEqual(taskA, taskB) {
		t.Fatalf("clones diverge: A = %+v, B = %+v", taskA, taskB)
	}
	if want := []string{"from-a"}; !slices.Equal(taskA.Labels, want) {
		t.Errorf("merged Labels = %v, want %v", taskA.Labels, want)
	}
	if taskA.Status != model.StatusInProgress {
		t.Errorf("merged Status = %s, want in_progress", taskA.Status)
	}
}

// TestPlainPushDivergedEntityRef pins the plain-git contract the README
// states: a diverged entity ref makes `git push` exit 1, but refspecs fail
// independently — the branch still lands on the remote, and the remote's
// entity tip is never clobbered. Sync's union merge is the only path that
// resolves the entity ref.
func TestPlainPushDivergedEntityRef(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	for _, s := range []*store.Store{a, b} {
		if _, err := ccsync.Install(t.Context(), s.Git, "origin"); err != nil {
			t.Fatalf("Install: %v", err)
		}
	}
	gittest.Git(t, a.Git.Dir, "commit", "-q", "--allow-empty", "-m", "init")
	task := createTask(t, a, "diverged", "main")
	taskRef := refs.For(model.KindTask, task.ID)
	gittest.Git(t, a.Git.Dir, "push", "-q", "origin")
	gittest.Git(t, b.Git.Dir, "fetch", "-q", "origin")
	gittest.Git(t, b.Git.Dir, "reset", "-q", "--hard", "origin/main")
	// A plain fetch now only mirrors into the tracking namespace, so B converges
	// the canonical entity ref via sync before diverging it.
	sync(t, b)

	appendOps(t, a, taskRef, model.AddLabel{Label: "from-a"})
	gittest.Git(t, a.Git.Dir, "push", "-q", "origin")
	remoteEntity := gittest.Git(t, bare, "rev-parse", taskRef)
	appendOps(t, b, taskRef, model.AddComment{Body: "from b"})
	gittest.Git(t, b.Git.Dir, "commit", "-q", "--allow-empty", "-m", "b work")
	bHead := gittest.Git(t, b.Git.Dir, "rev-parse", "HEAD")

	//nolint:gosec // G204: test shells out to git with fixed argv[0] and literal push args.
	out, err := exec.Command("git", "-C", b.Git.Dir, "push", "origin").CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("plain push with diverged entity ref: err = %v, want exit 1; output:\n%s", err, out)
	}
	if got := gittest.Git(t, bare, "rev-parse", "refs/heads/main"); got != bHead {
		t.Errorf("remote main = %s, want B's commit %s: the branch must land despite the rejected entity ref", got, bHead)
	}
	if got := gittest.Git(t, bare, "rev-parse", taskRef); got != remoteEntity {
		t.Errorf("remote %s = %s, want %s: a diverged entity ref must never clobber", taskRef, got, remoteEntity)
	}
}

// TestPlainFetchPruneKeepsUnsyncedTask is the data-loss regression: a task
// created but never synced has no counterpart on the remote, so under the old
// same-namespace force-mirror refspec a plain fetch --prune deleted its
// canonical ref outright. With the tracking-namespace refspec the canonical ref
// is no longer a fetch destination, so prune can never touch it: the unsynced
// task survives the exact fetch --prune that used to erase it.
func TestPlainFetchPruneKeepsUnsyncedTask(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	if _, err := ccsync.Install(t.Context(), a.Git, "origin"); err != nil {
		t.Fatalf("Install A: %v", err)
	}
	task := createTask(t, a, "unsynced", "main")
	taskRef := refs.For(model.KindTask, task.ID)
	if got := gittest.Git(t, a.Git.Dir, "rev-parse", taskRef); got != string(task.Head) {
		t.Fatalf("before fetch, %s = %s, want %s", taskRef, got, task.Head)
	}

	gittest.Git(t, a.Git.Dir, "-c", "fetch.prune=true", "-c", "fetch.pruneTags=true", "fetch", "-q", "origin")

	if got := gittest.Git(t, a.Git.Dir, "rev-parse", taskRef); got != string(task.Head) {
		t.Fatalf("after plain fetch --prune, %s = %s, want the unsynced task to survive at %s", taskRef, got, task.Head)
	}
	if tracking := gittest.Git(t, a.Git.Dir, "for-each-ref", "refs/cc-notes-sync/"); tracking != "" {
		t.Errorf("tracking namespace = %q, want empty (remote has no entity refs)", tracking)
	}
}

// TestPlainFetchNeverClobbersDivergedRef pins the no-clobber half of the
// contract that the pre-fix forced same-namespace refspec broke: a local entity
// ref carrying unsynced ops that has diverged from the advanced remote tip is
// neither clobbered nor pruned by a plain fetch --prune. The fetch only mirrors
// the remote tip into the tracking namespace; Sync's union merge remains the one
// path that folds the two sides, losing neither.
func TestPlainFetchNeverClobbersDivergedRef(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	for _, s := range []*store.Store{a, b} {
		if _, err := ccsync.Install(t.Context(), s.Git, "origin"); err != nil {
			t.Fatalf("Install: %v", err)
		}
	}
	task := createTask(t, a, "diverged", "main")
	taskRef := refs.For(model.KindTask, task.ID)
	sync(t, a)
	sync(t, b)

	local := appendOps(t, b, taskRef, model.AddComment{Body: "unsynced local"}).(model.Task).Head
	appendOps(t, a, taskRef, model.AddLabel{Label: "remote-wins"})
	sync(t, a)
	remoteTip := gittest.Git(t, bare, "rev-parse", taskRef)
	trackingRef := "refs/cc-notes-sync/origin/tasks/" + string(task.ID)

	gittest.Git(t, b.Git.Dir, "-c", "fetch.prune=true", "fetch", "-q", "origin")

	if got := gittest.Git(t, b.Git.Dir, "rev-parse", taskRef); got != string(local) {
		t.Fatalf("after plain fetch --prune, %s = %s, want the diverged local tip %s untouched", taskRef, got, local)
	}
	if got := gittest.Git(t, b.Git.Dir, "rev-parse", trackingRef); got != remoteTip {
		t.Errorf("tracking %s = %s, want remote tip %s", trackingRef, got, remoteTip)
	}

	sync(t, b)
	sync(t, a)
	merged := loadTask(t, b, taskRef)
	if len(merged.Comments) != 1 || merged.Comments[0].Body != "unsynced local" {
		t.Errorf("merged Comments = %+v, want the preserved local comment", merged.Comments)
	}
	if want := []string{"remote-wins"}; !slices.Equal(merged.Labels, want) {
		t.Errorf("merged Labels = %v, want %v", merged.Labels, want)
	}
	if tipsA, tipsB := ccRefs(t, a.Git.Dir), ccRefs(t, b.Git.Dir); !reflect.DeepEqual(tipsA, tipsB) {
		t.Errorf("tips diverge after converge: A = %v, B = %v", tipsA, tipsB)
	}
}

// TestSyncPreservesDivergedOpsAfterInstall pins that Sync's own fetch never
// applies the forced fetch refspec Install wrote: git maps an
// explicit-refspec fetch through the configured remote.<r>.fetch refspecs
// opportunistically, which would clobber a diverged local entity tip before
// reconcile could union-merge it, stranding the local ops in the reflog.
func TestSyncPreservesDivergedOpsAfterInstall(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	for _, s := range []*store.Store{a, b} {
		if _, err := ccsync.Install(t.Context(), s.Git, "origin"); err != nil {
			t.Fatalf("Install: %v", err)
		}
	}
	task := createTask(t, a, "diverged", "main")
	taskRef := refs.For(model.KindTask, task.ID)
	sync(t, a)
	sync(t, b)

	appendOps(t, a, taskRef, model.AddComment{Body: "from alice"})
	sync(t, a)
	appendOps(t, b, taskRef, model.AddComment{Body: "from bob"})

	if got, want := sync(t, b), (ccsync.Report{Merged: 1, Pushed: 1, Reconciled: 1, Rounds: 1}); got != want {
		t.Fatalf("B diverged sync report = %+v, want %+v", got, want)
	}
	sync(t, a)
	for _, s := range []*store.Store{a, b} {
		merged := loadTask(t, s, taskRef)
		bodies := make([]string, len(merged.Comments))
		for i, c := range merged.Comments {
			bodies[i] = c.Body
		}
		slices.Sort(bodies)
		if want := []string{"from alice", "from bob"}; !slices.Equal(bodies, want) {
			t.Errorf("converged comments in %s = %v, want %v", s.Git.Dir, bodies, want)
		}
	}
	if tipsA, tipsB := ccRefs(t, a.Git.Dir), ccRefs(t, b.Git.Dir); !reflect.DeepEqual(tipsA, tipsB) {
		t.Errorf("tips diverge: A = %v, B = %v", tipsA, tipsB)
	}
}

// TestPlainPullThenSyncPropagatesDone pins STEP 3 case (c): B marks the shared
// task done and syncs, A does a plain pull (its fetch half, with prune, is the
// only part that touches cc-notes refs — the branch merge does not), then A
// syncs. B's done state propagates into A while A's own unsynced local edit is
// never clobbered: the plain pull leaves A's canonical ref untouched, and Sync's
// union merge folds both sides.
func TestPlainPullThenSyncPropagatesDone(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	for _, s := range []*store.Store{a, b} {
		if _, err := ccsync.Install(t.Context(), s.Git, "origin"); err != nil {
			t.Fatalf("Install: %v", err)
		}
	}
	task := createTask(t, a, "shared", "main")
	taskRef := refs.For(model.KindTask, task.ID)
	sync(t, a)
	sync(t, b)

	// A edits locally without syncing; B marks the task done and publishes it.
	aLocal := appendOps(t, a, taskRef, model.AddLabel{Label: "a-local"}).(model.Task).Head
	appendOps(t, b, taskRef, model.SetStatus{Status: model.StatusDone})
	sync(t, b)

	// A's plain pull (fetch half, prune on) must not clobber A's local tip.
	gittest.Git(t, a.Git.Dir, "-c", "fetch.prune=true", "fetch", "-q", "origin")
	if got := gittest.Git(t, a.Git.Dir, "rev-parse", taskRef); got != string(aLocal) {
		t.Fatalf("after plain pull, %s = %s, want A's unsynced local tip %s untouched", taskRef, got, aLocal)
	}

	sync(t, a)
	sync(t, b)
	for _, s := range []*store.Store{a, b} {
		got := loadTask(t, s, taskRef)
		if got.Status != model.StatusDone {
			t.Errorf("%s Status = %s, want done propagated", s.Git.Dir, got.Status)
		}
		if want := []string{"a-local"}; !slices.Equal(got.Labels, want) {
			t.Errorf("%s Labels = %v, want %v (A's local edit not clobbered)", s.Git.Dir, got.Labels, want)
		}
	}
	if tipsA, tipsB := ccRefs(t, a.Git.Dir), ccRefs(t, b.Git.Dir); !reflect.DeepEqual(tipsA, tipsB) {
		t.Errorf("tips diverge after converge: A = %v, B = %v", tipsA, tipsB)
	}
}

// TestSyncUnchangedReconcilesNothing pins the scoped-sync payoff: once a
// clone's tracking view matches the remote, a further sync folds and
// reconciles nothing — before and after the fetch are identical, so the scope
// is empty.
func TestSyncUnchangedReconcilesNothing(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	createNote(t, a, "shared note")
	createTask(t, a, "shared task", "main")
	sync(t, a)
	sync(t, b)
	if got, want := sync(t, b), (ccsync.Report{Rounds: 1}); got != want {
		t.Fatalf("unchanged sync report = %+v, want %+v (nothing reconciled)", got, want)
	}
}

// TestSyncScopedReconcilesOnlyChanged moves exactly one remote ref and pins
// that the next sync reconciles only that ref, leaving the unchanged ref out
// of scope.
func TestSyncScopedReconcilesOnlyChanged(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	createNote(t, a, "shared note")
	task := createTask(t, a, "shared task", "main")
	taskRef := refs.For(model.KindTask, task.ID)
	sync(t, a)
	sync(t, b)

	appendOps(t, a, taskRef, model.AddLabel{Label: "urgent"})
	sync(t, a)

	got := sync(t, b)
	if got.Reconciled != 1 {
		t.Fatalf("scoped sync Reconciled = %d, want 1 (only the moved ref)", got.Reconciled)
	}
	if want := (ccsync.Report{FastForwarded: 1, Reconciled: 1, Rounds: 1}); got != want {
		t.Fatalf("scoped sync report = %+v, want %+v", got, want)
	}
}

// advanceTracking simulates a prior sync that fetched but never reconciled —
// interrupted mid-reconcile by ctx cancel, contention, or a fold error. It
// advances only the per-remote tracking refs to the remote tips, leaving the
// canonical refs/cc-notes/ namespace behind, exactly the state a partial sync
// leaves: tracking==remote, local stale.
func advanceTracking(t *testing.T, s *store.Store) {
	t.Helper()
	gittest.Git(t, s.Git.Dir, "fetch", "-q", "origin", "+refs/cc-notes/*:refs/cc-notes-sync/origin/*")
}

// TestSyncScopedHealsBehindAfterInterruptedSync pins recovery from an
// interrupted sync that advanced tracking past a behind local ref. Scoping on
// the tracking delta alone would see before==after and skip the ref forever,
// folding nothing while a stale push fails non-fast-forward every round. The
// local-containment check pulls the ref back into scope so the next scoped sync
// fast-forwards it with no --full.
func TestSyncScopedHealsBehindAfterInterruptedSync(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	task := createTask(t, a, "shared task", "main")
	taskRef := refs.For(model.KindTask, task.ID)
	sync(t, a)
	sync(t, b)

	appendOps(t, a, taskRef, model.AddLabel{Label: "urgent"})
	sync(t, a)

	advanceTracking(t, b)

	if got, want := sync(t, b), (ccsync.Report{FastForwarded: 1, Reconciled: 1, Rounds: 1}); got != want {
		t.Fatalf("scoped heal report = %+v, want %+v", got, want)
	}
	got := loadTask(t, b, taskRef)
	if want := []string{"urgent"}; !slices.Equal(got.Labels, want) {
		t.Errorf("healed task Labels = %v, want %v", got.Labels, want)
	}
	if tipsA, tipsB := ccRefs(t, a.Git.Dir), ccRefs(t, b.Git.Dir); !reflect.DeepEqual(tipsA, tipsB) {
		t.Errorf("tips diverge after heal: A = %v, B = %v", tipsA, tipsB)
	}
}

// TestSyncScopedHealsDivergedAfterInterruptedSync is the data-loss case of the
// same skip: tracking advanced past a local ref that itself diverged with
// unpushed ops. Skipping it would strand those ops and loop ErrSyncContended;
// the containment check union-merges instead, preserving both sides.
func TestSyncScopedHealsDivergedAfterInterruptedSync(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	task := createTask(t, a, "shared task", "main")
	taskRef := refs.For(model.KindTask, task.ID)
	sync(t, a)
	sync(t, b)

	appendOps(t, a, taskRef, model.AddComment{Body: "from alice"})
	sync(t, a)
	appendOps(t, b, taskRef, model.AddComment{Body: "from bob"})

	advanceTracking(t, b)

	if got, want := sync(t, b), (ccsync.Report{Merged: 1, Pushed: 1, Reconciled: 1, Rounds: 1}); got != want {
		t.Fatalf("scoped diverged heal report = %+v, want %+v", got, want)
	}
	sync(t, a)
	for _, s := range []*store.Store{a, b} {
		merged := loadTask(t, s, taskRef)
		bodies := make([]string, len(merged.Comments))
		for i, c := range merged.Comments {
			bodies[i] = c.Body
		}
		slices.Sort(bodies)
		if want := []string{"from alice", "from bob"}; !slices.Equal(bodies, want) {
			t.Errorf("converged comments in %s = %v, want %v", s.Git.Dir, bodies, want)
		}
	}
	if tipsA, tipsB := ccRefs(t, a.Git.Dir), ccRefs(t, b.Git.Dir); !reflect.DeepEqual(tipsA, tipsB) {
		t.Errorf("tips diverge after heal: A = %v, B = %v", tipsA, tipsB)
	}
}

// TestSyncFullForcesFullReconcile pins the --full escape hatch: even on an
// unchanged remote, full reconciles every ref the remote knows.
func TestSyncFullForcesFullReconcile(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	createNote(t, a, "shared note")
	createTask(t, a, "shared task", "main")
	sync(t, a)
	sync(t, b)

	got := syncFull(t, b)
	if want := (ccsync.Report{Reconciled: 2, Rounds: 1}); got != want {
		t.Fatalf("full sync report = %+v, want %+v (every ref reconciled, all no-ops)", got, want)
	}
}

func TestSyncNoRemote(t *testing.T) {
	gittest.ScrubEnv(t)
	dir := t.TempDir()
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	_, err := ccsync.Sync(t.Context(), dir, "origin", false)
	if !errors.Is(err, ccsync.ErrRemoteNotFound) {
		t.Fatalf("Sync without remote: got %v, want ErrRemoteNotFound", err)
	}
	if !strings.Contains(err.Error(), "origin") {
		t.Errorf("error %q does not name the remote", err)
	}
}

// TestSetBranchConvergesAcrossClones pins LWW on the branch scalar across
// clones: two clones each move the same task to a different branch, and after
// sync both fold to the same branch — the linearization winner.
func TestSetBranchConvergesAcrossClones(t *testing.T) {
	bare := gittest.InitBare(t)
	a := clone(t, bare, "Alice", "alice@example.com")
	b := clone(t, bare, "Bob", "bob@example.com")
	task := createTask(t, a, "contested branch", "main")
	taskRef := refs.For(model.KindTask, task.ID)
	sync(t, a)
	sync(t, b)

	appendOps(t, a, taskRef, model.SetBranch{Branch: "feature/a"})
	appendOps(t, b, taskRef, model.SetBranch{Branch: "feature/b"})
	sync(t, b)
	sync(t, a)
	sync(t, b)

	taskA, taskB := loadTask(t, a, taskRef), loadTask(t, b, taskRef)
	if !reflect.DeepEqual(taskA, taskB) {
		t.Fatalf("clones diverge: A = %+v, B = %+v", taskA, taskB)
	}
	if taskA.Branch != "feature/a" && taskA.Branch != "feature/b" {
		t.Fatalf("converged Branch = %q, want one of the two set_branch destinations", taskA.Branch)
	}
}
