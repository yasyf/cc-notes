package sourceview

import (
	"crypto/sha256"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

type projectionFixture struct {
	entities map[string]Entity
	members  map[string][]Entity
	reads    []string
}

func (f *projectionFixture) Entity(kind model.Kind, id model.EntityID) (Entity, bool, error) {
	key := string(kind) + ":" + string(id)
	f.reads = append(f.reads, key)
	entity, found := f.entities[key]
	return entity, found, nil
}

func (f *projectionFixture) Members(prefix string) ([]Entity, error) {
	f.reads = append(f.reads, prefix)
	return f.members[prefix], nil
}

func TestTaskProjectionKeepsDirectAndSprintProjectUnion(t *testing.T) {
	projectID := model.EntityID(strings.Repeat("1", 40))
	sprintID := model.EntityID(strings.Repeat("2", 40))
	taskID := model.EntityID(strings.Repeat("3", 40))
	task := Entity{Ref: "task-ref", Tip: model.SHA(strings.Repeat("a", 40)), Root: model.SHA(taskID), Kind: model.KindTask, ID: taskID, Visible: true, Filename: taskID.Short() + ".json", Mode: 0o644, Sprint: sprintID, Project: projectID}
	fixture := projectionFixture{entities: map[string]Entity{"project:" + string(projectID): {Kind: model.KindProject, ID: projectID, Visible: true}, "sprint:" + string(sprintID): {Kind: model.KindSprint, ID: sprintID, Project: projectID, Visible: true}}}
	objects, err := Emit(task, &fixture)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]Object, len(objects))
	for _, object := range objects {
		byID[object.Key] = object
	}
	if len(byID) != 4 {
		t.Fatalf("objects=%+v want flat and three links", objects)
	}
	want := map[string]string{
		"sprint-task:" + string(sprintID) + ":" + string(taskID):                                   "../../../tasks/" + task.Filename,
		"project-task:" + string(projectID) + ":" + string(taskID):                                 "../../../tasks/" + task.Filename,
		"project-sprint-task:" + string(projectID) + ":" + string(sprintID) + ":" + string(taskID): "../../../../../tasks/" + task.Filename,
	}
	for key, target := range want {
		object := byID[key]
		if object.Kind != Symlink || object.Mode != 0o777 || object.LinkTarget != target {
			t.Fatalf("%s=%+v want target %s", key, object, target)
		}
	}
	task.Sprint = ""
	objects, err = Emit(task, &fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 2 || objects[1].Key != "project-task:"+string(projectID)+":"+string(taskID) {
		t.Fatalf("direct reason after sprint removal=%+v", objects)
	}
	task.Sprint = sprintID
	task.Project = ""
	objects, err = Emit(task, &fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 4 {
		t.Fatalf("sprint reason after direct removal=%+v", objects)
	}
}

func TestAttachmentShortGroupSuppressesAndRestoresExactOwner(t *testing.T) {
	id := model.EntityID("abcdef0" + strings.Repeat("1", 33))
	other := Entity{Ref: "other", Kind: model.KindDoc, ID: model.EntityID("abcdef0" + strings.Repeat("2", 33)), Visible: true}
	oid := fmt.Sprintf("%x", sha256.Sum256([]byte("binary")))
	entity := Entity{Ref: "note-ref", Tip: model.SHA(strings.Repeat("a", 40)), Root: model.SHA(id), Kind: model.KindNote, ID: id, Visible: true, Filename: "abcdef0-note.md", Mode: 0o644, Attachments: []model.Attachment{{Name: "proof.bin", OID: oid, Size: 6}}}
	prefix := "attachment-short/abcdef0/"
	fixture := projectionFixture{members: map[string][]Entity{prefix: {entity}}}
	objects, err := Emit(entity, &fixture)
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 3 || objects[1].Key != "attachments:"+string(id) || objects[1].Name != "abcdef0" || objects[2].Attachment != oid || objects[2].Size != 6 || objects[2].Mode != 0o444 {
		t.Fatalf("unique attachment projection=%+v", objects)
	}
	fixture.members[prefix] = []Entity{entity, other}
	suppressed, err := Emit(entity, &fixture)
	if err != nil || len(suppressed) != 1 {
		t.Fatalf("colliding projection=%+v err=%v", suppressed, err)
	}
	fixture.members[prefix] = []Entity{entity}
	restored, err := Emit(entity, &fixture)
	if err != nil || !slices.Equal(restored, objects) {
		t.Fatalf("restored projection=%+v err=%v want %+v", restored, err, objects)
	}
	if !slices.Equal(fixture.reads, []string{prefix, prefix, prefix}) {
		t.Fatalf("attachment dependency reads=%v", fixture.reads)
	}
}

func TestDependenciesRetainVisibleChildrenOfAbsentParents(t *testing.T) {
	task := Entity{Ref: "task-ref", Kind: model.KindTask, ID: model.EntityID(strings.Repeat("1", 40)), Visible: true, Sprint: "missing-sprint", Project: "missing-project"}
	want := []string{"task-sprint/missing-sprint/task-ref", "task-project/missing-project/task-ref"}
	if got := Dependencies(task); !slices.Equal(got, want) {
		t.Fatalf("dependencies=%v want %v", got, want)
	}
	task.Visible = false
	if got := Dependencies(task); len(got) != 0 {
		t.Fatalf("invisible dependencies=%v", got)
	}
}

func TestDescribePinsExistingCodecBytesAndSkippedOps(t *testing.T) {
	id := model.EntityID(strings.Repeat("a", 40))
	root := model.PackCommit{SHA: model.SHA(id)}
	snapshot := model.Note{ID: id, Title: "Codec bytes", Body: "body\n", SkippedOps: 3}
	entity, err := Describe("note-ref", model.SHA(strings.Repeat("b", 40)), root, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	body := RenderNote(snapshot)
	if entity.Hash != sha256.Sum256(body) || entity.Size != int64(len(body)) || entity.Filename != "aaaaaaa-codec-bytes.md" || entity.SkippedOps != 3 {
		t.Fatalf("descriptor=%+v bytes=%q", entity, body)
	}
	fixture := projectionFixture{}
	objects, err := Emit(entity, &fixture)
	if err != nil || len(objects) != 1 {
		t.Fatalf("flat objects=%+v err=%v", objects, err)
	}
	object := objects[0]
	if object.Key != "entity:note:"+string(id) || object.EntityTip != entity.Tip || object.RenderPolicy != RenderPolicy || object.Hash != entity.Hash {
		t.Fatalf("flat object=%+v", object)
	}
}

func TestZeroSizeAttachmentDescriptorMatchesExistingProjection(t *testing.T) {
	hash := sha256.Sum256(nil)
	object := Object{Key: "attachment:empty", Parent: "attachments:entity", Name: "empty.bin", Kind: File, Mode: 0o444, Attachment: fmt.Sprintf("%x", hash), Hash: hash, Size: 0}
	if err := ValidateObject(object); err != nil {
		t.Fatalf("empty attachment projection=%v", err)
	}
	object.Size = -1
	if err := ValidateObject(object); err == nil {
		t.Fatal("negative attachment size accepted")
	}
}
