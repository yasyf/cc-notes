package gitobj_test

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func TestReadWorkCountsNativeRefsAndPackDecodes(t *testing.T) {
	dir := initRepo(t)
	repo := open(t, dir)
	sig := gitobj.Signature{Name: testName, Email: testEmail, When: t0}
	root, err := repo.WriteOpsCommit(t.Context(), nil, sig, "root", createPack)
	if err != nil {
		t.Fatal(err)
	}
	child, err := repo.WriteOpsCommit(t.Context(), []model.SHA{root}, sig, "child", retitlePack)
	if err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "update-ref", "refs/cc-notes/notes/root", string(root))
	gittest.Git(t, dir, "update-ref", "refs/cc-notes/notes/child", string(child))
	gittest.Git(t, dir, "repack", "-adq")
	ctx, work := gitobj.WithReadWork(t.Context())
	tips, err := repo.ListPrefixes(ctx, "refs/cc-notes/notes/")
	if err != nil || len(tips) != 2 {
		t.Fatalf("native tips=%v: %v", tips, err)
	}
	chain, err := repo.ReadChain(ctx, child)
	if err != nil || len(chain) != 2 {
		t.Fatalf("native chain=%v: %v", chain, err)
	}
	if lists, entries, decodes := work.Snapshot(); lists != 1 || entries != 2 || decodes != 2 {
		t.Fatalf("work=%d/%d/%d, want 1/2/2", lists, entries, decodes)
	}
	otherCtx, other := gitobj.WithReadWork(t.Context())
	if _, err := repo.ReadCommit(otherCtx, root); err != nil {
		t.Fatal(err)
	}
	if lists, entries, decodes := other.Snapshot(); lists != 0 || entries != 0 || decodes != 1 {
		t.Fatalf("independent work=%d/%d/%d, want 0/0/1", lists, entries, decodes)
	}
	if lists, entries, decodes := work.Snapshot(); lists != 1 || entries != 2 || decodes != 2 {
		t.Fatalf("independent context changed prior work=%d/%d/%d", lists, entries, decodes)
	}
}
