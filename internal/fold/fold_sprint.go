package fold

import (
	"fmt"
	"slices"

	"github.com/yasyf/cc-notes/model"
)

type sprintFolder struct {
	sprint  model.Sprint
	labels  map[string]bool
	commits map[model.SHA]bool
}

func newSprintFolder() *sprintFolder {
	return &sprintFolder{
		labels:  map[string]bool{},
		commits: map[model.SHA]bool{},
	}
}

func foldSprint(ordered []model.PackCommit) (model.Sprint, error) {
	return run[model.Sprint](ordered, newSprintFolder())
}

func (f *sprintFolder) fresh(sha model.SHA, createdAt int64) {
	f.sprint = model.Sprint{ID: model.EntityID(sha), CreatedAt: createdAt, Comments: []model.Comment{}}
}

func (f *sprintFolder) seed(state model.Snapshot) error {
	seed, ok := state.(model.Sprint)
	if !ok {
		return fmt.Errorf("%w: checkpoint over a non-sprint folded as a sprint", ErrKindMismatch)
	}
	f.sprint = seed
	f.sprint.Comments = slices.Clone(seed.Comments)
	for _, l := range seed.Labels {
		f.labels[l] = true
	}
	for _, sha := range seed.Commits {
		f.commits[sha] = true
	}
	return nil
}

func (f *sprintFolder) create(op model.CreateOp, author model.Actor) error {
	o, ok := op.(model.CreateSprint)
	if !ok {
		return fmt.Errorf("%w: %s chain folded as a sprint", ErrKindMismatch, op.OpKind())
	}
	f.sprint.Title, f.sprint.Description = o.Title, o.Description
	f.sprint.Project, f.sprint.Author = o.Project, author
	f.sprint.Status = model.SprintPlanned
	for _, l := range o.Labels {
		f.labels[l] = true
	}
	return nil
}

func (f *sprintFolder) apply(op model.Op, c model.PackCommit) error {
	if applyLabel(f.labels, op) || applyCommitLink(f.commits, op) || applyComment(&f.sprint.Comments, op, c) {
		return nil
	}
	switch o := op.(type) {
	case model.SetTitle:
		f.sprint.Title = o.Title
	case model.SetDescription:
		f.sprint.Description = o.Description
	case model.SetProject:
		f.sprint.Project = o.Project
	case model.SetSprintStatus:
		applySprintStatus(&f.sprint, o.Status, c.AuthorTime)
	case model.SetStartDate:
		f.sprint.StartDate = o.Date
	case model.SetEndDate:
		f.sprint.EndDate = o.Date
	case model.DeleteNote:
		f.sprint.Deleted = true
	default:
		return fmt.Errorf("%w: %s on a sprint", ErrKindMismatch, op.OpKind())
	}
	return nil
}

func (f *sprintFolder) touch(c model.PackCommit) {
	f.sprint.UpdatedAt = c.AuthorTime
}

func (f *sprintFolder) finalize(head model.SHA) model.Sprint {
	f.sprint.Labels = sortedKeys(f.labels)
	f.sprint.Commits = sortedKeys(f.commits)
	f.sprint.Head = head
	return f.sprint
}

func applySprintStatus(s *model.Sprint, status model.SprintStatus, at int64) {
	if status == model.SprintActive && s.Status != model.SprintActive {
		s.StartedAt = at
	}
	s.Status = status
	switch status {
	case model.SprintCompleted, model.SprintCancelled:
		s.ClosedAt = at
	case model.SprintPlanned, model.SprintActive:
		s.ClosedAt = 0
	}
}
