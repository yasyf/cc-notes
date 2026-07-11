package notes

import (
	"context"
	"errors"
	"fmt"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// ClaimTask claims the task for the client's actor. The claim applies only if
// the task is open and unassigned at fold time; if another actor holds it,
// ClaimTask fails with a *ConflictError.
func (c *Client) ClaimTask(ctx context.Context, id model.EntityID) (model.Task, error) {
	me, err := c.s.Actor(ctx)
	if err != nil {
		return model.Task{}, err
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), []model.Op{model.Claim{Assignee: me}})
	if err != nil {
		return model.Task{}, err
	}
	task := snapshot.(model.Task)
	if task.Assignee != me {
		return model.Task{}, &ConflictError{ID: id, Msg: fmt.Sprintf("already claimed by %s (%s)", task.Assignee, task.Status)}
	}
	return task, nil
}

// StartTask claims the task for the client's actor and moves it onto the
// repository's current branch. It fails with a *ConflictError if another actor
// holds the task, and with ErrDetachedHead if HEAD is detached.
func (c *Client) StartTask(ctx context.Context, id model.EntityID) (model.Task, error) {
	branch, err := c.s.Git.HeadBranch(ctx)
	if err != nil {
		return model.Task{}, err
	}
	me, err := c.s.Actor(ctx)
	if err != nil {
		return model.Task{}, err
	}
	ref := refs.For(model.KindTask, id)
	snapshot, err := c.s.Append(ctx, ref, []model.Op{model.Claim{Assignee: me}})
	if err != nil {
		return model.Task{}, err
	}
	if task := snapshot.(model.Task); task.Assignee != me {
		return model.Task{}, &ConflictError{ID: id, Msg: fmt.Sprintf("already claimed by %s (%s)", task.Assignee, task.Status)}
	}
	snapshot, err = c.s.Append(ctx, ref, []model.Op{model.SetBranch{Branch: branch}})
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// RenewTask refreshes the lease heartbeat on a task the client's actor holds.
// It fails with a *ConflictError if the caller is not the assignee.
func (c *Client) RenewTask(ctx context.Context, id model.EntityID) (model.Task, error) {
	me, err := c.s.Actor(ctx)
	if err != nil {
		return model.Task{}, err
	}
	task, err := c.Task(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	if task.Assignee != me {
		return model.Task{}, &ConflictError{ID: id, Msg: fmt.Sprintf("held by %s, not you", task.Assignee)}
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), []model.Op{model.Renew{}})
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// DoneTask marks the task done and links the repository's HEAD commit when one
// exists. It fails with a *ConflictError if the task is already closed. Unlike
// the CLI, DoneTask does not gate on unmet acceptance criteria.
func (c *Client) DoneTask(ctx context.Context, id model.EntityID) (model.Task, error) {
	task, err := c.Task(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	if err := openOrInProgress(id, task.Status); err != nil {
		return model.Task{}, err
	}
	ops := []model.Op{model.SetStatus{Status: model.StatusDone}}
	head, err := c.head(ctx)
	if err != nil {
		return model.Task{}, err
	}
	if head != "" {
		ops = append(ops, model.LinkCommit{SHA: head})
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), ops)
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// CancelTask marks the task cancelled. It fails with a *ConflictError if the
// task is already closed.
func (c *Client) CancelTask(ctx context.Context, id model.EntityID) (model.Task, error) {
	task, err := c.Task(ctx, id)
	if err != nil {
		return model.Task{}, err
	}
	if err := openOrInProgress(id, task.Status); err != nil {
		return model.Task{}, err
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindTask, id), []model.Op{model.SetStatus{Status: model.StatusCancelled}})
	if err != nil {
		return model.Task{}, err
	}
	return snapshot.(model.Task), nil
}

// StartSprint marks the sprint active.
func (c *Client) StartSprint(ctx context.Context, id model.EntityID) (model.Sprint, error) {
	return c.setSprintStatus(ctx, id, model.SprintActive)
}

// CompleteSprint marks the sprint completed.
func (c *Client) CompleteSprint(ctx context.Context, id model.EntityID) (model.Sprint, error) {
	return c.setSprintStatus(ctx, id, model.SprintCompleted)
}

// CancelSprint marks the sprint cancelled.
func (c *Client) CancelSprint(ctx context.Context, id model.EntityID) (model.Sprint, error) {
	return c.setSprintStatus(ctx, id, model.SprintCancelled)
}

// CompleteProject marks the project completed.
func (c *Client) CompleteProject(ctx context.Context, id model.EntityID) (model.Project, error) {
	return c.setProjectStatus(ctx, id, model.ProjectCompleted)
}

// ArchiveProject marks the project archived.
func (c *Client) ArchiveProject(ctx context.Context, id model.EntityID) (model.Project, error) {
	return c.setProjectStatus(ctx, id, model.ProjectArchived)
}

// CancelProject marks the project cancelled.
func (c *Client) CancelProject(ctx context.Context, id model.EntityID) (model.Project, error) {
	return c.setProjectStatus(ctx, id, model.ProjectCancelled)
}

// openOrInProgress reports a *ConflictError unless status is open or
// in_progress — the only states a task close transition accepts.
func openOrInProgress(id model.EntityID, status model.Status) error {
	switch status {
	case model.StatusOpen, model.StatusInProgress:
		return nil
	}
	return &ConflictError{ID: id, Msg: "already " + string(status)}
}

// head returns the repository's HEAD commit, or "" on an unborn branch.
func (c *Client) head(ctx context.Context) (model.SHA, error) {
	head, err := c.s.Repo.Tip(ctx, "HEAD")
	if errors.Is(err, gitobj.ErrRefNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return head, nil
}

func (c *Client) setSprintStatus(ctx context.Context, id model.EntityID, status model.SprintStatus) (model.Sprint, error) {
	sprint, err := c.Sprint(ctx, id)
	if err != nil {
		return model.Sprint{}, err
	}
	switch sprint.Status {
	case model.SprintPlanned, model.SprintActive:
	default:
		return model.Sprint{}, &ConflictError{ID: id, Msg: "already " + string(sprint.Status)}
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindSprint, id), []model.Op{model.SetSprintStatus{Status: status}})
	if err != nil {
		return model.Sprint{}, err
	}
	return snapshot.(model.Sprint), nil
}

func (c *Client) setProjectStatus(ctx context.Context, id model.EntityID, status model.ProjectStatus) (model.Project, error) {
	project, err := c.Project(ctx, id)
	if err != nil {
		return model.Project{}, err
	}
	if project.Status != model.ProjectActive {
		return model.Project{}, &ConflictError{ID: id, Msg: "already " + string(project.Status)}
	}
	snapshot, err := c.s.Append(ctx, refs.For(model.KindProject, id), []model.Op{model.SetProjectStatus{Status: status}})
	if err != nil {
		return model.Project{}, err
	}
	return snapshot.(model.Project), nil
}
