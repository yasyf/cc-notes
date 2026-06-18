package notes

import (
	"context"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// Project loads the project with the given id and folds it. A missing entity
// fails with ErrRefNotFound.
func (c *Client) Project(ctx context.Context, id model.EntityID) (model.Project, error) {
	snapshot, err := c.s.Load(ctx, refs.Project(id))
	if err != nil {
		return model.Project{}, err
	}
	return snapshot.(model.Project), nil
}

// Sprint loads the sprint with the given id and folds it. A missing entity
// fails with ErrRefNotFound.
func (c *Client) Sprint(ctx context.Context, id model.EntityID) (model.Sprint, error) {
	snapshot, err := c.s.Load(ctx, refs.Sprint(id))
	if err != nil {
		return model.Sprint{}, err
	}
	return snapshot.(model.Sprint), nil
}

// Task loads the task with the given id and folds it. A missing entity fails
// with ErrRefNotFound.
func (c *Client) Task(ctx context.Context, id model.EntityID) (model.Task, error) {
	snapshot, err := c.s.Load(ctx, refs.Task(id))
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// Projects folds every project in the repository, ordered by creation time
// then id.
func (c *Client) Projects(ctx context.Context) ([]model.Project, error) {
	return c.s.ListProjects(ctx)
}

// Sprints folds every sprint in the repository, ordered by creation time then
// id.
func (c *Client) Sprints(ctx context.Context) ([]model.Sprint, error) {
	return c.s.ListSprints(ctx)
}

// Tasks folds every task in the repository, ordered by creation time then id.
func (c *Client) Tasks(ctx context.Context) ([]model.Task, error) {
	return c.s.ListTasks(ctx)
}

// ResolveProject expands a project id prefix to its full EntityID. No match
// fails with ErrNotFound; an ambiguous prefix fails with ErrAmbiguous.
func (c *Client) ResolveProject(ctx context.Context, prefix string) (model.EntityID, error) {
	return c.resolve(ctx, refs.KindProject, prefix)
}

// ResolveSprint expands a sprint id prefix to its full EntityID. No match
// fails with ErrNotFound; an ambiguous prefix fails with ErrAmbiguous.
func (c *Client) ResolveSprint(ctx context.Context, prefix string) (model.EntityID, error) {
	return c.resolve(ctx, refs.KindSprint, prefix)
}

// ResolveTask expands a task id prefix to its full EntityID. No match fails
// with ErrNotFound; an ambiguous prefix fails with ErrAmbiguous.
func (c *Client) ResolveTask(ctx context.Context, prefix string) (model.EntityID, error) {
	return c.resolve(ctx, refs.KindTask, prefix)
}

func (c *Client) resolve(ctx context.Context, kind refs.Kind, prefix string) (model.EntityID, error) {
	ref, err := c.s.Resolve(ctx, kind, prefix)
	if err != nil {
		return "", err
	}
	parsed, err := refs.Parse(ref)
	if err != nil {
		return "", err
	}
	return parsed.ID, nil
}
