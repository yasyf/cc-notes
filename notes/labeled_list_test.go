package notes

import (
	"compress/zlib"
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

func TestTaskLabelsMatchAuthoritativeResidualFilters(t *testing.T) {
	c, _, ids := labeledListFixture(t, model.KindTask)
	appendLabeledListOps(t, c, model.KindTask, ids["both"], model.SetBranch{Branch: "feature"}, model.SetAssignee{Assignee: "alice"}, model.SetType{Type: model.TypeBug}, model.SetPriority{Priority: 0})
	appendLabeledListOps(t, c, model.KindTask, ids["closed"], model.SetStatus{Status: model.StatusDone})
	for _, f := range []TaskFilter{
		{},
		{Labels: []string{"a"}},
		{Labels: []string{"a", "b"}},
		{Labels: []string{"a", "a"}},
		{Labels: []string{"missing"}},
		{Labels: []string{""}},
		{Labels: []string{"a"}, Scope: ScopeNamed, Branch: "feature"},
		{Labels: []string{"a"}, Scope: ScopeBacklog},
		{Labels: []string{"a"}, Scope: ScopeCurrentBranch},
		{Labels: []string{"a"}, Statuses: []model.Status{model.StatusDone}},
		{Labels: []string{"a"}, AssigneeSet: true, Assignee: "alice"},
		{Labels: []string{"a"}, AssigneeSet: true},
		{Labels: []string{"a"}, Type: model.TypeBug},
		{Labels: []string{"a"}, ArchiveCutoff: time.Now().Add(time.Hour)},
	} {
		want, err := c.s.ListTasks(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want, err = c.filteredTasks(t.Context(), want, f)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.Tasks(t.Context(), f)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Tasks(%+v) = %+v, %v; want %+v", f, got, err, want)
		}
	}
}

func TestLogLabelsMatchAuthoritativeLifecycleAndAnchors(t *testing.T) {
	c, _, ids := labeledListFixture(t, model.KindLog)
	appendLabeledListOps(t, c, model.KindLog, ids["both"], model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorBranch, Value: "main"}})
	appendLabeledListOps(t, c, model.KindLog, ids["closed"], model.DeleteNote{})
	for _, f := range []LogFilter{
		{},
		{Labels: []string{"a"}},
		{Labels: []string{"a", "b"}},
		{Labels: []string{"a", "a"}},
		{Labels: []string{"missing"}},
		{Labels: []string{""}},
		{Labels: []string{"a"}, Anchors: AnchorFilter{Branch: "main"}},
		{Labels: []string{"a"}, IncludeDeleted: true},
	} {
		want, err := c.s.ListLogs(t.Context(), f.IncludeDeleted)
		if err != nil {
			t.Fatal(err)
		}
		want = filteredLogs(want, f)
		got, err := c.Logs(t.Context(), f)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Logs(%+v) = %+v, %v; want %+v", f, got, err, want)
		}
	}
}

func TestInvestigationLabelsMatchAuthoritativeLifecycleAndAnchors(t *testing.T) {
	c, _, ids := labeledListFixture(t, model.KindInvestigation)
	appendLabeledListOps(t, c, model.KindInvestigation, ids["both"], model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorBranch, Value: "main"}})
	appendLabeledListOps(t, c, model.KindInvestigation, ids["closed"], model.DeleteNote{})
	appendLabeledListOps(t, c, model.KindInvestigation, ids["superseded"], model.AddSupersededBy{ID: ids["both"]})
	for _, f := range []InvestigationFilter{
		{},
		{Labels: []string{"a"}},
		{Labels: []string{"a", "b"}},
		{Labels: []string{"a", "a"}},
		{Labels: []string{"missing"}},
		{Labels: []string{""}},
		{Labels: []string{"a"}, Anchors: AnchorFilter{Branch: "main"}},
		{Labels: []string{"a"}, Statuses: []model.InvestigationStatus{model.InvestigationOpen}},
	} {
		want, err := c.s.ListInvestigations(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		want = filteredInvestigations(want, f)
		got, err := c.Investigations(t.Context(), f)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("Investigations(%+v) = %+v, %v; want %+v", f, got, err, want)
		}
	}
}

func TestLabeledListsRetainPinnedTipsAcrossChanges(t *testing.T) {
	for _, kind := range []model.Kind{model.KindTask, model.KindLog, model.KindInvestigation} {
		t.Run(string(kind), func(t *testing.T) {
			c, _, ids := labeledListFixture(t, kind)
			tip, err := c.s.Repo.Tip(t.Context(), refs.For(kind, ids["both"]))
			if err != nil {
				t.Fatal(err)
			}
			pinned := FromStore(c.s.Pinned(map[string]model.SHA{refs.For(kind, ids["both"]): tip}))
			if _, err := labeledListIDs(t.Context(), c, kind, []string{"a", "b"}); err != nil {
				t.Fatal(err)
			}
			remove := model.Op(model.RemoveTag{Tag: "b"})
			add := model.Op(model.AddTag{Tag: "b"})
			if kind == model.KindTask {
				remove, add = model.RemoveLabel{Label: "b"}, model.AddLabel{Label: "b"}
			}
			appendLabeledListOps(t, c, kind, ids["both"], remove)
			appendLabeledListOps(t, c, kind, ids["first"], add)
			got, err := labeledListIDs(t.Context(), c, kind, []string{"a", "b"})
			if err != nil || !slices.Contains(got, ids["first"]) || slices.Contains(got, ids["both"]) {
				t.Fatalf("updated labels = %v, %v", got, err)
			}
			got, err = labeledListIDs(t.Context(), pinned, kind, []string{"a", "b"})
			if err != nil || !slices.Equal(got, []model.EntityID{ids["both"]}) {
				t.Fatalf("old pinned labels = %v, %v", got, err)
			}
			empty := FromStore(c.s.Pinned(map[string]model.SHA{}))
			got, err = labeledListIDs(t.Context(), empty, kind, []string{"a"})
			if err != nil || len(got) != 0 {
				t.Fatalf("empty pinned = %v, %v", got, err)
			}
		})
	}
}

func TestLabeledListsKeepUnavailableIndexAndCancellationBehavior(t *testing.T) {
	for _, kind := range []model.Kind{model.KindTask, model.KindLog, model.KindInvestigation} {
		t.Run(string(kind), func(t *testing.T) {
			c, _, ids := labeledListFixture(t, kind)
			if err := os.WriteFile(filepath.Join(c.s.CommonDir(), "cc-notes-query"), []byte("unavailable"), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := labeledListIDs(t.Context(), c, kind, []string{"a", "b"})
			if err != nil || !slices.Contains(got, ids["both"]) {
				t.Fatalf("unavailable index = %v, %v", got, err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if got, err := labeledListIDs(ctx, c, kind, []string{"a"}); got != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled = %v, %v", got, err)
			}
		})
	}
}

func TestLabeledListsReportSelectedObjectFailures(t *testing.T) {
	for _, kind := range []model.Kind{model.KindTask, model.KindLog, model.KindInvestigation} {
		for _, broken := range []string{"missing", "corrupt"} {
			t.Run(string(kind)+"/"+broken, func(t *testing.T) {
				c, dir := newWBClient(t)
				id := createLabeledListEntity(t, c, kind, "selected", []string{"target"})
				time.Sleep(2100 * time.Millisecond)
				if _, hit, err := c.s.StatusMetadata(t.Context(), kind); err != nil || !hit {
					t.Fatalf("prime metadata = %v, %v", hit, err)
				}
				tip, err := c.s.Repo.Tip(t.Context(), refs.For(kind, id))
				if err != nil {
					t.Fatal(err)
				}
				want := error(gitobj.ErrIncompleteChain)
				if broken == "missing" {
					err = os.Remove(labeledListObjectPath(c, tip))
				} else {
					err = os.WriteFile(labeledListObjectPath(c, tip), []byte("corrupt selected object"), 0o600)
					want = zlib.ErrHeader
				}
				if err != nil {
					t.Fatal(err)
				}
				if err := os.RemoveAll(filepath.Join(c.s.CommonDir(), "cc-notes", "folds-v2")); err != nil {
					t.Fatal(err)
				}
				fresh, err := Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				got, err := labeledListIDs(t.Context(), fresh, kind, []string{"target"})
				if got != nil || !errors.Is(err, want) {
					t.Fatalf("%s selected object = %v, %v; want %v", broken, got, err, want)
				}
			})
		}
	}
}

func labeledListFixture(t *testing.T, kind model.Kind) (*Client, string, map[string]model.EntityID) {
	t.Helper()
	c, dir := newWBClient(t)
	ids := make(map[string]model.EntityID)
	for _, item := range []struct {
		title  string
		labels []string
	}{
		{"both", []string{"a", "b"}},
		{"first", []string{"a"}},
		{"second", []string{"b"}},
		{"closed", []string{"a", "b"}},
		{"superseded", []string{"a", "b"}},
	} {
		ids[item.title] = createLabeledListEntity(t, c, kind, item.title, item.labels)
	}
	return c, dir, ids
}

func appendLabeledListOps(t *testing.T, c *Client, kind model.Kind, id model.EntityID, ops ...model.Op) {
	t.Helper()
	if _, err := c.s.Append(t.Context(), refs.For(kind, id), ops); err != nil {
		t.Fatal(err)
	}
}
