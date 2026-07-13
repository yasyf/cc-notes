package notes_test

import (
	"context"
	"errors"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// storeAppend appends ops to an existing entity chain through a fresh store,
// so tests advance chains the notes.Client cannot yet mutate directly.
func storeAppend(t *testing.T, dir, ref string, ops ...model.Op) {
	t.Helper()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := s.Append(context.Background(), ref, ops); err != nil {
		t.Fatalf("store.Append(%s): %v", ref, err)
	}
}

// installRemote wires a bare remote as name on the repo at dir, both as a git
// remote and with cc-notes' refspecs, so WiredRemotes reports it.
func installRemote(t *testing.T, dir, name, url string) {
	t.Helper()
	gittest.Git(t, dir, "remote", "add", name, url)
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if _, err := ccsync.Install(context.Background(), s.Git, name); err != nil {
		t.Fatalf("Install(%s): %v", name, err)
	}
}

func TestSyncSingleRemotePushes(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	bare := gittest.InitBare(t)
	gittest.Git(t, dir, "remote", "add", "origin", bare)

	note := storeCreate(t, dir, model.CreateNote{Nonce: model.NewNonce(), Title: "n"}).(model.Note)
	ref := refs.For(model.KindNote, note.ID)

	report, err := c.Sync(ctx, notes.SyncOptions{Remote: "origin"})
	if err != nil {
		t.Fatalf("Sync(origin): %v", err)
	}
	if report.Pushed != 1 {
		t.Errorf("Pushed = %d, want 1", report.Pushed)
	}
	if report.Rounds != 1 {
		t.Errorf("Rounds = %d, want 1", report.Rounds)
	}
	if report.Created != 0 || report.Reconciled != 0 {
		t.Errorf("Created/Reconciled = %d/%d, want 0/0 on a first push", report.Created, report.Reconciled)
	}
	if got := gittest.Git(t, bare, "for-each-ref", "--format=%(refname)", ref); got != ref {
		t.Errorf("remote ref after sync = %q, want %q", got, ref)
	}
}

func TestSyncDefaultFansOutOverWiredRemotes(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	bareA := gittest.InitBare(t)
	bareB := gittest.InitBare(t)
	installRemote(t, dir, "up-a", bareA)
	installRemote(t, dir, "up-b", bareB)

	note := storeCreate(t, dir, model.CreateNote{Nonce: model.NewNonce(), Title: "n"}).(model.Note)
	ref := refs.For(model.KindNote, note.ID)

	// An empty Remote syncs every cc-notes-wired remote and sums the tallies.
	report, err := c.Sync(ctx, notes.SyncOptions{})
	if err != nil {
		t.Fatalf("Sync(all): %v", err)
	}
	if report.Pushed != 2 {
		t.Errorf("Pushed = %d, want 2 (one per wired remote)", report.Pushed)
	}
	if report.Rounds != 2 {
		t.Errorf("Rounds = %d, want 2 (one clean pass per remote)", report.Rounds)
	}
	for _, bare := range []string{bareA, bareB} {
		if got := gittest.Git(t, bare, "for-each-ref", "--format=%(refname)", ref); got != ref {
			t.Errorf("remote %s ref after sync = %q, want %q", bare, got, ref)
		}
	}
}

func TestSyncUnknownRemote(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	// A note exists but the target remote is not configured at all.
	storeCreate(t, dir, model.CreateNote{Nonce: model.NewNonce(), Title: "n"})

	report, err := c.Sync(ctx, notes.SyncOptions{Remote: "ghost"})
	if !errors.Is(err, ccsync.ErrRemoteNotFound) {
		t.Fatalf("Sync(ghost) err = %v, want ErrRemoteNotFound", err)
	}
	if (report != notes.SyncReport{}) {
		t.Errorf("report = %+v, want zero when the remote never resolved", report)
	}
}

func TestReconcileMovesMergedBranchTasks(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")

	// feature/x merges into main, carrying its open and in-progress tasks.
	gittest.Git(t, dir, "checkout", "-q", "-b", "feature/x")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "x work")
	open := storeCreate(t, dir, model.CreateTask{Nonce: model.NewNonce(), Title: "open", Type: model.TypeTask, Branch: "feature/x"}).(model.Task)
	wip := storeCreate(t, dir, model.CreateTask{Nonce: model.NewNonce(), Title: "wip", Type: model.TypeTask, Branch: "feature/x"}).(model.Task)
	storeAppend(t, dir, refs.For(model.KindTask, wip.ID), model.SetStatus{Status: model.StatusInProgress})
	gittest.Git(t, dir, "checkout", "-q", "main")
	gittest.Git(t, dir, "merge", "-q", "--no-ff", "-m", "merge x", "feature/x")

	// feature/y never merges, so its task stays put.
	gittest.Git(t, dir, "checkout", "-q", "-b", "feature/y")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "y work")
	stray := storeCreate(t, dir, model.CreateTask{Nonce: model.NewNonce(), Title: "stray", Type: model.TypeTask, Branch: "feature/y"}).(model.Task)
	gittest.Git(t, dir, "checkout", "-q", "main")

	report, err := c.Reconcile(ctx, notes.ReconcileOptions{Into: "main"})
	if err != nil {
		t.Fatalf("Reconcile(main): %v", err)
	}
	if got, want := report.Into, model.Branch("main"); got != want {
		t.Errorf("Into = %q, want %q", got, want)
	}
	if got, want := report.Scanned(), 2; got != want {
		t.Errorf("Scanned = %d, want %d", got, want)
	}
	if got, want := report.Merged(), 1; got != want {
		t.Errorf("Merged = %d, want %d", got, want)
	}
	if got, want := report.Carried(), 2; got != want {
		t.Errorf("Carried = %d, want %d", got, want)
	}

	bx := findBranchResult(t, report, "feature/x")
	if !bx.Merged || bx.Reason != "" || len(bx.Tasks) != 2 {
		t.Errorf("feature/x result = %+v, want merged, no reason, 2 tasks", bx)
	}
	by := findBranchResult(t, report, "feature/y")
	if by.Merged || by.Reason != "not merged" || len(by.Tasks) != 1 {
		t.Errorf("feature/y result = %+v, want unmerged, reason \"not merged\", 1 task", by)
	}

	// The merged branch's tasks now fold to main; the unmerged one's does not.
	for _, id := range []model.EntityID{open.ID, wip.ID} {
		if got := taskBranch(ctx, t, c, id); got != "main" {
			t.Errorf("task %s branch = %q, want main after reconcile", id.Short(), got)
		}
	}
	if got := taskBranch(ctx, t, c, stray.ID); got != "feature/y" {
		t.Errorf("stray task branch = %q, want feature/y (unmoved)", got)
	}
}

func TestReconcileDryRunPlansWithoutMoving(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	gittest.Git(t, dir, "checkout", "-q", "-b", "feature/z")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "z work")
	task := storeCreate(t, dir, model.CreateTask{Nonce: model.NewNonce(), Title: "planned", Type: model.TypeTask, Branch: "feature/z"}).(model.Task)
	gittest.Git(t, dir, "checkout", "-q", "main")
	gittest.Git(t, dir, "merge", "-q", "--no-ff", "-m", "merge z", "feature/z")

	report, err := c.Reconcile(ctx, notes.ReconcileOptions{Into: "main", DryRun: true})
	if err != nil {
		t.Fatalf("Reconcile(dry-run): %v", err)
	}
	if got, want := report.Carried(), 1; got != want {
		t.Errorf("Carried = %d, want %d (the planned move)", got, want)
	}
	if got := taskBranch(ctx, t, c, task.ID); got != "feature/z" {
		t.Errorf("task branch = %q, want feature/z (dry run must not move it)", got)
	}
}

func TestGCTidiesOrphanedCacheEntry(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	note := storeCreate(t, dir, model.CreateNote{Nonce: model.NewNonce(), Title: "n"}).(model.Note)
	ref := refs.For(model.KindNote, note.ID)

	// Load caches the current tip; the append orphans it behind a new tip.
	if _, err := c.Note(ctx, note.ID); err != nil {
		t.Fatalf("Note (populate cache): %v", err)
	}
	storeAppend(t, dir, ref, model.AddTag{Tag: "x"})

	report, err := c.GC(ctx, notes.GCOptions{})
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if report.Tidied != 1 {
		t.Errorf("Tidied = %d, want 1 (the orphaned cache entry)", report.Tidied)
	}
	if report.Pruned != 0 || report.Failed != 0 {
		t.Errorf("Pruned/Failed = %d/%d, want 0/0 without PruneRemote", report.Pruned, report.Failed)
	}
}

func TestGCPruneRemoteDeletesTombstone(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	bare := gittest.InitBare(t)
	gittest.Git(t, dir, "remote", "add", "origin", bare)

	note := storeCreate(t, dir, model.CreateNote{Nonce: model.NewNonce(), Title: "doomed"}).(model.Note)
	ref := refs.For(model.KindNote, note.ID)
	storeAppend(t, dir, ref, model.DeleteNote{})
	gittest.Git(t, dir, "push", "origin", ref+":"+ref)

	report, err := c.GC(ctx, notes.GCOptions{PruneRemote: true})
	if err != nil {
		t.Fatalf("GC(prune): %v", err)
	}
	if report.Pruned != 1 || report.Failed != 0 {
		t.Errorf("Pruned/Failed = %d/%d, want 1/0", report.Pruned, report.Failed)
	}
	if _, err := c.Note(ctx, note.ID); !errors.Is(err, notes.ErrRefNotFound) {
		t.Errorf("Note after prune = %v, want ErrRefNotFound (local ref deleted)", err)
	}
	if got := gittest.Git(t, bare, "for-each-ref", "--format=%(refname)", ref); got != "" {
		t.Errorf("remote ref after prune = %q, want empty", got)
	}
}

// findBranchResult returns the BranchResult for branch in report, failing when
// it is absent.
func findBranchResult(t *testing.T, report notes.ReconcileReport, branch model.Branch) notes.BranchResult {
	t.Helper()
	for _, b := range report.Branches {
		if b.Branch == branch {
			return b
		}
	}
	t.Fatalf("branch %q not in report %+v", branch, report.Branches)
	return notes.BranchResult{}
}

// taskBranch loads the task and returns its folded branch.
func taskBranch(ctx context.Context, t *testing.T, c *notes.Client, id model.EntityID) model.Branch {
	t.Helper()
	task, err := c.Task(ctx, id)
	if err != nil {
		t.Fatalf("Task(%s): %v", id.Short(), err)
	}
	return task.Branch
}
