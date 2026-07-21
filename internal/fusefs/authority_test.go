package fusefs

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/sourcedriver"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

func TestAuthoritySnapshotRendersStableRootCommitIdentities(t *testing.T) {
	source := authorityStore(t)
	note := createSnapshot(t, source, model.CreateNote{
		Nonce: model.NewNonce(), Title: "Durable note", Body: "body",
	}).(model.Note)
	project := createSnapshot(t, source, model.CreateProject{
		Nonce: model.NewNonce(), Title: "Project",
	}).(model.Project)
	sprint := createSnapshot(t, source, model.CreateSprint{
		Nonce: model.NewNonce(), Title: "Sprint", Project: project.ID,
	}).(model.Sprint)
	task := createSnapshot(t, source, model.CreateTask{
		Nonce: model.NewNonce(), Title: "Task", Type: model.TypeTask, Branch: "main",
	}).(model.Task)
	updated, err := source.Append(t.Context(), refs.For(model.KindTask, task.ID), []model.Op{
		model.SetSprint{Sprint: sprint.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	task = updated.(model.Task)

	snapshot, err := BuildAuthoritySnapshot(t.Context(), source)
	if err != nil {
		t.Fatalf("BuildAuthoritySnapshot: %v", err)
	}
	objects := indexAuthorityObjects(snapshot.objects)
	noteKey := sourceKeyForEntity(t, source, model.KindNote, note.ID)
	noteObject := objects[noteKey]
	if noteObject.name != Filename(note) || !strings.Contains(string(noteObject.content), "Durable note") ||
		noteObject.kind != catalog.KindFile || noteObject.size != int64(len(noteObject.content)) ||
		noteObject.hash == (catalog.ContentHash{}) {
		t.Fatalf("note object = %+v content=%q", noteObject, noteObject.content)
	}
	for _, key := range []string{
		"sprint-task:" + string(sprint.ID) + ":" + string(task.ID),
		"project-sprint-task:" + string(project.ID) + ":" + string(sprint.ID) + ":" + string(task.ID),
		"project-task:" + string(project.ID) + ":" + string(task.ID),
	} {
		object, found := objects[key]
		if !found || object.kind != catalog.KindSymlink || object.linkTarget == "" {
			t.Fatalf("symlink %q = %+v, found=%v", key, object, found)
		}
	}
	cached := cacheSnapshotForTest(snapshot)
	projections, err := snapshotValues(
		cached, []Tenant{testTenant(t)}, sourcedriver.RevisionToken(strings.Repeat("a", 40)), 0, cachedSnapshotCount(cached, 1),
	)
	if err != nil {
		t.Fatalf("projections: %v", err)
	}
	if len(projections) != snapshot.Len() {
		t.Fatalf("projection count = %d, want %d", len(projections), snapshot.Len())
	}
}

func TestAuthoritySnapshotKeepsAttachmentBodiesOutOfMetadata(t *testing.T) {
	source := authorityStore(t)
	note := createSnapshot(t, source, model.CreateNote{
		Nonce: model.NewNonce(), Title: "Evidence", Body: "body",
	}).(model.Note)
	body := []byte(strings.Repeat("attachment-content-", 256))
	attachmentPath := filepath.Join(t.TempDir(), "evidence.bin")
	if err := os.WriteFile(attachmentPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	attachment, _, err := source.AttachFile(t.Context(), attachmentPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Append(t.Context(), refs.For(model.KindNote, note.ID), []model.Op{
		model.AddAttachment(attachment),
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := BuildAuthoritySnapshot(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	key := attachmentKey(note.ID, attachment.Name)
	object := indexAuthorityObjects(snapshot.objects)[key]
	wantHash, err := contentHash(attachment.OID)
	if err != nil {
		t.Fatal(err)
	}
	if object.content != nil || object.attachment != attachment.OID || object.size != int64(len(body)) || object.hash != wantHash {
		t.Fatalf("attachment object = %+v", object)
	}
}

func TestAttachmentIdentityIsBoundedForMaximumName(t *testing.T) {
	id := model.EntityID(strings.Repeat("a", 64))
	name := strings.Repeat("x", 255)
	key := attachmentKey(id, name)
	if len(key) > 255 || strings.ContainsAny(key, "/\x00") {
		t.Fatalf("attachment key is not catalog-safe: length=%d key=%q", len(key), key)
	}
	if key == attachmentKey(id, strings.Repeat("y", 255)) {
		t.Fatal("distinct attachment names share an identity")
	}
}

func TestAuthorityDeltaPreservesRootIdentityAcrossRevision(t *testing.T) {
	source := authorityStore(t)
	note := createSnapshot(t, source, model.CreateNote{
		Nonce: model.NewNonce(), Title: "Before", Body: "body",
	}).(model.Note)
	key := sourceKeyForEntity(t, source, model.KindNote, note.ID)
	before, err := BuildAuthoritySnapshot(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := source.Append(t.Context(), refs.For(model.KindNote, note.ID), []model.Op{
		model.SetTitle{Title: "After"},
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := BuildAuthoritySnapshot(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	delta := DiffAuthoritySnapshots(before, after)
	if delta.Len() != 1 || len(delta.objects) != 1 || len(delta.deletes) != 0 ||
		delta.objects[0].key != key || delta.objects[0].name != Filename(updated) {
		t.Fatalf("revision delta = objects=%+v deletes=%+v", delta.objects, delta.deletes)
	}

	removed := AuthoritySnapshot{objects: []authorityObject{{key: "z", name: "z"}, {key: "a", name: "a"}}}
	deletes := DiffAuthoritySnapshots(removed, AuthoritySnapshot{})
	if len(deletes.deletes) != 2 || deletes.deletes[0] != "a" || deletes.deletes[1] != "z" {
		t.Fatalf("sorted deletes = %+v", deletes.deletes)
	}
}

func TestAuthorityProjectionPagesFenceAndOrderEveryDeclaredTenant(t *testing.T) {
	source := authorityStore(t)
	note := createSnapshot(t, source, model.CreateNote{
		Nonce: model.NewNonce(), Title: "Before", Body: "body",
	}).(model.Note)
	before, err := BuildAuthoritySnapshot(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Append(t.Context(), refs.For(model.KindNote, note.ID), []model.Op{
		model.SetTitle{Title: "After"},
	}); err != nil {
		t.Fatal(err)
	}
	after, err := BuildAuthoritySnapshot(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	first := testTenant(t)
	secondID, err := catalog.NewTenantID("cc-notes-second")
	if err != nil {
		t.Fatal(err)
	}
	second := first
	second.ID = secondID
	second.Generation = 7
	second.RouteName = "second"
	revision := sourcedriver.RevisionToken(strings.Repeat("a", 40))
	tenants := []Tenant{first, second}
	slices.SortFunc(tenants, func(a, b Tenant) int { return strings.Compare(string(a.ID), string(b.ID)) })
	cachedAfter := cacheSnapshotForTest(after)
	projections, err := snapshotValues(
		cachedAfter, tenants, revision, 0, cachedSnapshotCount(cachedAfter, len(tenants)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(projections) != after.Len()*2 {
		t.Fatalf("projection count = %d, want %d", len(projections), after.Len()*2)
	}
	for index, projection := range projections {
		if index > 0 && compareProjection(projections[index-1], projection) >= 0 {
			t.Fatalf("projections are not globally tuple ordered: %+v", projections)
		}
		if projection.Kind == catalog.KindFile &&
			(projection.Content == nil || projection.Content.Tenant != projection.Tenant ||
				projection.Content.Generation != projection.Generation) {
			t.Fatalf("content fence differs from projection: %+v", projection)
		}
	}

	cachedDelta := cacheDeltaForTest(DiffAuthoritySnapshots(before, after))
	changes, err := changeValues(
		cachedDelta, tenants, revision, 0, cachedDeltaCount(cachedDelta, len(tenants)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(changes) != 2 {
		t.Fatalf("change count = %d, want one per tenant", len(changes))
	}
	for index, change := range changes {
		if change.Sequence == 0 || change.Tenant == "" || change.Generation == 0 {
			t.Fatalf("change fence is incomplete: %+v", change)
		}
		if index > 0 && compareChange(changes[index-1], change) >= 0 {
			t.Fatalf("changes are not globally tuple ordered: %+v", changes)
		}
	}
	if changes[0].ID != changes[1].ID || changes[0].Tenant == changes[1].Tenant {
		t.Fatalf("same logical change was not projected independently: %+v", changes)
	}
}

func TestTenantValidationUsesOnlyImmutableFuseKitIdentity(t *testing.T) {
	tenant := testTenant(t)
	if err := tenant.Validate(); err != nil {
		t.Fatal(err)
	}
	shared := tenant
	shared.Authority = "cc-notes:shared-repository"
	if err := shared.Validate(); err != nil {
		t.Fatalf("tenant rejected shared source authority: %v", err)
	}
	invalid := tenant
	invalid.Authority = ""
	if err := invalid.Validate(); err == nil {
		t.Fatal("tenant accepted invalid source authority")
	}
	invalid = tenant
	invalid.RepoRoot = "relative"
	if err := invalid.Validate(); err == nil {
		t.Fatal("tenant accepted relative repository root")
	}
	for _, test := range []struct {
		name   string
		mutate func(*Tenant)
	}{
		{name: "overlong tenant", mutate: func(value *Tenant) {
			value.ID = catalog.TenantID(strings.Repeat("x", 256))
		}},
		{name: "derived root separator", mutate: func(value *Tenant) { value.ID = "tenant/child" }},
		{name: "route control", mutate: func(value *Tenant) { value.RouteName = "route\nname" }},
		{name: "overlong route", mutate: func(value *Tenant) { value.RouteName = strings.Repeat("r", 256) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := tenant
			test.mutate(&value)
			if err := value.Validate(); err == nil {
				t.Fatalf("invalid tenant accepted: %+v", value)
			}
		})
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

func sourceKeyForEntity(t *testing.T, source *store.Store, kind model.Kind, id model.EntityID) string {
	t.Helper()
	tip, err := source.Repo.Tip(t.Context(), refs.For(kind, id))
	if err != nil {
		t.Fatal(err)
	}
	rooted, err := source.LoadRootedAt(t.Context(), tip)
	if err != nil {
		t.Fatal(err)
	}
	key, err := entitySourceKey(kind, rooted.Root)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func indexAuthorityObjects(objects []authorityObject) map[string]authorityObject {
	result := make(map[string]authorityObject, len(objects))
	for _, object := range objects {
		result[object.key] = object
	}
	return result
}

func cacheSnapshotForTest(snapshot AuthoritySnapshot) *cachedAuthoritySnapshot {
	objects := slices.Clone(snapshot.objects)
	slices.SortFunc(objects, func(a, b authorityObject) int { return strings.Compare(a.key, b.key) })
	return &cachedAuthoritySnapshot{objects: objects, byID: indexAuthorityObjects(objects)}
}

func cacheDeltaForTest(delta AuthorityDelta) *cachedAuthorityDelta {
	values := make([]authorityDeltaValue, 0, delta.Len())
	for _, object := range delta.objects {
		value := object
		values = append(values, authorityDeltaValue{id: sourcedriver.LogicalID(value.key), object: &value})
	}
	for _, id := range delta.deletes {
		values = append(values, authorityDeltaValue{id: id})
	}
	slices.SortFunc(values, func(a, b authorityDeltaValue) int { return stringCompare(a.id, b.id) })
	return &cachedAuthorityDelta{values: values}
}

func cachedSnapshotCount(snapshot *cachedAuthoritySnapshot, tenants int) int {
	return len(snapshot.objects) * tenants
}

func cachedDeltaCount(delta *cachedAuthorityDelta, tenants int) int {
	return len(delta.values) * tenants
}

func testTenant(t *testing.T) Tenant {
	t.Helper()
	id, err := catalog.NewTenantID("cc-notes-test")
	if err != nil {
		t.Fatal(err)
	}
	return Tenant{
		ID: id, Generation: 3, Authority: AuthorityForTenant(id),
		RouteName: "repo", RepoRoot: filepath.Join(t.TempDir(), "repo"),
	}
}
