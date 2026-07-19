package fusefs

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/catalogproto"
	"github.com/yasyf/fusekit/holder"
	"github.com/yasyf/fusekit/mountproto"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

func TestAuthoritySnapshotRendersStableObjectsAndSymlinks(t *testing.T) {
	source := authorityStore(t)
	noteNonce := model.NewNonce()
	note := createSnapshot(t, source, model.CreateNote{Nonce: noteNonce, Title: "Durable note", Body: "body"}).(model.Note)
	project := createSnapshot(t, source, model.CreateProject{Nonce: model.NewNonce(), Title: "Project"}).(model.Project)
	sprint := createSnapshot(t, source, model.CreateSprint{Nonce: model.NewNonce(), Title: "Sprint", Project: project.ID}).(model.Sprint)
	task := createSnapshot(t, source, model.CreateTask{
		Nonce: model.NewNonce(), Title: "Task", Type: model.TypeTask, Branch: "main",
	}).(model.Task)
	updated, err := source.Append(t.Context(), refs.For(model.KindTask, task.ID), []model.Op{model.SetSprint{Sprint: sprint.ID}})
	if err != nil {
		t.Fatalf("link task to sprint: %v", err)
	}
	task = updated.(model.Task)

	snapshot, err := BuildAuthoritySnapshot(t.Context(), source)
	if err != nil {
		t.Fatalf("BuildAuthoritySnapshot: %v", err)
	}
	objects := indexAuthorityObjects(snapshot.objects)
	noteObject := objects["entity:note:"+noteNonce]
	if noteObject.name != Filename(note) || !strings.Contains(string(noteObject.content), "Durable note") {
		t.Fatalf("note object = %+v content=%q", noteObject, noteObject.content)
	}
	for _, key := range []string{
		"sprint-task:" + string(sprint.ID) + ":" + string(task.ID),
		"project-sprint-task:" + string(project.ID) + ":" + string(sprint.ID) + ":" + string(task.ID),
		"project-task:" + string(project.ID) + ":" + string(task.ID),
	} {
		object, found := objects[key]
		if !found || object.kind != catalogproto.ObjectKindSymlink || object.linkTarget == "" {
			t.Fatalf("symlink %q = %+v, found=%v", key, object, found)
		}
	}

	tenant := testTenant(t)
	input, closers, err := sourceInput(tenant, 7, snapshot.objects, nil)
	if err != nil {
		t.Fatalf("sourceInput: %v", err)
	}
	defer closeAll(closers)
	if input.Record.ObjectCount != uint32(snapshot.Len()) || input.Record.DeleteCount != 0 {
		t.Fatalf("source record = %+v snapshot len=%d", input.Record, snapshot.Len())
	}
	if input.Record.RootKey != RootKeyForTenant(tenant.ID) {
		t.Fatalf("root key = %q, want %q", input.Record.RootKey, RootKeyForTenant(tenant.ID))
	}
	for _, object := range input.Objects {
		if object.Record.Kind == catalogproto.ObjectKindDirectory {
			if object.Record.ContentRevision != 0 || object.Content != nil {
				t.Fatalf("directory carries content: %+v", object.Record)
			}
			continue
		}
		if object.Record.ContentRevision != 7 {
			t.Fatalf("content revision = %d, want 7", object.Record.ContentRevision)
		}
		if object.Record.Kind == catalogproto.ObjectKindFile {
			body, err := io.ReadAll(object.Content)
			if err != nil || uint64(len(body)) != object.Record.Size {
				t.Fatalf("read %s: bytes=%d size=%d err=%v", object.Record.SourceKey, len(body), object.Record.Size, err)
			}
		}
	}
}

func TestAuthoritySnapshotStreamsVerifiedAttachment(t *testing.T) {
	source := authorityStore(t)
	note := createSnapshot(t, source, model.CreateNote{Nonce: model.NewNonce(), Title: "Evidence", Body: "body"}).(model.Note)
	body := []byte(strings.Repeat("attachment-content-", 256))
	attachmentPath := filepath.Join(t.TempDir(), "evidence.bin")
	if err := os.WriteFile(attachmentPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, _, err := source.AttachFile(t.Context(), attachmentPath)
	if err != nil {
		t.Fatalf("AttachFile: %v", err)
	}
	if _, err := source.Append(t.Context(), refs.For(model.KindNote, note.ID), []model.Op{model.AddAttachment(attachment)}); err != nil {
		t.Fatalf("append attachment: %v", err)
	}
	snapshot, err := BuildAuthoritySnapshot(t.Context(), source)
	if err != nil {
		t.Fatalf("BuildAuthoritySnapshot: %v", err)
	}
	key := "attachment:" + string(note.ID) + ":" + attachment.Name
	object := indexAuthorityObjects(snapshot.objects)[key]
	if object.content != nil || object.contentPath == "" || object.size != uint64(len(body)) || object.hash != attachment.OID {
		t.Fatalf("attachment object = %+v", object)
	}
	input, closers, err := sourceInput(testTenant(t), 3, []authorityObject{object}, nil)
	if err != nil {
		t.Fatalf("sourceInput: %v", err)
	}
	defer closeAll(closers)
	got, err := io.ReadAll(input.Objects[0].Content)
	if err != nil {
		t.Fatalf("read attachment stream: %v", err)
	}
	if string(got) != string(body) || input.Objects[0].Record.Hash != attachment.OID {
		t.Fatalf("streamed bytes/hash mismatch: bytes=%d hash=%q", len(got), input.Objects[0].Record.Hash)
	}
}

func TestAuthorityDeltaPreservesIdentityAcrossRenameAndSortsDeletes(t *testing.T) {
	source := authorityStore(t)
	nonce := model.NewNonce()
	note := createSnapshot(t, source, model.CreateNote{Nonce: nonce, Title: "Before", Body: "body"}).(model.Note)
	before, err := BuildAuthoritySnapshot(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := source.Append(t.Context(), refs.For(model.KindNote, note.ID), []model.Op{model.SetTitle{Title: "After"}})
	if err != nil {
		t.Fatal(err)
	}
	after, err := BuildAuthoritySnapshot(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	delta := DiffAuthoritySnapshots(before, after)
	if delta.Len() != 1 || len(delta.objects) != 1 || len(delta.deletes) != 0 {
		t.Fatalf("rename delta = upserts=%+v deletes=%+v", delta.objects, delta.deletes)
	}
	wantKey := "entity:note:" + nonce
	if delta.objects[0].key != wantKey || delta.objects[0].name != Filename(updated) {
		t.Fatalf("rename upsert = %+v, want key %q name %q", delta.objects[0], wantKey, Filename(updated))
	}

	removed := AuthoritySnapshot{objects: []authorityObject{{key: "z", name: "z"}, {key: "a", name: "a"}}}
	deletes := DiffAuthoritySnapshots(removed, AuthoritySnapshot{})
	if len(deletes.deletes) != 2 || deletes.deletes[0].SourceKey != "a" || deletes.deletes[1].SourceKey != "z" {
		t.Fatalf("sorted deletes = %+v", deletes.deletes)
	}
	keys := affectedKeys([]authorityObject{{key: "z"}, {key: "a"}}, []catalogproto.SourceDeleteRecord{{SourceKey: "z"}, {SourceKey: "b"}})
	if strings.Join(keys, ",") != "a,b,z" {
		t.Fatalf("affected keys = %v", keys)
	}
}

func TestTenantDefinitionAndPrepareRequestAreDerivedFromPlanAndCommit(t *testing.T) {
	plan := testPlan(t)
	tenant := testTenant(t)
	if err := tenant.Validate(plan); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	definition := tenantDefinition(plan, tenant)
	if definition.PresentationRoot != filepath.Join(plan.Paths().PresentationRoot, tenant.RouteName) ||
		definition.BackingRoot != tenant.RepoRoot || definition.ContentSourceID != string(tenant.Authority) ||
		definition.Generation != uint64(tenant.Generation) || definition.AccessMode != mountproto.AccessModeReadWrite {
		t.Fatalf("definition = %+v", definition)
	}
	response := catalogproto.SourceReconcileResponse{
		SourceAuthority: tenant.Authority, SourceRevision: 9,
		ChangeID: "11111111111111111111111111111111", OperationID: "22222222222222222222222222222222",
		Commits: []catalogproto.SourceCommit{{TenantID: catalogproto.TenantID(tenant.ID), CatalogRevision: 12}},
	}
	request := prepareRequest(tenant, response)
	if request.CatalogRevision != 12 || request.SourceRevision != 9 || request.DomainID != tenant.Domain ||
		request.ChangeID != response.ChangeID || request.OperationID != response.OperationID {
		t.Fatalf("prepare request = %+v", request)
	}
}

func authorityStore(t *testing.T) *store.Store {
	t.Helper()
	dir := gittest.InitRepo(t)
	source, err := store.Open(dir)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	return source
}

func createSnapshot(t *testing.T, source *store.Store, operation model.Op) model.Snapshot {
	t.Helper()
	snapshot, err := source.Create(t.Context(), []model.Op{operation})
	if err != nil {
		t.Fatalf("create %s: %v", operation.OpKind(), err)
	}
	return snapshot
}

func indexAuthorityObjects(objects []authorityObject) map[string]authorityObject {
	result := make(map[string]authorityObject, len(objects))
	for _, object := range objects {
		result[object.key] = object
	}
	return result
}

func testPlan(t *testing.T) holder.Plan {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	plan, err := NewHolderPlan(holder.SignedApplication{
		AppPath: "/Applications/CCNotesTest.app", BundleID: "com.example.cc-notes-test",
		TeamID: "ABCDE12345", ExecutableName: "CCNotesTest", SigningIdentifier: "com.example.cc-notes-test",
	}, filepath.Join(home, ".ccn-test"))
	if err != nil {
		t.Fatalf("NewHolderPlan: %v", err)
	}
	return plan
}

func testTenant(t *testing.T) Tenant {
	t.Helper()
	id, err := catalog.NewTenantID("cc-notes-test")
	if err != nil {
		t.Fatal(err)
	}
	return Tenant{
		ID: id, Generation: 3, Authority: AuthorityForTenant(id), Domain: "mount:cc-notes-test",
		RouteName: "repo", RepoRoot: filepath.Join(t.TempDir(), "repo"),
	}
}
