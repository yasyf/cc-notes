package sync_test

import (
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// TestReconcileBoundMovesRecordsByCheckoutAncestry reconciles a depth-1 clone
// bound to its full source. Branch tips and ancestry come from the clone: a
// branch merged only there moves its task, and a branch that exists only in
// the source is "branch ref missing" even though the source merged it. The
// tracking fold reads the records repository's remotes, so a task staged only
// in the source's tracking namespace is folded and carried. Every SetBranch op
// lands in the source; the clone gains no cc-notes ref.
func TestReconcileBoundMovesRecordsByCheckoutAncestry(t *testing.T) {
	source := gittest.InitRepo(t)
	gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "init")
	thin := gittest.ShallowClone(t, source, 1)
	gittest.Git(t, source, "checkout", "-q", "-b", "feature/src")
	gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "source-only work")
	gittest.Git(t, source, "checkout", "-q", "main")
	gittest.Git(t, source, "merge", "-q", "--no-ff", "-m", "merge feature/src", "feature/src")
	if _, err := store.Bind(t.Context(), thin, source); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	s, err := store.Open(thin)
	if err != nil {
		t.Fatalf("Open(%s): %v", thin, err)
	}

	branchFrom(t, thin, "feature/x")
	merged := createTask(t, s, "merged in the clone", "feature/x")
	staged := createTask(t, s, "staged in the source's tracking namespace", "feature/x")
	sourceOnly := createTask(t, s, "on a source-only branch", "feature/src")
	mergeInto(t, thin, "main", "feature/x")

	stagedRef := refs.For(model.KindTask, staged.ID)
	stagedTip := gittest.Git(t, source, "rev-parse", stagedRef)
	gittest.Git(t, source, "remote", "add", "up", "https://example.com/x.git")
	gittest.Git(t, source, "update-ref", "refs/cc-notes-sync/up/tasks/"+string(staged.ID), stagedTip)
	gittest.Git(t, source, "update-ref", "-d", stagedRef)

	report := reconcile(t, s, "main", nil, false, false)

	if br := findBranch(t, report, "feature/src"); br.Merged || br.Reason != "branch ref missing" {
		t.Errorf("feature/src = %+v, want branch ref missing: the clone has no such branch", br)
	}
	x := findBranch(t, report, "feature/x")
	if !x.Merged || !slices.Equal(taskIDs(x.Tasks), slices.Sorted(slices.Values([]model.EntityID{merged.ID, staged.ID}))) {
		t.Errorf("feature/x = %+v, want merged carrying both tasks", x)
	}
	for _, id := range []model.EntityID{merged.ID, staged.ID} {
		ref := refs.For(model.KindTask, id)
		task := loadTask(t, s, ref)
		if task.Branch != "main" {
			t.Errorf("task %s branch = %q, want main", id.Short(), task.Branch)
		}
		if got := model.SHA(gittest.Git(t, source, "rev-parse", ref)); got != task.Head {
			t.Errorf("source %s = %s, want the moved head %s", ref, got, task.Head)
		}
	}
	if got := loadTask(t, s, refs.For(model.KindTask, sourceOnly.ID)).Branch; got != "feature/src" {
		t.Errorf("source-only task branch = %q, want feature/src untouched", got)
	}
	if got := gittest.Git(t, thin, "for-each-ref", "refs/cc-notes/", "refs/cc-notes-sync/"); got != "" {
		t.Errorf("clone gained cc-notes refs:\n%s", got)
	}
}
