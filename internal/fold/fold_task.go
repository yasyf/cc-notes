package fold

import (
	"fmt"
	"slices"

	"github.com/yasyf/cc-notes/model"
)

type taskFolder struct {
	task     model.Task
	labels   map[string]bool
	deps     map[model.EntityID]bool
	commits  map[model.SHA]bool
	criteria []model.Criterion
}

func newTaskFolder() *taskFolder {
	return &taskFolder{
		labels:  map[string]bool{},
		deps:    map[model.EntityID]bool{},
		commits: map[model.SHA]bool{},
	}
}

func foldTask(ordered []model.PackCommit) (model.Task, error) {
	return run[model.Task](ordered, newTaskFolder())
}

func (f *taskFolder) fresh(sha model.SHA, createdAt int64) {
	f.task = model.Task{ID: model.EntityID(sha), CreatedAt: createdAt, Comments: []model.Comment{}}
	f.criteria = []model.Criterion{}
}

func (f *taskFolder) seed(state model.Snapshot) error {
	seed, ok := state.(model.Task)
	if !ok {
		return fmt.Errorf("%w: checkpoint over a note folded as a task", ErrKindMismatch)
	}
	f.task = seed
	f.task.Comments = slices.Clone(seed.Comments)
	f.criteria = slices.Clone(seed.Criteria)
	for _, l := range seed.Labels {
		f.labels[l] = true
	}
	for _, id := range seed.BlockedBy {
		f.deps[id] = true
	}
	for _, sha := range seed.Commits {
		f.commits[sha] = true
	}
	return nil
}

func (f *taskFolder) create(op model.CreateOp, _ model.Actor) error {
	o, ok := op.(model.CreateTask)
	if !ok {
		return fmt.Errorf("%w: %s chain folded as a task", ErrKindMismatch, op.OpKind())
	}
	f.task.Title, f.task.Description = o.Title, o.Description
	f.task.Type, f.task.Priority = o.Type, o.Priority
	f.task.Branch, f.task.Parent = o.Branch, o.Parent
	f.task.Status = model.StatusOpen
	for _, l := range o.Labels {
		f.labels[l] = true
	}
	return nil
}

func (f *taskFolder) apply(op model.Op, c model.PackCommit) error {
	if applyLabel(f.labels, op) || applyCommitLink(f.commits, op) || applyComment(&f.task.Comments, op, c) {
		return nil
	}
	switch o := op.(type) {
	case model.SetTitle:
		f.task.Title = o.Title
	case model.SetDescription:
		f.task.Description = o.Description
	case model.SetType:
		f.task.Type = o.Type
	case model.SetPriority:
		f.task.Priority = o.Priority
	case model.SetStatus:
		applyStatus(&f.task, o.Status, c.AuthorTime)
	case model.SetAssignee:
		f.task.Assignee = o.Assignee
	case model.Claim:
		if f.task.Status == model.StatusOpen && f.task.Assignee == "" {
			f.task.Status = model.StatusInProgress
			f.task.Assignee = o.Assignee
			f.task.StartedAt = c.AuthorTime
		}
	case model.AddDep:
		f.deps[o.ID] = true
	case model.RemoveDep:
		delete(f.deps, o.ID)
	case model.SetParent:
		f.task.Parent = o.Parent
	case model.SetBranch:
		f.task.Branch = o.Branch
	case model.Renew:
		// heartbeat refresh handled uniformly in touch
	case model.Reclaim:
		if f.task.Assignee == o.From && f.task.HeartbeatLamport <= o.AfterLamport {
			f.task.Assignee = o.Assignee
			f.task.Status = model.StatusInProgress
			f.task.ClosedAt = 0
			f.task.HeartbeatAt = c.AuthorTime
			f.task.HeartbeatLamport = c.Pack.Lamport
		}
	case model.SetSprint:
		f.task.Sprint = o.Sprint
	case model.SetProject:
		f.task.Project = o.Project
	case model.DeleteNote:
		f.task.Deleted = true
	case model.AddCriterion:
		if criterionIndex(f.criteria, o.ID) < 0 {
			f.criteria = append(f.criteria, model.Criterion{ID: o.ID, Text: o.Text, Script: o.Script, Status: model.CriterionPending})
		}
	case model.RemoveCriterion:
		if i := criterionIndex(f.criteria, o.ID); i >= 0 {
			f.criteria = slices.Delete(f.criteria, i, i+1)
		}
	case model.SetCriterionText:
		if i := criterionIndex(f.criteria, o.ID); i >= 0 {
			f.criteria[i].Text = o.Text
		}
	case model.SetCriterionStatus:
		if i := criterionIndex(f.criteria, o.ID); i >= 0 {
			f.criteria[i].Status = o.Status
			f.criteria[i].Note = o.Note
		}
	case model.SetCriterionScript:
		if i := criterionIndex(f.criteria, o.ID); i >= 0 {
			f.criteria[i].Script = o.Script
		}
	default:
		return fmt.Errorf("%w: %s on a task", ErrKindMismatch, op.OpKind())
	}
	return nil
}

func (f *taskFolder) touch(c model.PackCommit) {
	f.task.UpdatedAt = c.AuthorTime
	if f.task.Assignee != "" && c.Author == f.task.Assignee {
		f.task.HeartbeatAt = c.AuthorTime
		f.task.HeartbeatLamport = c.Pack.Lamport
	}
}

func (f *taskFolder) finalize(head model.SHA) model.Task {
	f.task.Labels = sortedKeys(f.labels)
	f.task.BlockedBy = sortedKeys(f.deps)
	f.task.Commits = sortedKeys(f.commits)
	if f.criteria == nil {
		f.criteria = []model.Criterion{}
	}
	f.task.Criteria = f.criteria
	f.task.Head = head
	return f.task
}

// criterionIndex returns the index of the criterion with the given id in the
// slice, or -1 when absent. Criteria lists are tiny, so a linear scan is the
// right tool; there is no stored lamport.
func criterionIndex(criteria []model.Criterion, id string) int {
	for i := range criteria {
		if criteria[i].ID == id {
			return i
		}
	}
	return -1
}

func applyStatus(task *model.Task, status model.Status, at int64) {
	if status == model.StatusInProgress && task.Status != model.StatusInProgress {
		task.StartedAt = at
	}
	task.Status = status
	switch status {
	case model.StatusDone, model.StatusCancelled:
		task.ClosedAt = at
	case model.StatusOpen, model.StatusInProgress:
		task.ClosedAt = 0
	}
}
