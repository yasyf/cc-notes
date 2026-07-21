package fusefs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/causal"
	"github.com/yasyf/fusekit/contentstream"
	"github.com/yasyf/fusekit/sourcedriver"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

const testDriverAuthorityGeneration causal.Generation = 11

var testDriverDeclarationDigest = sha256.Sum256([]byte("cc-notes-test-driver-declaration"))

var (
	testDriverTargets = []sourcedriver.TargetDeclaration{{
		Tenant: "cc-notes-driver-test", Generation: 1,
	}}
	testDriverTargetSet = func() sourcedriver.TargetSetRef {
		ref, err := sourcedriver.NewTargetSetRef(
			AuthorityForTenant("cc-notes-driver-test"), testDriverAuthorityGeneration, 1,
			testDriverDeclarationDigest, testDriverTargets,
		)
		if err != nil {
			panic(err)
		}
		return ref
	}()
)

func TestGitDriverCreateReplaysExactDurableReceipt(t *testing.T) {
	driver, tenant, _ := newGitDriverTest(t)
	head := refreshDriver(t, driver, tenant.Authority)
	body := NewNoteTemplate("Created through source driver", nil, nil)
	request := mutationRequest(t, tenant, "11", head.Revision, testDriverCreateContext(tenant, 1), body)

	first, err := driver.ApplyMutation(t.Context(), tenant.Authority, request, newMemorySource(body))
	if err != nil {
		t.Fatalf("ApplyMutation create: %v", err)
	}
	if first.State != sourcedriver.MutationApplied || first.Expected != head.Revision || first.Result == "" {
		t.Fatalf("create receipt = %+v", first)
	}
	replaySource := newMemorySource(body)
	replayed, err := driver.ApplyMutation(t.Context(), tenant.Authority, request, replaySource)
	if err != nil {
		t.Fatalf("ApplyMutation replay: %v", err)
	}
	if replaySource.reads != 0 {
		t.Fatalf("exact replay read content %d times", replaySource.reads)
	}
	if replayed != first {
		t.Fatalf("replayed receipt = %+v, want %+v", replayed, first)
	}
	inspected, err := driver.InspectMutation(
		t.Context(), tenant.Authority, request.OperationID, mutationRequestDigest(t, request),
	)
	if err != nil || inspected != first {
		t.Fatalf("InspectMutation = %+v err=%v, want %+v", inspected, err, first)
	}
	if _, err := driver.InspectMutation(
		t.Context(), tenant.Authority, request.OperationID, sha256.Sum256([]byte("forged request")),
	); !errors.Is(err, sourcedriver.ErrConflict) {
		t.Fatalf("InspectMutation forged request error = %v, want ErrConflict", err)
	}

	conflict := request
	conflict.Context.Operation.Name = "different.md"
	if _, err := driver.ApplyMutation(t.Context(), tenant.Authority, conflict, newMemorySource(body)); !errors.Is(err, sourcedriver.ErrConflict) {
		t.Fatalf("conflicting replay error = %v, want ErrConflict", err)
	}

	page, err := driver.Snapshot(
		t.Context(), tenant.Authority,
		testDriverSnapshotRequest(first.Committed, nil, sourcedriver.MaxPageItems),
	)
	if err != nil {
		t.Fatalf("Snapshot committed create: %v", err)
	}
	if !projectionExists(page.Objects, first.Result) {
		t.Fatalf("created source object %q absent from committed snapshot", first.Result)
	}

	unknown := mutationID(t, "22")
	notFound, err := driver.InspectMutation(
		t.Context(), tenant.Authority, unknown, sha256.Sum256([]byte("unknown request")),
	)
	if err != nil || notFound.State != sourcedriver.MutationNotFound || notFound.OperationID != unknown {
		t.Fatalf("unknown receipt = %+v err=%v", notFound, err)
	}
}

func TestGitDriverReceiptSettlementIsDurableExactAndTombstoned(t *testing.T) {
	driver, tenant, _ := newGitDriverTest(t)
	head := refreshDriver(t, driver, tenant.Authority)
	body := NewNoteTemplate("Settled source receipt", nil, nil)
	request := mutationRequest(t, tenant, "24", head.Revision, testDriverCreateContext(tenant, 1), body)
	receipt, err := driver.ApplyMutation(t.Context(), tenant.Authority, request, newMemorySource(body))
	if err != nil {
		t.Fatal(err)
	}
	settlement := sourcedriver.MutationSettlement{
		TargetSet:     request.TargetSet,
		OperationID:   request.OperationID,
		RequestDigest: receipt.RequestDigest,
		ReceiptDigest: receipt.Digest,
		Kind:          sourcedriver.MutationSettlementAcknowledge,
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := driver.SettleMutation(t.Context(), tenant.Authority, settlement); err != nil {
			t.Fatalf("acknowledge attempt %d: %v", attempt+1, err)
		}
	}
	inspected, err := driver.InspectMutation(
		t.Context(), tenant.Authority, request.OperationID, receipt.RequestDigest,
	)
	if err != nil || inspected != receipt {
		t.Fatalf("acknowledged receipt = %+v err=%v", inspected, err)
	}
	forged := settlement
	forged.ReceiptDigest[0] ^= 0xff
	if err := driver.SettleMutation(t.Context(), tenant.Authority, forged); !errors.Is(err, sourcedriver.ErrConflict) {
		t.Fatalf("forged settlement = %v, want conflict", err)
	}
	settlement.Kind = sourcedriver.MutationSettlementForget
	for attempt := 0; attempt < 2; attempt++ {
		if err := driver.SettleMutation(t.Context(), tenant.Authority, settlement); err != nil {
			t.Fatalf("forget attempt %d: %v", attempt+1, err)
		}
	}
	forgotten, err := driver.InspectMutation(
		t.Context(), tenant.Authority, request.OperationID, receipt.RequestDigest,
	)
	if err != nil || forgotten.State != sourcedriver.MutationNotFound {
		t.Fatalf("forgotten receipt = %+v err=%v", forgotten, err)
	}
	replaySource := newMemorySource(body)
	if _, err := driver.ApplyMutation(t.Context(), tenant.Authority, request, replaySource); !errors.Is(err, sourcedriver.ErrConflict) {
		t.Fatalf("forgotten operation replay = %v, want conflict", err)
	}
	if replaySource.reads != 0 {
		t.Fatalf("forgotten operation replay consumed content %d times", replaySource.reads)
	}
}

func TestGitDriverAppliedReceiptSurvivesRestartChurnGCAndLFSPrune(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs not installed")
	}
	driver, tenant, source := newGitDriverTest(t)
	gittest.Git(t, tenant.RepoRoot, "commit", "-q", "--allow-empty", "-m", "init")
	note, noteKey := createRootedNote(t, source, "Receipt source", "before")
	attachmentBody := []byte("receipt-pinned LFS body")
	attachmentOID := fmt.Sprintf("%x", sha256.Sum256(attachmentBody))
	content, err := source.LFS(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := content.PutVerified(bytes.NewReader(attachmentBody), attachmentOID, int64(len(attachmentBody))); err != nil {
		t.Fatal(err)
	}
	entityRef := refs.For(model.KindNote, note.ID)
	if _, err := source.Append(t.Context(), entityRef, []model.Op{model.AddAttachment{
		Name: "receipt.bin", OID: attachmentOID, Size: int64(len(attachmentBody)),
	}}); err != nil {
		t.Fatal(err)
	}
	before := refreshDriver(t, driver, tenant.Authority)
	object := sourceLocator(tenant, noteKey, 3)
	request := mutationRequest(t, tenant, "99", before.Revision, catalog.SourceMutationContext{
		Operation: writableFileOperation(catalog.MutationRevise, Filename(note), true),
		Object:    &object,
		Parent:    ptrSourceLocator(sourceLocator(tenant, "kind:note", 3)),
	}, NewNoteTemplate("Receipt applied", nil, nil))
	receipt, err := driver.ApplyMutation(
		t.Context(), tenant.Authority, request, newMemorySource(NewNoteTemplate("Receipt applied", nil, nil)),
	)
	if err != nil {
		t.Fatalf("ApplyMutation: %v", err)
	}
	wantPages := snapshotPages(t, driver, tenant.Authority, request.TargetSet, receipt.Committed, 2)
	wantChanges := changePages(t, driver, tenant.Authority, request.TargetSet, receipt.Expected, receipt.Committed, 1)
	attachmentRef := findContentRef(t, wantPages, attachmentOID)
	assertContentBody(t, driver, tenant.Authority, attachmentRef, attachmentBody)

	restarted, err := NewGitDriver(
		tenant.Authority, testDriverAuthorityGeneration, testDriverDeclarationDigest, tenant.RepoRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	declareDriverTargetSet(t, restarted, tenant.Authority, testDriverTargetSet, testDriverTargets)
	changedTargets := []sourcedriver.TargetDeclaration{{Tenant: tenant.ID, Generation: 2}}
	changedRef, err := sourcedriver.NewTargetSetRef(
		tenant.Authority, testDriverAuthorityGeneration, 2, testDriverDeclarationDigest, changedTargets,
	)
	if err != nil {
		t.Fatal(err)
	}
	declareDriverTargetSet(t, restarted, tenant.Authority, changedRef, changedTargets)
	if pages := snapshotPages(t, restarted, tenant.Authority, changedRef, receipt.Committed, 2); len(pages) == 0 || pages[0].Objects[0].Generation != 2 {
		t.Fatalf("changed target-set snapshot = %+v", pages)
	}
	createRootedNote(t, source, "New head", "after receipt")
	currentTip, err := source.Repo.Tip(t.Context(), entityRef)
	if err != nil {
		t.Fatal(err)
	}
	if err := source.Git.DeleteRef(t.Context(), entityRef, currentTip); err != nil {
		t.Fatal(err)
	}
	if head := refreshDriver(t, restarted, tenant.Authority); head.Revision == receipt.Committed {
		t.Fatal("head did not advance beyond receipt")
	}
	restarted.cacheMu.Lock()
	restarted.snapshots = make(map[sourcedriver.RevisionToken]*cachedAuthoritySnapshot)
	restarted.snapshotOrder = nil
	restarted.deltas = make(map[authorityDeltaKey]*cachedAuthorityDelta)
	restarted.deltaOrder = nil
	restarted.snapshotBytes = 0
	restarted.deltaBytes = 0
	restarted.cacheMu.Unlock()
	gittest.Git(t, tenant.RepoRoot, "reflog", "expire", "--expire=now", "--all")
	gittest.Git(t, tenant.RepoRoot, "gc", "--prune=now")
	gittest.Git(t, tenant.RepoRoot, "lfs", "prune")
	if !content.Has(attachmentOID) {
		t.Fatal("LFS prune removed unacknowledged receipt content")
	}
	if got := snapshotPages(t, restarted, tenant.Authority, request.TargetSet, receipt.Committed, 2); !reflect.DeepEqual(got, wantPages) {
		t.Fatalf("receipt snapshot after restart/churn/gc differs\ngot:  %+v\nwant: %+v", got, wantPages)
	}
	if got := changePages(t, restarted, tenant.Authority, request.TargetSet, receipt.Expected, receipt.Committed, 1); !reflect.DeepEqual(got, wantChanges) {
		t.Fatalf("receipt changes after restart/churn/gc differ\ngot:  %+v\nwant: %+v", got, wantChanges)
	}
	assertContentBody(t, restarted, tenant.Authority, attachmentRef, attachmentBody)

	settlement := sourcedriver.MutationSettlement{
		TargetSet: request.TargetSet, OperationID: request.OperationID,
		RequestDigest: receipt.RequestDigest, ReceiptDigest: receipt.Digest,
		Kind: sourcedriver.MutationSettlementAcknowledge,
	}
	if err := restarted.SettleMutation(t.Context(), tenant.Authority, settlement); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	gittest.Git(t, tenant.RepoRoot, "reflog", "expire", "--expire=now", "--all")
	gittest.Git(t, tenant.RepoRoot, "gc", "--prune=now")
	gittest.Git(t, tenant.RepoRoot, "lfs", "prune")
	if content.Has(attachmentOID) {
		t.Fatal("acknowledged receipt retained reclaimable LFS content")
	}
	settlement.Kind = sourcedriver.MutationSettlementForget
	if err := restarted.SettleMutation(t.Context(), tenant.Authority, settlement); err != nil {
		t.Fatalf("forget: %v", err)
	}
	replay := newMemorySource(NewNoteTemplate("Receipt applied", nil, nil))
	if _, err := restarted.ApplyMutation(t.Context(), tenant.Authority, request, replay); !errors.Is(err, sourcedriver.ErrConflict) {
		t.Fatalf("forgotten operation replay = %v, want conflict", err)
	}
	if replay.reads != 0 {
		t.Fatalf("forgotten operation replay consumed content %d times", replay.reads)
	}
}

func TestGitDriverCommittedMutationSurvivesLostResponse(t *testing.T) {
	driver, tenant, _ := newGitDriverTest(t)
	head := refreshDriver(t, driver, tenant.Authority)
	body := NewNoteTemplate("Lost response", nil, nil)
	request := mutationRequest(t, tenant, "77", head.Revision, testDriverCreateContext(tenant, 1), body)
	lost := errors.New("response stream settlement failed")
	source := newMemorySource(body)
	source.settleFailure = lost
	if _, err := driver.ApplyMutation(t.Context(), tenant.Authority, request, source); !errors.Is(err, lost) {
		t.Fatalf("ApplyMutation error = %v, want lost response", err)
	}
	inspected, err := driver.InspectMutation(
		t.Context(), tenant.Authority, request.OperationID, mutationRequestDigest(t, request),
	)
	if err != nil || inspected.State != sourcedriver.MutationApplied || inspected.Result == "" {
		t.Fatalf("InspectMutation after lost response = %+v err=%v", inspected, err)
	}
	replayed, err := driver.ApplyMutation(t.Context(), tenant.Authority, request, newMemorySource(body))
	if err != nil || replayed != inspected {
		t.Fatalf("replay after lost response = %+v err=%v, want %+v", replayed, err, inspected)
	}
}

func TestGitDriverReviseDeleteAndAtomicReplace(t *testing.T) {
	driver, tenant, source := newGitDriverTest(t)
	note, noteKey := createRootedNote(t, source, "Before", "old body")
	head := refreshDriver(t, driver, tenant.Authority)
	parent := sourceLocator(tenant, "kind:note", 3)
	object := sourceLocator(tenant, noteKey, 3)

	revisedBody := NewNoteTemplate("After", nil, nil)
	reviseContext := catalog.SourceMutationContext{
		Operation: writableFileOperation(catalog.MutationRevise, Filename(note), true),
		Object:    &object,
		Parent:    &parent,
	}
	revise := mutationRequest(t, tenant, "33", head.Revision, reviseContext, revisedBody)
	revised, err := driver.ApplyMutation(t.Context(), tenant.Authority, revise, newMemorySource(revisedBody))
	if err != nil {
		t.Fatalf("ApplyMutation revise: %v", err)
	}
	if revised.Result != sourcedriver.LogicalID(noteKey) {
		t.Fatalf("revise result = %q, want %q", revised.Result, noteKey)
	}
	assertContentContains(t, driver, tenant.Authority, revised.Committed, revised.Result, []byte("title: After"))

	deleteContext := catalog.SourceMutationContext{
		Operation: writableFileOperation(catalog.MutationDelete, Filename(note), false),
		Object:    &object,
		Parent:    &parent,
	}
	deleted, err := driver.ApplyMutation(
		t.Context(), tenant.Authority,
		mutationRequest(t, tenant, "44", revised.Committed, deleteContext, nil), nil,
	)
	if err != nil {
		t.Fatalf("ApplyMutation delete: %v", err)
	}
	if deleted.Result != "" {
		t.Fatalf("delete result = %q", deleted.Result)
	}
	assertProjectionAbsent(t, driver, tenant.Authority, deleted.Committed, sourcedriver.LogicalID(noteKey))

	replacement, replacementKey := createRootedNote(t, source, "Replacement", "replacement body")
	target, targetKey := createRootedNote(t, source, "Target", "target body")
	replaceHead := refreshDriver(t, driver, tenant.Authority)
	replacementLocator := sourceLocator(tenant, replacementKey, 7)
	targetLocator := sourceLocator(tenant, targetKey, 7)
	replaceContext := catalog.SourceMutationContext{
		Operation: writableFileOperation(catalog.MutationReplace, Filename(replacement), false),
		Object:    &replacementLocator,
		Parent:    ptrSourceLocator(sourceLocator(tenant, "kind:note", 7)),
		Target:    &targetLocator,
	}
	replaced, err := driver.ApplyMutation(
		t.Context(), tenant.Authority,
		mutationRequest(t, tenant, "55", replaceHead.Revision, replaceContext, nil), nil,
	)
	if err != nil {
		t.Fatalf("ApplyMutation replace: %v", err)
	}
	if replaced.Result != sourcedriver.LogicalID(replacementKey) || replacement.ID == target.ID {
		t.Fatalf("replace result = %q replacement=%s target=%s", replaced.Result, replacement.ID, target.ID)
	}
	page, err := driver.Snapshot(
		t.Context(), tenant.Authority,
		testDriverSnapshotRequest(replaced.Committed, nil, sourcedriver.MaxPageItems),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !projectionExists(page.Objects, sourcedriver.LogicalID(replacementKey)) ||
		projectionExists(page.Objects, sourcedriver.LogicalID(targetKey)) {
		t.Fatalf("atomic replace projections do not contain only replacement")
	}
}

func TestGitDriverRejectsStaleSourceRevision(t *testing.T) {
	driver, tenant, source := newGitDriverTest(t)
	stale := refreshDriver(t, driver, tenant.Authority)
	createRootedNote(t, source, "External", "change")
	current := refreshDriver(t, driver, tenant.Authority)
	body := NewNoteTemplate("Stale create", nil, nil)
	request := mutationRequest(t, tenant, "66", stale.Revision, testDriverCreateContext(tenant, 1), body)
	_, err := driver.ApplyMutation(t.Context(), tenant.Authority, request, newMemorySource(body))
	var staleErr *sourcedriver.StaleRevisionError
	if !errors.As(err, &staleErr) || staleErr.Expected != stale.Revision || staleErr.Actual != current.Revision {
		t.Fatalf("stale error = %#v (%v), current=%q", staleErr, err, current.Revision)
	}
}

func TestGitDriverRejectsInvalidMutationTargetBeforeReadingContent(t *testing.T) {
	driver, tenant, _ := newGitDriverTest(t)
	head := refreshDriver(t, driver, tenant.Authority)
	body := NewNoteTemplate("Foreign target", nil, nil)
	base := mutationRequest(t, tenant, "88", head.Revision, testDriverCreateContext(tenant, 1), body)
	tests := []struct {
		name   string
		mutate func(*sourcedriver.MutationRequest)
	}{
		{name: "tenant", mutate: func(request *sourcedriver.MutationRequest) {
			request.Tenant = ""
		}},
		{name: "generation", mutate: func(request *sourcedriver.MutationRequest) {
			request.Generation = 0
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := base
			test.mutate(&request)
			source := newMemorySource(body)
			if _, err := driver.ApplyMutation(t.Context(), tenant.Authority, request, source); !errors.Is(err, sourcedriver.ErrInvalidValue) {
				t.Fatalf("ApplyMutation invalid target error = %v", err)
			}
			if source.reads != 0 {
				t.Fatalf("invalid target read content %d times", source.reads)
			}
		})
	}
	if after := refreshDriver(t, driver, tenant.Authority); after != head {
		t.Fatalf("invalid target changed head from %+v to %+v", head, after)
	}
}

func TestGitDriverSnapshotAndDeltaCursorsAreExactAndReplayable(t *testing.T) {
	driver, tenant, source := newGitDriverTest(t)
	before := refreshDriver(t, driver, tenant.Authority)
	for index := 0; index < 5; index++ {
		createRootedNote(t, source, fmt.Sprintf("Note %d", index), "body")
	}
	after := refreshDriver(t, driver, tenant.Authority)

	var snapshotIDs []sourcedriver.LogicalID
	var snapshotCursor *sourcedriver.PageCursor
	for {
		request := testDriverSnapshotRequest(after.Revision, snapshotCursor, 2)
		page, err := driver.Snapshot(t.Context(), tenant.Authority, request)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := driver.Snapshot(t.Context(), tenant.Authority, request)
		if err != nil || !reflect.DeepEqual(replayed, page) {
			t.Fatalf("snapshot page replay differs: got=%+v replay=%+v err=%v", page, replayed, err)
		}
		for _, object := range page.Objects {
			snapshotIDs = append(snapshotIDs, object.ID)
		}
		if page.Next == nil {
			break
		}
		next := *page.Next
		snapshotCursor = &next
	}
	if len(snapshotIDs) < 5 || !strictLogicalIDs(snapshotIDs) {
		t.Fatalf("snapshot ids are incomplete or unordered: %v", snapshotIDs)
	}
	if snapshotCursor == nil {
		t.Fatal("snapshot did not produce a continuation cursor")
	}
	wrongGeneration := testDriverSnapshotRequest(after.Revision, snapshotCursor, 2)
	wrongGeneration.TargetSet.AuthorityGeneration++
	if _, err := driver.Snapshot(t.Context(), tenant.Authority, wrongGeneration); err == nil {
		t.Fatal("snapshot accepted a cursor from an older authority generation")
	}
	wrongDeclaration := testDriverSnapshotRequest(after.Revision, snapshotCursor, 2)
	wrongDeclaration.TargetSet.DeclarationDigest = sha256.Sum256([]byte("changed target declaration"))
	if _, err := driver.Snapshot(t.Context(), tenant.Authority, wrongDeclaration); err == nil {
		t.Fatal("snapshot accepted a cursor from a different target declaration")
	}
	changedTarget := testDriverSnapshotRequest(after.Revision, nil, 2)
	changedTargets := append([]sourcedriver.TargetDeclaration(nil), testDriverTargets...)
	changedTargets[0].Generation++
	changedRef, err := sourcedriver.NewTargetSetRef(
		tenant.Authority, testDriverAuthorityGeneration, 2, testDriverDeclarationDigest, changedTargets,
	)
	if err != nil {
		t.Fatal(err)
	}
	declareDriverTargetSet(t, driver, tenant.Authority, changedRef, changedTargets)
	changedTarget.TargetSet = changedRef
	changedPage, err := driver.Snapshot(t.Context(), tenant.Authority, changedTarget)
	if err != nil || len(changedPage.Objects) == 0 || changedPage.Objects[0].Generation != changedTargets[0].Generation {
		t.Fatalf("snapshot under changed target set = %+v, %v", changedPage, err)
	}
	changedTarget.Cursor = snapshotCursor
	if _, err := driver.Snapshot(t.Context(), tenant.Authority, changedTarget); err == nil {
		t.Fatal("old cursor crossed into a different target generation")
	}
	forgedSnapshot, err := sourcedriver.NewPageCursor(
		testDriverTargetSet, sourcedriver.PageSnapshot,
		"", after.Revision, 1, 2,
		sourcedriver.PagePosition{
			Tenant: tenant.ID, Generation: causal.Generation(tenant.Generation), ID: snapshotIDs[len(snapshotIDs)-1],
		}, nil, sha256.Sum256([]byte("forged")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Snapshot(
		t.Context(), tenant.Authority, testDriverSnapshotRequest(after.Revision, &forgedSnapshot, 2),
	); err == nil {
		t.Fatal("snapshot accepted a self-consistent cursor without exact page history")
	}
	hostileSnapshot, err := sourcedriver.NewPageCursor(
		testDriverTargetSet, sourcedriver.PageSnapshot,
		"", after.Revision, math.MaxUint32, 2,
		sourcedriver.PagePosition{
			Tenant: tenant.ID, Generation: causal.Generation(tenant.Generation), ID: snapshotIDs[0],
		}, nil, sha256.Sum256([]byte("hostile")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Snapshot(
		t.Context(), tenant.Authority, testDriverSnapshotRequest(after.Revision, &hostileSnapshot, 2),
	); err == nil {
		t.Fatal("snapshot accepted an impossible maximum-page cursor")
	}

	var changes []sourcedriver.Change
	var changeCursor *sourcedriver.PageCursor
	for {
		request := testDriverChangesRequest(before.Revision, after.Revision, changeCursor, 2)
		page, err := driver.ChangesSince(t.Context(), tenant.Authority, request)
		if err != nil {
			t.Fatal(err)
		}
		replayed, err := driver.ChangesSince(t.Context(), tenant.Authority, request)
		if err != nil || !reflect.DeepEqual(replayed, page) {
			t.Fatalf("change page replay differs: got=%+v replay=%+v err=%v", page, replayed, err)
		}
		changes = append(changes, page.Changes...)
		if page.Next == nil {
			break
		}
		next := *page.Next
		changeCursor = &next
	}
	if len(changes) != 5 {
		t.Fatalf("delta changes = %d, want 5", len(changes))
	}
	for index, change := range changes {
		if change.Kind != sourcedriver.ChangeUpsert ||
			index > 0 && string(changes[index-1].ID) >= string(change.ID) {
			t.Fatalf("change sequence is not strictly ordered upserts: %+v", changes)
		}
	}
	forgedChange, err := sourcedriver.NewPageCursor(
		testDriverTargetSet, sourcedriver.PageChanges,
		before.Revision, after.Revision, 1, 2,
		sourcedriver.PagePosition{
			Tenant: changes[len(changes)-1].Tenant, Generation: changes[len(changes)-1].Generation,
			Sequence: changes[len(changes)-1].Sequence, ID: changes[len(changes)-1].ID,
		}, nil, sha256.Sum256([]byte("forged")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.ChangesSince(
		t.Context(), tenant.Authority,
		testDriverChangesRequest(before.Revision, after.Revision, &forgedChange, 2),
	); err == nil {
		t.Fatal("changes accepted a self-consistent cursor without exact page history")
	}
	hostileChange, err := sourcedriver.NewPageCursor(
		testDriverTargetSet, sourcedriver.PageChanges,
		before.Revision, after.Revision, math.MaxUint32, 2,
		sourcedriver.PagePosition{
			Tenant: changes[0].Tenant, Generation: changes[0].Generation,
			Sequence: changes[0].Sequence, ID: changes[0].ID,
		}, nil, sha256.Sum256([]byte("hostile")),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.ChangesSince(
		t.Context(), tenant.Authority,
		testDriverChangesRequest(before.Revision, after.Revision, &hostileChange, 2),
	); err == nil {
		t.Fatal("changes accepted an impossible maximum-page cursor")
	}
}

func TestGitDriverTargetSetDeclarationIsPagedExactAndCompleteBeforeUse(t *testing.T) {
	repo := gittest.InitRepo(t)
	authority := causal.SourceAuthorityID("cc-notes:target-set-test")
	driver, err := NewGitDriver(authority, testDriverAuthorityGeneration, testDriverDeclarationDigest, repo)
	if err != nil {
		t.Fatal(err)
	}
	targets := make([]sourcedriver.TargetDeclaration, sourcedriver.MaxTargetPageItems+1)
	for index := range targets {
		targets[index] = sourcedriver.TargetDeclaration{
			Tenant: catalog.TenantID(fmt.Sprintf("target-%03d", index)), Generation: 1,
		}
	}
	ref, err := sourcedriver.NewTargetSetRef(
		authority, testDriverAuthorityGeneration, 1, testDriverDeclarationDigest, targets,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.InspectTargetSet(t.Context(), authority, ref); !errors.Is(err, sourcedriver.ErrNotFound) {
		t.Fatalf("InspectTargetSet unknown = %v, want ErrNotFound", err)
	}
	state, err := sourcedriver.NewTargetSetState(authority, ref)
	if err != nil {
		t.Fatal(err)
	}
	first, err := sourcedriver.NewTargetSetPage(state, targets[:sourcedriver.MaxTargetPageItems])
	if err != nil {
		t.Fatal(err)
	}
	partial, err := driver.DeclareTargetSet(t.Context(), authority, first)
	if err != nil || partial.Complete {
		t.Fatalf("first target page = %+v, %v", partial, err)
	}
	if replay, err := driver.DeclareTargetSet(t.Context(), authority, first); err != nil || replay != partial {
		t.Fatalf("first target page replay = %+v, %v; want %+v", replay, err, partial)
	}
	forged := first
	forged.Targets = append([]sourcedriver.TargetDeclaration(nil), first.Targets...)
	forged.Targets[0].Generation++
	if _, err := driver.DeclareTargetSet(t.Context(), authority, forged); !errors.Is(err, sourcedriver.ErrIntegrity) {
		t.Fatalf("forged target page = %v, want ErrIntegrity", err)
	}
	if _, err := driver.Snapshot(t.Context(), authority, sourcedriver.SnapshotRequest{
		TargetSet: ref, Revision: sourcedriver.RevisionToken(strings.Repeat("a", 40)), Limit: 1,
	}); !errors.Is(err, sourcedriver.ErrConflict) {
		t.Fatalf("Snapshot incomplete target set = %v, want ErrConflict", err)
	}
	last, err := sourcedriver.NewTargetSetPage(partial, targets[sourcedriver.MaxTargetPageItems:])
	if err != nil {
		t.Fatal(err)
	}
	complete, err := driver.DeclareTargetSet(t.Context(), authority, last)
	if err != nil || !complete.Complete {
		t.Fatalf("last target page = %+v, %v", complete, err)
	}
	if replay, err := driver.DeclareTargetSet(t.Context(), authority, first); err != nil || replay != partial {
		t.Fatalf("old target page replay = %+v, %v; want %+v", replay, err, partial)
	}
	if inspected, err := driver.InspectTargetSet(t.Context(), authority, ref); err != nil || inspected != complete {
		t.Fatalf("InspectTargetSet complete = %+v, %v; want %+v", inspected, err, complete)
	}
}

func TestGitDriverHundredTargetPagingBuildsOneImmutableSnapshotWithBoundedContinuations(t *testing.T) {
	repo := gittest.InitRepo(t)
	authority := causal.SourceAuthorityID("cc-notes:scale-authority")
	tenants := make([]Tenant, 100)
	for index := range tenants {
		id, err := catalog.NewTenantID(fmt.Sprintf("cc-notes-scale-%03d", index))
		if err != nil {
			t.Fatal(err)
		}
		tenants[index] = Tenant{
			ID: id, Generation: catalog.Generation(index + 1), Authority: authority,
			RouteName: fmt.Sprintf("scale-%03d", index), RepoRoot: repo,
		}
	}
	driver, err := NewGitDriver(authority, testDriverAuthorityGeneration, testDriverDeclarationDigest, repo)
	if err != nil {
		t.Fatal(err)
	}
	source, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	createRootedNote(t, source, "Scale", "one immutable body")
	head := refreshDriver(t, driver, authority)
	targets := make([]sourcedriver.TargetDeclaration, len(tenants))
	for index, tenant := range tenants {
		targets[index] = sourcedriver.TargetDeclaration{
			Tenant: tenant.ID, Generation: causal.Generation(tenant.Generation),
		}
	}
	targetSet, err := sourcedriver.NewTargetSetRef(
		authority, testDriverAuthorityGeneration, 1, testDriverDeclarationDigest, targets,
	)
	if err != nil {
		t.Fatal(err)
	}
	declareDriverTargetSet(t, driver, authority, targetSet, targets)
	request := sourcedriver.SnapshotRequest{
		TargetSet: targetSet,
		Revision:  head.Revision,
		Limit:     17,
	}
	var previous *sourcedriver.Projection
	var content *sourcedriver.ContentRef
	count := 0
	pages := 0
	for {
		page, err := driver.Snapshot(t.Context(), authority, request)
		if err != nil {
			t.Fatal(err)
		}
		pages++
		for index := range page.Objects {
			object := page.Objects[index]
			if previous != nil && compareProjection(*previous, object) >= 0 {
				t.Fatalf("projection tuple order regressed at page %d", pages)
			}
			current := object
			previous = &current
			if content == nil && object.Content != nil {
				ref := *object.Content
				content = &ref
			}
			count++
		}
		if page.Next == nil {
			break
		}
		next := *page.Next
		request.Cursor = &next
	}
	if count == 0 || pages < 2 || driver.snapshotBuilds.Load() != 1 || driver.continuations.Load() != uint64(pages-1) {
		t.Fatalf(
			"scale instrumentation objects=%d pages=%d snapshot_builds=%d continuation_checks=%d",
			count, pages, driver.snapshotBuilds.Load(), driver.continuations.Load(),
		)
	}
	if content == nil {
		t.Fatal("scale snapshot contained no content reference")
	}
	stream, err := driver.OpenContent(t.Context(), authority, *content)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(stream)
	settleErr := stream.Settle(readErr)
	waitErr := stream.Wait(t.Context())
	if err := errors.Join(readErr, settleErr, waitErr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("one immutable body")) || driver.snapshotBuilds.Load() != 1 {
		t.Fatalf("indexed content open rebuilt snapshot or returned wrong body: builds=%d body=%q", driver.snapshotBuilds.Load(), body)
	}
}

func TestVerifiedSourceCancellationClosesAndJoinsRead(t *testing.T) {
	reader := newBlockingReadCloser()
	source := newVerifiedSource(reader, 1, catalog.ContentHash(sha256.Sum256([]byte("x"))))
	readDone := make(chan error, 1)
	go func() {
		_, err := source.Read(make([]byte, 1))
		readDone <- err
	}()
	<-reader.started
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := source.Wait(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait cancellation = %v", err)
	}
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked read did not join")
	}
	if err := source.Settle(nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("replayed settlement = %v, want cancellation", err)
	}
}

func TestVerifiedSourceRejectsPrematureSuccessfulSettlement(t *testing.T) {
	body := []byte("body")
	source := newVerifiedSource(io.NopCloser(bytes.NewReader(body)), int64(len(body)), catalog.ContentHash(sha256.Sum256(body)))
	settleErr := source.Settle(nil)
	if settleErr == nil {
		t.Fatal("premature settlement returned success")
	}
	if replayed := source.Settle(nil); !errors.Is(replayed, settleErr) {
		t.Fatalf("replayed settlement = %v, want identical %v", replayed, settleErr)
	}
	if err := source.Wait(t.Context()); err == nil || err.Error() != settleErr.Error() {
		t.Fatal("premature successful settlement was accepted")
	}
}

func TestVerifiedSourceBoundsOversizedUnderlyingContent(t *testing.T) {
	want := []byte("bounded")
	underlying := append(append([]byte(nil), want...), bytes.Repeat([]byte("x"), 1024)...)
	source := newVerifiedSource(
		io.NopCloser(bytes.NewReader(underlying)), int64(len(want)), catalog.ContentHash(sha256.Sum256(want)),
	)
	body, err := io.ReadAll(source)
	if !errors.Is(err, sourcedriver.ErrIntegrity) {
		t.Fatalf("oversized read error = %v, want ErrIntegrity", err)
	}
	if len(body) > len(want) {
		t.Fatalf("oversized source exposed %d bytes, declared %d", len(body), len(want))
	}
	if replay := source.Settle(err); !errors.Is(replay, sourcedriver.ErrIntegrity) {
		t.Fatalf("oversized settlement = %v", replay)
	}
	if waitErr := source.Wait(t.Context()); !errors.Is(waitErr, sourcedriver.ErrIntegrity) {
		t.Fatalf("oversized wait = %v", waitErr)
	}
}

func declareDriverTargetSet(
	t *testing.T,
	driver *GitDriver,
	authority causal.SourceAuthorityID,
	ref sourcedriver.TargetSetRef,
	targets []sourcedriver.TargetDeclaration,
) sourcedriver.TargetSetState {
	t.Helper()
	state, err := sourcedriver.NewTargetSetState(authority, ref)
	if err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < len(targets); offset += sourcedriver.MaxTargetPageItems {
		end := min(offset+sourcedriver.MaxTargetPageItems, len(targets))
		page, err := sourcedriver.NewTargetSetPage(state, targets[offset:end])
		if err != nil {
			t.Fatal(err)
		}
		state, err = driver.DeclareTargetSet(t.Context(), authority, page)
		if err != nil {
			t.Fatal(err)
		}
	}
	return state
}

func snapshotPages(
	t *testing.T,
	driver *GitDriver,
	authority causal.SourceAuthorityID,
	targetSet sourcedriver.TargetSetRef,
	revision sourcedriver.RevisionToken,
	limit int,
) []sourcedriver.SnapshotPage {
	t.Helper()
	request := sourcedriver.SnapshotRequest{TargetSet: targetSet, Revision: revision, Limit: limit}
	var pages []sourcedriver.SnapshotPage
	for {
		page, err := driver.Snapshot(t.Context(), authority, request)
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, page)
		if page.Next == nil {
			return pages
		}
		next := *page.Next
		request.Cursor = &next
	}
}

func changePages(
	t *testing.T,
	driver *GitDriver,
	authority causal.SourceAuthorityID,
	targetSet sourcedriver.TargetSetRef,
	from, to sourcedriver.RevisionToken,
	limit int,
) []sourcedriver.ChangePage {
	t.Helper()
	request := sourcedriver.ChangesRequest{TargetSet: targetSet, From: from, To: to, Limit: limit}
	var pages []sourcedriver.ChangePage
	for {
		page, err := driver.ChangesSince(t.Context(), authority, request)
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, page)
		if page.Next == nil {
			return pages
		}
		next := *page.Next
		request.Cursor = &next
	}
}

func findContentRef(t *testing.T, pages []sourcedriver.SnapshotPage, oid string) sourcedriver.ContentRef {
	t.Helper()
	for _, page := range pages {
		for _, object := range page.Objects {
			if object.Content != nil && fmt.Sprintf("%x", object.Content.Hash) == oid {
				return *object.Content
			}
		}
	}
	t.Fatalf("snapshot has no content ref for %s", oid)
	return sourcedriver.ContentRef{}
}

func assertContentBody(
	t *testing.T,
	driver *GitDriver,
	authority causal.SourceAuthorityID,
	ref sourcedriver.ContentRef,
	want []byte,
) {
	t.Helper()
	stream, err := driver.OpenContent(t.Context(), authority, ref)
	if err != nil {
		t.Fatal(err)
	}
	got, readErr := io.ReadAll(stream)
	settleErr := stream.Settle(readErr)
	waitErr := stream.Wait(t.Context())
	if err := errors.Join(readErr, settleErr, waitErr); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("content = %q, want %q", got, want)
	}
}

func newGitDriverTest(t *testing.T) (*GitDriver, Tenant, *store.Store) {
	t.Helper()
	repo := gittest.InitRepo(t)
	id, err := catalog.NewTenantID("cc-notes-driver-test")
	if err != nil {
		t.Fatal(err)
	}
	tenant := Tenant{
		ID: id, Generation: 1, Authority: AuthorityForTenant(id),
		RouteName: "repo", RepoRoot: repo,
	}
	driver, err := NewGitDriver(
		tenant.Authority, testDriverAuthorityGeneration, testDriverDeclarationDigest, tenant.RepoRoot,
	)
	if err != nil {
		t.Fatal(err)
	}
	declareDriverTargetSet(t, driver, tenant.Authority, testDriverTargetSet, testDriverTargets)
	source, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	return driver, tenant, source
}

func refreshDriver(t *testing.T, driver *GitDriver, authority causal.SourceAuthorityID) sourcedriver.Head {
	t.Helper()
	head, err := driver.Refresh(t.Context(), authority)
	if err != nil {
		t.Fatal(err)
	}
	return head
}

func testDriverSnapshotRequest(
	revision sourcedriver.RevisionToken,
	cursor *sourcedriver.PageCursor,
	limit int,
) sourcedriver.SnapshotRequest {
	return sourcedriver.SnapshotRequest{
		TargetSet: testDriverTargetSet,
		Revision:  revision,
		Cursor:    cursor,
		Limit:     limit,
	}
}

func testDriverChangesRequest(
	from, to sourcedriver.RevisionToken,
	cursor *sourcedriver.PageCursor,
	limit int,
) sourcedriver.ChangesRequest {
	return sourcedriver.ChangesRequest{
		TargetSet: testDriverTargetSet,
		From:      from,
		To:        to,
		Cursor:    cursor,
		Limit:     limit,
	}
}

func createRootedNote(t *testing.T, source *store.Store, title, body string) (model.Note, string) {
	t.Helper()
	note := createSnapshot(t, source, model.CreateNote{Nonce: model.NewNonce(), Title: title, Body: body}).(model.Note)
	tip, err := source.Repo.Tip(t.Context(), refs.For(model.KindNote, note.ID))
	if err != nil {
		t.Fatal(err)
	}
	rooted, err := source.LoadRootedAt(t.Context(), tip)
	if err != nil {
		t.Fatal(err)
	}
	key, err := entitySourceKey(model.KindNote, rooted.Root)
	if err != nil {
		t.Fatal(err)
	}
	return note, key
}

func mutationRequest(
	t *testing.T,
	tenant Tenant,
	idByte string,
	expected sourcedriver.RevisionToken,
	sourceContext catalog.SourceMutationContext,
	body []byte,
) sourcedriver.MutationRequest {
	t.Helper()
	request := sourcedriver.MutationRequest{
		TargetSet:   testDriverTargetSet,
		Tenant:      tenant.ID,
		Generation:  causal.Generation(tenant.Generation),
		OperationID: mutationID(t, idByte),
		Expected:    expected,
		Context:     sourceContext,
		HasContent:  sourceContext.Operation.HasContent,
	}
	if request.HasContent {
		request.ContentSize = int64(len(body))
		request.ContentHash = catalog.ContentHash(sha256.Sum256(body))
	}
	return request
}

func mutationID(t *testing.T, pair string) catalog.MutationID {
	t.Helper()
	id, err := catalog.ParseMutationID(strings.Repeat(pair, 32))
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mutationRequestDigest(t *testing.T, request sourcedriver.MutationRequest) [sha256.Size]byte {
	t.Helper()
	digest, err := sourcedriver.MutationRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func writableFileOperation(kind catalog.MutationKind, name string, content bool) catalog.SourceMutationOperation {
	return catalog.SourceMutationOperation{
		Kind: kind, Name: name, ObjectKind: catalog.KindFile, Mode: 0o644, HasContent: content,
	}
}

func testDriverCreateContext(tenant Tenant, revision causal.Revision) catalog.SourceMutationContext {
	parent := sourceLocator(tenant, "kind:note", revision)
	return catalog.SourceMutationContext{
		Operation: writableFileOperation(catalog.MutationCreate, "new-note.md", true),
		Parent:    &parent,
	}
}

func sourceLocator(tenant Tenant, key string, revision causal.Revision) catalog.SourceLocator {
	return catalog.SourceLocator{
		SourceAuthority: tenant.Authority,
		SourceKey:       catalog.SourceObjectKey(key),
		SourceRevision:  revision,
	}
}

func ptrSourceLocator(locator catalog.SourceLocator) *catalog.SourceLocator { return &locator }

func assertContentContains(
	t *testing.T,
	driver *GitDriver,
	authority causal.SourceAuthorityID,
	revision sourcedriver.RevisionToken,
	id sourcedriver.LogicalID,
	want []byte,
) {
	t.Helper()
	page, err := driver.Snapshot(
		t.Context(), authority, testDriverSnapshotRequest(revision, nil, sourcedriver.MaxPageItems),
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range page.Objects {
		if object.ID != id || object.Content == nil {
			continue
		}
		stream, err := driver.OpenContent(t.Context(), authority, *object.Content)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(stream)
		settleErr := stream.Settle(readErr)
		waitErr := stream.Wait(t.Context())
		if err := errors.Join(readErr, settleErr, waitErr); err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(body, want) {
			t.Fatalf("content = %q, want fragment %q", body, want)
		}
		return
	}
	t.Fatalf("content projection %q not found", id)
}

func assertProjectionAbsent(
	t *testing.T,
	driver *GitDriver,
	authority causal.SourceAuthorityID,
	revision sourcedriver.RevisionToken,
	id sourcedriver.LogicalID,
) {
	t.Helper()
	page, err := driver.Snapshot(
		t.Context(), authority, testDriverSnapshotRequest(revision, nil, sourcedriver.MaxPageItems),
	)
	if err != nil {
		t.Fatal(err)
	}
	if projectionExists(page.Objects, id) {
		t.Fatalf("projection %q remains after delete", id)
	}
}

func projectionExists(objects []sourcedriver.Projection, id sourcedriver.LogicalID) bool {
	for _, object := range objects {
		if object.ID == id {
			return true
		}
	}
	return false
}

func strictLogicalIDs(values []sourcedriver.LogicalID) bool {
	for index := 1; index < len(values); index++ {
		if values[index-1] >= values[index] {
			return false
		}
	}
	return true
}

type memorySource struct {
	*bytes.Reader
	once          sync.Once
	done          chan struct{}
	err           error
	settleFailure error
	reads         int
}

func newMemorySource(body []byte) *memorySource {
	return &memorySource{Reader: bytes.NewReader(body), done: make(chan struct{})}
}

func (s *memorySource) Read(buffer []byte) (int, error) {
	s.reads++
	return s.Reader.Read(buffer)
}

func (s *memorySource) Settle(cause error) error {
	s.once.Do(func() {
		s.err = cause
		if s.err == nil {
			s.err = s.settleFailure
		}
		close(s.done)
	})
	return s.err
}

func (s *memorySource) Wait(ctx context.Context) error {
	select {
	case <-s.done:
		return s.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

var _ contentstream.Source = (*memorySource)(nil)

type blockingReadCloser struct {
	started chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{started: make(chan struct{}), closed: make(chan struct{})}
}

func (r *blockingReadCloser) Read([]byte) (int, error) {
	select {
	case <-r.started:
	default:
		close(r.started)
	}
	<-r.closed
	return 0, io.ErrClosedPipe
}

func (r *blockingReadCloser) Close() error {
	r.once.Do(func() { close(r.closed) })
	return nil
}
