package notes_test

import (
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func TestTasksBlocking(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	blocker := mustTask(t, c, notes.TaskSpec{Title: "blocker", Branch: "main"})
	b1 := mustTask(t, c, notes.TaskSpec{Title: "b1", Branch: "main", BlockedBy: []model.EntityID{blocker.ID}})
	b2 := mustTask(t, c, notes.TaskSpec{Title: "b2", Branch: "main", BlockedBy: []model.EntityID{blocker.ID}})
	mustTask(t, c, notes.TaskSpec{Title: "free", Branch: "main"})

	want := []model.EntityID{b1.ID, b2.ID}
	slices.Sort(want)
	got, err := c.TasksBlocking(ctx, blocker.ID)
	if err != nil {
		t.Fatalf("TasksBlocking: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("TasksBlocking(%s) = %v, want %v", blocker.ID, got, want)
	}

	none, err := c.TasksBlocking(ctx, b1.ID)
	if err != nil {
		t.Fatalf("TasksBlocking(no blockers): %v", err)
	}
	if len(none) != 0 {
		t.Errorf("TasksBlocking(%s) = %v, want empty", b1.ID, none)
	}
}

func TestSprintTasks(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	sp, _, err := c.CreateSprint(ctx, notes.SprintSpec{Title: "S"})
	if err != nil {
		t.Fatalf("CreateSprint: %v", err)
	}
	other, _, err := c.CreateSprint(ctx, notes.SprintSpec{Title: "Other"})
	if err != nil {
		t.Fatalf("CreateSprint other: %v", err)
	}
	t1 := mustTask(t, c, notes.TaskSpec{Title: "t1", Branch: "main", Sprint: sp.ID})
	t2 := mustTask(t, c, notes.TaskSpec{Title: "t2", Branch: "main", Sprint: sp.ID})
	mustTask(t, c, notes.TaskSpec{Title: "elsewhere", Branch: "main", Sprint: other.ID})
	mustTask(t, c, notes.TaskSpec{Title: "loose", Branch: "main"})

	want := []model.EntityID{t1.ID, t2.ID}
	slices.Sort(want)
	got, err := c.SprintTasks(ctx, sp.ID)
	if err != nil {
		t.Fatalf("SprintTasks: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("SprintTasks(%s) = %v, want %v", sp.ID, got, want)
	}
}

func TestProjectSprints(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	p, _, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "P"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	s1, _, _ := c.CreateSprint(ctx, notes.SprintSpec{Title: "s1", Project: p.ID})
	s2, _, _ := c.CreateSprint(ctx, notes.SprintSpec{Title: "s2", Project: p.ID})
	if _, _, err := c.CreateSprint(ctx, notes.SprintSpec{Title: "loose"}); err != nil {
		t.Fatalf("CreateSprint loose: %v", err)
	}

	want := []model.EntityID{s1.ID, s2.ID}
	slices.Sort(want)
	got, err := c.ProjectSprints(ctx, p.ID)
	if err != nil {
		t.Fatalf("ProjectSprints: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("ProjectSprints(%s) = %v, want %v", p.ID, got, want)
	}
}

func TestProjectTasks(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	p, _, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "P"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	sp, _, _ := c.CreateSprint(ctx, notes.SprintSpec{Title: "s", Project: p.ID})

	direct := mustTask(t, c, notes.TaskSpec{Title: "direct", Branch: "main", Project: p.ID})
	viaSprint := mustTask(t, c, notes.TaskSpec{Title: "viaSprint", Branch: "main", Sprint: sp.ID})
	both := mustTask(t, c, notes.TaskSpec{Title: "both", Branch: "main", Project: p.ID, Sprint: sp.ID})
	mustTask(t, c, notes.TaskSpec{Title: "unrelated", Branch: "main"})

	// A task counted both ways (direct project + a sprint in the project)
	// appears once.
	want := []model.EntityID{direct.ID, viaSprint.ID, both.ID}
	slices.Sort(want)
	got, err := c.ProjectTasks(ctx, p.ID)
	if err != nil {
		t.Fatalf("ProjectTasks: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("ProjectTasks(%s) = %v, want %v", p.ID, got, want)
	}
}
