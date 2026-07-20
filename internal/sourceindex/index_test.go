package sourceindex_test

import (
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/model"
)

func fixture(t *testing.T) (sourceindex.Index, *gitobj.Repo) {
	t.Helper()
	dir := gittest.InitRepo(t)
	repo, err := gitobj.Open(dir)
	if err != nil {
		t.Fatalf("gitobj.Open: %v", err)
	}
	return sourceindex.Index{Repo: repo, Git: gitcmd.Git{Dir: dir}}, repo
}

func commit(t *testing.T, repo *gitobj.Repo, parent model.SHA, pack model.Pack) model.SHA {
	t.Helper()
	parents := []model.SHA(nil)
	if parent != "" {
		parents = []model.SHA{parent}
	}
	sha, err := repo.WriteOpsCommit(t.Context(), parents, gitobj.Signature{
		Name: "Test User", Email: "test@example.com", When: time.Unix(int64(pack.Lamport), 0).UTC(),
	}, "cc-notes: source index test", pack)
	if err != nil {
		t.Fatalf("WriteOpsCommit: %v", err)
	}
	return sha
}

func createCommit(t *testing.T, repo *gitobj.Repo, nonce string) model.SHA {
	t.Helper()
	return commit(t, repo, "", model.Pack{Lamport: 1, Ops: []model.Op{model.CreateNote{
		Nonce: nonce, Title: nonce,
	}}})
}

func TestRefreshIsIdempotentAndSealsExternalChanges(t *testing.T) {
	index, repo := fixture(t)
	root := createCommit(t, repo, "0123456789abcdef0123456789abcdef")
	ref := refs.For(model.KindNote, model.EntityID(root))
	if err := index.Git.UpdateRef(t.Context(), ref, root, ""); err != nil {
		t.Fatalf("create entity ref: %v", err)
	}
	first, err := index.Refresh(t.Context())
	if err != nil {
		t.Fatalf("Refresh first: %v", err)
	}
	again, err := index.Refresh(t.Context())
	if err != nil {
		t.Fatalf("Refresh unchanged: %v", err)
	}
	if again != first {
		t.Fatalf("unchanged refresh = %s, want %s", again, first)
	}
	nextEntity := commit(t, repo, root, model.Pack{Lamport: 2, Ops: []model.Op{model.SetTitle{Title: "changed"}}})
	if err := index.Git.UpdateRef(t.Context(), ref, nextEntity, root); err != nil {
		t.Fatalf("external entity update: %v", err)
	}
	second, err := index.Refresh(t.Context())
	if err != nil {
		t.Fatalf("Refresh changed: %v", err)
	}
	if second == first {
		t.Fatal("changed refresh reused old source revision")
	}
	changes, err := index.ChangesSince(t.Context(), first, second)
	if err != nil {
		t.Fatalf("ChangesSince: %v", err)
	}
	if len(changes.Upserts) != 1 || changes.Upserts[0].Ref != ref || changes.Upserts[0].Tip != nextEntity {
		t.Fatalf("upserts = %+v, want %s at %s", changes.Upserts, ref, nextEntity)
	}
	if len(changes.Deletes) != 0 {
		t.Fatalf("deletes = %v, want none", changes.Deletes)
	}
}

func TestCommitAtomicallyAdvancesEntityAndSourceRevision(t *testing.T) {
	index, repo := fixture(t)
	root := createCommit(t, repo, "0123456789abcdef0123456789abcdef")
	ref := refs.For(model.KindNote, model.EntityID(root))
	if err := index.Git.UpdateRef(t.Context(), ref, root, ""); err != nil {
		t.Fatalf("create entity ref: %v", err)
	}
	before, err := index.Refresh(t.Context())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	nextEntity := commit(t, repo, root, model.Pack{Lamport: 2, Session: "operation-1", Ops: []model.Op{model.SetTitle{Title: "changed"}}})
	operationID := "1111111111111111111111111111111111111111111111111111111111111111"
	requestDigest := sha256.Sum256([]byte("request-1"))
	after, err := index.CommitOperation(t.Context(), before, operationID, "entity:note:"+string(root), requestDigest, []gitcmd.RefUpdate{{Ref: ref, New: nextEntity, Old: root}})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	snapshot, err := index.Snapshot(t.Context(), after)
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if got := snapshot.Tips()[ref]; got != nextEntity {
		t.Fatalf("source manifest tip = %s, want %s", got, nextEntity)
	}
	if got, err := repo.Tip(t.Context(), ref); err != nil || got != nextEntity {
		t.Fatalf("entity ref = %s, %v; want %s", got, err, nextEntity)
	}
	proof, found, err := index.InspectOperation(t.Context(), operationID)
	if err != nil || !found {
		t.Fatalf("InspectOperation = %+v found=%v err=%v", proof, found, err)
	}
	if proof.Previous != before || proof.RequestDigest != requestDigest || proof.Result != "entity:note:"+string(root) ||
		len(proof.Changes.Upserts) != 1 || proof.Changes.Upserts[0].Tip != nextEntity {
		t.Fatalf("operation proof = %+v", proof)
	}
	if _, err := index.CommitOperation(t.Context(), after, operationID, "different", sha256.Sum256([]byte("different")), []gitcmd.RefUpdate{{Ref: ref, New: root, Old: nextEntity}}); !errors.Is(err, sourceindex.ErrOperationExists) {
		t.Fatalf("replayed CommitOperation = %v, want ErrOperationExists", err)
	}
}

func TestCommitRejectsStaleSourceWithoutMovingEntity(t *testing.T) {
	index, repo := fixture(t)
	root := createCommit(t, repo, "0123456789abcdef0123456789abcdef")
	ref := refs.For(model.KindNote, model.EntityID(root))
	if err := index.Git.UpdateRef(t.Context(), ref, root, ""); err != nil {
		t.Fatalf("create entity ref: %v", err)
	}
	stale, err := index.Refresh(t.Context())
	if err != nil {
		t.Fatalf("Refresh stale: %v", err)
	}
	external := commit(t, repo, root, model.Pack{Lamport: 2, Ops: []model.Op{model.SetTitle{Title: "external"}}})
	if err := index.Git.UpdateRef(t.Context(), ref, external, root); err != nil {
		t.Fatalf("external update: %v", err)
	}
	if _, err := index.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh external: %v", err)
	}
	proposed := commit(t, repo, root, model.Pack{Lamport: 2, Ops: []model.Op{model.SetTitle{Title: "proposed"}}})
	_, err = index.CommitOperation(t.Context(), stale, "2222222222222222222222222222222222222222222222222222222222222222", "", sha256.Sum256([]byte("stale")), []gitcmd.RefUpdate{{Ref: ref, New: proposed, Old: root}})
	if !errors.Is(err, gitcmd.ErrCASMismatch) {
		t.Fatalf("stale Commit = %v, want ErrCASMismatch", err)
	}
	if got, err := repo.Tip(t.Context(), ref); err != nil || got != external {
		t.Fatalf("entity ref = %s, %v; want external %s", got, err, external)
	}
}

func TestInspectOperationDistinguishesNeverCommitted(t *testing.T) {
	index, _ := fixture(t)
	if _, err := index.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	operation, found, err := index.InspectOperation(t.Context(), "3333333333333333333333333333333333333333333333333333333333333333")
	if err != nil || found || operation.Token != "" {
		t.Fatalf("InspectOperation = %+v found=%v err=%v, want absent", operation, found, err)
	}
}

func TestRefreshUsesSeparateUnsyncedNamespace(t *testing.T) {
	index, repo := fixture(t)
	root := createCommit(t, repo, "0123456789abcdef0123456789abcdef")
	ref := refs.For(model.KindNote, model.EntityID(root))
	if err := index.Git.UpdateRef(t.Context(), ref, root, ""); err != nil {
		t.Fatalf("create entity ref: %v", err)
	}
	if _, err := index.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	entityRefs, err := repo.ListPrefix(t.Context(), refs.Namespace)
	if err != nil {
		t.Fatalf("ListPrefix: %v", err)
	}
	if len(entityRefs) != 1 || entityRefs[ref] != root {
		t.Fatalf("entity refs = %v, want only %s", entityRefs, ref)
	}
	if _, found := entityRefs[sourceindex.Ref]; found {
		t.Fatalf("derived source ref leaked into entity namespace")
	}
}
