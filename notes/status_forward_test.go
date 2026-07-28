package notes

import (
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// appendForeignOp extends an entity's chain with one op behind the store's
// back. Append folds strictly and would refuse it, which is the point: the op
// stands in for one a newer cc-notes writes and this binary's folder skips.
func appendForeignOp(t *testing.T, c *Client, kind model.Kind, id model.EntityID, op model.Op) {
	t.Helper()
	ctx := t.Context()
	ref := refs.For(kind, id)
	tip, err := c.s.Repo.Tip(ctx, ref)
	if err != nil {
		t.Fatalf("Tip(%s): %v", ref, err)
	}
	chain, err := c.s.Repo.ReadChain(ctx, tip)
	if err != nil {
		t.Fatalf("ReadChain(%s): %v", ref, err)
	}
	var lamport model.Lamport
	for _, commit := range chain {
		lamport = max(lamport, commit.Pack.Lamport)
	}
	sig := gitobj.Signature{Name: "Test User", Email: "test@example.com", When: time.Unix(1700000000, 0)}
	pack := model.Pack{Lamport: lamport + 1, Session: "newer-cc-notes", Ops: []model.Op{op}}
	sha, err := c.s.Repo.WriteOpsCommit(ctx, []model.SHA{tip}, sig, "cc-notes: "+op.OpKind(), pack)
	if err != nil {
		t.Fatalf("WriteOpsCommit(%s): %v", ref, err)
	}
	if err := c.s.Git.UpdateRef(ctx, ref, sha, tip); err != nil {
		t.Fatalf("UpdateRef(%s): %v", ref, err)
	}
}

// TestStatusCountsSkippedOps folds real chains carrying ops this binary cannot
// apply through Client.Status and pins the repo-wide total — the one surface
// that tells a user their cc-notes is behind the history in the repo. The
// sprint row is load-bearing: sprints and projects contribute nothing else to
// the report, and are folded only so the total covers every entity kind.
func TestStatusCountsSkippedOps(t *testing.T) {
	c, dir := newWBClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")

	created, err := c.CreateTask(ctx, TaskSpec{Title: "ship it", Branch: "main"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	sprint, _, err := c.CreateSprint(ctx, SprintSpec{Title: "week one"})
	if err != nil {
		t.Fatalf("CreateSprint: %v", err)
	}

	before, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if before.SkippedOps != 0 {
		t.Fatalf("SkippedOps = %d over history this binary folds whole, want 0", before.SkippedOps)
	}

	appendForeignOp(t, c, model.KindTask, created.Task.ID, model.AddTag{Tag: "x"})
	appendForeignOp(t, c, model.KindSprint, sprint.ID, model.SetStatus{Status: model.StatusDone})

	after, err := c.Status(ctx)
	if err != nil {
		t.Fatalf("Status over newer history: %v", err)
	}
	if after.SkippedOps != 2 {
		t.Fatalf("SkippedOps = %d, want 2: one op on the task chain, one on the sprint chain", after.SkippedOps)
	}
	if len(after.YourBranch) != 1 || after.YourBranch[0].Title != "ship it" {
		t.Fatalf("YourBranch = %v, want the task still folded", after.YourBranch)
	}
}
