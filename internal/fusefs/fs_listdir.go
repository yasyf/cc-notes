//go:build fuse

package fusefs

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/winfsp/cgofuse/fuse"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// listDir synthesizes dir p's entries: live entities plus in-memory scratch
// files, sorted and deduplicated.
func (f *FS) listDir(p string) ([]string, int) {
	node, err := ParsePath(p)
	if err != nil {
		return nil, -fuse.ENOENT
	}
	names := map[string]bool{}
	switch n := node.(type) {
	case Root:
		for _, layout := range layouts {
			names[strings.TrimPrefix(layout.dir, "/")] = true
		}
		names["attachments"] = true
	case KindDir:
		snaps, err := f.store.ListSnapshots(f.ctx, n.Kind, store.ListOpts{})
		if err != nil {
			return nil, errno(err)
		}
		browsable := codecOf(n.Kind).Browsable()
		for _, snap := range snaps {
			names[Filename(snap)] = true
			if browsable {
				// Sprints and projects list their browse directory beside
				// the flat file, both keyed by the entity's short id.
				names[snap.EntityID().Short()] = true
			}
		}
	case ProjectBrowseDir:
		if _, errc := f.lookupProject(n.ProjShort); errc != 0 {
			return nil, errc
		}
		names["sprints"], names["tasks"] = true, true
	case ProjectSprintsDir:
		project, errc := f.lookupProject(n.ProjShort)
		if errc != 0 {
			return nil, errc
		}
		sprints, err := f.store.ListSprints(f.ctx)
		if err != nil {
			return nil, errno(err)
		}
		for _, s := range sprints {
			if s.Project == project.ID {
				names[s.ID.Short()] = true
			}
		}
	case ProjectSprintDir:
		if _, errc := f.lookupSprintInProject(n.ProjShort, n.SprintShort); errc != 0 {
			return nil, errc
		}
		names["tasks"] = true
	case ProjectSprintTasksDir:
		sprint, errc := f.lookupSprintInProject(n.ProjShort, n.SprintShort)
		if errc != 0 {
			return nil, errc
		}
		tasks, err := f.store.ListTasks(f.ctx)
		if err != nil {
			return nil, errno(err)
		}
		for _, t := range tasks {
			if t.Sprint == sprint.ID {
				names[Filename(t)] = true
			}
		}
	case ProjectTasksDir:
		project, errc := f.lookupProject(n.ProjShort)
		if errc != 0 {
			return nil, errc
		}
		tasks, err := f.store.ListTasks(f.ctx)
		if err != nil {
			return nil, errno(err)
		}
		sprints, err := f.store.ListSprints(f.ctx)
		if err != nil {
			return nil, errno(err)
		}
		inProject := projectTaskSet(tasks, sprints, project.ID)
		for _, t := range tasks {
			if inProject[t.ID] {
				names[Filename(t)] = true
			}
		}
	case SprintBrowseDir:
		if _, errc := f.lookupSprint(n.SprintShort); errc != 0 {
			return nil, errc
		}
		names["tasks"] = true
	case SprintTasksDir:
		sprint, errc := f.lookupSprint(n.SprintShort)
		if errc != 0 {
			return nil, errc
		}
		tasks, err := f.store.ListTasks(f.ctx)
		if err != nil {
			return nil, errno(err)
		}
		for _, t := range tasks {
			if t.Sprint == sprint.ID {
				names[Filename(t)] = true
			}
		}
	case AttachmentsDir:
		attached, errc := f.listAttachables()
		if errc != 0 {
			return nil, errc
		}
		maps.Copy(names, attached)
	case AttachmentEntityDir:
		ent, errc := f.lookupAttachable(n.EntityShort)
		if errc != 0 {
			return nil, errc
		}
		for _, a := range ent.atts {
			names[a.Name] = true
		}
	default:
		return nil, -fuse.ENOTDIR
	}
	for sp := range f.scratch {
		if path.Dir(sp) == p {
			names[path.Base(sp)] = true
		}
	}
	return slices.Sorted(maps.Keys(names)), 0
}

// lookupProject resolves a project browse-dir short id to its snapshot,
// mapping any failure to an errno.
func (f *FS) lookupProject(shortID string) (model.Project, int) {
	_, r, err := f.resolveEntity(model.KindProject, shortID)
	if err != nil {
		return model.Project{}, errno(err)
	}
	return r.snapshot.(model.Project), 0
}

// lookupSprint resolves a sprint browse-dir short id to its snapshot.
func (f *FS) lookupSprint(shortID string) (model.Sprint, int) {
	_, r, err := f.resolveEntity(model.KindSprint, shortID)
	if err != nil {
		return model.Sprint{}, errno(err)
	}
	return r.snapshot.(model.Sprint), 0
}

// lookupTask resolves a task leaf short id to its snapshot.
func (f *FS) lookupTask(shortID string) (model.Task, int) {
	_, r, err := f.resolveEntity(model.KindTask, shortID)
	if err != nil {
		return model.Task{}, errno(err)
	}
	return r.snapshot.(model.Task), 0
}

// lookupSprintInProject resolves the sprint and confirms it belongs to the
// project the browse path names.
func (f *FS) lookupSprintInProject(projShort, sprintShort string) (model.Sprint, int) {
	project, errc := f.lookupProject(projShort)
	if errc != 0 {
		return model.Sprint{}, errc
	}
	sprint, errc := f.lookupSprint(sprintShort)
	if errc != 0 {
		return model.Sprint{}, errc
	}
	if sprint.Project != project.ID {
		return model.Sprint{}, -fuse.ENOENT
	}
	return sprint, 0
}

// validateBrowseDir confirms the project/sprint chain a browse directory names
// exists and is linked as the path claims.
func (f *FS) validateBrowseDir(node Node) int {
	switch n := node.(type) {
	case ProjectBrowseDir:
		_, errc := f.lookupProject(n.ProjShort)
		return errc
	case ProjectSprintsDir:
		_, errc := f.lookupProject(n.ProjShort)
		return errc
	case ProjectTasksDir:
		_, errc := f.lookupProject(n.ProjShort)
		return errc
	case ProjectSprintDir:
		_, errc := f.lookupSprintInProject(n.ProjShort, n.SprintShort)
		return errc
	case ProjectSprintTasksDir:
		_, errc := f.lookupSprintInProject(n.ProjShort, n.SprintShort)
		return errc
	case SprintBrowseDir:
		_, errc := f.lookupSprint(n.SprintShort)
		return errc
	case SprintTasksDir:
		_, errc := f.lookupSprint(n.SprintShort)
		return errc
	default:
		panic(fmt.Sprintf("fusefs: validateBrowseDir on non-dir node %T", node))
	}
}

// resolveLink validates a browse-tree link's full membership chain, resolves the
// leaf task, and returns that task plus the relative symlink target. A broken
// chain or unknown id maps to ENOENT.
func (f *FS) resolveLink(p string, node Node) (model.Task, string, int) {
	task, errc := f.linkTask(node)
	if errc != 0 {
		return model.Task{}, "", errc
	}
	return task, SymlinkTarget(p, "tasks/"+Filename(task)), 0
}

// linkTask validates the membership chain a browse-tree link encodes and
// returns the leaf task it points at.
func (f *FS) linkTask(node Node) (model.Task, int) {
	switch n := node.(type) {
	case SprintTaskLink:
		sprint, errc := f.lookupSprint(n.SprintShort)
		if errc != 0 {
			return model.Task{}, errc
		}
		task, errc := f.lookupTask(n.TaskShort)
		if errc != 0 {
			return model.Task{}, errc
		}
		if task.Sprint != sprint.ID {
			return model.Task{}, -fuse.ENOENT
		}
		return task, 0
	case ProjectSprintTaskLink:
		sprint, errc := f.lookupSprintInProject(n.ProjShort, n.SprintShort)
		if errc != 0 {
			return model.Task{}, errc
		}
		task, errc := f.lookupTask(n.TaskShort)
		if errc != 0 {
			return model.Task{}, errc
		}
		if task.Sprint != sprint.ID {
			return model.Task{}, -fuse.ENOENT
		}
		return task, 0
	case ProjectTaskLink:
		project, errc := f.lookupProject(n.ProjShort)
		if errc != 0 {
			return model.Task{}, errc
		}
		task, errc := f.lookupTask(n.TaskShort)
		if errc != 0 {
			return model.Task{}, errc
		}
		member, err := f.taskInProject(task, project.ID)
		if err != nil {
			return model.Task{}, errno(err)
		}
		if !member {
			return model.Task{}, -fuse.ENOENT
		}
		return task, 0
	default:
		panic(fmt.Sprintf("fusefs: linkTask on non-link node %T", node))
	}
}

// taskInProject reports whether task belongs to projectID, mirroring the CLI's
// reverse index: a direct project pointer, or membership through a sprint whose
// project is projectID.
func (f *FS) taskInProject(task model.Task, projectID model.EntityID) (bool, error) {
	if task.Project == projectID {
		return true, nil
	}
	if task.Sprint == "" {
		return false, nil
	}
	sprints, err := f.store.ListSprints(f.ctx)
	if err != nil {
		return false, err
	}
	for _, s := range sprints {
		if s.ID == task.Sprint {
			return s.Project == projectID, nil
		}
	}
	return false, nil
}

// projectTaskSet returns the set of task ids in projectID: tasks pointed
// directly at the project, unioned with tasks whose sprint belongs to it.
func projectTaskSet(tasks []model.Task, sprints []model.Sprint, projectID model.EntityID) map[model.EntityID]bool {
	projectSprints := make(map[model.EntityID]bool)
	for _, s := range sprints {
		if s.Project == projectID {
			projectSprints[s.ID] = true
		}
	}
	set := make(map[model.EntityID]bool)
	for _, t := range tasks {
		if t.Project == projectID || (t.Sprint != "" && projectSprints[t.Sprint]) {
			set[t.ID] = true
		}
	}
	return set
}
