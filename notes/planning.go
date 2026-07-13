package notes

import (
	"context"
	"errors"
	"slices"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// ProjectSpec is the input to CreateProject. Title is required; the rest are
// optional and may be left zero.
type ProjectSpec struct {
	Title       string
	Description string
	Labels      []string
}

// SprintSpec is the input to CreateSprint. Title is required. Project, when
// non-empty, makes the sprint a member of that project. StartDate and EndDate
// are unix seconds; zero leaves them unset.
type SprintSpec struct {
	Title       string
	Description string
	Project     model.EntityID
	Labels      []string
	StartDate   int64
	EndDate     int64
}

// SprintFilter narrows a sprint listing. The zero value matches every sprint.
// Project constrains to sprints in that project; Statuses is a status
// allow-list.
type SprintFilter struct {
	Project  model.EntityID
	Statuses []model.SprintStatus
}

// ProjectFilter narrows a project listing. The zero value matches every
// project. Statuses is a status allow-list.
type ProjectFilter struct {
	Statuses []model.ProjectStatus
}

// SprintEdit is the field mask for EditSprint: a pointer field is the
// sanctioned tri-state (nil leaves it untouched, a non-nil pointer sets it, a
// pointer to the zero value clears it — an empty Project, or a zero StartDate or
// EndDate); the label slices apply in order. An all-empty mask is ErrEmptyEdit.
type SprintEdit struct {
	Title        *string
	Description  *string
	Project      *model.EntityID
	StartDate    *int64
	EndDate      *int64
	AddLabels    []string
	RemoveLabels []string
}

// empty reports whether the mask sets nothing.
func (e SprintEdit) empty() bool {
	return e.Title == nil && e.Description == nil && e.Project == nil &&
		e.StartDate == nil && e.EndDate == nil &&
		len(e.AddLabels) == 0 && len(e.RemoveLabels) == 0
}

// ProjectEdit is the field mask for EditProject: a nil Title or Description
// leaves the field untouched, a non-nil pointer sets it; the label slices apply
// in order. An all-empty mask is ErrEmptyEdit.
type ProjectEdit struct {
	Title        *string
	Description  *string
	AddLabels    []string
	RemoveLabels []string
}

// empty reports whether the mask sets nothing.
func (e ProjectEdit) empty() bool {
	return e.Title == nil && e.Description == nil && len(e.AddLabels) == 0 && len(e.RemoveLabels) == 0
}

// CreateProject roots a new project chain and returns its folded snapshot. The
// returned bool reports that Create's best-effort duplicate guard converged on
// an existing live project instead of rooting a new one, though truly
// concurrent identical creates can both land.
func (c *Client) CreateProject(ctx context.Context, spec ProjectSpec) (model.Project, bool, error) {
	snap, err := c.s.Create(ctx, []model.Op{model.CreateProject{
		Nonce:       model.NewNonce(),
		Title:       spec.Title,
		Description: spec.Description,
		Labels:      spec.Labels,
	}})
	reused := false
	var dup *store.DuplicateError
	if errors.As(err, &dup) {
		snap, reused = dup.Existing, true
	} else if err != nil {
		return model.Project{}, false, err
	}
	return snap.(model.Project), reused, nil
}

// CreateSprint roots a new sprint chain and returns its folded snapshot. The
// create pack carries the sprint op followed by the start- and end-date ops when
// those dates are set. The returned bool reports that Create's best-effort
// duplicate guard converged on an existing live sprint.
func (c *Client) CreateSprint(ctx context.Context, spec SprintSpec) (model.Sprint, bool, error) {
	ops := []model.Op{model.CreateSprint{
		Nonce:       model.NewNonce(),
		Title:       spec.Title,
		Description: spec.Description,
		Project:     spec.Project,
		Labels:      spec.Labels,
	}}
	if spec.StartDate != 0 {
		ops = append(ops, model.SetStartDate{Date: spec.StartDate})
	}
	if spec.EndDate != 0 {
		ops = append(ops, model.SetEndDate{Date: spec.EndDate})
	}
	snap, err := c.s.Create(ctx, ops)
	reused := false
	var dup *store.DuplicateError
	if errors.As(err, &dup) {
		snap, reused = dup.Existing, true
	} else if err != nil {
		return model.Sprint{}, false, err
	}
	return snap.(model.Sprint), reused, nil
}

// Sprints folds the sprint set the filter selects, in store order (creation
// time then id).
func (c *Client) Sprints(ctx context.Context, f SprintFilter) ([]model.Sprint, error) {
	sprints, err := c.s.ListSprints(ctx)
	if err != nil {
		return nil, err
	}
	sprints = slices.DeleteFunc(sprints, func(sp model.Sprint) bool {
		return (f.Project != "" && sp.Project != f.Project) ||
			(len(f.Statuses) > 0 && !slices.Contains(f.Statuses, sp.Status))
	})
	return sprints, nil
}

// Projects folds the project set the filter selects, in store order (creation
// time then id).
func (c *Client) Projects(ctx context.Context, f ProjectFilter) ([]model.Project, error) {
	projects, err := c.s.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	if len(f.Statuses) > 0 {
		projects = slices.DeleteFunc(projects, func(p model.Project) bool {
			return !slices.Contains(f.Statuses, p.Status)
		})
	}
	return projects, nil
}

// ActivateSprint marks the sprint active.
func (c *Client) ActivateSprint(ctx context.Context, id model.EntityID) (model.Sprint, error) {
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

// setSprintStatus transitions the sprint to status, allowing the change only
// from planned or active — a closed sprint refuses with a *ConflictError, so
// activate on an active sprint stays idempotent.
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
	return c.appendSprint(ctx, id, []model.Op{model.SetSprintStatus{Status: status}})
}

// setProjectStatus transitions the project to status, allowing the change only
// from active — a closed project refuses with a *ConflictError.
func (c *Client) setProjectStatus(ctx context.Context, id model.EntityID, status model.ProjectStatus) (model.Project, error) {
	project, err := c.Project(ctx, id)
	if err != nil {
		return model.Project{}, err
	}
	if project.Status != model.ProjectActive {
		return model.Project{}, &ConflictError{ID: id, Msg: "already " + string(project.Status)}
	}
	return c.appendProject(ctx, id, []model.Op{model.SetProjectStatus{Status: status}})
}

// EditSprint applies the mask to the sprint without transition checks. An
// all-empty mask is ErrEmptyEdit. Ops apply in title, description, project,
// start-date, end-date, add-label, remove-label order; a Project pointing at the
// empty id clears the membership, and a zero StartDate or EndDate clears that
// date.
func (c *Client) EditSprint(ctx context.Context, id model.EntityID, edit SprintEdit) (model.Sprint, error) {
	if edit.empty() {
		return model.Sprint{}, ErrEmptyEdit
	}
	var ops []model.Op
	if edit.Title != nil {
		ops = append(ops, model.SetTitle{Title: *edit.Title})
	}
	if edit.Description != nil {
		ops = append(ops, model.SetDescription{Description: *edit.Description})
	}
	if edit.Project != nil {
		ops = append(ops, model.SetProject{Project: *edit.Project})
	}
	if edit.StartDate != nil {
		ops = append(ops, model.SetStartDate{Date: *edit.StartDate})
	}
	if edit.EndDate != nil {
		ops = append(ops, model.SetEndDate{Date: *edit.EndDate})
	}
	for _, l := range edit.AddLabels {
		ops = append(ops, model.AddLabel{Label: l})
	}
	for _, l := range edit.RemoveLabels {
		ops = append(ops, model.RemoveLabel{Label: l})
	}
	return c.appendSprint(ctx, id, ops)
}

// EditProject applies the mask to the project without transition checks. An
// all-empty mask is ErrEmptyEdit. Ops apply in title, description, add-label,
// remove-label order.
func (c *Client) EditProject(ctx context.Context, id model.EntityID, edit ProjectEdit) (model.Project, error) {
	if edit.empty() {
		return model.Project{}, ErrEmptyEdit
	}
	var ops []model.Op
	if edit.Title != nil {
		ops = append(ops, model.SetTitle{Title: *edit.Title})
	}
	if edit.Description != nil {
		ops = append(ops, model.SetDescription{Description: *edit.Description})
	}
	for _, l := range edit.AddLabels {
		ops = append(ops, model.AddLabel{Label: l})
	}
	for _, l := range edit.RemoveLabels {
		ops = append(ops, model.RemoveLabel{Label: l})
	}
	return c.appendProject(ctx, id, ops)
}

// CommentSprint appends a comment to the sprint.
func (c *Client) CommentSprint(ctx context.Context, id model.EntityID, body string) (model.Sprint, error) {
	return c.appendSprint(ctx, id, []model.Op{model.AddComment{Body: body}})
}

// CommentProject appends a comment to the project.
func (c *Client) CommentProject(ctx context.Context, id model.EntityID, body string) (model.Project, error) {
	return c.appendProject(ctx, id, []model.Op{model.AddComment{Body: body}})
}

// appendSprint appends ops to the sprint chain and returns the folded snapshot.
func (c *Client) appendSprint(ctx context.Context, id model.EntityID, ops []model.Op) (model.Sprint, error) {
	snap, err := c.s.Append(ctx, refs.For(model.KindSprint, id), ops)
	if err != nil {
		return model.Sprint{}, err
	}
	return snap.(model.Sprint), nil
}

// appendProject appends ops to the project chain and returns the folded
// snapshot.
func (c *Client) appendProject(ctx context.Context, id model.EntityID, ops []model.Op) (model.Project, error) {
	snap, err := c.s.Append(ctx, refs.For(model.KindProject, id), ops)
	if err != nil {
		return model.Project{}, err
	}
	return snap.(model.Project), nil
}
