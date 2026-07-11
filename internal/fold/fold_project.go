package fold

import (
	"fmt"
	"slices"

	"github.com/yasyf/cc-notes/model"
)

type projectFolder struct {
	project model.Project
	labels  map[string]bool
	commits map[model.SHA]bool
}

func newProjectFolder() *projectFolder {
	return &projectFolder{
		labels:  map[string]bool{},
		commits: map[model.SHA]bool{},
	}
}

func foldProject(ordered []model.PackCommit) (model.Project, error) {
	return run[model.Project](ordered, newProjectFolder())
}

func (f *projectFolder) fresh(sha model.SHA, createdAt int64) {
	f.project = model.Project{ID: model.EntityID(sha), CreatedAt: createdAt, Comments: []model.Comment{}}
}

func (f *projectFolder) seed(state model.Snapshot) error {
	seed, ok := state.(model.Project)
	if !ok {
		return fmt.Errorf("%w: checkpoint over a non-project folded as a project", ErrKindMismatch)
	}
	f.project = seed
	f.project.Comments = slices.Clone(seed.Comments)
	for _, l := range seed.Labels {
		f.labels[l] = true
	}
	for _, sha := range seed.Commits {
		f.commits[sha] = true
	}
	return nil
}

func (f *projectFolder) create(op model.CreateOp, author model.Actor) error {
	o, ok := op.(model.CreateProject)
	if !ok {
		return fmt.Errorf("%w: %s chain folded as a project", ErrKindMismatch, op.OpKind())
	}
	f.project.Title, f.project.Description = o.Title, o.Description
	f.project.Author = author
	f.project.Status = model.ProjectActive
	for _, l := range o.Labels {
		f.labels[l] = true
	}
	return nil
}

func (f *projectFolder) apply(op model.Op, c model.PackCommit) error {
	if applyLabel(f.labels, op) || applyCommitLink(f.commits, op) || applyComment(&f.project.Comments, op, c) {
		return nil
	}
	switch o := op.(type) {
	case model.SetTitle:
		f.project.Title = o.Title
	case model.SetDescription:
		f.project.Description = o.Description
	case model.SetProjectStatus:
		applyProjectStatus(&f.project, o.Status, c.AuthorTime)
	default:
		return fmt.Errorf("%w: %s on a project", ErrKindMismatch, op.OpKind())
	}
	return nil
}

func (f *projectFolder) touch(c model.PackCommit) {
	f.project.UpdatedAt = c.AuthorTime
}

func (f *projectFolder) finalize(head model.SHA) model.Project {
	f.project.Labels = sortedKeys(f.labels)
	f.project.Commits = sortedKeys(f.commits)
	f.project.Head = head
	return f.project
}

func applyProjectStatus(p *model.Project, status model.ProjectStatus, at int64) {
	p.Status = status
	switch status {
	case model.ProjectCompleted, model.ProjectArchived, model.ProjectCancelled:
		p.ClosedAt = at
	case model.ProjectActive:
		p.ClosedAt = 0
	}
}
