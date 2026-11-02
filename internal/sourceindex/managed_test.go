package sourceindex_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/internal/sourceview"
	"github.com/yasyf/cc-notes/model"
)

func TestRefreshDoesNotImportDirectGitWrites(t *testing.T) {
	index, repo := fixture(t)
	before, err := index.Refresh(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	root := managedCreate(t, repo, "direct-git-write")
	ref := refs.For(model.KindNote, model.EntityID(root))
	if err := index.Git.source.RecordsGit.UpdateRef(t.Context(), ref, root, ""); err != nil {
		t.Fatal(err)
	}
	current, err := index.Refresh(t.Context())
	if err != nil || current != before {
		t.Fatalf("unmanaged refresh=%s err=%v want %s", current, err, before)
	}
	snapshot, err := index.Snapshot(t.Context(), current)
	if err != nil || len(snapshot) != 0 {
		t.Fatalf("unmanaged snapshot=%v err=%v", snapshot, err)
	}
	if err := index.Git.source.Reindex(t.Context()); err != nil {
		t.Fatal(err)
	}
	after, err := index.Refresh(t.Context())
	if err != nil || after == before {
		t.Fatalf("reindexed head=%s err=%v", after, err)
	}
	snapshot, err = index.Snapshot(t.Context(), after)
	if err != nil || snapshot.Tips()[ref] != root {
		t.Fatalf("imported snapshot=%v err=%v", snapshot, err)
	}
}

func TestConcurrentIdenticalOperationReplaysBeforeStaleFence(t *testing.T) {
	index, repo := fixture(t)
	root := managedCreate(t, repo, "same-id-root")
	ref := refs.For(model.KindNote, model.EntityID(root))
	if err := index.Git.UpdateRef(t.Context(), ref, root, ""); err != nil {
		t.Fatal(err)
	}
	expected, err := index.Refresh(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	next := commit(t, repo, root, model.Pack{Lamport: 2, Ops: []model.Op{model.SetTitle{Title: "same operation"}}})
	id := strings.Repeat("a", 64)
	digest := sha256.Sum256([]byte("identical"))
	updates := []gitcmd.RefUpdate{{Ref: ref, Old: root, New: next}}
	type answer struct {
		token model.SHA
		err   error
	}
	answers := make(chan answer, 2)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			token, err := index.CommitOperation(t.Context(), expected, id, "same-result", digest, updates, nil)
			answers <- answer{token, err}
		}()
	}
	ready.Wait()
	close(start)
	first, second := <-answers, <-answers
	if first.err != nil || second.err != nil || first.token != second.token || first.token == expected {
		t.Fatalf("concurrent receipts=%+v %+v", first, second)
	}
	head, err := index.Backend.SourceHead(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	original, err := index.Backend.ResolveRevision(t.Context(), expected)
	if err != nil {
		t.Fatal(err)
	}
	if head.EntityGeneration != original.EntityGeneration+1 {
		t.Fatalf("generation=%d want %d", head.EntityGeneration, original.EntityGeneration+1)
	}
	if _, err := index.CommitOperation(t.Context(), expected, id, "different", sha256.Sum256([]byte("different")), updates, nil); !errors.Is(err, sourceindex.ErrOperationExists) {
		t.Fatalf("different digest=%v", err)
	}
	receipt := sha256.Sum256([]byte("receipt"))
	if _, err := index.SettleOperation(t.Context(), id, digest, receipt, gitobj.SourceOperationAcknowledged); err != nil {
		t.Fatal(err)
	}
	if _, err := index.SettleOperation(t.Context(), id, digest, receipt, gitobj.SourceOperationForgotten); err != nil {
		t.Fatal(err)
	}
	if _, err := index.CommitOperation(t.Context(), model.SHA(strings.Repeat("0", 40)), id, "reused", digest, updates, nil); !errors.Is(err, sourceindex.ErrOperationExists) {
		t.Fatalf("forgotten ID before unknown expected=%v", err)
	}
}

func TestGlobalRetainedLFSConflictCanBeRepaired(t *testing.T) {
	index, repo := fixture(t)
	body := []byte("shared attachment body")
	oid := fmtOID(body)
	content := index.Git.source.LFS()
	if err := content.PutVerified(bytes.NewReader(body), oid, int64(len(body))); err != nil {
		t.Fatal(err)
	}
	first := managedCreate(t, repo, "first shared LFS")
	second := managedCreate(t, repo, "second shared LFS")
	firstTip := commit(t, repo, first, model.Pack{Lamport: 2, Ops: []model.Op{model.AddAttachment{Name: "one", OID: oid, Size: int64(len(body))}}})
	secondTip := commit(t, repo, second, model.Pack{Lamport: 2, Ops: []model.Op{model.AddAttachment{Name: "two", OID: oid, Size: int64(len(body)) + 1}}})
	firstRef, secondRef := refs.For(model.KindNote, model.EntityID(first)), refs.For(model.KindNote, model.EntityID(second))
	if err := index.Git.UpdateRef(t.Context(), firstRef, firstTip, ""); err != nil {
		t.Fatal(err)
	}
	if err := index.Git.UpdateRef(t.Context(), secondRef, secondTip, ""); err != nil {
		t.Fatalf("ordinary publication rejected conflicting LFS: %v", err)
	}
	head, err := index.Backend.SourceHead(t.Context())
	if err != nil || head.RetentionConflicts != 1 {
		t.Fatalf("conflicting head=%+v err=%v", head, err)
	}
	invalid := commit(t, repo, firstTip, model.Pack{Lamport: 3, Ops: []model.Op{model.SetTitle{Title: "still invalid"}}})
	if _, err := index.CommitOperation(t.Context(), head.Token, strings.Repeat("b", 64), "invalid", sha256.Sum256([]byte("invalid")), []gitcmd.RefUpdate{{Ref: firstRef, Old: firstTip, New: invalid}}, nil); err == nil {
		t.Fatal("helper accepted conflicting candidate")
	}
	repaired := commit(t, repo, secondTip, model.Pack{Lamport: 3, Ops: []model.Op{model.RemoveAttachment{Name: "two"}}})
	if _, err := index.CommitOperation(t.Context(), head.Token, strings.Repeat("c", 64), "repair", sha256.Sum256([]byte("repair")), []gitcmd.RefUpdate{{Ref: secondRef, Old: secondTip, New: repaired}}, nil); err != nil {
		t.Fatalf("repair candidate=%v", err)
	}
	current, err := index.Backend.SourceHead(t.Context())
	if err != nil || current.RetentionConflicts != 0 {
		t.Fatalf("repaired head=%+v err=%v", current, err)
	}
}

func TestAcquiredEntityContentSurvivesAcknowledgementAndGC(t *testing.T) {
	index, repo := fixture(t)
	root := managedCreate(t, repo, "active content root")
	ref := refs.For(model.KindNote, model.EntityID(root))
	if err := index.Git.UpdateRef(t.Context(), ref, root, ""); err != nil {
		t.Fatal(err)
	}
	before, err := index.Refresh(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	next := commit(t, repo, root, model.Pack{Lamport: 2, Ops: []model.Op{model.SetTitle{Title: "active content"}}})
	id := strings.Repeat("d", 64)
	digest := sha256.Sum256([]byte("active content request"))
	token, err := index.CommitOperation(t.Context(), before, id, "content", digest, []gitcmd.RefUpdate{{Ref: ref, Old: root, New: next}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, stream, err := index.Backend.OpenSourceContent(t.Context(), token, "entity:note:"+string(root))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if descriptor.Kind != sourceview.File {
		t.Fatalf("descriptor=%+v", descriptor)
	}
	if err := index.Git.DeleteRef(t.Context(), ref, next); err != nil {
		t.Fatal(err)
	}
	if _, err := index.SettleOperation(t.Context(), id, digest, sha256.Sum256([]byte("active receipt")), gitobj.SourceOperationAcknowledged); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, index.Git.Dir, "reflog", "expire", "--expire=now", "--all")
	gittest.Git(t, index.Git.Dir, "gc", "--prune=now")
	body, err := io.ReadAll(stream)
	if err != nil || sha256.Sum256(body) != descriptor.Hash || int64(len(body)) != descriptor.Size {
		t.Fatalf("acquired body=%q err=%v descriptor=%+v", body, err, descriptor)
	}
}

func fmtOID(body []byte) string { return fmt.Sprintf("%x", sha256.Sum256(body)) }

func TestLegacyAppliedImportRequiresProvenanceWithoutMovingRefs(t *testing.T) {
	index, repo := fixture(t)
	root := managedCreate(t, repo, "legacy Applied root")
	ref := refs.For(model.KindNote, model.EntityID(root))
	if err := index.Git.UpdateRef(t.Context(), ref, root, ""); err != nil {
		t.Fatal(err)
	}
	manifest, err := gitobj.NewSourceManifest(map[string]model.SHA{ref: root})
	if err != nil {
		t.Fatal(err)
	}
	old, err := repo.WriteSourceManifestCommit(t.Context(), "", manifest)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := repo.WriteSourceManifestCommit(t.Context(), old, manifest)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("e", 64)
	proof, err := repo.WriteSourceOperationProof(t.Context(), gitobj.SourceOperationProof{OperationID: id, State: gitobj.SourceOperationApplied, Expected: old, Committed: legacy, Result: "legacy", RequestDigest: sha256.Sum256([]byte("legacy")), RetainedTips: []model.SHA{root}})
	if err != nil {
		t.Fatal(err)
	}
	operationRef, _ := sourceindex.OperationRef(id)
	if err := index.Git.source.RecordsGit.UpdateRefs(t.Context(), []gitcmd.RefUpdate{{Ref: sourceindex.LegacyRef, New: legacy}, {Ref: operationRef, New: proof}, {Ref: sourceindex.OperationPinRef(id), New: proof}}); err != nil {
		t.Fatal(err)
	}
	before, err := index.Refresh(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	err = index.Git.source.Reindex(t.Context())
	if err == nil || !strings.Contains(err.Error(), id) || !strings.Contains(err.Error(), string(legacy)) {
		t.Fatalf("Applied import gate=%v", err)
	}
	after, readErr := index.Refresh(t.Context())
	if readErr != nil || after != before {
		t.Fatalf("failed import changed head=%s err=%v want %s", after, readErr, before)
	}
	for name, want := range map[string]model.SHA{sourceindex.LegacyRef: legacy, operationRef: proof, sourceindex.OperationPinRef(id): proof} {
		got, err := repo.Tip(t.Context(), name)
		if err != nil || got != want {
			t.Fatalf("failed import changed %s=%s err=%v want %s", name, got, err, want)
		}
	}
}

func TestLegacyAcknowledgedPrunedImportPreservesMetadata(t *testing.T) {
	index, repo := fixture(t)
	root := managedCreate(t, repo, "released legacy root")
	ref := refs.For(model.KindNote, model.EntityID(root))
	empty, err := repo.WriteSourceManifestCommit(t.Context(), "", gitobj.SourceManifest{})
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := gitobj.NewSourceManifest(map[string]model.SHA{ref: root})
	if err != nil {
		t.Fatal(err)
	}
	token, err := repo.WriteSourceManifestCommit(t.Context(), empty, manifest)
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("f", 64)
	request, receipt := sha256.Sum256([]byte("released request")), sha256.Sum256([]byte("released receipt"))
	proof, err := repo.WriteSourceOperationProof(t.Context(), gitobj.SourceOperationProof{OperationID: id, State: gitobj.SourceOperationAcknowledged, Expected: empty, Committed: token, Result: "released", RequestDigest: request, ReceiptDigest: receipt})
	if err != nil {
		t.Fatal(err)
	}
	opref, _ := sourceindex.OperationRef(id)
	if err := index.Git.source.RecordsGit.UpdateRefs(t.Context(), []gitcmd.RefUpdate{{Ref: sourceindex.LegacyRef, New: token}, {Ref: opref, New: proof}}); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, index.Git.Dir, "reflog", "expire", "--expire=now", "--all")
	gittest.Git(t, index.Git.Dir, "gc", "--prune=now")
	reopenFixture(t, index)
	if err := index.Git.source.Reindex(t.Context()); err != nil {
		t.Fatalf("released historical import=%v", err)
	}
	snapshot, err := index.Snapshot(t.Context(), token)
	if err != nil || snapshot.Tips()[ref] != root {
		t.Fatalf("released metadata=%v err=%v", snapshot, err)
	}
	operation, found, err := index.InspectOperation(t.Context(), id)
	if err != nil || !found || operation.Token != token || operation.RequestDigest != request || operation.ReceiptDigest != receipt || operation.State != gitobj.SourceOperationAcknowledged || len(operation.Changes.Upserts) != 1 {
		t.Fatalf("released operation=%+v found=%v err=%v", operation, found, err)
	}
	if _, _, err := index.Backend.SourceObject(t.Context(), token, "entity:note:"+string(root)); !errors.Is(err, sourceindex.ErrProjectionUnavailable) {
		t.Fatalf("released projection=%v", err)
	}
}

func managedCreate(t *testing.T, repo *gitobj.Repo, name string) model.SHA {
	digest := fmtOID([]byte(name))
	return createCommit(t, repo, digest[:32])
}

func TestCommitOperationRejectsInvalidProofFieldsWithoutPublication(t *testing.T) {
	index, repo := fixture(t)
	root := managedCreate(t, repo, "invalid proof fields")
	ref := refs.For(model.KindNote, model.EntityID(root))
	if err := index.Git.UpdateRef(t.Context(), ref, root, ""); err != nil {
		t.Fatal(err)
	}
	expected, err := index.Refresh(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	next := commit(t, repo, root, model.Pack{Lamport: 2, Ops: []model.Op{model.SetTitle{Title: "must not publish"}}})
	for position, test := range []struct {
		name, result string
		digest       [sha256.Size]byte
	}{{name: "zero request digest", result: "valid"}, {name: "newline result", result: "invalid\nresult", digest: sha256.Sum256([]byte("newline"))}, {name: "NUL result", result: "invalid\x00result", digest: sha256.Sum256([]byte("NUL"))}, {name: "CR result", result: "invalid\rresult", digest: sha256.Sum256([]byte("CR"))}} {
		t.Run(test.name, func(t *testing.T) {
			id := fmt.Sprintf("%064x", position+256)
			if _, err := index.CommitOperation(t.Context(), expected, id, test.result, test.digest, []gitcmd.RefUpdate{{Ref: ref, Old: root, New: next}}, nil); err == nil {
				t.Fatal("invalid proof committed")
			}
			actual, err := index.Refresh(t.Context())
			if err != nil || actual != expected {
				t.Fatalf("head=%s err=%v want %s", actual, err, expected)
			}
			tip, err := repo.Tip(t.Context(), ref)
			if err != nil || tip != root {
				t.Fatalf("entity=%s err=%v want %s", tip, err, root)
			}
			if _, found, err := index.InspectOperationState(t.Context(), id); err != nil || found {
				t.Fatalf("invalid operation found=%v err=%v", found, err)
			}
		})
	}
}

func TestZeroSizeAttachmentProjectionAndContentRemainServable(t *testing.T) {
	index, repo := fixture(t)
	root := managedCreate(t, repo, "zero size attachment")
	ref := refs.For(model.KindNote, model.EntityID(root))
	oid := fmtOID(nil)
	content := index.Git.source.LFS()
	if err := content.PutVerified(bytes.NewReader(nil), oid, 0); err != nil {
		t.Fatal(err)
	}
	attached := commit(t, repo, root, model.Pack{Lamport: 2, Ops: []model.Op{model.AddAttachment{Name: "empty.bin", OID: oid, Size: 0}}})
	if err := index.Git.UpdateRef(t.Context(), ref, attached, ""); err != nil {
		t.Fatal(err)
	}
	head, err := index.Backend.SourceHead(t.Context())
	if err != nil || head.ProjectionErrors != 0 || head.RetentionConflicts != 1 {
		t.Fatalf("zero-size head=%+v err=%v", head, err)
	}
	objects, total, err := index.Backend.SourceObjectRange(t.Context(), head.Token, 0, 100)
	if err != nil || uint64(len(objects)) != total {
		t.Fatalf("zero-size page objects=%v total=%d err=%v", objects, total, err)
	}
	digest := sha256.Sum256([]byte("empty.bin"))
	key := "attachment:" + string(root) + ":" + fmt.Sprintf("%x", digest)
	descriptor, stream, err := index.Backend.OpenSourceContent(t.Context(), head.Token, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	body, err := io.ReadAll(stream)
	if err != nil || len(body) != 0 || descriptor.Size != 0 || descriptor.Hash != sha256.Sum256(nil) {
		t.Fatalf("empty content=%q descriptor=%+v err=%v", body, descriptor, err)
	}
}
