package notes

import (
	"cmp"
	"context"
	"slices"

	"github.com/yasyf/cc-notes/model"
)

// TasksBlocking returns the sorted ids of live tasks whose BlockedBy contains
// id — the reverse dependency index a task's blocks field renders.
func (c *Client) TasksBlocking(ctx context.Context, id model.EntityID) ([]model.EntityID, error) {
	tasks, err := c.s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	var blocks []model.EntityID
	for _, t := range tasks {
		if slices.Contains(t.BlockedBy, id) {
			blocks = append(blocks, t.ID)
		}
	}
	slices.Sort(blocks)
	return blocks, nil
}

// TasksBlockingIndex folds ListTasks once, mapping each task id to the sorted
// ids of live tasks whose BlockedBy contains it — TasksBlocking for every id in
// one pass.
func (c *Client) TasksBlockingIndex(ctx context.Context) (map[model.EntityID][]model.EntityID, error) {
	tasks, err := c.s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	index := make(map[model.EntityID][]model.EntityID)
	for _, t := range tasks {
		for _, dep := range t.BlockedBy {
			index[dep] = append(index[dep], t.ID)
		}
	}
	for id := range index {
		slices.Sort(index[id])
	}
	return index, nil
}

// TaskChildren returns the sorted ids of the live tasks whose Parent is id —
// the reverse of a task's parent edge.
func (c *Client) TaskChildren(ctx context.Context, id model.EntityID) ([]model.EntityID, error) {
	tasks, err := c.s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	var children []model.EntityID
	for _, t := range tasks {
		if t.Parent == id {
			children = append(children, t.ID)
		}
	}
	slices.Sort(children)
	return children, nil
}

// TaskChildrenIndex folds ListTasks once, mapping each task id to the sorted
// ids of the live tasks whose Parent is it — TaskChildren for every id in one
// pass.
func (c *Client) TaskChildrenIndex(ctx context.Context) (map[model.EntityID][]model.EntityID, error) {
	tasks, err := c.s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	index := make(map[model.EntityID][]model.EntityID)
	for _, t := range tasks {
		if t.Parent != "" {
			index[t.Parent] = append(index[t.Parent], t.ID)
		}
	}
	for id := range index {
		slices.Sort(index[id])
	}
	return index, nil
}

// TaskRun is one tracked runbook run that cites a task, pairing the run with
// the runbook whose chain owns it — a run id is unique only within its runbook.
type TaskRun struct {
	Runbook model.EntityID
	Run     model.RunbookRun
}

// TaskRuns returns the runbook runs whose loose task citation is id — the
// reverse of RunbookRun.Task — oldest start first, ties broken by runbook then
// run id.
func (c *Client) TaskRuns(ctx context.Context, id model.EntityID) ([]TaskRun, error) {
	runbooks, err := c.s.ListRunbooks(ctx)
	if err != nil {
		return nil, err
	}
	var runs []TaskRun
	for _, rb := range runbooks {
		for _, run := range rb.Runs {
			if run.Task == id {
				runs = append(runs, TaskRun{Runbook: rb.ID, Run: run})
			}
		}
	}
	slices.SortFunc(runs, func(a, b TaskRun) int {
		if a.Run.StartedAt != b.Run.StartedAt {
			return cmp.Compare(a.Run.StartedAt, b.Run.StartedAt)
		}
		if a.Runbook != b.Runbook {
			return cmp.Compare(a.Runbook, b.Runbook)
		}
		return cmp.Compare(a.Run.ID, b.Run.ID)
	})
	return runs, nil
}

// SprintTasks returns the sorted ids of the tasks whose folded sprint is id —
// the reverse of a task's LWW sprint membership.
func (c *Client) SprintTasks(ctx context.Context, id model.EntityID) ([]model.EntityID, error) {
	tasks, err := c.s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	var ids []model.EntityID
	for _, t := range tasks {
		if t.Sprint == id {
			ids = append(ids, t.ID)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// ProjectSprints returns the sorted ids of the sprints whose folded project is
// id — the reverse of a sprint's project membership.
func (c *Client) ProjectSprints(ctx context.Context, id model.EntityID) ([]model.EntityID, error) {
	sprints, err := c.s.ListSprints(ctx)
	if err != nil {
		return nil, err
	}
	var ids []model.EntityID
	for _, s := range sprints {
		if s.Project == id {
			ids = append(ids, s.ID)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// ProjectTasks returns the sorted, deduplicated ids of the tasks belonging to
// id: the union of tasks pointed directly at the project and tasks whose sprint
// belongs to the project. A task counted both ways appears once.
func (c *Client) ProjectTasks(ctx context.Context, id model.EntityID) ([]model.EntityID, error) {
	sprints, err := c.s.ListSprints(ctx)
	if err != nil {
		return nil, err
	}
	tasks, err := c.s.ListTasks(ctx)
	if err != nil {
		return nil, err
	}
	projectSprints := make(map[model.EntityID]bool)
	for _, s := range sprints {
		if s.Project == id {
			projectSprints[s.ID] = true
		}
	}
	seen := make(map[model.EntityID]bool)
	var ids []model.EntityID
	for _, t := range tasks {
		if t.Project == id || (t.Sprint != "" && projectSprints[t.Sprint]) {
			if !seen[t.ID] {
				seen[t.ID] = true
				ids = append(ids, t.ID)
			}
		}
	}
	slices.Sort(ids)
	return ids, nil
}
