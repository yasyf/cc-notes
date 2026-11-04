package notes

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestDocAnchorsPreserveCompleteBodiesAttachmentsLifecycleAndOrder(t *testing.T) {
	c, dir := newWBClient(t)
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "private anchor fixture")
	head := gittest.Git(t, dir, "rev-parse", "HEAD")
	path := filepath.Join(t.TempDir(), "proof.txt")
	if err := os.WriteFile(path, []byte("preserved attachment"), 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, _, err := c.AttachFile(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	all := AnchorSpec{Commits: []string{head}, Paths: []string{"src/one.go"}, Dirs: []string{"src"}, Branches: []string{"main"}}
	ids := make(map[string]model.EntityID)
	for _, spec := range []DocSpec{
		{Title: "both", Body: "complete selected body α", When: "read the complete document", Tags: []string{"a", "b"}, Anchors: all, Attachments: []model.Attachment{attachment}},
		{Title: "path", Body: "path body", Tags: []string{"a"}, Anchors: AnchorSpec{Paths: []string{"src/one.go"}}},
		{Title: "dir", Body: "dir body", Tags: []string{"a"}, Anchors: AnchorSpec{Dirs: []string{"src"}}},
		{Title: "branch", Body: "branch body", Tags: []string{"a"}, Anchors: AnchorSpec{Branches: []string{"main"}}},
		{Title: "commit", Body: "commit body", Tags: []string{"a"}, Anchors: AnchorSpec{Commits: []string{head}}},
		{Title: "deleted", Body: "deleted body", Tags: []string{"a", "b"}, Anchors: all},
		{Title: "superseded", Body: "superseded body", Tags: []string{"a", "b"}, Anchors: all},
		{Title: "expired", Body: "expired body", Tags: []string{"a", "b"}, Anchors: all},
		{Title: "none", Body: "unanchored body", Tags: []string{"a", "b"}},
	} {
		doc, reused, err := c.CreateDoc(t.Context(), spec)
		if err != nil || reused {
			t.Fatalf("create %s: reused=%v error=%v", spec.Title, reused, err)
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
	ids["early"] = anchoredDocAt(t, c, "early", 99).ID
	ids["late"] = anchoredDocAt(t, c, "late", 101).ID
	foreign := createLabeledListEntity(t, c, model.KindLog, "foreign log", []string{"a", "b"})
	appendLabeledListOps(t, c, model.KindLog, foreign, model.AddAnchor{Anchor: model.Anchor{Kind: model.AnchorBranch, Value: "main"}})
	for _, test := range []struct {
		name   string
		filter DocumentFilter
		titles []string
	}{
		{"unfiltered", DocumentFilter{}, []string{"both", "path", "dir", "branch", "commit", "expired", "none", "early", "late"}},
		{"labels only", DocumentFilter{Labels: []string{"a", "b"}}, []string{"both", "expired", "none", "early", "late"}},
		{"branch", DocumentFilter{Anchor: AnchorFilter{Branch: "main"}}, []string{"both", "branch", "expired", "early", "late"}},
		{"path", DocumentFilter{Anchor: AnchorFilter{Path: "src/one.go"}}, []string{"both", "path", "expired"}},
		{"dir", DocumentFilter{Anchor: AnchorFilter{Dir: "src"}}, []string{"both", "dir", "expired"}},
		{"commit", DocumentFilter{Anchor: AnchorFilter{Commit: head}}, []string{"both", "commit", "expired"}},
		{"all anchors", DocumentFilter{Anchor: AnchorFilter{Commit: head, Path: "src/one.go", Dir: "src", Branch: "main"}}, []string{"both", "expired"}},
		{"path and branch", DocumentFilter{Anchor: AnchorFilter{Path: "src/one.go", Branch: "main"}}, []string{"both", "expired"}},
		{"label and branch", DocumentFilter{Labels: []string{"b"}, Anchor: AnchorFilter{Branch: "main"}}, []string{"both", "expired", "early", "late"}},
		{"repeated label", DocumentFilter{Labels: []string{"a", "a"}, Anchor: AnchorFilter{Branch: "main"}}, []string{"both", "branch", "expired", "early", "late"}},
		{"missing label", DocumentFilter{Labels: []string{"missing"}, Anchor: AnchorFilter{Branch: "main"}}, nil},
		{"empty label", DocumentFilter{Labels: []string{""}, Anchor: AnchorFilter{Branch: "main"}}, nil},
		{"missing anchor", DocumentFilter{Anchor: AnchorFilter{Branch: "other"}}, nil},
		{"anchor kind", DocumentFilter{Anchor: AnchorFilter{Path: "src"}}, nil},
		{"anchor nul", DocumentFilter{Anchor: AnchorFilter{Branch: "main\x00"}}, nil},
		{"deleted", DocumentFilter{Anchor: AnchorFilter{Branch: "main"}, IncludeTombstoned: true}, []string{"both", "branch", "deleted", "expired", "early", "late"}},
		{"superseded", DocumentFilter{Anchor: AnchorFilter{Branch: "main"}, IncludeSuperseded: true}, []string{"both", "branch", "superseded", "expired", "early", "late"}},
		{"all dispositions", DocumentFilter{Anchor: AnchorFilter{Branch: "main"}, IncludeTombstoned: true, IncludeSuperseded: true}, []string{"both", "branch", "deleted", "superseded", "expired", "early", "late"}},
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
				t.Fatalf("Docs(%+v) differs from complete authoritative selection/order: %v", test.filter, err)
			}
		})
	}
}

func TestDocAnchorsRetainPinnedTipsAcrossChanges(t *testing.T) {
	c, _, selected, other := anchoredDocsFixture(t)
	tip, err := c.s.Repo.Tip(t.Context(), refs.For(model.KindDoc, selected.ID))
	if err != nil {
		t.Fatal(err)
	}
	pinned := FromStore(c.s.Pinned(map[string]model.SHA{refs.For(model.KindDoc, selected.ID): tip}))
	filter := DocumentFilter{Anchor: AnchorFilter{Branch: "main"}}
	if _, err := c.Docs(t.Context(), filter); err != nil {
		t.Fatal(err)
	}
	anchor := model.Anchor{Kind: model.AnchorBranch, Value: "main"}
	appendLabeledListOps(t, c, model.KindDoc, selected.ID, model.RemoveAnchor{Anchor: anchor})
	appendLabeledListOps(t, c, model.KindDoc, other.ID, model.AddAnchor{Anchor: anchor})
	current, err := c.Doc(t.Context(), other.ID)
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Docs(t.Context(), filter)
	if err != nil || !reflect.DeepEqual(got, []model.Doc{current}) {
		t.Fatalf("current anchors = %+v, %v", got, err)
	}
	got, err = pinned.Docs(t.Context(), filter)
	if err != nil || !reflect.DeepEqual(got, []model.Doc{selected}) {
		t.Fatalf("old pinned anchors = %+v, %v", got, err)
	}
	empty := FromStore(c.s.Pinned(map[string]model.SHA{}))
	if got, err := empty.Docs(t.Context(), filter); err != nil || len(got) != 0 {
		t.Fatalf("empty pinned anchors = %+v, %v", got, err)
	}
}

func TestDocAnchorsKeepUnavailableIndexAndCancellationBehavior(t *testing.T) {
	c, _, selected, _ := anchoredDocsFixture(t)
	indexPath := filepath.Join(c.s.CommonDir(), "cc-notes-query")
	if err := os.RemoveAll(indexPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(indexPath, []byte("unavailable"), 0o600); err != nil {
		t.Fatal(err)
	}
	filter := DocumentFilter{Anchor: AnchorFilter{Branch: "main"}}
	got, err := c.Docs(t.Context(), filter)
	if err != nil || !reflect.DeepEqual(got, []model.Doc{selected}) {
		t.Fatalf("unavailable index = %+v, %v", got, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got, err := c.Docs(ctx, filter); got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled = %+v, %v", got, err)
	}
}

func TestDocAnchorsReadOnlySelectedBodies(t *testing.T) {
	c, dir, selected, _ := anchoredDocsFixture(t)
	for i := range 23 {
		_, reused, err := c.CreateDoc(t.Context(), DocSpec{Title: fmt.Sprintf("unrelated %d", i), Body: "unrelated body", Anchors: AnchorSpec{Branches: []string{"other"}}})
		if err != nil || reused {
			t.Fatalf("unrelated %d: reused=%v error=%v", i, reused, err)
		}
	}
	time.Sleep(2100 * time.Millisecond)
	if _, hit, err := c.s.StatusMetadata(t.Context(), model.KindDoc); err != nil || !hit {
		t.Fatalf("prime metadata = %v, %v", hit, err)
	}
	tips, err := c.s.Repo.ListPrefix(t.Context(), refs.Root(model.KindDoc))
	if err != nil {
		t.Fatal(err)
	}
	if len(tips) != 25 {
		t.Fatalf("private doc universe = %d; want 25", len(tips))
	}
	objects := make(map[model.SHA][]byte, len(tips))
	for _, tip := range tips {
		path := labeledListObjectPath(c, tip)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		objects[tip] = data
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	clearClientCache(t, c, "folds-v6")
	fresh, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	var reads atomic.Int64
	fresh.s.Repo.BackfillWith(func(ctx context.Context, tip model.SHA) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, ok := objects[tip]
		if !ok {
			return fmt.Errorf("unexpected entity body %s", tip)
		}
		reads.Add(1)
		return os.WriteFile(labeledListObjectPath(fresh, tip), data, 0o600)
	})
	got, err := fresh.Docs(t.Context(), DocumentFilter{Anchor: AnchorFilter{Branch: "main"}})
	if err != nil || !reflect.DeepEqual(got, []model.Doc{selected}) {
		t.Fatalf("selected anchor = %+v, %v", got, err)
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("read %d authoritative bodies for one selected doc among %d; want 1", got, len(objects))
	}
}

func TestDocAnchorsReportSelectedObjectFailures(t *testing.T) {
	for _, broken := range []string{"missing", "corrupt"} {
		t.Run(broken, func(t *testing.T) {
			c, dir, selected, _ := anchoredDocsFixture(t)
			time.Sleep(2100 * time.Millisecond)
			if _, hit, err := c.s.StatusMetadata(t.Context(), model.KindDoc); err != nil || !hit {
				t.Fatalf("prime metadata = %v, %v", hit, err)
			}
			tip, err := c.s.Repo.Tip(t.Context(), refs.For(model.KindDoc, selected.ID))
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
			got, err := fresh.Docs(t.Context(), DocumentFilter{Anchor: AnchorFilter{Branch: "main"}})
			if got != nil {
				t.Fatalf("%s selected object = %+v; want nil", broken, got)
			}
			if broken == "missing" {
				if !errors.Is(err, gitobj.ErrIncompleteChain) {
					t.Fatalf("missing error = %v; want %v", err, gitobj.ErrIncompleteChain)
				}
			} else {
				var corrupt *packfile.Error
				if !errors.As(err, &corrupt) || corrupt.Error() != "zlib reading error: zlib: invalid header" {
					t.Fatalf("corrupt error = %v; want invalid zlib header", err)
				}
			}
		})
	}
}

func anchoredDocsFixture(t *testing.T) (*Client, string, model.Doc, model.Doc) {
	t.Helper()
	c, dir := newWBClient(t)
	selected, reused, err := c.CreateDoc(t.Context(), DocSpec{Title: "selected", Body: "complete selected body", Tags: []string{"a", "b"}, Anchors: AnchorSpec{Branches: []string{"main"}}})
	if err != nil || reused {
		t.Fatalf("selected fixture: reused=%v error=%v", reused, err)
	}
	other, reused, err := c.CreateDoc(t.Context(), DocSpec{Title: "other", Body: "other body", Tags: []string{"a", "b"}, Anchors: AnchorSpec{Branches: []string{"other"}}})
	if err != nil || reused {
		t.Fatalf("other fixture: reused=%v error=%v", reused, err)
	}
	selected, err = c.Doc(t.Context(), selected.ID)
	if err != nil {
		t.Fatal(err)
	}
	other, err = c.Doc(t.Context(), other.ID)
	if err != nil {
		t.Fatal(err)
	}
	return c, dir, selected, other
}

func anchoredDocAt(t *testing.T, c *Client, title string, at int64) model.Doc {
	t.Helper()
	ops := []model.Op{model.CreateDoc{Nonce: model.NewNonce(), Title: title, Body: "ordering fixture", When: "private trigger", Tags: []string{"a", "b"}, Anchors: []model.Anchor{{Kind: model.AnchorBranch, Value: "main"}}}}
	tip, err := c.s.Repo.WriteOpsCommit(t.Context(), nil, gitobj.Signature{Name: "Contract User", Email: "contract@example.com", When: time.Unix(at, 0).UTC()}, "doc anchor ordering fixture", model.Pack{Lamport: 1, Ops: ops})
	if err != nil {
		t.Fatal(err)
	}
	id := model.EntityID(tip)
	if err := c.s.PublishRef(t.Context(), refs.For(model.KindDoc, id), tip, ""); err != nil {
		t.Fatal(err)
	}
	doc, err := c.Doc(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}
