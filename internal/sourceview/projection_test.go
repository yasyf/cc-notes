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
	task := Entity{Ref: "task-ref", Tip: model.SHA(strings.Repeat("a", 40)), Root: model.SHA(taskID), Kind: model.KindTask, ID: taskID, Visible: true, Filename: string(taskID) + ".json", Mode: 0o644, Sprint: sprintID, Project: projectID}
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

func TestAttachmentFullIDsKeepShortPrefixCollisionOwners(t *testing.T) {
	ids := []model.EntityID{model.EntityID("abcdef0" + strings.Repeat("1", 33)), model.EntityID("abcdef0" + strings.Repeat("2", 33))}
	oid := fmt.Sprintf("%x", sha256.Sum256([]byte("binary")))
	fixture := projectionFixture{members: make(map[string][]Entity)}
	for _, id := range ids {
		entity := Entity{Ref: string(id), Tip: model.SHA(id), Root: model.SHA(id), Kind: model.KindNote, ID: id, Visible: true, Filename: string(id) + "-note.md", Mode: 0o644, Attachments: []model.Attachment{{Name: "proof.bin", OID: oid, Size: 6}}}
		fixture.members["attachment-owner/"+string(id)+"/"] = []Entity{entity}
		objects, err := Emit(entity, &fixture)
		if err != nil || len(objects) != 3 || objects[1].Key != "attachments:"+string(id) || objects[1].Name != string(id) || objects[2].Attachment != oid || objects[2].Size != 6 || objects[2].Mode != 0o444 {
			t.Fatalf("full ID attachment projection=%+v err=%v", objects, err)
		}
	}
	if !slices.Equal(fixture.reads, []string{"attachment-owner/" + string(ids[0]) + "/", "attachment-owner/" + string(ids[1]) + "/"}) {
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
	if entity.Hash != sha256.Sum256(body) || entity.Size != int64(len(body)) || entity.Filename != string(id)+"-codec-bytes.md" || entity.SkippedOps != 3 {
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
