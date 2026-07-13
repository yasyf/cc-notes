package notes

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/model"
)

// Blame attributes rev to the task(s) it implemented: the tasks whose linked
// commits include its full sha, plus the tasks named by its cc-task trailers,
// deduplicated and sorted in task list order. It returns the full sha with the
// tasks. A rev that names no commit fails with ErrNotFound.
func (c *Client) Blame(ctx context.Context, rev string) (model.SHA, []model.Task, error) {
	full, err := c.s.Git.CommitSHA(ctx, rev)
	if errors.Is(err, gitcmd.ErrRevNotFound) {
		return "", nil, fmt.Errorf("%w: no commit %s", ErrNotFound, rev)
	}
	if err != nil {
		return "", nil, err
	}
	all, err := c.s.ListTasks(ctx)
	if err != nil {
		return "", nil, err
	}
	seen := map[model.EntityID]bool{}
	var tasks []model.Task
	for _, t := range all {
		if slices.Contains(t.Commits, full) {
			seen[t.ID] = true
			tasks = append(tasks, t)
		}
	}
	trailers, err := c.s.Git.TaskTrailers(ctx, string(full))
	if err != nil {
		return "", nil, err
	}
	for _, val := range trailers {
		id, err := c.ResolveTask(ctx, val)
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrAmbiguous) {
			continue
		}
		if err != nil {
			return "", nil, err
		}
		task, err := c.Task(ctx, id)
		if err != nil {
			return "", nil, err
		}
		if seen[task.ID] {
			continue
		}
		seen[task.ID] = true
		tasks = append(tasks, task)
	}
	sortTasks(tasks)
	return full, tasks, nil
}
