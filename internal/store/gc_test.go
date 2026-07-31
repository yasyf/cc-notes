package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// initBareRemote creates a bare repository and wires it as origin on s.
func initBareRemote(t *testing.T, s *Store) string {
	t.Helper()
	bare := gittest.InitBare(t)
	gittest.Git(t, s.Git.Dir, "remote", "add", "origin", bare)
	return bare
}

func TestPruneTombstonesDeletesNoteRefLocalAndRemote(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	bare := initBareRemote(t, s)

	note := create(t, s, noteOps("doomed")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)
	if _, err := s.Append(ctx, ref, []model.Op{model.DeleteNote{}}); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	gittest.Git(t, s.Git.Dir, "push", "origin", ref+":"+ref)

	// A stale clone fetches the tombstoned ref before the prune, so it can later
	// re-advertise what the prune removed.
	stale := t.TempDir()
	gittest.Git(t, stale, "init", "-q", "-b", "main")
	gittest.Git(t, stale, "remote", "add", "origin", bare)
	gittest.Git(t, stale, "fetch", "origin", ref+":"+ref)

	pruned, failed, err := s.PruneTombstones(ctx, "origin")
	if err != nil {
		t.Fatalf("PruneTombstones: %v", err)
	}
	if pruned != 1 || failed != 0 {
		t.Fatalf("pruned/failed = %d/%d, want 1/0", pruned, failed)
	}
	if _, err := s.Repo.Tip(ctx, ref); !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Fatalf("local ref still present: %v", err)
	}
	if got := gittest.Git(t, bare, "for-each-ref", "--format=%(refname)", ref); got != "" {
		t.Fatalf("remote ref still present after prune: %q", got)
	}

	// Pin the documented non-convergence: the stale clone re-advertises the ref
	// and resurrects it on the remote.
	gittest.Git(t, stale, "push", "origin", ref+":"+ref)
	if got := gittest.Git(t, bare, "for-each-ref", "--format=%(refname)", ref); got != ref {
		t.Fatalf("stale clone did not resurrect ref: for-each-ref = %q, want %q", got, ref)
	}
}

func TestPruneTombstonesSkipsSupersededAndTasks(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	initBareRemote(t, s)

	old := create(t, s, noteOps("old")).(model.Note)
	replacement := create(t, s, noteOps("new")).(model.Note)
	oldRef := refs.For(model.KindNote, old.ID)
	if _, err := s.Append(ctx, oldRef, []model.Op{model.AddSupersededBy{ID: replacement.ID}}); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	task := create(t, s, taskOps("ship it", "main")).(model.Task)
	taskRef := refs.For(model.KindTask, task.ID)
	if _, err := s.Append(ctx, taskRef, []model.Op{model.SetStatus{Status: model.StatusDone}}); err != nil {
		t.Fatalf("done: %v", err)
	}

	pruned, failed, err := s.PruneTombstones(ctx, "origin")
	if err != nil {
		t.Fatalf("PruneTombstones: %v", err)
	}
	if pruned != 0 || failed != 0 {
		t.Fatalf("pruned/failed = %d/%d, want 0/0", pruned, failed)
	}
	if _, err := s.Repo.Tip(ctx, oldRef); err != nil {
		t.Fatalf("superseded note ref pruned: %v", err)
	}
	if _, err := s.Repo.Tip(ctx, taskRef); err != nil {
		t.Fatalf("done task ref pruned: %v", err)
	}
}

func TestPruneTombstonesDeletesDocRefLocalAndRemote(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	bare := initBareRemote(t, s)

	doc := create(t, s, docOps("doomed")).(model.Doc)
	ref := refs.For(model.KindDoc, doc.ID)
	if _, err := s.Append(ctx, ref, []model.Op{model.DeleteNote{}}); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	gittest.Git(t, s.Git.Dir, "push", "origin", ref+":"+ref)

	pruned, failed, err := s.PruneTombstones(ctx, "origin")
	if err != nil {
		t.Fatalf("PruneTombstones: %v", err)
	}
	if pruned != 1 || failed != 0 {
		t.Fatalf("pruned/failed = %d/%d, want 1/0", pruned, failed)
	}
	if _, err := s.Repo.Tip(ctx, ref); !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Fatalf("local doc ref still present: %v", err)
	}
	if got := gittest.Git(t, bare, "for-each-ref", "--format=%(refname)", ref); got != "" {
		t.Fatalf("remote doc ref still present after prune: %q", got)
	}
}

func TestPruneTombstonesDeletesLogRefLocalAndRemote(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	bare := initBareRemote(t, s)

	log := create(t, s, logOps("doomed")).(model.Log)
	ref := refs.For(model.KindLog, log.ID)
	if _, err := s.Append(ctx, ref, []model.Op{model.DeleteNote{}}); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	gittest.Git(t, s.Git.Dir, "push", "origin", ref+":"+ref)

	pruned, failed, err := s.PruneTombstones(ctx, "origin")
	if err != nil {
		t.Fatalf("PruneTombstones: %v", err)
	}
	if pruned != 1 || failed != 0 {
		t.Fatalf("pruned/failed = %d/%d, want 1/0", pruned, failed)
	}
	if _, err := s.Repo.Tip(ctx, ref); !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Fatalf("local log ref still present: %v", err)
	}
	if got := gittest.Git(t, bare, "for-each-ref", "--format=%(refname)", ref); got != "" {
		t.Fatalf("remote log ref still present after prune: %q", got)
	}
}

func TestPruneTombstonesDeletesRunbookRefLocalAndRemote(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	bare := initBareRemote(t, s)

	keep := create(t, s, runbookOps("keep")).(model.Runbook)
	keepRef := refs.For(model.KindRunbook, keep.ID)

	rb := create(t, s, runbookOps("doomed")).(model.Runbook)
	ref := refs.For(model.KindRunbook, rb.ID)
	if _, err := s.Append(ctx, ref, []model.Op{model.DeleteNote{}}); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	gittest.Git(t, s.Git.Dir, "push", "origin", ref+":"+ref)

	pruned, failed, err := s.PruneTombstones(ctx, "origin")
	if err != nil {
		t.Fatalf("PruneTombstones: %v", err)
	}
	if pruned != 1 || failed != 0 {
		t.Fatalf("pruned/failed = %d/%d, want 1/0", pruned, failed)
	}
	if _, err := s.Repo.Tip(ctx, ref); !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Fatalf("local runbook ref still present: %v", err)
	}
	if got := gittest.Git(t, bare, "for-each-ref", "--format=%(refname)", ref); got != "" {
		t.Fatalf("remote runbook ref still present after prune: %q", got)
	}
	if _, err := s.Repo.Tip(ctx, keepRef); err != nil {
		t.Fatalf("active runbook ref pruned: %v", err)
	}
}

func TestPruneTombstonesDeletesInvestigationRefLocalAndRemote(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	bare := initBareRemote(t, s)

	keep := create(t, s, investigationOps("keep")).(model.Investigation)
	keepRef := refs.For(model.KindInvestigation, keep.ID)

	inv := create(t, s, investigationOps("doomed")).(model.Investigation)
	ref := refs.For(model.KindInvestigation, inv.ID)
	if _, err := s.Append(ctx, ref, []model.Op{model.DeleteNote{}}); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	gittest.Git(t, s.Git.Dir, "push", "origin", ref+":"+ref)

	pruned, failed, err := s.PruneTombstones(ctx, "origin")
	if err != nil {
		t.Fatalf("PruneTombstones: %v", err)
	}
	if pruned != 1 || failed != 0 {
		t.Fatalf("pruned/failed = %d/%d, want 1/0", pruned, failed)
	}
	if _, err := s.Repo.Tip(ctx, ref); !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Fatalf("local investigation ref still present: %v", err)
	}
	if got := gittest.Git(t, bare, "for-each-ref", "--format=%(refname)", ref); got != "" {
		t.Fatalf("remote investigation ref still present after prune: %q", got)
	}
	if _, err := s.Repo.Tip(ctx, keepRef); err != nil {
		t.Fatalf("live investigation ref pruned: %v", err)
	}
}

func TestPruneTombstonesDeletesPlanRefLocalAndRemote(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	bare := initBareRemote(t, s)

	keep := create(t, s, planOps("keep")).(model.Plan)
	keepRef := refs.For(model.KindPlan, keep.ID)

	plan := create(t, s, planOps("doomed")).(model.Plan)
	ref := refs.For(model.KindPlan, plan.ID)
	if _, err := s.Append(ctx, ref, []model.Op{model.DeleteNote{}}); err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	gittest.Git(t, s.Git.Dir, "push", "origin", ref+":"+ref)

	pruned, failed, err := s.PruneTombstones(ctx, "origin")
	if err != nil {
		t.Fatalf("PruneTombstones: %v", err)
	}
	if pruned != 1 || failed != 0 {
		t.Fatalf("pruned/failed = %d/%d, want 1/0", pruned, failed)
	}
	if _, err := s.Repo.Tip(ctx, ref); !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Fatalf("local plan ref still present: %v", err)
	}
	if got := gittest.Git(t, bare, "for-each-ref", "--format=%(refname)", ref); got != "" {
		t.Fatalf("remote plan ref still present after prune: %q", got)
	}
	if _, err := s.Repo.Tip(ctx, keepRef); err != nil {
		t.Fatalf("live plan ref pruned: %v", err)
	}
}

// TestPruneTombstonesSkipsClosedPlan pins that a plan's terminal statuses are
// not tombstones: only DeleteNote removes a ref, so the record of a done or
// abandoned plan survives gc.
func TestPruneTombstonesSkipsClosedPlan(t *testing.T) {
	for _, status := range []model.PlanStatus{model.PlanDone, model.PlanAbandoned} {
		t.Run(string(status), func(t *testing.T) {
			s := initStore(t)
			ctx := t.Context()
			initBareRemote(t, s)

			plan := create(t, s, planOps("closed")).(model.Plan)
			ref := refs.For(model.KindPlan, plan.ID)
			if _, err := s.Append(ctx, ref, []model.Op{model.SetPlanStatus{Status: status}}); err != nil {
				t.Fatalf("close: %v", err)
			}

			pruned, failed, err := s.PruneTombstones(ctx, "origin")
			if err != nil {
				t.Fatalf("PruneTombstones: %v", err)
			}
			if pruned != 0 || failed != 0 {
				t.Fatalf("pruned/failed = %d/%d, want 0/0", pruned, failed)
			}
			if _, err := s.Repo.Tip(ctx, ref); err != nil {
				t.Fatalf("%s plan ref pruned: %v", status, err)
			}
		})
	}
}

func TestPruneTombstonesSkipsSupersededDoc(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	initBareRemote(t, s)

	old := create(t, s, docOps("old")).(model.Doc)
	replacement := create(t, s, docOps("new")).(model.Doc)
	oldRef := refs.For(model.KindDoc, old.ID)
	if _, err := s.Append(ctx, oldRef, []model.Op{model.AddSupersededBy{ID: replacement.ID}}); err != nil {
		t.Fatalf("supersede: %v", err)
	}

	pruned, failed, err := s.PruneTombstones(ctx, "origin")
	if err != nil {
		t.Fatalf("PruneTombstones: %v", err)
	}
	if pruned != 0 || failed != 0 {
		t.Fatalf("pruned/failed = %d/%d, want 0/0", pruned, failed)
	}
	if _, err := s.Repo.Tip(ctx, oldRef); err != nil {
		t.Fatalf("superseded doc ref pruned: %v", err)
	}
}

// TestGCLocalKeepsEveryKindsLiveEntry pins liveTips against model.Kinds(). A
// kind missing from that scan has its fold-cache entry evicted on every GCLocal
// run despite the entity being live — what sprint, project, and runbook silently
// suffered while the kind list was spelled out by hand.
func TestGCLocalKeepsEveryKindsLiveEntry(t *testing.T) {
	s := initStore(t)
	byKind := map[model.Kind][]model.Op{
		model.KindNote:          noteOps("n"),
		model.KindDoc:           docOps("d"),
		model.KindLog:           logOps("l"),
		model.KindTask:          taskOps("t", "main"),
		model.KindSprint:        sprintOps("sp"),
		model.KindProject:       projectOps("pr"),
		model.KindRunbook:       runbookOps("rb"),
		model.KindInvestigation: investigationOps("iv"),
		model.KindPlan:          planOps("pl"),
	}
	tips := make(map[model.Kind]model.SHA, len(byKind))
	for _, kind := range model.Kinds() {
		ops, ok := byKind[kind]
		if !ok {
			t.Fatalf("kind %q has no create ops in this table", kind)
		}
		tips[kind] = create(t, s, ops).Meta().Head
	}

	tidied, err := s.GCLocal(t.Context())
	if err != nil {
		t.Fatalf("GCLocal: %v", err)
	}
	if tidied != 0 {
		t.Fatalf("tidied = %d, want 0: GCLocal evicted a live entity's cache entry", tidied)
	}
	for _, kind := range model.Kinds() {
		if _, ok := s.cache.get(tips[kind]); !ok {
			t.Errorf("%s: live cache entry evicted by GCLocal", kind)
		}
	}
}

func TestGCLocalPrunesOrphanedCacheEntry(t *testing.T) {
	s := initStore(t)
	ctx := t.Context()
	note := create(t, s, noteOps("v1")).(model.Note)
	ref := refs.For(model.KindNote, note.ID)

	// Populate the cache against the current tip, then append so that tip is
	// orphaned and a fresh entry is written for the new tip.
	if _, err := s.Load(ctx, ref); err != nil {
		t.Fatalf("Load (populate): %v", err)
	}
	oldTip, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		t.Fatalf("Tip pre: %v", err)
	}
	if _, err := s.Append(ctx, ref, []model.Op{model.AddTag{Tag: "x"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	newTip, err := s.Repo.Tip(ctx, ref)
	if err != nil {
		t.Fatalf("Tip post: %v", err)
	}

	dir := s.cache.dir
	if _, err := os.Stat(filepath.Join(dir, string(oldTip))); err != nil {
		t.Fatalf("orphaned entry missing before gc: %v", err)
	}

	tidied, err := s.GCLocal(ctx)
	if err != nil {
		t.Fatalf("GCLocal: %v", err)
	}
	if tidied != 1 {
		t.Fatalf("tidied = %d, want 1", tidied)
	}
	if _, err := os.Stat(filepath.Join(dir, string(oldTip))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned entry survived gc: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, string(newTip))); err != nil {
		t.Fatalf("live entry removed by gc: %v", err)
	}
}
