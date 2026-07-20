package sourceindex_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/lfs"
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
	if _, err := index.ChangesSince(t.Context(), second, first); !errors.Is(err, sourceindex.ErrNotAncestor) {
		t.Fatalf("reverse ChangesSince = %v, want ErrNotAncestor", err)
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

func TestOperationProofRetainsAppliedViewUntilAcknowledge(t *testing.T) {
	index, repo := fixture(t)
	first := createCommit(t, repo, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	second := createCommit(t, repo, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	firstRef := refs.For(model.KindNote, model.EntityID(first))
	secondRef := refs.For(model.KindNote, model.EntityID(second))
	if err := index.Git.UpdateRef(t.Context(), firstRef, first, ""); err != nil {
		t.Fatalf("create first entity ref: %v", err)
	}
	if err := index.Git.UpdateRef(t.Context(), secondRef, second, ""); err != nil {
		t.Fatalf("create second entity ref: %v", err)
	}
	before, err := index.Refresh(t.Context())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	revised := commit(t, repo, first, model.Pack{Lamport: 2, Ops: []model.Op{model.SetTitle{Title: "retained"}}})
	operationID := "5555555555555555555555555555555555555555555555555555555555555555"
	requestDigest := sha256.Sum256([]byte("retained request"))
	receiptDigest := sha256.Sum256([]byte("retained receipt"))
	committed, err := index.CommitOperation(t.Context(), before, operationID, "entity:note:"+string(first), requestDigest, []gitcmd.RefUpdate{{Ref: firstRef, New: revised, Old: first}})
	if err != nil {
		t.Fatalf("CommitOperation: %v", err)
	}
	if err := index.Git.DeleteRef(t.Context(), firstRef, revised); err != nil {
		t.Fatalf("delete first entity ref: %v", err)
	}
	if err := index.Git.DeleteRef(t.Context(), secondRef, second); err != nil {
		t.Fatalf("delete second entity ref: %v", err)
	}
	if _, err := index.Refresh(t.Context()); err != nil {
		t.Fatalf("advance source head after membership churn: %v", err)
	}
	gittest.Git(t, index.Git.Dir, "reflog", "expire", "--expire=now", "--all")
	gittest.Git(t, index.Git.Dir, "gc", "--prune=now")
	repo, err = gitobj.Open(index.Git.Dir)
	if err != nil {
		t.Fatalf("reopen git objects: %v", err)
	}
	index.Repo = repo
	operation, found, err := index.InspectOperation(t.Context(), operationID)
	if err != nil || !found {
		t.Fatalf("InspectOperation after restart = %+v found=%v err=%v", operation, found, err)
	}
	if operation.Token != committed || operation.RequestDigest != requestDigest || operation.ReceiptDigest != ([sha256.Size]byte{}) {
		t.Fatalf("applied operation = %+v", operation)
	}
	snapshot, err := index.Snapshot(t.Context(), operation.Token)
	if err != nil {
		t.Fatalf("Snapshot applied receipt: %v", err)
	}
	if snapshot.Tips()[firstRef] != revised || snapshot.Tips()[secondRef] != second {
		t.Fatalf("applied snapshot = %+v", snapshot)
	}
	for _, tip := range []model.SHA{revised, second} {
		if _, err := repo.ReadChain(t.Context(), tip); err != nil {
			t.Fatalf("ReadChain retained %s: %v", tip, err)
		}
	}

	acknowledged, err := index.SettleOperation(t.Context(), operationID, requestDigest, receiptDigest, gitobj.SourceOperationAcknowledged)
	if err != nil {
		t.Fatalf("SettleOperation acknowledge: %v", err)
	}
	if acknowledged.ReceiptDigest != receiptDigest {
		t.Fatalf("acknowledged receipt digest = %x, want %x", acknowledged.ReceiptDigest, receiptDigest)
	}
	gittest.Git(t, index.Git.Dir, "reflog", "expire", "--expire=now", "--all")
	gittest.Git(t, index.Git.Dir, "gc", "--prune=now")
	repo, err = gitobj.Open(index.Git.Dir)
	if err != nil {
		t.Fatalf("reopen after acknowledge: %v", err)
	}
	index.Repo = repo
	if _, err := index.Snapshot(t.Context(), committed); err != nil {
		t.Fatalf("acknowledged source manifest: %v", err)
	}
	for _, tip := range []model.SHA{revised, second} {
		if _, err := repo.ReadChain(t.Context(), tip); !errors.Is(err, gitobj.ErrIncompleteChain) {
			t.Fatalf("ReadChain released %s = %v, want ErrIncompleteChain", tip, err)
		}
	}

	forgotten, err := index.SettleOperation(t.Context(), operationID, requestDigest, receiptDigest, gitobj.SourceOperationForgotten)
	if err != nil {
		t.Fatalf("SettleOperation forget: %v", err)
	}
	if forgotten.Token != "" || forgotten.Previous != "" || forgotten.Result != "" || forgotten.RequestDigest != requestDigest || forgotten.ReceiptDigest != receiptDigest {
		t.Fatalf("forgotten operation = %+v", forgotten)
	}
	if _, found, err := index.InspectOperation(t.Context(), operationID); err != nil || found {
		t.Fatalf("InspectOperation forgotten found=%v err=%v", found, err)
	}
	current, err := repo.Tip(t.Context(), sourceindex.Ref)
	if err != nil {
		t.Fatalf("source head: %v", err)
	}
	if err := index.Git.DeleteRef(t.Context(), sourceindex.Ref, current); err != nil {
		t.Fatalf("delete source head: %v", err)
	}
	gittest.Git(t, index.Git.Dir, "reflog", "expire", "--expire=now", "--all")
	gittest.Git(t, index.Git.Dir, "gc", "--prune=now")
	repo, err = gitobj.Open(index.Git.Dir)
	if err != nil {
		t.Fatalf("reopen after forget: %v", err)
	}
	index.Repo = repo
	tombstone, found, err := index.InspectOperationState(t.Context(), operationID)
	if err != nil || !found || tombstone.State != gitobj.SourceOperationForgotten || tombstone.RequestDigest != requestDigest || tombstone.ReceiptDigest != receiptDigest {
		t.Fatalf("InspectOperationState tombstone = %+v found=%v err=%v", tombstone, found, err)
	}
	if _, err := index.CommitOperation(t.Context(), committed, operationID, "reused", requestDigest, []gitcmd.RefUpdate{{Ref: firstRef, New: revised}}); !errors.Is(err, sourceindex.ErrOperationExists) {
		t.Fatalf("CommitOperation reused ID = %v, want ErrOperationExists", err)
	}
}

func TestOperationProofPinsLFSUntilAcknowledge(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs not installed")
	}
	index, repo := fixture(t)
	gittest.Git(t, index.Git.Dir, "commit", "-q", "--allow-empty", "-m", "init")
	body := []byte("durable applied receipt attachment")
	oid := fmt.Sprintf("%x", sha256.Sum256(body))
	common, err := index.Git.CommonDir(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	content := lfs.Store{Dir: filepath.Join(common, "lfs")}
	if err := content.PutVerified(bytes.NewReader(body), oid, int64(len(body))); err != nil {
		t.Fatalf("PutVerified: %v", err)
	}
	root := createCommit(t, repo, "cccccccccccccccccccccccccccccccc")
	attached := commit(t, repo, root, model.Pack{Lamport: 2, Ops: []model.Op{model.AddAttachment{
		Name: "proof.bin", OID: oid, Size: int64(len(body)),
	}}})
	ref := refs.For(model.KindNote, model.EntityID(root))
	if err := index.Git.UpdateRef(t.Context(), ref, attached, ""); err != nil {
		t.Fatalf("create attached entity ref: %v", err)
	}
	before, err := index.Refresh(t.Context())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	revised := commit(t, repo, attached, model.Pack{Lamport: 3, Ops: []model.Op{model.SetTitle{Title: "receipt"}}})
	operationID := "6666666666666666666666666666666666666666666666666666666666666666"
	requestDigest := sha256.Sum256([]byte("LFS request"))
	receiptDigest := sha256.Sum256([]byte("LFS receipt"))
	if _, err := index.CommitOperation(t.Context(), before, operationID, "entity:note:"+string(root), requestDigest, []gitcmd.RefUpdate{{Ref: ref, New: revised, Old: attached}}); err != nil {
		t.Fatalf("CommitOperation: %v", err)
	}
	if err := index.Git.DeleteRef(t.Context(), ref, revised); err != nil {
		t.Fatalf("delete entity ref: %v", err)
	}
	if _, err := index.Refresh(t.Context()); err != nil {
		t.Fatalf("advance source head: %v", err)
	}
	gittest.Git(t, index.Git.Dir, "reflog", "expire", "--expire=now", "--all")
	gittest.Git(t, index.Git.Dir, "gc", "--prune=now")
	gittest.Git(t, index.Git.Dir, "fsck", "--strict", "--no-dangling")
	if listed := gittest.Git(t, index.Git.Dir, "lfs", "ls-files", "--all"); !strings.Contains(listed, oid[:10]) {
		t.Fatalf("git lfs did not discover Applied receipt pointer: %q", listed)
	}
	gittest.Git(t, index.Git.Dir, "lfs", "prune")
	if !content.Has(oid) {
		t.Fatal("git lfs prune removed Applied receipt content")
	}
	if _, err := index.SettleOperation(t.Context(), operationID, requestDigest, receiptDigest, gitobj.SourceOperationAcknowledged); err != nil {
		t.Fatalf("SettleOperation acknowledge: %v", err)
	}
	gittest.Git(t, index.Git.Dir, "reflog", "expire", "--expire=now", "--all")
	gittest.Git(t, index.Git.Dir, "gc", "--prune=now")
	gittest.Git(t, index.Git.Dir, "lfs", "prune")
	if content.Has(oid) {
		t.Fatal("git lfs prune retained acknowledged receipt content")
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

func TestRefreshIgnoresPriorDerivedEpoch(t *testing.T) {
	index, repo := fixture(t)
	legacy := createCommit(t, repo, "11111111111111111111111111111111")
	const legacyRef = "refs/cc-notes-source/head"
	if err := index.Git.UpdateRef(t.Context(), legacyRef, legacy, ""); err != nil {
		t.Fatalf("create prior derived ref: %v", err)
	}
	root := createCommit(t, repo, "22222222222222222222222222222222")
	ref := refs.For(model.KindNote, model.EntityID(root))
	if err := index.Git.UpdateRef(t.Context(), ref, root, ""); err != nil {
		t.Fatalf("create entity ref: %v", err)
	}
	token, err := index.Refresh(t.Context())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if got, err := repo.Tip(t.Context(), sourceindex.Ref); err != nil || got != token {
		t.Fatalf("v1 source ref = %s, %v; want %s", got, err, token)
	}
	if got, err := repo.Tip(t.Context(), legacyRef); err != nil || got != legacy {
		t.Fatalf("prior derived ref = %s, %v; want untouched %s", got, err, legacy)
	}
}

func TestInspectOperationIgnoresPriorDerivedEpoch(t *testing.T) {
	index, repo := fixture(t)
	legacy := createCommit(t, repo, "33333333333333333333333333333333")
	const operationID = "4444444444444444444444444444444444444444444444444444444444444444"
	legacyRef := "refs/cc-notes-source/operations/" + operationID
	if err := index.Git.UpdateRef(t.Context(), legacyRef, legacy, ""); err != nil {
		t.Fatalf("create prior operation ref: %v", err)
	}
	operation, found, err := index.InspectOperation(t.Context(), operationID)
	if err != nil || found || operation.Proof != "" {
		t.Fatalf("InspectOperation = %+v found=%v err=%v, want absent", operation, found, err)
	}
	if got, err := repo.Tip(t.Context(), legacyRef); err != nil || got != legacy {
		t.Fatalf("prior operation ref = %s, %v; want untouched %s", got, err, legacy)
	}
}
