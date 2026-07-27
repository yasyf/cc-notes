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

func TestTaskChildren(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	epic := mustTask(t, c, notes.TaskSpec{Title: "epic", Branch: "main", Type: model.TypeEpic})
	other := mustTask(t, c, notes.TaskSpec{Title: "other epic", Branch: "main", Type: model.TypeEpic})
	c1 := mustTask(t, c, notes.TaskSpec{Title: "c1", Branch: "main", Parent: epic.ID})
	c2 := mustTask(t, c, notes.TaskSpec{Title: "c2", Branch: "main", Parent: epic.ID})
	elsewhere := mustTask(t, c, notes.TaskSpec{Title: "elsewhere", Branch: "main", Parent: other.ID})
	loose := mustTask(t, c, notes.TaskSpec{Title: "loose", Branch: "main"})

	children := []model.EntityID{c1.ID, c2.ID}
	slices.Sort(children)
	for _, tc := range []struct {
		name   string
		parent model.EntityID
		want   []model.EntityID
	}{
		{"two children sorted", epic.ID, children},
		{"single child", other.ID, []model.EntityID{elsewhere.ID}},
		{"leaf task", c1.ID, nil},
		{"parentless task", loose.ID, nil},
		{"unknown id", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.TaskChildren(ctx, tc.parent)
			if err != nil {
				t.Fatalf("TaskChildren: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("TaskChildren(%s) = %v, want %v", tc.parent, got, tc.want)
			}
		})
	}

	// The index is TaskChildren for every parent in one pass, and never keys
	// the empty parent of a top-level task.
	index, err := c.TaskChildrenIndex(ctx)
	if err != nil {
		t.Fatalf("TaskChildrenIndex: %v", err)
	}
	want := map[model.EntityID][]model.EntityID{epic.ID: children, other.ID: {elsewhere.ID}}
	if len(index) != len(want) {
		t.Fatalf("TaskChildrenIndex = %v, want %v", index, want)
	}
	for parent, ids := range want {
		if !slices.Equal(index[parent], ids) {
			t.Errorf("TaskChildrenIndex[%s] = %v, want %v", parent, index[parent], ids)
		}
	}
}

func TestTaskRuns(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	served := mustTask(t, c, notes.TaskSpec{Title: "served", Branch: "main"})
	other := mustTask(t, c, notes.TaskSpec{Title: "other", Branch: "main"})
	untouched := mustTask(t, c, notes.TaskSpec{Title: "untouched", Branch: "main"})

	deploy, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "deploy", Steps: []string{"build"}})
	if err != nil {
		t.Fatalf("CreateRunbook deploy: %v", err)
	}
	rollback, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "rollback", Steps: []string{"revert"}})
	if err != nil {
		t.Fatalf("CreateRunbook rollback: %v", err)
	}
	// deploy: one finished run for served, one running run for other, one
	// run citing no task at all; rollback: a second run for served.
	startRun(t, c, deploy.ID, served.ID)
	finished := runIDByTask(t, c, deploy.ID, served.ID)
	if _, err := c.FinishRun(ctx, deploy.ID, finished, model.RunSucceeded); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}
	startRun(t, c, deploy.ID, other.ID)
	startRun(t, c, deploy.ID, "")
	startRun(t, c, rollback.ID, served.ID)

	for _, tc := range []struct {
		name string
		task model.EntityID
		want []string
	}{
		{"runs across two runbooks", served.ID, []string{
			string(deploy.ID) + "/" + finished,
			string(rollback.ID) + "/" + runIDByTask(t, c, rollback.ID, served.ID),
		}},
		{"single run", other.ID, []string{string(deploy.ID) + "/" + runIDByTask(t, c, deploy.ID, other.ID)}},
		{"task with no run", untouched.ID, nil},
		{"unknown id", "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := c.TaskRuns(ctx, tc.task)
			if err != nil {
				t.Fatalf("TaskRuns: %v", err)
			}
			keys := make([]string, 0, len(got))
			for _, r := range got {
				keys = append(keys, string(r.Runbook)+"/"+r.Run.ID)
			}
			slices.Sort(keys)
			want := slices.Clone(tc.want)
			slices.Sort(want)
			if !slices.Equal(keys, want) {
				t.Fatalf("TaskRuns(%s) = %v, want %v", tc.task, keys, want)
			}
			for i := 1; i < len(got); i++ {
				if got[i-1].Run.StartedAt > got[i].Run.StartedAt {
					t.Errorf("TaskRuns not ordered by start: %d before %d", got[i-1].Run.StartedAt, got[i].Run.StartedAt)
				}
			}
		})
	}

	// The whole run rides along, not just its id.
	runs, err := c.TaskRuns(ctx, served.ID)
	if err != nil {
		t.Fatalf("TaskRuns(served): %v", err)
	}
	byRun := map[string]model.RunbookRun{}
	for _, r := range runs {
		byRun[r.Run.ID] = r.Run
	}
	if got := byRun[finished]; got.Status != model.RunSucceeded || got.FinishedAt == 0 {
		t.Errorf("finished run = %+v, want succeeded with a finish time", got)
	}
}

// startRun starts a run of runbook rb citing task, failing the test on error.
func startRun(t *testing.T, c *notes.Client, rb, task model.EntityID) {
	t.Helper()
	if _, err := c.StartRun(t.Context(), rb, task); err != nil {
		t.Fatalf("StartRun(%s, %s): %v", rb, task, err)
	}
}

// runIDByTask returns the id of runbook rb's single run citing task.
func runIDByTask(t *testing.T, c *notes.Client, rb, task model.EntityID) string {
	t.Helper()
	loaded, err := c.Runbook(t.Context(), rb)
	if err != nil {
		t.Fatalf("Runbook(%s): %v", rb, err)
	}
	var found string
	for _, run := range loaded.Runs {
		if run.Task == task {
			if found != "" {
				t.Fatalf("runbook %s has more than one run citing %s", rb, task)
			}
			found = run.ID
		}
	}
	if found == "" {
		t.Fatalf("runbook %s has no run citing %s", rb, task)
	}
	return found
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

	elsewhere, _, _ := c.CreateSprint(ctx, notes.SprintSpec{Title: "elsewhere"})

	direct := mustTask(t, c, notes.TaskSpec{Title: "direct", Branch: "main", Project: p.ID})
	viaSprint := mustTask(t, c, notes.TaskSpec{Title: "viaSprint", Branch: "main", Sprint: sp.ID})
	both := mustTask(t, c, notes.TaskSpec{Title: "both", Branch: "main", Project: p.ID, Sprint: sp.ID})
	crossed := mustTask(t, c, notes.TaskSpec{Title: "crossed", Branch: "main", Project: p.ID, Sprint: elsewhere.ID})
	mustTask(t, c, notes.TaskSpec{Title: "unrelated", Branch: "main", Sprint: elsewhere.ID})

	// A task counted both ways (direct project + a sprint in the project)
	// appears once; a direct member whose sprint sits outside the project
	// still counts.
	want := []model.EntityID{direct.ID, viaSprint.ID, both.ID, crossed.ID}
	slices.Sort(want)
	got, err := c.ProjectTasks(ctx, p.ID)
	if err != nil {
		t.Fatalf("ProjectTasks: %v", err)
	}
	if !slices.Equal(got, want) {
		t.Errorf("ProjectTasks(%s) = %v, want %v", p.ID, got, want)
	}
}
