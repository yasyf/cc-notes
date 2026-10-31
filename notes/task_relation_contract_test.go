package notes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestTaskRelationsPreserveEmptyIDsAndLifecycle(t *testing.T) {
	c, _ := newWBClient(t)
	target := statusCreate(t, c, model.CreateTask{Nonce: model.NewNonce(), Title: "target", Type: model.TypeTask}).EntityID()
	root := statusCreate(t, c, model.CreateTask{Nonce: model.NewNonce(), Title: "other root", Type: model.TypeTask}).EntityID()
	children := make([]model.EntityID, 0, 2)
	for _, status := range []model.Status{model.StatusDone, model.StatusCancelled} {
		child := statusCreate(t, c,
			model.CreateTask{Nonce: model.NewNonce(), Title: string(status), Type: model.TypeTask, Parent: target},
			model.AddDep{ID: target},
			model.AddDep{ID: target},
			model.SetStatus{Status: status},
		)
		children = append(children, child.EntityID())
	}
	statusCreate(t, c,
		model.CreateTask{Nonce: model.NewNonce(), Title: "deleted", Type: model.TypeTask, Parent: target},
		model.AddDep{ID: target}, model.DeleteNote{},
	)
	slices.Sort(children)
	selected := []model.EntityID{target, root, target, "unknown", ""}
	wantBlocks := map[model.EntityID][]model.EntityID{target: children, root: nil, "unknown": nil, "": nil}
	if got, err := c.TasksBlockingFor(t.Context(), selected); err != nil || !reflect.DeepEqual(got, wantBlocks) {
		t.Fatalf("selected blockers = %#v, %v; want %#v", got, err, wantBlocks)
	}
	if got, err := c.TasksBlockingFor(t.Context(), nil); err != nil || len(got) != 0 {
		t.Fatalf("empty selected blockers = %#v, %v", got, err)
	}
	if got, err := c.TasksBlocking(t.Context(), target); err != nil || !slices.Equal(got, children) {
		t.Fatalf("blocking includes terminal live tasks = %v, %v; want %v", got, err, children)
	}
	if got, err := c.TaskChildren(t.Context(), target); err != nil || !slices.Equal(got, children) {
		t.Fatalf("children includes terminal live tasks = %v, %v; want %v", got, err, children)
	}
	roots := []model.EntityID{target, root}
	slices.Sort(roots)
	if got, err := c.TaskChildren(t.Context(), ""); err != nil || !slices.Equal(got, roots) {
		t.Fatalf("empty parent roots = %v, %v; want %v", got, err, roots)
	}
	if got, err := c.TasksBlocking(t.Context(), ""); err != nil || got != nil {
		t.Fatalf("empty blocker = %v, %v", got, err)
	}
	if got, err := c.TaskChildren(t.Context(), "unknown"); err != nil || got != nil {
		t.Fatalf("unknown parent = %v, %v", got, err)
	}
}

func taskRelationRunbookAt(t *testing.T, c *Client, at int64, runs ...model.StartRun) model.Runbook {
	t.Helper()
	ops := make([]model.Op, 0, 1+len(runs))
	ops = append(ops, model.CreateRunbook{Nonce: model.NewNonce(), Title: "relation fixture"})
	for _, run := range runs {
		ops = append(ops, run)
	}
	tip, err := c.s.Repo.WriteOpsCommit(t.Context(), nil, gitobj.Signature{Name: "Contract User", Email: "contract@example.com", When: time.Unix(at, 0).UTC()}, "run relation fixture", model.Pack{Lamport: 1, Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	id := model.EntityID(tip)
	if err := c.s.RecordsGit.UpdateRef(t.Context(), refs.For(model.KindRunbook, id), tip, ""); err != nil {
		t.Fatal(err)
	}
	runbook, err := c.Runbook(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return runbook
}

func TestTaskRunsPreserveUncitedRunsAndExactOrdering(t *testing.T) {
	c, _ := newWBClient(t)
	target := statusCreate(t, c, model.CreateTask{Nonce: model.NewNonce(), Title: "served", Type: model.TypeTask}).EntityID()
	early := taskRelationRunbookAt(t, c, 99, model.StartRun{ID: "early", Task: target})
	first := taskRelationRunbookAt(t, c, 100, model.StartRun{ID: "z", Task: target}, model.StartRun{ID: "a", Task: target}, model.StartRun{ID: "uncited"})
	second := taskRelationRunbookAt(t, c, 100, model.StartRun{ID: "c", Task: target}, model.StartRun{ID: "b", Task: target})
	deleted := taskRelationRunbookAt(t, c, 98, model.StartRun{ID: "deleted", Task: target}, model.StartRun{ID: "deleted-uncited"})
	if _, err := c.s.Append(t.Context(), refs.For(model.KindRunbook, deleted.ID), []model.Op{model.DeleteNote{}}); err != nil {
		t.Fatal(err)
	}
	want := []TaskRun{{Runbook: early.ID, Run: early.Runs[0]}}
	ties := []model.Runbook{first, second}
	slices.SortFunc(ties, func(a, b model.Runbook) int {
		if a.ID < b.ID {
			return -1
		}
		if a.ID > b.ID {
			return 1
		}
		return 0
	})
	for _, book := range ties {
		ids := []string{"a", "z"}
		if book.ID == second.ID {
			ids = []string{"b", "c"}
		}
		for _, id := range ids {
			for _, run := range book.Runs {
				if run.ID == id {
					want = append(want, TaskRun{Runbook: book.ID, Run: run})
				}
			}
		}
	}
	if got, err := c.TaskRuns(t.Context(), target); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("ordered full run values = %#v, %v; want %#v", got, err, want)
	}
	var uncited model.RunbookRun
	for _, run := range first.Runs {
		if run.ID == "uncited" {
			uncited = run
		}
	}
	if got, err := c.TaskRuns(t.Context(), ""); err != nil || !reflect.DeepEqual(got, []TaskRun{{Runbook: first.ID, Run: uncited}}) {
		t.Fatalf("uncited runs = %#v, %v", got, err)
	}
	if got, err := c.TaskRuns(t.Context(), "unknown"); err != nil || got != nil {
		t.Fatalf("unknown task runs = %#v, %v", got, err)
	}
}

type taskRelationValues struct {
	blocks   []model.EntityID
	children []model.EntityID
	runs     []TaskRun
}

func taskRelationContractValues(t *testing.T, c *Client, id model.EntityID) taskRelationValues {
	t.Helper()
	blocks, err := c.TasksBlocking(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := c.TasksBlockingFor(t.Context(), []model.EntityID{id, id})
	if err != nil || !reflect.DeepEqual(batch, map[model.EntityID][]model.EntityID{id: blocks}) {
		t.Fatalf("selected blocker batch = %#v, %v; want %v", batch, err, blocks)
	}
	children, err := c.TaskChildren(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := c.TaskRuns(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return taskRelationValues{blocks: blocks, children: children, runs: runs}
}

func TestTaskRelationsPreservePinnedAndUnavailableCacheViews(t *testing.T) {
	c, dir := newWBClient(t)
	target := statusCreate(t, c, model.CreateTask{Nonce: model.NewNonce(), Title: "target", Type: model.TypeTask}).EntityID()
	statusCreate(t, c, model.CreateTask{Nonce: model.NewNonce(), Title: "old child", Type: model.TypeTask, Parent: target}, model.AddDep{ID: target})
	taskRelationRunbookAt(t, c, 100, model.StartRun{ID: "old", Task: target})
	view, hit, err := c.s.StatusMetadata(t.Context(), model.KindTask, model.KindRunbook)
	if err != nil || !hit {
		t.Fatalf("initial metadata = %v, %v", hit, err)
	}
	old := FromStore(view.Captured)
	wantOld := taskRelationContractValues(t, old, target)
	statusCreate(t, c, model.CreateTask{Nonce: model.NewNonce(), Title: "new child", Type: model.TypeTask, Parent: target}, model.AddDep{ID: target})
	taskRelationRunbookAt(t, c, 101, model.StartRun{ID: "new", Task: target})
	wantLive := taskRelationContractValues(t, c, target)
	if len(wantLive.blocks) != 2 || len(wantLive.children) != 2 || len(wantLive.runs) != 2 {
		t.Fatalf("new live relations = %#v", wantLive)
	}
	if got := taskRelationContractValues(t, old, target); !reflect.DeepEqual(got, wantOld) {
		t.Fatalf("old pinned universe = %#v, want %#v", got, wantOld)
	}
	empty := FromStore(c.s.Pinned(map[string]model.SHA{}))
	if got := taskRelationContractValues(t, empty, target); !reflect.DeepEqual(got, taskRelationValues{}) {
		t.Fatalf("empty pinned universe = %#v", got)
	}
	fresh, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	path := filepath.Join(fresh.s.CommonDir(), "cc-notes-query")
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unavailable derived index"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := taskRelationContractValues(t, fresh, target); !reflect.DeepEqual(got, wantLive) {
		t.Fatalf("authoritative live cache miss = %#v, want %#v", got, wantLive)
	}
	if got := taskRelationContractValues(t, old, target); !reflect.DeepEqual(got, wantOld) {
		t.Fatalf("authoritative pinned cache miss = %#v, want %#v", got, wantOld)
	}
}

func TestTaskRelationsCancellationHasNoResult(t *testing.T) {
	c, _ := newWBClient(t)
	target := statusCreate(t, c, model.CreateTask{Nonce: model.NewNonce(), Title: "target", Type: model.TypeTask}).EntityID()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := c.TasksBlocking(ctx, target); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled blockers = %v, %v", got, err)
	}
	for _, ids := range [][]model.EntityID{nil, {target}} {
		if got, err := c.TasksBlockingFor(ctx, ids); got != nil || !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled selected blockers = %v, %v", got, err)
		}
	}
	if got, err := c.TaskChildren(ctx, target); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled children = %v, %v", got, err)
	}
	if got, err := c.TaskRuns(ctx, target); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled runs = %v, %v", got, err)
	}
}

func TestTaskRunsUnreadableSelectedBodyIsAnError(t *testing.T) {
	c, dir := newWBClient(t)
	target := statusCreate(t, c, model.CreateTask{Nonce: model.NewNonce(), Title: "target", Type: model.TypeTask}).EntityID()
	selected := taskRelationRunbookAt(t, c, 100, model.StartRun{ID: "selected", Task: target})
	if got, err := c.TaskRuns(t.Context(), target); err != nil || len(got) != 1 {
		t.Fatalf("initial selected body = %#v, %v", got, err)
	}
	removeStatusCommit(t, c, selected.Head)
	clearStatusFoldCache(t, c)
	fresh, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	if got, err := fresh.TaskRuns(t.Context(), target); got != nil || !errors.Is(err, gitobj.ErrIncompleteChain) {
		t.Fatalf("unreadable selected runbook = %#v, %v; want incomplete-chain error", got, err)
	}
}
