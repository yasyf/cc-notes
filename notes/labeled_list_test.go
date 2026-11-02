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

	"github.com/go-git/go-git/v5/plumbing/format/packfile"
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

func TestLedgerLabelsMatchAuthoritativeLifecycleAnchorsAndOrder(t *testing.T) {
	c, _, ids := labeledListFixture(t, model.KindLedger)
	appendLabeledListOps(t, c, model.KindLedger, ids["both"], model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorBranch, Value: "main"}})
	if _, err := c.ArchiveLedger(t.Context(), ids["closed"]); err != nil {
		t.Fatal(err)
	}
	if _, err := c.RemoveLedger(t.Context(), ids["superseded"]); err != nil {
		t.Fatal(err)
	}
	ids["late"] = labeledLedgerAt(t, c, "late", 101).ID
	ids["early"] = labeledLedgerAt(t, c, "early", 99).ID
	authoritative, err := c.s.ListLedgers(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		filter LedgerFilter
		titles []string
	}{
		{"all active", LedgerFilter{}, []string{"early", "late", "both", "first", "second"}},
		{"one label", LedgerFilter{Labels: []string{"a"}}, []string{"early", "late", "both", "first"}},
		{"all labels", LedgerFilter{Labels: []string{"a", "b"}}, []string{"early", "late", "both"}},
		{"repeated label", LedgerFilter{Labels: []string{"a", "a"}}, []string{"early", "late", "both", "first"}},
		{"missing label", LedgerFilter{Labels: []string{"missing"}}, nil},
		{"empty label", LedgerFilter{Labels: []string{""}}, nil},
		{"anchor", LedgerFilter{Labels: []string{"a"}, Anchors: AnchorFilter{Branch: "main"}}, []string{"both"}},
		{"missing anchor", LedgerFilter{Labels: []string{"a"}, Anchors: AnchorFilter{Branch: "other"}}, nil},
		{"archived", LedgerFilter{Labels: []string{"a"}, IncludeArchived: true}, []string{"early", "late", "both", "first", "closed"}},
		{"unfiltered archived", LedgerFilter{IncludeArchived: true}, []string{"early", "late", "both", "first", "second", "closed"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			selected := make(map[model.EntityID]bool, len(test.titles))
			for _, title := range test.titles {
				selected[ids[title]] = true
			}
			want := make([]model.Ledger, 0, len(selected))
			for _, ledger := range authoritative {
				if selected[ledger.ID] {
					want = append(want, ledger)
				}
			}
			got, err := c.Ledgers(t.Context(), test.filter)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("Ledgers(%+v) differs from complete authoritative selection: %v", test.filter, err)
			}
		})
	}
}

func TestDocLabelsPreserveCompleteBodiesAttachmentsLifecycleAndOrder(t *testing.T) {
	c, _ := newWBClient(t)
	path := filepath.Join(t.TempDir(), "proof.txt")
	if err := os.WriteFile(path, []byte("preserved attachment"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, _, err := c.AttachFile(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	ids := make(map[string]model.EntityID)
	for _, spec := range []DocSpec{
		{Title: "both", Body: "complete selected body", When: "read the complete document", Tags: []string{"a", "b"}, Anchors: AnchorSpec{Branches: []string{"main"}}, Attachments: []model.Attachment{attachment}},
		{Title: "first", Body: "first body", Tags: []string{"a"}},
		{Title: "second", Body: "second body", Tags: []string{"b"}},
		{Title: "deleted", Body: "deleted body", Tags: []string{"a", "b"}},
		{Title: "superseded", Body: "superseded body", Tags: []string{"a", "b"}},
		{Title: "expired", Body: "expired body", Tags: []string{"a", "b"}},
	} {
		doc, _, err := c.CreateDoc(t.Context(), spec)
		if err != nil {
			t.Fatal(err)
		}
		ids[spec.Title] = doc.ID
	}
	if _, err := c.RemoveDoc(t.Context(), ids["deleted"]); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SupersedeDoc(t.Context(), ids["superseded"], ids["both"]); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ExpireDoc(t.Context(), ids["expired"], "still listed"); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		filter DocumentFilter
		titles []string
	}{
		{"all live", DocumentFilter{}, []string{"both", "first", "second", "expired"}},
		{"one label", DocumentFilter{Labels: []string{"a"}}, []string{"both", "first", "expired"}},
		{"all labels", DocumentFilter{Labels: []string{"a", "b"}}, []string{"both", "expired"}},
		{"repeated label", DocumentFilter{Labels: []string{"a", "a"}}, []string{"both", "first", "expired"}},
		{"missing label", DocumentFilter{Labels: []string{"missing"}}, nil},
		{"empty label", DocumentFilter{Labels: []string{""}}, nil},
		{"anchor", DocumentFilter{Labels: []string{"a"}, Anchor: AnchorFilter{Branch: "main"}}, []string{"both"}},
		{"missing anchor", DocumentFilter{Labels: []string{"a"}, Anchor: AnchorFilter{Branch: "other"}}, nil},
		{"deleted", DocumentFilter{Labels: []string{"a"}, IncludeTombstoned: true}, []string{"both", "first", "deleted", "expired"}},
		{"superseded", DocumentFilter{Labels: []string{"a"}, IncludeSuperseded: true}, []string{"both", "first", "superseded", "expired"}},
		{"all dispositions", DocumentFilter{Labels: []string{"a"}, IncludeTombstoned: true, IncludeSuperseded: true}, []string{"both", "first", "deleted", "superseded", "expired"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			authoritative, err := c.s.ListDocs(t.Context(), test.filter.IncludeTombstoned, test.filter.IncludeSuperseded)
			if err != nil {
				t.Fatal(err)
			}
			selected := make(map[model.EntityID]bool, len(test.titles))
			for _, title := range test.titles {
				selected[ids[title]] = true
			}
			want := make([]model.Doc, 0, len(selected))
			for _, doc := range authoritative {
				if selected[doc.ID] {
					want = append(want, doc)
				}
			}
			sortDocuments(want)
			got, err := c.Docs(t.Context(), test.filter)
			if err != nil || !reflect.DeepEqual(got, want) {
				t.Fatalf("Docs(%+v) differs from complete authoritative bodies/attachments and order: %v", test.filter, err)
			}
		})
	}
}

func TestLabeledListsRetainPinnedTipsAcrossChanges(t *testing.T) {
	for _, kind := range []model.Kind{model.KindTask, model.KindLog, model.KindInvestigation, model.KindLedger, model.KindDoc} {
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
			if kind == model.KindTask || kind == model.KindLedger {
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
	for _, kind := range []model.Kind{model.KindTask, model.KindLog, model.KindInvestigation, model.KindLedger, model.KindDoc} {
		t.Run(string(kind), func(t *testing.T) {
			c, _, ids := labeledListFixture(t, kind)
			indexPath := filepath.Join(c.s.CommonDir(), "cc-notes-query")
			if err := os.RemoveAll(indexPath); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(indexPath, []byte("unavailable"), 0o600); err != nil {
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
	for _, kind := range []model.Kind{model.KindTask, model.KindLog, model.KindInvestigation, model.KindLedger, model.KindDoc} {
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
				if broken == "missing" {
					err = os.Remove(labeledListObjectPath(c, tip))
				} else {
					err = os.WriteFile(labeledListObjectPath(c, tip), []byte("corrupt selected object"), 0o600)
				}
				if err != nil {
					t.Fatal(err)
				}
				clearClientCache(t, c, "folds-v6")
				fresh, err := Open(dir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = fresh.Close() })
				got, err := labeledListIDs(t.Context(), fresh, kind, []string{"target"})
				if got != nil {
					t.Fatalf("%s selected object = %v; want nil", broken, got)
				}
				if broken == "missing" {
					if !errors.Is(err, gitobj.ErrIncompleteChain) {
						t.Fatalf("missing selected object error = %v; want %v", err, gitobj.ErrIncompleteChain)
					}
				} else {
					var corrupt *packfile.Error
					if !errors.As(err, &corrupt) || corrupt.Error() != "zlib reading error: zlib: invalid header" {
						t.Fatalf("corrupt selected object error = %v; want *packfile.Error: zlib reading error: zlib: invalid header", err)
					}
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

func labeledLedgerAt(t *testing.T, c *Client, title string, at int64) model.Ledger {
	t.Helper()
	ops := []model.Op{model.CreateLedger{Nonce: model.NewNonce(), Title: title, Labels: []string{"a", "b"}}}
	tip, err := c.s.Repo.WriteOpsCommit(t.Context(), nil, gitobj.Signature{Name: "Contract User", Email: "contract@example.com", When: time.Unix(at, 0).UTC()}, "ledger label ordering fixture", model.Pack{Lamport: 1, Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	id := model.EntityID(tip)
	if err := c.s.PublishRef(t.Context(), refs.For(model.KindLedger, id), tip, ""); err != nil {
		t.Fatal(err)
	}
	ledger, err := c.Ledger(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ledger
}
