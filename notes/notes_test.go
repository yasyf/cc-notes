// Black-box tests exercise only the public notes surface against a real git
// repository in t.TempDir(), mirroring the store package's harness.
package notes_test

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

const testActor = "Test User <test@example.com>"

// newRepo initializes a fresh git repo on main with a cc-notes actor identity
// and returns its directory.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := gittest.InitRepo(t)
	t.Setenv("CC_NOTES_ACTOR", testActor)
	return dir
}

func newClient(t *testing.T) (*notes.Client, string) {
	t.Helper()
	dir := newRepo(t)
	c, err := notes.Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	return c, dir
}

// isHexID reports whether id looks like a folded entity id: 40 or 64 lowercase
// hex characters.
func isHexID(id model.EntityID) bool {
	if len(id) != 40 && len(id) != 64 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func TestHasNotes(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	has, err := c.HasNotes(ctx)
	if err != nil {
		t.Fatalf("HasNotes: %v", err)
	}
	if has {
		t.Fatal("fresh repo HasNotes = true, want false")
	}

	if _, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "p"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	has, err = c.HasNotes(ctx)
	if err != nil {
		t.Fatalf("HasNotes after create: %v", err)
	}
	if !has {
		t.Fatal("HasNotes after create = false, want true")
	}
}

func TestCreateProject(t *testing.T) {
	c, _ := newClient(t)
	p, err := c.CreateProject(t.Context(), notes.ProjectSpec{Title: "Platform", Description: "infra", Labels: []string{"b", "a"}})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
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
}

func TestCreateSprint(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	p, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "P"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	s, err := c.CreateSprint(ctx, notes.SprintSpec{Title: "Sprint 1", Project: p.ID, StartDate: 1000, EndDate: 2000})
	if err != nil {
		t.Fatalf("CreateSprint: %v", err)
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
}

func TestCreateTask(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	p, _ := c.CreateProject(ctx, notes.ProjectSpec{Title: "P"})
	s, _ := c.CreateSprint(ctx, notes.SprintSpec{Title: "S", Project: p.ID})
	blocker, err := c.CreateTask(ctx, notes.TaskSpec{Title: "blocker", Branch: "main"})
	if err != nil {
		t.Fatalf("CreateTask blocker: %v", err)
	}

	task, err := c.CreateTask(ctx, notes.TaskSpec{
		Title:     "ship it",
		Branch:    "feature/x",
		Sprint:    s.ID,
		Project:   p.ID,
		Priority:  1,
		Criteria:  []string{"tests pass", "docs updated"},
		BlockedBy: []model.EntityID{blocker.ID},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.Title != "ship it" || task.Branch != "feature/x" {
		t.Errorf("task = %q/%q, want ship it/feature/x", task.Title, task.Branch)
	}
	if task.Status != model.StatusOpen || task.Type != model.TypeTask || task.Priority != 1 {
		t.Errorf("task status/type/priority = %q/%q/%d, want open/task/1", task.Status, task.Type, task.Priority)
	}
	if task.Sprint != s.ID || task.Project != p.ID {
		t.Errorf("membership = sprint %q project %q, want %q/%q", task.Sprint, task.Project, s.ID, p.ID)
	}
	if len(task.Criteria) != 2 {
		t.Fatalf("Criteria = %d, want 2", len(task.Criteria))
	}
	if task.Criteria[0].Text != "tests pass" || task.Criteria[0].Status != model.CriterionPending {
		t.Errorf("criterion[0] = %+v, want text 'tests pass' status pending", task.Criteria[0])
	}
	if want := []model.EntityID{blocker.ID}; !slices.Equal(task.BlockedBy, want) {
		t.Errorf("BlockedBy = %v, want %v", task.BlockedBy, want)
	}

	// An empty Criteria slice creates a task with no acceptance criteria — the
	// in-process equivalent of the CLI's --no-validation-criteria.
	bare, err := c.CreateTask(ctx, notes.TaskSpec{Title: "bare", Branch: "main"})
	if err != nil {
		t.Fatalf("CreateTask bare: %v", err)
	}
	if len(bare.Criteria) != 0 {
		t.Errorf("bare Criteria = %d, want 0", len(bare.Criteria))
	}
}

func TestCreateTaskBranchFromHead(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")

	task, err := c.CreateTask(ctx, notes.TaskSpec{Title: "on head", BranchFromHead: true})
	if err != nil {
		t.Fatalf("CreateTask BranchFromHead: %v", err)
	}
	if task.Branch != "main" {
		t.Errorf("Branch = %q, want main", task.Branch)
	}

	gittest.Git(t, dir, "checkout", "-q", "--detach", "HEAD")
	if _, err := c.CreateTask(ctx, notes.TaskSpec{Title: "detached", BranchFromHead: true}); !errors.Is(err, notes.ErrDetachedHead) {
		t.Fatalf("CreateTask on detached HEAD = %v, want ErrDetachedHead", err)
	}
}

func TestReadAndList(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	created, err := c.CreateTask(ctx, notes.TaskSpec{Title: "t1", Branch: "main"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := c.CreateTask(ctx, notes.TaskSpec{Title: "t2", Branch: "main"}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	loaded, err := c.Task(ctx, created.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if loaded.ID != created.ID || loaded.Title != "t1" {
		t.Errorf("Task = %q/%q, want %q/t1", loaded.ID, loaded.Title, created.ID)
	}

	tasks, err := c.Tasks(ctx)
	if err != nil {
		t.Fatalf("Tasks: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("Tasks = %d, want 2", len(tasks))
	}

	if _, err := c.Task(ctx, model.EntityID(strings.Repeat("a", 40))); !errors.Is(err, notes.ErrRefNotFound) {
		t.Errorf("Task on missing id = %v, want ErrRefNotFound", err)
	}
}

func TestResolve(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	p1, _ := c.CreateProject(ctx, notes.ProjectSpec{Title: "p1"})
	if _, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "p2"}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	got, err := c.ResolveProject(ctx, string(p1.ID))
	if err != nil {
		t.Fatalf("ResolveProject(full): %v", err)
	}
	if got != p1.ID {
		t.Errorf("ResolveProject(full) = %q, want %q", got, p1.ID)
	}

	if _, err := c.ResolveProject(ctx, ""); !errors.Is(err, notes.ErrAmbiguous) {
		t.Errorf("ResolveProject(\"\") with two projects = %v, want ErrAmbiguous", err)
	}
	if _, err := c.ResolveProject(ctx, "zzz"); !errors.Is(err, notes.ErrNotFound) {
		t.Errorf("ResolveProject(zzz) = %v, want ErrNotFound", err)
	}
}

func TestTaskLifecycle(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	head := gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root") // ensure HEAD resolves
	headSHA := model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD"))
	_ = head

	task, err := c.CreateTask(ctx, notes.TaskSpec{Title: "work", Branch: "feature/x"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	// Renew before claiming is refused.
	if _, err := c.RenewTask(ctx, task.ID); !isConflict(err) {
		t.Errorf("RenewTask before claim = %v, want *ConflictError", err)
	}

	claimed, err := c.ClaimTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if claimed.Assignee != model.Actor(testActor) {
		t.Errorf("Assignee = %q, want %q", claimed.Assignee, testActor)
	}

	if _, err := c.RenewTask(ctx, task.ID); err != nil {
		t.Errorf("RenewTask after claim: %v", err)
	}

	done, err := c.DoneTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("DoneTask: %v", err)
	}
	if done.Status != model.StatusDone {
		t.Errorf("Status = %q, want done", done.Status)
	}
	if !slices.Contains(done.Commits, headSHA) {
		t.Errorf("Commits = %v, want to contain HEAD %s", done.Commits, headSHA)
	}

	// Closing an already-closed task conflicts.
	if _, err := c.CancelTask(ctx, task.ID); !isConflict(err) {
		t.Errorf("CancelTask on done task = %v, want *ConflictError", err)
	}
}

func TestStartTask(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")

	task, err := c.CreateTask(ctx, notes.TaskSpec{Title: "work", Branch: "backlog-ish"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := c.StartTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if started.Assignee != model.Actor(testActor) {
		t.Errorf("Assignee = %q, want %q", started.Assignee, testActor)
	}
	if started.Branch != "main" {
		t.Errorf("Branch = %q, want main (current branch)", started.Branch)
	}
}

func TestClaimConflict(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	task, err := c.CreateTask(ctx, notes.TaskSpec{Title: "contested", Branch: "main"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := c.ClaimTask(ctx, task.ID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	// A different actor cannot steal the claim.
	t.Setenv("CC_NOTES_ACTOR", "Other Agent <other@example.com>")
	if _, err := c.ClaimTask(ctx, task.ID); !isConflict(err) {
		t.Fatalf("second ClaimTask = %v, want *ConflictError", err)
	}
	reloaded, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if reloaded.Assignee != model.Actor(testActor) {
		t.Errorf("Assignee = %q, want original %q", reloaded.Assignee, testActor)
	}
}

func TestSprintTransitions(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	s, err := c.CreateSprint(ctx, notes.SprintSpec{Title: "S"})
	if err != nil {
		t.Fatalf("CreateSprint: %v", err)
	}
	if got, err := c.StartSprint(ctx, s.ID); err != nil || got.Status != model.SprintActive {
		t.Fatalf("StartSprint = %q/%v, want active/nil", got.Status, err)
	}
	if got, err := c.CompleteSprint(ctx, s.ID); err != nil || got.Status != model.SprintCompleted {
		t.Fatalf("CompleteSprint = %q/%v, want completed/nil", got.Status, err)
	}
	// A completed sprint refuses further transitions.
	if _, err := c.CancelSprint(ctx, s.ID); !isConflict(err) {
		t.Errorf("CancelSprint on completed = %v, want *ConflictError", err)
	}
}

func TestProjectTransitions(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	p, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "P"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if got, err := c.CompleteProject(ctx, p.ID); err != nil || got.Status != model.ProjectCompleted {
		t.Fatalf("CompleteProject = %q/%v, want completed/nil", got.Status, err)
	}
	if _, err := c.ArchiveProject(ctx, p.ID); !isConflict(err) {
		t.Errorf("ArchiveProject on completed = %v, want *ConflictError", err)
	}
}

func TestOpenNonGit(t *testing.T) {
	gittest.ScrubEnv(t)
	if _, err := notes.Open(t.TempDir()); err == nil {
		t.Fatal("Open on a non-git dir succeeded, want error")
	}
}

func TestCreateRejectsInvalid(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	for _, tc := range []struct {
		name string
		spec notes.TaskSpec
	}{
		{"bad priority", notes.TaskSpec{Title: "x", Branch: "main", Priority: 5}},
		{"bad branch", notes.TaskSpec{Title: "x", Branch: "bad branch"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := c.CreateTask(ctx, tc.spec); !errors.Is(err, model.ErrInvalidValue) {
				t.Fatalf("CreateTask(%+v) = %v, want ErrInvalidValue", tc.spec, err)
			}
		})
	}
}

// TestCreateDedupe proves the create helpers are idempotent over content: a
// second identical create converges on the first entity — same id, nil error,
// nothing new rooted. It covers a single-op pack (project) and a multi-op pack
// (task with a criterion).
func TestCreateDedupe(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	first, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "dup", Description: "d"})
	if err != nil {
		t.Fatalf("first CreateProject: %v", err)
	}
	again, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "dup", Description: "d"})
	if err != nil {
		t.Fatalf("second CreateProject: %v", err)
	}
	if again.ID != first.ID {
		t.Errorf("second CreateProject id = %s, want existing %s (idempotent)", again.ID, first.ID)
	}

	firstTask, err := c.CreateTask(ctx, notes.TaskSpec{Title: "t", Branch: "main", Criteria: []string{"builds"}})
	if err != nil {
		t.Fatalf("first CreateTask: %v", err)
	}
	againTask, err := c.CreateTask(ctx, notes.TaskSpec{Title: "t", Branch: "main", Criteria: []string{"builds"}})
	if err != nil {
		t.Fatalf("second CreateTask: %v", err)
	}
	if againTask.ID != firstTask.ID {
		t.Errorf("second CreateTask id = %s, want existing %s (idempotent)", againTask.ID, firstTask.ID)
	}
}

func isConflict(err error) bool {
	var conflict *notes.ConflictError
	return errors.As(err, &conflict)
}
