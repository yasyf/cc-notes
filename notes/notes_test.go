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

	if _, _, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "p"}); err != nil {
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

func TestCreateTask(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	p, _, _ := c.CreateProject(ctx, notes.ProjectSpec{Title: "P"})
	s, _, _ := c.CreateSprint(ctx, notes.SprintSpec{Title: "S", Project: p.ID})
	blocker, err := c.CreateTask(ctx, notes.TaskSpec{Title: "blocker", Branch: "main"})
	if err != nil {
		t.Fatalf("CreateTask blocker: %v", err)
	}

	created, err := c.CreateTask(ctx, notes.TaskSpec{
		Title:     "ship it",
		Branch:    "feature/x",
		Sprint:    s.ID,
		Project:   p.ID,
		Priority:  1,
		Criteria:  []string{"tests pass", "docs updated"},
		BlockedBy: []model.EntityID{blocker.Task.ID},
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	task := created.Task
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
	if want := []model.EntityID{blocker.Task.ID}; !slices.Equal(task.BlockedBy, want) {
		t.Errorf("BlockedBy = %v, want %v", task.BlockedBy, want)
	}

	// An empty Criteria slice creates a task with no acceptance criteria — the
	// in-process equivalent of the CLI's --no-validation-criteria.
	bare, err := c.CreateTask(ctx, notes.TaskSpec{Title: "bare", Branch: "main"})
	if err != nil {
		t.Fatalf("CreateTask bare: %v", err)
	}
	if len(bare.Task.Criteria) != 0 {
		t.Errorf("bare Criteria = %d, want 0", len(bare.Task.Criteria))
	}
}

// TestCreateTaskBranchDegrade proves an empty-branch, non-backlog spec resolves
// the current branch on an attached HEAD and degrades onto the backlog (empty
// branch, Degraded=true) on a genuinely unresolvable HEAD — the replacement for
// the old BranchFromHead hard error.
func TestCreateTaskBranchDegrade(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")

	onHead, err := c.CreateTask(ctx, notes.TaskSpec{Title: "on head"})
	if err != nil {
		t.Fatalf("CreateTask on attached HEAD: %v", err)
	}
	if onHead.Task.Branch != "main" {
		t.Errorf("Branch = %q, want main", onHead.Task.Branch)
	}
	if onHead.Degraded {
		t.Error("Degraded = true on attached HEAD, want false")
	}

	// An explicit Backlog spec lands on the backlog without degrading.
	backlog, err := c.CreateTask(ctx, notes.TaskSpec{Title: "explicit backlog", Backlog: true})
	if err != nil {
		t.Fatalf("CreateTask Backlog: %v", err)
	}
	if backlog.Degraded || backlog.Task.Branch != "" {
		t.Errorf("Backlog spec = Degraded %v branch %q, want false/empty", backlog.Degraded, backlog.Task.Branch)
	}

	// A genuinely unresolvable HEAD degrades onto the backlog: no trunk (main
	// never exists) and HEAD advanced past the sole bookmark.
	dc, ddir := newClient(t)
	gittest.Git(t, ddir, "checkout", "-q", "-b", "wip")
	gittest.Git(t, ddir, "commit", "-q", "--allow-empty", "-m", "c1")
	gittest.Git(t, ddir, "checkout", "-q", "--detach")
	gittest.Git(t, ddir, "commit", "-q", "--allow-empty", "-m", "c2")

	degraded, err := dc.CreateTask(ctx, notes.TaskSpec{Title: "detached"})
	if err != nil {
		t.Fatalf("CreateTask on unresolvable HEAD: %v", err)
	}
	if !degraded.Degraded {
		t.Error("Degraded = false on unresolvable HEAD, want true")
	}
	if degraded.Task.Branch != "" {
		t.Errorf("Branch = %q, want empty (degraded to backlog)", degraded.Task.Branch)
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

	loaded, err := c.Task(ctx, created.Task.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	if loaded.ID != created.Task.ID || loaded.Title != "t1" {
		t.Errorf("Task = %q/%q, want %q/t1", loaded.ID, loaded.Title, created.Task.ID)
	}

	tasks, err := c.Tasks(ctx, notes.TaskFilter{})
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
	p1, _, _ := c.CreateProject(ctx, notes.ProjectSpec{Title: "p1"})
	if _, _, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "p2"}); err != nil {
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

	created, err := c.CreateTask(ctx, notes.TaskSpec{Title: "work", Branch: "feature/x"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	task := created.Task

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

	done, err := c.DoneTask(ctx, task.ID, false)
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

	created, err := c.CreateTask(ctx, notes.TaskSpec{Title: "work", Branch: "backlog-ish"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	started, err := c.StartTask(ctx, created.Task.ID, "")
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if !started.BranchSet {
		t.Error("BranchSet = false, want true (attached HEAD resolves a branch)")
	}
	if started.Task.Assignee != model.Actor(testActor) {
		t.Errorf("Assignee = %q, want %q", started.Task.Assignee, testActor)
	}
	if started.Branch != "main" || started.Task.Branch != "main" {
		t.Errorf("Branch = %q/%q, want main (current branch)", started.Branch, started.Task.Branch)
	}
}

func TestClaimConflict(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	created, err := c.CreateTask(ctx, notes.TaskSpec{Title: "contested", Branch: "main"})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	task := created.Task
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

	first, _, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "dup", Description: "d"})
	if err != nil {
		t.Fatalf("first CreateProject: %v", err)
	}
	again, _, err := c.CreateProject(ctx, notes.ProjectSpec{Title: "dup", Description: "d"})
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
	if againTask.Task.ID != firstTask.Task.ID {
		t.Errorf("second CreateTask id = %s, want existing %s (idempotent)", againTask.Task.ID, firstTask.Task.ID)
	}
	if !againTask.Reused {
		t.Error("second CreateTask Reused = false, want true (idempotent)")
	}
}

func isConflict(err error) bool {
	var conflict *notes.ConflictError
	return errors.As(err, &conflict)
}
