package fusefs

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/causal"
	"github.com/yasyf/fusekit/tenant"
)

func TestSourcePlannerReservesCreateNonceBeforeWorker(t *testing.T) {
	configured := testTenant(t)
	operationID := testMutationID(t, "11111111111111111111111111111111")
	step := tenant.SourceMutationStep{
		TenantID: configured.ID, Generation: configured.Generation, OperationID: operationID,
		SourceID: string(configured.Authority), ExpectedHead: 9,
		Kind: catalog.MutationCreate, Source: testCreateContext(configured, 7),
	}
	planner := sourceMutationPlanner{
		executable: "/Applications/CCNotesTest.app/Contents/MacOS/CCNotesTest",
		tenants:    map[catalog.TenantID]Tenant{configured.ID: configured},
	}
	worker, err := planner.PrepareSourceMutation(t.Context(), step)
	if err != nil {
		t.Fatalf("PrepareSourceMutation: %v", err)
	}
	wantKey := catalog.SourceObjectKey("entity:note:" + operationID.String())
	if worker.SourceResult == nil || worker.SourceResult.SourceKey != wantKey ||
		worker.SourceResult.SourceAuthority != step.Source.Parent.SourceAuthority ||
		worker.SourceResult.SourceRevision != step.Source.Parent.SourceRevision {
		t.Fatalf("source result = %+v, want key %q with parent authority/revision", worker.SourceResult, wantKey)
	}
	decoded, handled, err := parseSourceWorkerArguments(worker.Spec.Args)
	if err != nil || !handled || decoded.OperationID != operationID || decoded.RepoRoot != configured.RepoRoot {
		t.Fatalf("worker contract = %+v handled=%v err=%v", decoded, handled, err)
	}
	wantSession := "CC_NOTES_SESSION_ID=" + operationID.String()
	if !containsExact(worker.Spec.Env, wantSession) {
		t.Fatalf("worker environment lacks %q", wantSession)
	}
}

func TestSourceCreateReplayAndRestartPreserveReservedIdentity(t *testing.T) {
	repo := gittest.InitRepo(t)
	configured := testTenant(t)
	configured.RepoRoot = repo
	operationID := testMutationID(t, "22222222222222222222222222222222")
	config := sourceWorkerConfig{
		Tenant: configured.ID, Generation: configured.Generation, Revision: 9,
		OperationID: operationID, RepoRoot: repo, Source: testCreateContext(configured, 7),
	}
	t.Setenv("CC_NOTES_SESSION_ID", operationID.String())
	content := NewNoteTemplate("Created through FuseKit", nil, nil)
	for attempt := 0; attempt < 2; attempt++ {
		var proof bytes.Buffer
		if err := runSourceWorker(t.Context(), config, bytes.NewReader(content), &proof); err != nil {
			t.Fatalf("runSourceWorker attempt %d: %v", attempt+1, err)
		}
		if !strings.Contains(proof.String(), `"lane":1`) {
			t.Fatalf("proof = %q", proof.String())
		}
	}

	reopened, err := store.Open(repo)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	rooted, err := reopened.ListRootedSnapshots(t.Context(), model.KindNote, store.ListOpts{})
	if err != nil {
		t.Fatalf("ListRootedSnapshots: %v", err)
	}
	if len(rooted) != 1 {
		t.Fatalf("created entities = %d, want 1", len(rooted))
	}
	key, err := entitySourceKey(model.KindNote, rooted[0].Root)
	if err != nil {
		t.Fatalf("entitySourceKey: %v", err)
	}
	if key != "entity:note:"+operationID.String() {
		t.Fatalf("source key = %q", key)
	}
	ref := "refs/cc-notes/notes/" + string(rooted[0].Snapshot.EntityID())
	applied, err := reopened.HasSession(t.Context(), ref, operationID.String())
	if err != nil || !applied {
		t.Fatalf("durable session applied=%v err=%v", applied, err)
	}
}

func TestSourceReplaceIsIdempotentAcrossBothGitEntities(t *testing.T) {
	repo := gittest.InitRepo(t)
	source, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	targetNonce, replacementNonce := "target-nonce", "replacement-nonce"
	target := createSnapshot(t, source, model.CreateNote{Nonce: targetNonce, Title: "Target", Body: "old"}).(model.Note)
	replacement := createSnapshot(t, source, model.CreateNote{Nonce: replacementNonce, Title: "Replacement", Body: "new"}).(model.Note)
	configured := testTenant(t)
	configured.RepoRoot = repo
	operationID := testMutationID(t, "33333333333333333333333333333333")
	t.Setenv("CC_NOTES_SESSION_ID", operationID.String())
	authority := causal.SourceAuthorityID(configured.Authority)
	config := sourceWorkerConfig{
		Tenant: configured.ID, Generation: configured.Generation, Revision: 12,
		OperationID: operationID, RepoRoot: repo,
		Source: catalog.SourceMutationContext{
			Operation: catalog.SourceMutationOperation{Kind: catalog.MutationReplace, Name: Filename(replacement), ObjectKind: catalog.KindFile, Mode: 0o644},
			Object:    &catalog.SourceLocator{SourceAuthority: authority, SourceKey: catalog.SourceObjectKey("entity:note:" + replacementNonce), SourceRevision: 4},
			Target:    &catalog.SourceLocator{SourceAuthority: authority, SourceKey: catalog.SourceObjectKey("entity:note:" + targetNonce), SourceRevision: 4},
			Parent:    &catalog.SourceLocator{SourceAuthority: authority, SourceKey: "kind:note", SourceRevision: 4},
		},
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := runSourceWorker(t.Context(), config, nil, &bytes.Buffer{}); err != nil {
			t.Fatalf("replace attempt %d: %v", attempt+1, err)
		}
	}
	reopened, err := store.Open(repo)
	if err != nil {
		t.Fatal(err)
	}
	rooted, err := reopened.ListRootedSnapshots(t.Context(), model.KindNote, store.ListOpts{IncludeDeleted: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(rooted) != 2 {
		t.Fatalf("rooted notes = %d", len(rooted))
	}
	states := map[string]bool{}
	for _, value := range rooted {
		key, keyErr := entitySourceKey(model.KindNote, value.Root)
		if keyErr != nil {
			t.Fatal(keyErr)
		}
		states[key] = value.Snapshot.Meta().Deleted
	}
	if states["entity:note:"+replacementNonce] || !states["entity:note:"+targetNonce] || target.ID == replacement.ID {
		t.Fatalf("replace states = %+v", states)
	}
}

func testCreateContext(configured Tenant, revision causal.Revision) catalog.SourceMutationContext {
	return catalog.SourceMutationContext{
		Operation: catalog.SourceMutationOperation{
			Kind: catalog.MutationCreate, Name: "new-note.md", ObjectKind: catalog.KindFile,
			Mode: 0o644, HasContent: true,
		},
		Parent: &catalog.SourceLocator{
			SourceAuthority: causal.SourceAuthorityID(configured.Authority),
			SourceKey:       "kind:note", SourceRevision: revision,
		},
	}
}

func testMutationID(t *testing.T, value string) catalog.MutationID {
	t.Helper()
	id, err := catalog.ParseMutationID(value)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func containsExact(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
