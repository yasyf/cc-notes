package notes_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func TestCreateProject(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	p, reused, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "Platform", Description: "infra", Labels: []string{"b", "a"}})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if reused {
		t.Error("first CreateProject reused = true, want false")
	}
	if p.Title != "Platform" || p.Description != "infra" {
		t.Errorf("project = %q/%q, want Platform/infra", p.Title, p.Description)
	}
	if p.Status != model.ProjectActive {
		t.Errorf("Status = %q, want %q", p.Status, model.ProjectActive)
	}
	if want := []string{"a", "b"}; !slices.Equal(p.Labels, want) {
		t.Errorf("Labels = %v, want %v", p.Labels, want)
	}
	if p.Author != model.Actor(testActor) {
		t.Errorf("Author = %q, want %q", p.Author, testActor)
	}
	if !isHexID(p.ID) {
		t.Errorf("ID = %q, want a hex entity id", p.ID)
	}

	again, reused, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "Platform", Description: "infra", Labels: []string{"b", "a"}})
	if err != nil {
		t.Fatalf("second CreateProject: %v", err)
	}
	if !reused || again.ID != p.ID {
		t.Errorf("second CreateProject = id %s reused %v, want %s/true (idempotent)", again.ID, reused, p.ID)
	}
}

func TestCreateSprint(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	p, _, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "P"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	s, reused, err := c.CreateSprint(ctx, notes.SprintSpec{Title: "Sprint 1", Project: p.ID, StartDate: 1000, EndDate: 2000})
	if err != nil {
		t.Fatalf("CreateSprint: %v", err)
	}
	if reused {
		t.Error("first CreateSprint reused = true, want false")
	}
	if s.Title != "Sprint 1" {
		t.Errorf("Title = %q, want Sprint 1", s.Title)
	}
	if s.Status != model.SprintPlanned {
		t.Errorf("Status = %q, want %q", s.Status, model.SprintPlanned)
	}
	if s.Project != p.ID {
		t.Errorf("Project = %q, want %q", s.Project, p.ID)
	}
	if s.StartDate != 1000 || s.EndDate != 2000 {
		t.Errorf("dates = %d/%d, want 1000/2000", s.StartDate, s.EndDate)
	}

	again, reused, err := c.CreateSprint(ctx, notes.SprintSpec{Title: "Sprint 1", Project: p.ID, StartDate: 1000, EndDate: 2000})
	if err != nil {
		t.Fatalf("second CreateSprint: %v", err)
	}
	if !reused || again.ID != s.ID {
		t.Errorf("second CreateSprint = id %s reused %v, want %s/true (idempotent)", again.ID, reused, s.ID)
	}
}

func TestSprintsFilter(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	p, _, _ := c.CreateProject(ctx, notes.ProjectSpec{Title: "P"})
	a, _, _ := c.CreateSprint(ctx, notes.SprintSpec{Title: "A", Project: p.ID})
	b, _, _ := c.CreateSprint(ctx, notes.SprintSpec{Title: "B"})
	cc, _, _ := c.CreateSprint(ctx, notes.SprintSpec{Title: "C"})
	if _, err := c.ActivateSprint(ctx, a.ID); err != nil {
		t.Fatalf("ActivateSprint: %v", err)
	}

	all, err := c.Sprints(ctx, notes.SprintFilter{})
	if err != nil {
		t.Fatalf("Sprints: %v", err)
	}
	got := sprintIDs(all)
	if len(got) != 3 || !slices.Contains(got, a.ID) || !slices.Contains(got, b.ID) || !slices.Contains(got, cc.ID) {
		t.Errorf("Sprints() = %v, want all of [%s %s %s]", got, a.ID, b.ID, cc.ID)
	}
	if !slices.IsSortedFunc(all, func(x, y model.Sprint) int {
		if x.CreatedAt != y.CreatedAt {
			return int(x.CreatedAt - y.CreatedAt)
		}
		return strings.Compare(string(x.ID), string(y.ID))
	}) {
		t.Errorf("Sprints() not in store order (created_at, id): %v", got)
	}

	active, err := c.Sprints(ctx, notes.SprintFilter{Statuses: []model.SprintStatus{model.SprintActive}})
	if err != nil {
		t.Fatalf("Sprints active: %v", err)
	}
	if len(active) != 1 || active[0].ID != a.ID {
		t.Errorf("Sprints(active) = %v, want only %s", sprintIDs(active), a.ID)
	}

	both, err := c.Sprints(ctx, notes.SprintFilter{Statuses: []model.SprintStatus{model.SprintPlanned, model.SprintActive}})
	if err != nil {
		t.Fatalf("Sprints planned,active: %v", err)
	}
	if len(both) != 3 {
		t.Errorf("Sprints(planned,active) = %d, want 3", len(both))
	}

	inProj, err := c.Sprints(ctx, notes.SprintFilter{Project: p.ID})
	if err != nil {
		t.Fatalf("Sprints in project: %v", err)
	}
	if len(inProj) != 1 || inProj[0].ID != a.ID {
		t.Errorf("Sprints(project=%s) = %v, want only %s", p.ID, sprintIDs(inProj), a.ID)
	}
}

func TestProjectsFilter(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	a, _, _ := c.CreateProject(ctx, notes.ProjectSpec{Title: "A"})
	b, _, _ := c.CreateProject(ctx, notes.ProjectSpec{Title: "B"})
	if _, err := c.CompleteProject(ctx, a.ID); err != nil {
		t.Fatalf("CompleteProject: %v", err)
	}

	all, err := c.Projects(ctx, notes.ProjectFilter{})
	if err != nil {
		t.Fatalf("Projects: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("Projects() = %d, want 2", len(all))
	}

	completed, err := c.Projects(ctx, notes.ProjectFilter{Statuses: []model.ProjectStatus{model.ProjectCompleted}})
	if err != nil {
		t.Fatalf("Projects completed: %v", err)
	}
	if len(completed) != 1 || completed[0].ID != a.ID {
		t.Errorf("Projects(completed) = %v, want only %s", projectIDs(completed), a.ID)
	}
	active, err := c.Projects(ctx, notes.ProjectFilter{Statuses: []model.ProjectStatus{model.ProjectActive}})
	if err != nil {
		t.Fatalf("Projects active: %v", err)
	}
	if len(active) != 1 || active[0].ID != b.ID {
		t.Errorf("Projects(active) = %v, want only %s", projectIDs(active), b.ID)
	}
}

func TestEditSprint(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	p1, _, _ := c.CreateProject(ctx, notes.ProjectSpec{Title: "P1"})
	p2, _, _ := c.CreateProject(ctx, notes.ProjectSpec{Title: "P2"})
	s, _, _ := c.CreateSprint(ctx, notes.SprintSpec{Title: "S", Project: p1.ID, StartDate: 1000, EndDate: 2000, Labels: []string{"keep", "drop"}})

	title, empty, cleared := "S2", model.EntityID(""), int64(0)
	newStart := int64(3000)
	edited, err := c.EditSprint(ctx, s.ID, notes.SprintEdit{
		Title:        &title,
		Project:      &p2.ID,
		StartDate:    &newStart,
		EndDate:      &cleared,
		AddLabels:    []string{"new"},
		RemoveLabels: []string{"drop"},
	})
	if err != nil {
		t.Fatalf("EditSprint: %v", err)
	}
	if edited.Title != "S2" || edited.Project != p2.ID {
		t.Errorf("edited = title %q project %q, want S2/%s", edited.Title, edited.Project, p2.ID)
	}
	if edited.StartDate != 3000 || edited.EndDate != 0 {
		t.Errorf("dates = %d/%d, want 3000/0 (end cleared)", edited.StartDate, edited.EndDate)
	}
	if want := []string{"keep", "new"}; !slices.Equal(edited.Labels, want) {
		t.Errorf("labels = %v, want %v", edited.Labels, want)
	}

	// A pointer-to-zero clears the project and start date.
	c2, err := c.EditSprint(ctx, s.ID, notes.SprintEdit{Project: &empty, StartDate: &cleared})
	if err != nil {
		t.Fatalf("EditSprint clear: %v", err)
	}
	if c2.Project != "" || c2.StartDate != 0 {
		t.Errorf("cleared = project %q start %d, want empty/0", c2.Project, c2.StartDate)
	}

	if _, err := c.EditSprint(ctx, s.ID, notes.SprintEdit{}); !errors.Is(err, notes.ErrEmptyEdit) {
		t.Errorf("empty EditSprint = %v, want ErrEmptyEdit", err)
	}
}

func TestEditProject(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	p, _, _ := c.CreateProject(ctx, notes.ProjectSpec{Title: "P", Labels: []string{"keep", "drop"}})

	desc := "new"
	title := "P2"
	edited, err := c.EditProject(ctx, p.ID, notes.ProjectEdit{
		Title:        &title,
		Description:  &desc,
		AddLabels:    []string{"new"},
		RemoveLabels: []string{"drop"},
	})
	if err != nil {
		t.Fatalf("EditProject: %v", err)
	}
	if edited.Title != "P2" || edited.Description != "new" {
		t.Errorf("edited = %q/%q, want P2/new", edited.Title, edited.Description)
	}
	if want := []string{"keep", "new"}; !slices.Equal(edited.Labels, want) {
		t.Errorf("labels = %v, want %v", edited.Labels, want)
	}

	if _, err := c.EditProject(ctx, p.ID, notes.ProjectEdit{}); !errors.Is(err, notes.ErrEmptyEdit) {
		t.Errorf("empty EditProject = %v, want ErrEmptyEdit", err)
	}
}

func TestSprintTransitions(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	s, _, err := c.CreateSprint(ctx, notes.SprintSpec{Title: "S"})
	if err != nil {
		t.Fatalf("CreateSprint: %v", err)
	}
	active, err := c.ActivateSprint(ctx, s.ID)
	if err != nil || active.Status != model.SprintActive {
		t.Fatalf("ActivateSprint = %q/%v, want active/nil", active.Status, err)
	}
	// Activate on an active sprint stays idempotent (planned or active allowed).
	if _, err := c.ActivateSprint(ctx, s.ID); err != nil {
		t.Errorf("ActivateSprint on active = %v, want nil (idempotent)", err)
	}
	if got, err := c.CompleteSprint(ctx, s.ID); err != nil || got.Status != model.SprintCompleted {
		t.Fatalf("CompleteSprint = %q/%v, want completed/nil", got.Status, err)
	}
	// A completed sprint refuses further transitions with a *ConflictError
	// whose Msg names the offending status.
	_, err = c.CancelSprint(ctx, s.ID)
	var conflict *notes.ConflictError
	if !errors.As(err, &conflict) {
		t.Errorf("CancelSprint on completed = %v, want *ConflictError", err)
	} else if conflict.Msg != "already completed" {
		t.Errorf("conflict.Msg = %q, want %q", conflict.Msg, "already completed")
	}
}

func TestProjectTransitions(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	p, _, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "P"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if got, err := c.CompleteProject(ctx, p.ID); err != nil || got.Status != model.ProjectCompleted {
		t.Fatalf("CompleteProject = %q/%v, want completed/nil", got.Status, err)
	}
	if _, err := c.ArchiveProject(ctx, p.ID); !isConflict(err) {
		t.Errorf("ArchiveProject on completed = %v, want *ConflictError", err)
	}
	// A fresh project cancels straight from active.
	fresh, _, _ := c.CreateProject(ctx, notes.ProjectSpec{Title: "Q"})
	if got, err := c.CancelProject(ctx, fresh.ID); err != nil || got.Status != model.ProjectCancelled {
		t.Fatalf("CancelProject = %q/%v, want cancelled/nil", got.Status, err)
	}
}

func TestPlanningComments(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	s, _, _ := c.CreateSprint(ctx, notes.SprintSpec{Title: "S"})
	sp, err := c.CommentSprint(ctx, s.ID, "sprint note")
	if err != nil {
		t.Fatalf("CommentSprint: %v", err)
	}
	if len(sp.Comments) != 1 || sp.Comments[0].Body != "sprint note" {
		t.Errorf("sprint comments = %+v, want one 'sprint note'", sp.Comments)
	}

	p, _, _ := c.CreateProject(ctx, notes.ProjectSpec{Title: "P"})
	pr, err := c.CommentProject(ctx, p.ID, "project note")
	if err != nil {
		t.Fatalf("CommentProject: %v", err)
	}
	if len(pr.Comments) != 1 || pr.Comments[0].Body != "project note" {
		t.Errorf("project comments = %+v, want one 'project note'", pr.Comments)
	}
}

func sprintIDs(sprints []model.Sprint) []model.EntityID {
	out := make([]model.EntityID, len(sprints))
	for i, s := range sprints {
		out[i] = s.ID
	}
	return out
}

func projectIDs(projects []model.Project) []model.EntityID {
	out := make([]model.EntityID, len(projects))
	for i, p := range projects {
		out[i] = p.ID
	}
	return out
}
