package notes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func statusCreate(t *testing.T, c *Client, ops ...model.Op) model.Snapshot {
	t.Helper()
	snapshot, err := c.s.CreateExact(t.Context(), ops)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func statusParity(t *testing.T, c *Client) StatusReport {
	t.Helper()
	ctx := t.Context()
	view, hit, err := c.s.StatusMetadata(ctx, statusKinds...)
	if err != nil || !hit {
		t.Fatalf("metadata hit=%t error=%v", hit, err)
	}
	v := FromStore(view.Captured)
	branch, _, err := v.currentBranchOrBacklog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ttl, err := v.LeaseTTL(ctx)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	want, err := v.statusFromSnapshots(ctx, branch, now, ttl)
	if err != nil {
		t.Fatal(err)
	}
	got, err := v.statusFromMetadata(ctx, view.Records, branch, now, ttl)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("metadata status = %#v, %v; want %#v", got, err, want)
	}
	public, err := c.Status(ctx)
	if err != nil || !reflect.DeepEqual(public, want) {
		t.Fatalf("public status = %#v, %v; want %#v", public, err, want)
	}
	return public
}

func TestStatusMetadataMatchesAuthoritativeReport(t *testing.T) {
	c, dir := newWBClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
	blocker := statusCreate(t, c,
		model.CreateTask{Nonce: model.NewNonce(), Title: "done blocker", Type: model.TypeTask, Branch: "elsewhere"},
		model.SetStatus{Status: model.StatusDone},
	).EntityID()
	statusCreate(t, c,
		model.CreateTask{Nonce: model.NewNonce(), Title: "ready backlog", Type: model.TypeTask, Priority: 2},
		model.AddDep{ID: blocker},
	)
	statusCreate(t, c, model.CreateTask{Nonce: model.NewNonce(), Title: "current", Type: model.TypeTask, Branch: "main"})
	statusCreate(t, c, model.CreateTask{Nonce: model.NewNonce(), Title: "hidden", Type: model.TypeTask, Branch: "elsewhere"})
	statusCreate(t, c,
		model.CreateTask{Nonce: model.NewNonce(), Title: "working", Type: model.TypeTask, Branch: "elsewhere"},
		model.SetStatus{Status: model.StatusInProgress},
		model.SetAssignee{Assignee: "worker"},
	)
	statusCreate(t, c, model.CreateNote{Nonce: model.NewNonce(), Title: "unverified", Body: "note payload"})
	statusCreate(t, c,
		model.CreateNote{Nonce: model.NewNonce(), Title: "dangling"},
		model.AddSupersededBy{ID: model.EntityID(strings.Repeat("a", 40))},
	)
	statusCreate(t, c, model.CreateDoc{Nonce: model.NewNonce(), Title: "document", Body: "doc payload"})
	statusCreate(t, c, model.CreateAnswer{Nonce: model.NewNonce(), Title: "answer", Body: "answer payload"})
	statusCreate(t, c, model.CreateInvestigation{Nonce: model.NewNonce(), Title: "investigation", Premise: "a cause"})
	statusCreate(t, c, model.CreatePlan{Nonce: model.NewNonce(), Title: "plan", Body: "an approach", Status: model.PlanApproved})
	statusCreate(t, c, model.CreateSprint{Nonce: model.NewNonce(), Title: "sprint"})
	statusCreate(t, c, model.CreateProject{Nonce: model.NewNonce(), Title: "project"})
	log, _, err := c.CreateLog(ctx, LogSpec{Title: "papercuts", Tags: []string{PapercutTag}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.AppendLog(ctx, log.ID, LogAppend{Text: "one incident"}); err != nil {
		t.Fatal(err)
	}
	rb, _, err := c.CreateRunbook(ctx, RunbookSpec{Title: "running", Steps: []string{"step"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.StartRun(ctx, rb.ID, ""); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CC_NOTES_LEASE_TTL", "24h")
	report := statusParity(t, c)
	if report.Notes != (SummaryCount{Total: 1, NeedsReview: 2}) || report.Papercuts != 1 || len(report.Runs) != 1 {
		t.Fatalf("report = %#v", report)
	}
}

func TestStatusMetadataDoesNotReadUnexposedBodies(t *testing.T) {
	for _, count := range []int{8, 80} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			c, dir := newWBClient(t)
			gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
			visible := statusCreate(t, c, model.CreateTask{Nonce: model.NewNonce(), Title: "visible", Type: model.TypeTask})
			var hidden []model.Snapshot
			for i := range count {
				hidden = append(hidden, statusCreate(t, c, model.CreateDoc{Nonce: model.NewNonce(), Title: fmt.Sprintf("doc %d", i), Body: strings.Repeat("unused", 2048)}))
			}
			hidden = append(hidden, statusCreate(t, c,
				model.CreateTask{Nonce: model.NewNonce(), Title: "done", Type: model.TypeTask},
				model.SetStatus{Status: model.StatusDone},
			))
			want := statusParity(t, c)
			for _, snapshot := range hidden {
				removeStatusCommit(t, c, snapshot.Meta().Head)
			}
			clearStatusFoldCache(t, c)
			fresh, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			initialFresh := fresh
			t.Cleanup(func() { _ = initialFresh.Close() })
			got, err := fresh.Status(t.Context())
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("unreadable unused bodies = %#v, %v; want %#v", got, err, want)
			}
			if _, err := fresh.s.ListDocs(t.Context(), false, true); err == nil {
				t.Fatal("removed document commits remain readable")
			}
			statusCreate(t, fresh, model.CreateDoc{Nonce: model.NewNonce(), Title: "one changed tip"})
			got, err = fresh.Status(t.Context())
			if err != nil || got.Docs.Total != count+1 || len(got.Backlog) != 1 || got.Backlog[0].Task.ID != visible.EntityID() {
				t.Fatalf("new tip with unreadable unchanged bodies = %#v, %v", got, err)
			}
			removeStatusCommit(t, fresh, visible.Meta().Head)
			clearStatusFoldCache(t, fresh)
			fresh, err = Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			reopenedFresh := fresh
			t.Cleanup(func() { _ = reopenedFresh.Close() })
			if report, err := fresh.Status(t.Context()); err == nil || !reflect.DeepEqual(report, StatusReport{}) {
				t.Fatalf("unreadable exposed body = %#v, %v", report, err)
			}
		})
	}
}

func removeStatusCommit(t *testing.T, c *Client, tip model.SHA) {
	t.Helper()
	if err := os.Remove(filepath.Join(c.s.CommonDir(), "objects", string(tip)[:2], string(tip)[2:])); err != nil {
		t.Fatal(err)
	}
}

func clearStatusFoldCache(t *testing.T, c *Client) {
	t.Helper()
	clearClientCache(t, c, "folds-v5")
}

func TestStatusMetadataOldAndEmptyPinsAndCacheMiss(t *testing.T) {
	c, dir := newWBClient(t)
	statusCreate(t, c, model.CreateDoc{Nonce: model.NewNonce(), Title: "original"})
	view, hit, err := c.s.StatusMetadata(t.Context(), statusKinds...)
	if err != nil || !hit {
		t.Fatalf("initial = %v %v", hit, err)
	}
	old := FromStore(view.Captured)
	want, err := old.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	statusCreate(t, c, model.CreateDoc{Nonce: model.NewNonce(), Title: "new"})
	if report, err := c.Status(t.Context()); err != nil || report.Docs.Total != 2 {
		t.Fatalf("live = %#v, %v", report, err)
	}
	if report, err := old.Status(t.Context()); err != nil || !reflect.DeepEqual(report, want) {
		t.Fatalf("old pins = %#v, %v; want %#v", report, err, want)
	}
	empty := FromStore(c.s.Pinned(map[string]model.SHA{}))
	if report, err := empty.Status(t.Context()); err != nil || report.Docs.Total != 0 || len(report.Backlog) != 0 {
		t.Fatalf("empty pins = %#v, %v", report, err)
	}
	fresh, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	if err := os.RemoveAll(filepath.Join(fresh.s.CommonDir(), "cc-notes-query")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fresh.s.CommonDir(), "cc-notes-query"), []byte("unavailable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if report, err := fresh.Status(t.Context()); err != nil || report.Docs.Total != 2 {
		t.Fatalf("derived miss = %#v, %v", report, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if report, err := fresh.Status(ctx); !errors.Is(err, context.Canceled) || !reflect.DeepEqual(report, StatusReport{}) {
		t.Fatalf("cancelled = %#v, %v", report, err)
	}
}
