package sourceview

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"slices"
	"strings"
	"unicode"

	"github.com/yasyf/cc-notes/model"
)

// RenderPolicy identifies the byte policy used by immutable entity descriptors.
const RenderPolicy = "cc-notes-sourceview-v1"

// ObjectKind distinguishes catalog entries without a platform dependency.
type ObjectKind uint8

const (
	// Directory represents a catalog directory without content.
	Directory ObjectKind = iota + 1
	// File represents rendered entity bytes or one immutable LFS object.
	File
	// Symlink represents the existing relative browse target.
	Symlink
)

// Object captures one immutable logical entry and its content acquisition source.
type Object struct {
	Key          string
	Parent       string
	Name         string
	Kind         ObjectKind
	Mode         uint32
	Size         int64
	Hash         [sha256.Size]byte
	LinkTarget   string
	EntityRef    string
	EntityTip    model.SHA
	Attachment   string
	RenderPolicy string
}

// Entity is the bytes-independent projection and dependency summary of one tip.
type Entity struct {
	Ref             string
	Tip             model.SHA
	Root            model.SHA
	Kind            model.Kind
	ID              model.EntityID
	Visible         bool
	Deleted         bool
	SkippedOps      int
	Filename        string
	Mode            uint32
	Size            int64
	Hash            [sha256.Size]byte
	Sprint          model.EntityID
	Project         model.EntityID
	Attachments     []model.Attachment
	Objects         []Object
	ProjectionError string
}

// ProjectionReader resolves only requested entities and reverse membership groups.
type ProjectionReader interface {
	Entity(kind model.Kind, id model.EntityID) (Entity, bool, error)
	Members(prefix string) ([]Entity, error)
}

// Describe freezes the existing codec's rendered identity and dependency attributes.
func Describe(ref string, tip model.SHA, root model.PackCommit, snapshot model.Snapshot) (Entity, error) {
	kind := snapshot.Meta().Kind
	if _, err := EntitySourceKey(kind, root); err != nil {
		return Entity{}, err
	}
	codec := CodecOf(kind)
	body := codec.Render(snapshot)
	value := Entity{Ref: ref, Tip: tip, Root: root.SHA, Kind: kind, ID: snapshot.EntityID(), Visible: !snapshot.Meta().Deleted && !snapshot.Meta().Superseded, Deleted: snapshot.Meta().Deleted, SkippedOps: snapshot.Meta().SkippedOps, Filename: Filename(snapshot), Mode: 0o644, Size: int64(len(body)), Hash: sha256.Sum256(body), Attachments: slices.Clone(snapshot.Meta().Attachments)}
	if codec.ReadOnly() {
		value.Mode = 0o444
	}
	switch typed := snapshot.(type) {
	case model.Task:
		value.Sprint, value.Project = typed.Sprint, typed.Project
	case model.Sprint:
		value.Project = typed.Project
	}
	return value, nil
}

// BaseObjects defines the stable kind roots and attachment root.
func BaseObjects() []Object {
	result := make([]Object, 0, len(model.Kinds())+1)
	for _, kind := range model.Kinds() {
		layout, _ := LayoutOf(kind)
		result = append(result, directory("kind:"+string(kind), "", strings.TrimPrefix(layout.Dir, "/")))
	}
	return append(result, directory("attachments", "", "attachments"))
}

// Dependencies includes visible children even when their referenced parent is absent.
func Dependencies(entity Entity) []string {
	if !entity.Visible {
		return nil
	}
	var result []string
	switch entity.Kind {
	case model.KindTask:
		if entity.Sprint != "" {
			result = append(result, "task-sprint/"+string(entity.Sprint)+"/"+entity.Ref)
		}
		if entity.Project != "" {
			result = append(result, "task-project/"+string(entity.Project)+"/"+entity.Ref)
		}
	case model.KindSprint:
		if entity.Project != "" {
			result = append(result, "sprint-project/"+string(entity.Project)+"/"+entity.Ref)
		}
	}
	if attachmentKind(entity.Kind) && len(entity.Attachments) > 0 {
		result = append(result, "attachment-short/"+entity.ID.Short()+"/"+entity.Ref)
	}
	return result
}

// Emit emits only an entity's owned entries using its actual projection dependencies.
func Emit(entity Entity, reader ProjectionReader) ([]Object, error) {
	if !entity.Visible {
		return nil, nil
	}
	parent := "kind:" + string(entity.Kind)
	result := []Object{{Key: "entity:" + string(entity.Kind) + ":" + string(entity.Root), Parent: parent, Name: entity.Filename, Kind: File, Mode: entity.Mode, Size: entity.Size, Hash: entity.Hash, EntityRef: entity.Ref, EntityTip: entity.Tip, RenderPolicy: RenderPolicy}}
	if CodecOf(entity.Kind).Browsable() {
		result = append(result, directory(browseKey(entity.Kind, entity.ID), parent, entity.ID.Short()))
	}
	switch entity.Kind {
	case model.KindTask:
		emitted, err := taskLinks(entity, reader)
		if err != nil {
			return nil, err
		}
		result = append(result, emitted...)
	case model.KindSprint:
		base := browseKey(entity.Kind, entity.ID)
		result = append(result, directory(base+":tasks", base, "tasks"))
		project, found, err := reader.Entity(model.KindProject, entity.Project)
		if err != nil {
			return nil, err
		}
		if found && project.Visible {
			projectBase := browseKey(model.KindProject, project.ID)
			key := projectBase + ":sprint:" + string(entity.ID)
			result = append(result, directory(key, projectBase+":sprints", entity.ID.Short()), directory(key+":tasks", key, "tasks"))
		}
	case model.KindProject:
		base := browseKey(entity.Kind, entity.ID)
		result = append(result, directory(base+":sprints", base, "sprints"), directory(base+":tasks", base, "tasks"))
	}
	if attachmentKind(entity.Kind) && len(entity.Attachments) > 0 {
		members, err := reader.Members("attachment-short/" + entity.ID.Short() + "/")
		if err != nil {
			return nil, err
		}
		if len(members) == 1 {
			parent := "attachments:" + string(entity.ID)
			result = append(result, directory(parent, "attachments", entity.ID.Short()))
			for _, attachment := range entity.Attachments {
				raw, err := hex.DecodeString(attachment.OID)
				if err != nil || len(raw) != sha256.Size {
					return nil, fmt.Errorf("attachment %s/%s: content hash is not canonical SHA-256", entity.ID, attachment.Name)
				}
				if attachment.Size < 0 {
					return nil, fmt.Errorf("attachment %s/%s has negative size", entity.ID, attachment.Name)
				}
				digest := sha256.Sum256([]byte(attachment.Name))
				var hash [sha256.Size]byte
				copy(hash[:], raw)
				result = append(result, Object{Key: "attachment:" + string(entity.ID) + ":" + hex.EncodeToString(digest[:]), Parent: parent, Name: attachment.Name, Kind: File, Mode: 0o444, Size: attachment.Size, Hash: hash, Attachment: attachment.OID})
			}
		}
	}
	return result, nil
}

func taskLinks(task Entity, reader ProjectionReader) ([]Object, error) {
	var result []Object
	projects := make(map[model.EntityID]struct{}, 2)
	sprint, found, err := reader.Entity(model.KindSprint, task.Sprint)
	if err != nil {
		return nil, err
	}
	if found && sprint.Visible {
		linkPath := path.Join("/sprints", sprint.ID.Short(), "tasks", task.Filename)
		result = append(result, link("sprint-task:"+string(sprint.ID)+":"+string(task.ID), browseKey(model.KindSprint, sprint.ID)+":tasks", task.Filename, SymlinkTarget(linkPath, "tasks/"+task.Filename)))
		projects[sprint.Project] = struct{}{}
		project, live, err := reader.Entity(model.KindProject, sprint.Project)
		if err != nil {
			return nil, err
		}
		if live && project.Visible {
			linkPath := path.Join("/projects", project.ID.Short(), "sprints", sprint.ID.Short(), "tasks", task.Filename)
			key := "project-sprint-task:" + string(project.ID) + ":" + string(sprint.ID) + ":" + string(task.ID)
			result = append(result, link(key, browseKey(model.KindProject, project.ID)+":sprint:"+string(sprint.ID)+":tasks", task.Filename, SymlinkTarget(linkPath, "tasks/"+task.Filename)))
		}
	}
	projects[task.Project] = struct{}{}
	ids := make([]model.EntityID, 0, len(projects))
	for id := range projects {
		if id != "" {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		project, found, err := reader.Entity(model.KindProject, id)
		if err != nil {
			return nil, err
		}
		if !found || !project.Visible {
			continue
		}
		linkPath := path.Join("/projects", id.Short(), "tasks", task.Filename)
		result = append(result, link("project-task:"+string(id)+":"+string(task.ID), browseKey(model.KindProject, id)+":tasks", task.Filename, SymlinkTarget(linkPath, "tasks/"+task.Filename)))
	}
	return result, nil
}

// ValidateObject checks the namespace boundary independently from publication policy.
func ValidateObject(object Object) error {
	validKey := func(value string) bool {
		return value != "" && len(value) <= 255 && !strings.ContainsAny(value, "/\\") && strings.IndexFunc(value, unicode.IsControl) < 0
	}
	if !validKey(object.Key) || object.Name == "" || object.Name == "." || object.Name == ".." || strings.ContainsAny(object.Name, "/\x00") || object.Parent != "" && !validKey(object.Parent) {
		return fmt.Errorf("invalid object %q/%q", object.Key, object.Name)
	}
	if object.Kind != Directory && object.Kind != File && object.Kind != Symlink {
		return fmt.Errorf("invalid object kind for %q", object.Key)
	}
	if object.Size < 0 {
		return fmt.Errorf("negative object size for %q", object.Key)
	}
	if object.Attachment != "" {
		raw, err := hex.DecodeString(object.Attachment)
		if !model.ValidAttachmentOID(object.Attachment) || err != nil || len(raw) != sha256.Size || object.Kind != File || object.Size < 0 {
			return fmt.Errorf("invalid attachment descriptor %q", object.Key)
		}
		var digest [sha256.Size]byte
		copy(digest[:], raw)
		if digest != object.Hash {
			return fmt.Errorf("attachment digest differs for %q", object.Key)
		}
	}
	return nil
}

func directory(key, parent, name string) Object {
	return Object{Key: key, Parent: parent, Name: name, Kind: Directory, Mode: 0o755}
}

func link(key, parent, name, target string) Object {
	return Object{Key: key, Parent: parent, Name: name, Kind: Symlink, Mode: 0o777, LinkTarget: target}
}

func browseKey(kind model.Kind, id model.EntityID) string {
	return "browse:" + string(kind) + ":" + string(id)
}

func attachmentKind(kind model.Kind) bool {
	return kind == model.KindNote || kind == model.KindDoc || kind == model.KindLog || kind == model.KindInvestigation || kind == model.KindAnswer
}
