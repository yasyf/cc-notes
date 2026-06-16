package sync_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
)

// reconcileClone returns a clone whose main branch carries one initial
// commit, ready for branch and merge operations.
func reconcileClone(t *testing.T) *store.Store {
	t.Helper()
	bare := initBare(t)
	s := clone(t, bare, "Alice", "alice@example.com")
	mustGit(t, s.Git.Dir, "commit", "-q", "--allow-empty", "-m", "init")
	return s
}

// branchFrom creates name off the current HEAD, advances it by one empty
// commit, and leaves HEAD on name.
func branchFrom(t *testing.T, dir, name string) {
	t.Helper()
	mustGit(t, dir, "checkout", "-q", "-b", name)
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", name+" work")
}

// mergeInto checks out target and merges source with a merge commit, so
// source's tip becomes an ancestor of target.
func mergeInto(t *testing.T, dir, target, source string) {
	t.Helper()
	mustGit(t, dir, "checkout", "-q", target)
	mustGit(t, dir, "merge", "-q", "--no-ff", "-m", "merge "+source, source)
}

func setStatus(t *testing.T, s *store.Store, branch model.Branch, id model.EntityID, status model.Status) {
	t.Helper()
	appendOps(t, s, refs.Task(id), model.SetStatus{Status: status})
}

func taskIDs(tasks []model.Task) []model.EntityID {
	ids := make([]model.EntityID, len(tasks))
	for i, task := range tasks {
		ids[i] = task.ID
	}
	slices.Sort(ids)
	return ids
}

func findBranch(t *testing.T, r ccsync.ReconcileReport, branch model.Branch) ccsync.BranchResult {
	t.Helper()
	for _, br := range r.Branches {
		if br.Branch == branch {
			return br
		}
	}
	t.Fatalf("branch %q not in report %+v", branch, r.Branches)
	return ccsync.BranchResult{}
}

func reconcile(t *testing.T, s *store.Store, into model.Branch, from []model.Branch, force, dryRun bool) ccsync.ReconcileReport {
	t.Helper()
	report, err := ccsync.Reconcile(t.Context(), s, into, from, force, dryRun)
	if err != nil {
		t.Fatalf("Reconcile(into=%s, from=%v, force=%v, dry=%v): %v", into, from, force, dryRun, err)
	}
	return report
}

func TestReconcileMergedCarries(t *testing.T) {
	s := reconcileClone(t)
	dir := s.Git.Dir
	branchFrom(t, dir, "feature/x")
	open := createTask(t, s, "open one", "feature/x")
	wip := createTask(t, s, "wip two", "feature/x")
	setStatus(t, s, "feature/x", wip.ID, model.StatusInProgress)
	mergeInto(t, dir, "main", "feature/x")

	report := reconcile(t, s, "main", nil, false, false)

	if got, want := report.Scanned(), 1; got != want {
		t.Errorf("Scanned = %d, want %d", got, want)
	}
	if got, want := report.Merged(), 1; got != want {
		t.Errorf("Merged = %d, want %d", got, want)
	}
	if got, want := report.Carried(), 2; got != want {
		t.Errorf("Carried = %d, want %d", got, want)
	}
	br := findBranch(t, report, "feature/x")
	if !br.Merged || br.Reason != "" {
		t.Errorf("feature/x result = %+v, want merged with no reason", br)
	}
	want := slices.Sorted(slices.Values([]model.EntityID{open.ID, wip.ID}))
	if got := taskIDs(listTasks(t, s, "main")); !slices.Equal(got, want) {
		t.Errorf("ListTasks(main) ids = %v, want %v", got, want)
	}
	if got := listTasks(t, s, "feature/x"); len(got) != 0 {
		t.Errorf("ListTasks(feature/x) = %+v, want empty after reconcile", got)
	}
}

func TestReconcileNotMergedUntouched(t *testing.T) {
	s := reconcileClone(t)
	dir := s.Git.Dir
	branchFrom(t, dir, "feature/y")
	task := createTask(t, s, "stays put", "feature/y")
	mustGit(t, dir, "checkout", "-q", "main")

	report := reconcile(t, s, "main", nil, false, false)

	if got, want := report.Carried(), 0; got != want {
		t.Errorf("Carried = %d, want %d", got, want)
	}
	if got, want := report.Merged(), 0; got != want {
		t.Errorf("Merged = %d, want %d", got, want)
	}
	br := findBranch(t, report, "feature/y")
	if br.Merged || br.Reason != "not merged" {
		t.Errorf("feature/y result = %+v, want not merged with reason \"not merged\"", br)
	}
	if got := taskIDs(listTasks(t, s, "feature/y")); !slices.Equal(got, []model.EntityID{task.ID}) {
		t.Errorf("ListTasks(feature/y) = %v, want the still-live task", got)
	}
	if got := listTasks(t, s, "main"); len(got) != 0 {
		t.Errorf("ListTasks(main) = %+v, want empty", got)
	}
}

func TestReconcileIdempotent(t *testing.T) {
	s := reconcileClone(t)
	dir := s.Git.Dir
	branchFrom(t, dir, "feature/x")
	task := createTask(t, s, "carry once", "feature/x")
	mergeInto(t, dir, "main", "feature/x")

	if got := reconcile(t, s, "main", nil, false, false).Carried(); got != 1 {
		t.Fatalf("first Carried = %d, want 1", got)
	}
	before := ccRefs(t, dir)

	second := reconcile(t, s, "main", nil, false, false)
	if got, want := second.Carried(), 0; got != want {
		t.Errorf("second Carried = %d, want %d", got, want)
	}
	if got, want := second.Scanned(), 0; got != want {
		t.Errorf("second Scanned = %d, want %d (the moved task no longer folds on the source branch)", got, want)
	}
	if got := ccRefs(t, dir); !mapsEqual(got, before) {
		t.Errorf("refs moved on idempotent rerun: %v -> %v", before, got)
	}
	if got := taskIDs(listTasks(t, s, "main")); !slices.Equal(got, []model.EntityID{task.ID}) {
		t.Errorf("ListTasks(main) = %v, want the single carried task", got)
	}
}

func TestReconcileStatusFilter(t *testing.T) {
	s := reconcileClone(t)
	dir := s.Git.Dir
	branchFrom(t, dir, "feature/x")
	open := createTask(t, s, "open", "feature/x")
	wip := createTask(t, s, "wip", "feature/x")
	done := createTask(t, s, "done", "feature/x")
	cancelled := createTask(t, s, "cancelled", "feature/x")
	setStatus(t, s, "feature/x", wip.ID, model.StatusInProgress)
	setStatus(t, s, "feature/x", done.ID, model.StatusDone)
	setStatus(t, s, "feature/x", cancelled.ID, model.StatusCancelled)
	mergeInto(t, dir, "main", "feature/x")

	report := reconcile(t, s, "main", nil, false, false)

	if got, want := report.Carried(), 2; got != want {
		t.Errorf("Carried = %d, want %d (only open + in_progress)", got, want)
	}
	wantMoved := slices.Sorted(slices.Values([]model.EntityID{open.ID, wip.ID}))
	if got := taskIDs(listTasks(t, s, "main")); !slices.Equal(got, wantMoved) {
		t.Errorf("ListTasks(main) = %v, want %v", got, wantMoved)
	}
	wantStayed := slices.Sorted(slices.Values([]model.EntityID{done.ID, cancelled.ID}))
	if got := taskIDs(listTasks(t, s, "feature/x")); !slices.Equal(got, wantStayed) {
		t.Errorf("ListTasks(feature/x) = %v, want the closed tasks %v", got, wantStayed)
	}
}

func TestReconcileExplicitFrom(t *testing.T) {
	s := reconcileClone(t)
	dir := s.Git.Dir
	branchFrom(t, dir, "feature/a")
	a := createTask(t, s, "in a", "feature/a")
	mergeInto(t, dir, "main", "feature/a")
	branchFrom(t, dir, "feature/b")
	b := createTask(t, s, "in b", "feature/b")
	mergeInto(t, dir, "main", "feature/b")

	report := reconcile(t, s, "main", []model.Branch{"feature/a"}, false, false)

	if got, want := report.Scanned(), 1; got != want {
		t.Errorf("Scanned = %d, want %d (only the named source)", got, want)
	}
	if got := taskIDs(listTasks(t, s, "main")); !slices.Equal(got, []model.EntityID{a.ID}) {
		t.Errorf("ListTasks(main) = %v, want only feature/a's task", got)
	}
	if got := taskIDs(listTasks(t, s, "feature/b")); !slices.Equal(got, []model.EntityID{b.ID}) {
		t.Errorf("ListTasks(feature/b) = %v, want its task untouched", got)
	}
}

func TestReconcileIntoNamespace(t *testing.T) {
	s := reconcileClone(t)
	dir := s.Git.Dir
	mustGit(t, dir, "checkout", "-q", "-b", "release")
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "release base")
	branchFrom(t, dir, "feature/r")
	task := createTask(t, s, "for release", "feature/r")
	mergeInto(t, dir, "release", "feature/r")

	report := reconcile(t, s, "release", []model.Branch{"feature/r"}, false, false)

	if got, want := report.Into, model.Branch("release"); got != want {
		t.Errorf("Into = %q, want %q", got, want)
	}
	if got := taskIDs(listTasks(t, s, "release")); !slices.Equal(got, []model.EntityID{task.ID}) {
		t.Errorf("ListTasks(release) = %v, want the carried task", got)
	}
	if got := listTasks(t, s, "feature/r"); len(got) != 0 {
		t.Errorf("ListTasks(feature/r) = %+v, want empty", got)
	}
}

func TestReconcileForceSquash(t *testing.T) {
	s := reconcileClone(t)
	dir := s.Git.Dir
	branchFrom(t, dir, "feature/z")
	task := createTask(t, s, "squash merged", "feature/z")
	// Squash merge: main gains the content but not feature/z's tip as a parent,
	// so ancestry can never see it as merged.
	mustGit(t, dir, "checkout", "-q", "main")
	mustGit(t, dir, "merge", "-q", "--squash", "feature/z")
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "squash z")

	if br := findBranch(t, reconcile(t, s, "main", nil, false, false), "feature/z"); br.Merged {
		t.Fatalf("squash-merged feature/z reported merged %+v, want skipped", br)
	}
	if got := listTasks(t, s, "feature/z"); len(got) != 1 {
		t.Fatalf("ListTasks(feature/z) after no-op = %+v, want still live", got)
	}

	report := reconcile(t, s, "main", []model.Branch{"feature/z"}, true, false)
	if got, want := report.Carried(), 1; got != want {
		t.Errorf("forced Carried = %d, want %d", got, want)
	}
	if got := taskIDs(listTasks(t, s, "main")); !slices.Equal(got, []model.EntityID{task.ID}) {
		t.Errorf("ListTasks(main) = %v, want the force-carried task", got)
	}
	if got := listTasks(t, s, "feature/z"); len(got) != 0 {
		t.Errorf("ListTasks(feature/z) = %+v, want empty after force", got)
	}
}

func TestReconcileDeletedSourceBranch(t *testing.T) {
	s := reconcileClone(t)
	dir := s.Git.Dir
	branchFrom(t, dir, "feature/x")
	task := createTask(t, s, "orphaned ref", "feature/x")
	mergeInto(t, dir, "main", "feature/x")
	mustGit(t, dir, "branch", "-q", "-D", "feature/x")

	report := reconcile(t, s, "main", nil, false, false)
	br := findBranch(t, report, "feature/x")
	if br.Merged || br.Reason != "branch ref missing" {
		t.Errorf("deleted-branch result = %+v, want skipped with reason \"branch ref missing\"", br)
	}
	if got := taskIDs(listTasks(t, s, "feature/x")); !slices.Equal(got, []model.EntityID{task.ID}) {
		t.Errorf("ListTasks(feature/x) = %v, want still live (discoverable but unverifiable)", got)
	}

	forced := reconcile(t, s, "main", []model.Branch{"feature/x"}, true, false)
	if got, want := forced.Carried(), 1; got != want {
		t.Errorf("forced Carried = %d, want %d", got, want)
	}
	if got := taskIDs(listTasks(t, s, "main")); !slices.Equal(got, []model.EntityID{task.ID}) {
		t.Errorf("ListTasks(main) = %v, want the recovered task", got)
	}
}

func TestReconcileTargetExcluded(t *testing.T) {
	s := reconcileClone(t)
	createTask(t, s, "lives on main", "main")
	branchFrom(t, s.Git.Dir, "feature/x")
	createTask(t, s, "on feature", "feature/x")
	mergeInto(t, s.Git.Dir, "main", "feature/x")

	report := reconcile(t, s, "main", nil, false, false)
	for _, br := range report.Branches {
		if br.Branch == "main" {
			t.Fatalf("target main appeared as a source: %+v", report.Branches)
		}
	}
	if got, want := report.Scanned(), 1; got != want {
		t.Errorf("Scanned = %d, want %d (main excluded)", got, want)
	}
}

func TestReconcileDryRun(t *testing.T) {
	s := reconcileClone(t)
	dir := s.Git.Dir
	branchFrom(t, dir, "feature/x")
	task := createTask(t, s, "planned", "feature/x")
	mergeInto(t, dir, "main", "feature/x")
	before := ccRefs(t, dir)

	report := reconcile(t, s, "main", nil, false, true)

	if got, want := report.Carried(), 1; got != want {
		t.Errorf("dry-run Carried = %d, want %d (the plan)", got, want)
	}
	br := findBranch(t, report, "feature/x")
	if !br.Merged || len(br.Tasks) != 1 || br.Tasks[0].ID != task.ID {
		t.Errorf("dry-run feature/x = %+v, want the planned task", br)
	}
	if got := ccRefs(t, dir); !mapsEqual(got, before) {
		t.Errorf("dry-run moved refs: %v -> %v", before, got)
	}
	if got := loadTask(t, s, refs.Task(task.ID)).Branch; got != "feature/x" {
		t.Errorf("dry-run moved task to branch %q, want feature/x untouched", got)
	}
	if got := taskIDs(listTasks(t, s, "feature/x")); !slices.Equal(got, []model.EntityID{task.ID}) {
		t.Errorf("ListTasks(feature/x) = %v, want still live under dry-run", got)
	}
	if got := listTasks(t, s, "main"); len(got) != 0 {
		t.Errorf("ListTasks(main) = %+v, want empty under dry-run", got)
	}
}

func TestReconcileMissingTarget(t *testing.T) {
	s := reconcileClone(t)
	_, err := ccsync.Reconcile(t.Context(), s, "ghost", nil, false, false)
	if !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Fatalf("Reconcile into missing branch: got %v, want ErrRefNotFound", err)
	}
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
