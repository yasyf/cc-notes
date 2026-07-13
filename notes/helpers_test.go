// White-box tests exercise the unexported scaffolding helpers against a real
// git repository in t.TempDir().
package notes

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
)

func newWBClient(t *testing.T) (*Client, string) {
	t.Helper()
	dir := gittest.InitRepo(t)
	t.Setenv("CC_NOTES_ACTOR", "Test User <test@example.com>")
	c, err := Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	return c, dir
}

func TestResolveCommits(t *testing.T) {
	c, dir := newWBClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")
	full := gittest.Git(t, dir, "rev-parse", "HEAD")

	if got, err := c.resolveCommits(ctx, nil); err != nil || got != nil {
		t.Fatalf("resolveCommits(nil) = %v/%v, want nil/nil", got, err)
	}

	got, err := c.resolveCommits(ctx, []string{"HEAD"})
	if err != nil {
		t.Fatalf("resolveCommits(HEAD): %v", err)
	}
	if len(got) != 1 || got[0] != full {
		t.Fatalf("resolveCommits(HEAD) = %v, want [%s]", got, full)
	}
	if len(got[0]) != 40 {
		t.Errorf("resolved sha %q has len %d, want 40", got[0], len(got[0]))
	}

	if _, err := c.resolveCommits(ctx, []string{"deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("resolveCommits(unknown rev) = %v, want ErrNotFound", err)
	}
}

func TestBuildAnchors(t *testing.T) {
	got := buildAnchors(AnchorSpec{
		Commits:  []string{"c1"},
		Paths:    []string{"p1", "p2"},
		Dirs:     []string{"d1"},
		Branches: []string{"main"},
	})
	want := []model.Anchor{
		{Kind: model.AnchorCommit, Value: "c1"},
		{Kind: model.AnchorPath, Value: "p1"},
		{Kind: model.AnchorPath, Value: "p2"},
		{Kind: model.AnchorDir, Value: "d1"},
		{Kind: model.AnchorBranch, Value: "main"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("buildAnchors = %v, want %v", got, want)
	}
}

func TestBuildWitness(t *testing.T) {
	c, dir := newWBClient(t)
	ctx := t.Context()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	gittest.Git(t, dir, "add", "f.txt")
	gittest.Git(t, dir, "commit", "-q", "-m", "root")
	head, err := c.head(ctx)
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head == "" {
		t.Fatal("head is empty after commit")
	}
	blobOID := gittest.Git(t, dir, "rev-parse", "HEAD:f.txt")

	anchors := []model.Anchor{
		{Kind: model.AnchorPath, Value: "f.txt"},
		{Kind: model.AnchorCommit, Value: string(head)},
		{Kind: model.AnchorBranch, Value: "main"},
		{Kind: model.AnchorPath, Value: "missing.txt"},
	}
	w, err := c.buildWitness(ctx, head, anchors)
	if err != nil {
		t.Fatalf("buildWitness: %v", err)
	}
	// Present path + commit anchor are witnessed; branch and absent path skipped.
	if len(w) != 2 {
		t.Fatalf("witness count = %d, want 2: %+v", len(w), w)
	}
	if w[0].Anchor.Kind != model.AnchorPath || string(w[0].OID) != blobOID {
		t.Errorf("path witness = %+v, want oid %s", w[0], blobOID)
	}
	if w[1].Anchor.Kind != model.AnchorCommit || w[1].OID != head {
		t.Errorf("commit witness = %+v, want oid %s", w[1], head)
	}

	// An unborn HEAD skips path witnesses, keeping only the commit anchor.
	unborn, err := c.buildWitness(ctx, "", []model.Anchor{
		{Kind: model.AnchorPath, Value: "f.txt"},
		{Kind: model.AnchorCommit, Value: string(head)},
	})
	if err != nil {
		t.Fatalf("buildWitness unborn: %v", err)
	}
	if len(unborn) != 1 || unborn[0].Anchor.Kind != model.AnchorCommit {
		t.Errorf("unborn witness = %+v, want only the commit anchor", unborn)
	}
}

func TestDeriveRemote(t *testing.T) {
	c, dir := newWBClient(t)
	ctx := t.Context()

	got, err := c.deriveRemote(ctx)
	if err != nil {
		t.Fatalf("deriveRemote: %v", err)
	}
	if got != "origin" {
		t.Errorf("deriveRemote with no wired remote = %q, want origin", got)
	}

	gittest.Git(t, dir, "remote", "add", "up", "https://example.com/x.git")
	if _, err := ccsync.Install(ctx, c.s.Git, "up"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got, err = c.deriveRemote(ctx)
	if err != nil {
		t.Fatalf("deriveRemote after install: %v", err)
	}
	if got != "up" {
		t.Errorf("deriveRemote with one wired remote = %q, want up", got)
	}
}
