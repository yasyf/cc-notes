package notes_test

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// mustTask creates a task and returns its folded snapshot, failing the test on
// error.
func mustTask(t *testing.T, c *notes.Client, spec notes.TaskSpec) model.Task {
	t.Helper()
	created, err := c.CreateTask(t.Context(), spec)
	if err != nil {
		t.Fatalf("CreateTask(%q): %v", spec.Title, err)
	}
	return created.Task
}

func TestClaimGuard(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	// A claimed task refuses a second claim from another actor.
	held := mustTask(t, c, notes.TaskSpec{Title: "held", Branch: "main"})
	if _, err := c.ClaimTask(ctx, held.ID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	t.Setenv("CC_NOTES_ACTOR", "Other <other@example.com>")
	_, err := c.ClaimTask(ctx, held.ID)
	if !isConflict(err) {
		t.Fatalf("claim of held task = %v, want *ConflictError", err)
	}
	if want := "already claimed by " + testActor; !strings.Contains(err.Error(), want) {
		t.Errorf("conflict message = %q, want to contain %q", err.Error(), want)
	}

	// A closed task refuses a claim as "not open".
	t.Setenv("CC_NOTES_ACTOR", testActor)
	closed := mustTask(t, c, notes.TaskSpec{Title: "closed", Branch: "main"})
	if _, err := c.CancelTask(ctx, closed.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}
	if _, err := c.ClaimTask(ctx, closed.ID); !isConflict(err) || !strings.Contains(err.Error(), "not open (cancelled)") {
		t.Fatalf("claim of cancelled task = %v, want *ConflictError not-open", err)
	}
}

func TestDoneGate(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	task := mustTask(t, c, notes.TaskSpec{Title: "gated", Branch: "main", Criteria: []string{"tests pass"}})

	// The gate refuses close while a criterion is unmet.
	_, err := c.DoneTask(ctx, task.ID, false)
	var unmet *notes.UnmetCriteriaError
	if !errors.As(err, &unmet) {
		t.Fatalf("DoneTask with unmet criterion = %v, want *UnmetCriteriaError", err)
	}
	if unmet.ID != task.ID || len(unmet.Unmet) != 1 {
		t.Fatalf("UnmetCriteriaError = id %s / %d unmet, want %s / 1", unmet.ID, len(unmet.Unmet), task.ID)
	}

	// force closes despite the unmet criterion.
	forced := mustTask(t, c, notes.TaskSpec{Title: "forced", Branch: "main", Criteria: []string{"skip"}})
	done, err := c.DoneTask(ctx, forced.ID, true)
	if err != nil {
		t.Fatalf("DoneTask force: %v", err)
	}
	if done.Status != model.StatusDone {
		t.Errorf("forced status = %q, want done", done.Status)
	}

	// Meeting the criterion lets the gate pass.
	crit := task.Criteria[0]
	if _, err := c.SetCriterionStatus(ctx, task.ID, crit.ID[:7], model.CriterionMet, ""); err != nil {
		t.Fatalf("SetCriterionStatus: %v", err)
	}
	if done, err := c.DoneTask(ctx, task.ID, false); err != nil || done.Status != model.StatusDone {
		t.Fatalf("DoneTask after met = %q/%v, want done/nil", done.Status, err)
	}
}

func TestAddDepCycle(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	a := mustTask(t, c, notes.TaskSpec{Title: "a", Branch: "main"})
	b := mustTask(t, c, notes.TaskSpec{Title: "b", Branch: "main"})

	if _, err := c.AddDep(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("AddDep a<-b: %v", err)
	}
	// b blocked by a would close the cycle a<-b<-a.
	_, err := c.AddDep(ctx, b.ID, a.ID)
	if !errors.Is(err, notes.ErrCycle) {
		t.Fatalf("AddDep b<-a = %v, want ErrCycle", err)
	}
	if want := b.ID.Short() + " already blocks " + a.ID.Short(); !strings.Contains(err.Error(), want) {
		t.Errorf("cycle message = %q, want to contain %q", err.Error(), want)
	}

	// Removing the edge lets the reverse edge land.
	if _, err := c.RemoveDep(ctx, a.ID, b.ID); err != nil {
		t.Fatalf("RemoveDep: %v", err)
	}
	if _, err := c.AddDep(ctx, b.ID, a.ID); err != nil {
		t.Fatalf("AddDep b<-a after remove: %v", err)
	}
}

func TestTaskFilters(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	urgent := mustTask(t, c, notes.TaskSpec{Title: "urgent", Branch: "main", Priority: 0})
	labeled := mustTask(t, c, notes.TaskSpec{Title: "labeled", Branch: "main", Priority: 3, Labels: []string{"x"}})
	mustTask(t, c, notes.TaskSpec{Title: "feat", Branch: "feat", Priority: 3})
	backlog := mustTask(t, c, notes.TaskSpec{Title: "backlog", Priority: 3, Backlog: true})

	titles := func(tasks []model.Task) []string {
		out := make([]string, len(tasks))
		for i, task := range tasks {
			out[i] = task.Title
		}
		return out
	}

	all, err := c.Tasks(ctx, notes.TaskFilter{Scope: notes.ScopeAllBranches})
	if err != nil {
		t.Fatalf("Tasks all: %v", err)
	}
	if len(all) != 4 || all[0].ID != urgent.ID {
		t.Fatalf("Tasks all = %v, want 4 with the priority-0 task first", titles(all))
	}

	named, err := c.Tasks(ctx, notes.TaskFilter{Scope: notes.ScopeNamed, Branch: "main"})
	if err != nil {
		t.Fatalf("Tasks named: %v", err)
	}
	if got := titles(named); len(got) != 2 || !slices.Contains(got, "urgent") || !slices.Contains(got, "labeled") || slices.Contains(got, "feat") {
		t.Fatalf("Tasks branch=main = %v, want the two main-branch tasks", got)
	}

	bl, err := c.Tasks(ctx, notes.TaskFilter{Scope: notes.ScopeBacklog})
	if err != nil {
		t.Fatalf("Tasks backlog: %v", err)
	}
	if len(bl) != 1 || bl[0].ID != backlog.ID {
		t.Fatalf("Tasks backlog = %v, want only the backlog task", titles(bl))
	}

	byLabel, err := c.Tasks(ctx, notes.TaskFilter{Scope: notes.ScopeAllBranches, Labels: []string{"x"}})
	if err != nil {
		t.Fatalf("Tasks label: %v", err)
	}
	if len(byLabel) != 1 || byLabel[0].ID != labeled.ID {
		t.Fatalf("Tasks label=x = %v, want only the labeled task", titles(byLabel))
	}
}

func TestReadyTasks(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	blocker := mustTask(t, c, notes.TaskSpec{Title: "blocker", Branch: "main"})
	blocked := mustTask(t, c, notes.TaskSpec{Title: "blocked", Branch: "main", BlockedBy: []model.EntityID{blocker.ID}})
	free := mustTask(t, c, notes.TaskSpec{Title: "free", Branch: "main"})

	ready, err := c.ReadyTasks(ctx, notes.ScopeNamed, "main")
	if err != nil {
		t.Fatalf("ReadyTasks: %v", err)
	}
	ids := func(tasks []model.Task) []model.EntityID {
		out := make([]model.EntityID, len(tasks))
		for i, task := range tasks {
			out[i] = task.ID
		}
		return out
	}
	got := ids(ready)
	if !slices.Contains(got, blocker.ID) || !slices.Contains(got, free.ID) || slices.Contains(got, blocked.ID) {
		t.Fatalf("ReadyTasks = %v, want blocker+free unblocked and blocked excluded", got)
	}

	// Once the blocker is done, the blocked task becomes ready.
	if _, err := c.ClaimTask(ctx, blocker.ID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	if _, err := c.DoneTask(ctx, blocker.ID, false); err != nil {
		t.Fatalf("DoneTask: %v", err)
	}
	ready, err = c.ReadyTasks(ctx, notes.ScopeNamed, "main")
	if err != nil {
		t.Fatalf("ReadyTasks after unblock: %v", err)
	}
	if got := ids(ready); !slices.Contains(got, blocked.ID) {
		t.Fatalf("ReadyTasks after unblock = %v, want to contain the now-unblocked task", got)
	}
}

func TestStaleAndArchivedTasks(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")

	inProgress := mustTask(t, c, notes.TaskSpec{Title: "in progress", Branch: "main"})
	if _, err := c.ClaimTask(ctx, inProgress.ID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}
	closed := mustTask(t, c, notes.TaskSpec{Title: "closed", Branch: "main"})
	if _, err := c.CancelTask(ctx, closed.ID); err != nil {
		t.Fatalf("CancelTask: %v", err)
	}

	stale, err := c.StaleTasks(ctx, 0)
	if err != nil {
		t.Fatalf("StaleTasks: %v", err)
	}
	if len(stale) != 1 || stale[0].ID != inProgress.ID {
		t.Fatalf("StaleTasks(0) = %d tasks, want only the in-progress claim", len(stale))
	}

	archived, err := c.ArchivedTasks(ctx, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("ArchivedTasks: %v", err)
	}
	if len(archived) != 1 || archived[0].ID != closed.ID {
		t.Fatalf("ArchivedTasks = %d tasks, want only the cancelled task", len(archived))
	}
}

func TestStealTask(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	task := mustTask(t, c, notes.TaskSpec{Title: "crashed", Branch: "main"})
	if _, err := c.ClaimTask(ctx, task.ID); err != nil {
		t.Fatalf("ClaimTask: %v", err)
	}

	// Fresh lease: a long ttl refuses the steal with the time remaining.
	t.Setenv("CC_NOTES_ACTOR", "Other <other@example.com>")
	_, err := c.StealTask(ctx, task.ID, 8760*time.Hour)
	if !isConflict(err) || !strings.Contains(err.Error(), "lease held by "+testActor) {
		t.Fatalf("StealTask fresh lease = %v, want *ConflictError lease-held", err)
	}

	// Stale lease: a zero ttl reclaims it.
	stolen, err := c.StealTask(ctx, task.ID, 0)
	if err != nil {
		t.Fatalf("StealTask stale lease: %v", err)
	}
	if stolen.Assignee != "Other <other@example.com>" || stolen.Status != model.StatusInProgress {
		t.Fatalf("after steal assignee/status = %q/%q, want Other/in_progress", stolen.Assignee, stolen.Status)
	}
}

func TestClaimTaskSync(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	remote := gittest.InitBare(t)
	gittest.Git(t, dir, "remote", "add", "origin", remote)

	task := mustTask(t, c, notes.TaskSpec{Title: "sync me", Branch: "main"})
	claimed, err := c.ClaimTaskSync(ctx, task.ID)
	if err != nil {
		t.Fatalf("ClaimTaskSync: %v", err)
	}
	if claimed.Assignee != model.Actor(testActor) || claimed.Status != model.StatusInProgress {
		t.Fatalf("ClaimTaskSync assignee/status = %q/%q, want %s/in_progress", claimed.Assignee, claimed.Status, testActor)
	}
}

func TestEditTask(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	task := mustTask(t, c, notes.TaskSpec{Title: "orig", Branch: "main", Labels: []string{"drop"}})

	// An empty mask is refused before any write.
	if _, err := c.EditTask(ctx, task.ID, notes.TaskEdit{}); !errors.Is(err, notes.ErrEmptyEdit) {
		t.Fatalf("EditTask empty mask = %v, want ErrEmptyEdit", err)
	}

	title := "renamed"
	prio := model.Priority(0)
	backlog := model.Branch("")
	edited, err := c.EditTask(ctx, task.ID, notes.TaskEdit{
		Title:        &title,
		Priority:     &prio,
		AddLabels:    []string{"keep"},
		RemoveLabels: []string{"drop"},
		Branch:       &backlog,
	})
	if err != nil {
		t.Fatalf("EditTask: %v", err)
	}
	if edited.Title != "renamed" || edited.Priority != 0 || edited.Branch != "" {
		t.Errorf("edited = title %q priority %d branch %q, want renamed/0/empty", edited.Title, edited.Priority, edited.Branch)
	}
	if slices.Contains(edited.Labels, "drop") || !slices.Contains(edited.Labels, "keep") {
		t.Errorf("labels = %v, want keep without drop", edited.Labels)
	}
}

func TestCriteriaLifecycle(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	task := mustTask(t, c, notes.TaskSpec{Title: "crit", Branch: "main"})

	added, err := c.AddCriterion(ctx, task.ID, "builds clean", "exit 0")
	if err != nil {
		t.Fatalf("AddCriterion: %v", err)
	}
	if len(added.Criteria) != 1 || added.Criteria[0].Script != "exit 0" {
		t.Fatalf("added criteria = %+v, want one with script 'exit 0'", added.Criteria)
	}
	crit := added.Criteria[0]

	// ResolveCriterion on a unique prefix, and its error paths.
	if got, err := notes.ResolveCriterion(added, crit.ID[:7]); err != nil || got.ID != crit.ID {
		t.Fatalf("ResolveCriterion(prefix) = %q/%v, want %s/nil", got.ID, err, crit.ID)
	}
	if _, err := notes.ResolveCriterion(added, "zzzzzzz"); !errors.Is(err, notes.ErrNotFound) {
		t.Errorf("ResolveCriterion(unknown) = %v, want ErrNotFound", err)
	}

	if _, err := c.SetCriterionScript(ctx, task.ID, crit.ID[:7], ""); err != nil {
		t.Fatalf("SetCriterionScript clear: %v", err)
	}
	met, err := c.SetCriterionStatus(ctx, task.ID, crit.ID[:7], model.CriterionMet, "verified by hand")
	if err != nil {
		t.Fatalf("SetCriterionStatus: %v", err)
	}
	if got := met.Criteria[0]; got.Status != model.CriterionMet || got.Note != "verified by hand" {
		t.Fatalf("met criterion = %q/%q, want met/\"verified by hand\"", got.Status, got.Note)
	}

	// A later note-less verdict clears the note: Note is LWW with the status.
	cleared, err := c.SetCriterionStatus(ctx, task.ID, crit.ID[:7], model.CriterionFailed, "")
	if err != nil {
		t.Fatalf("SetCriterionStatus clear note: %v", err)
	}
	if got := cleared.Criteria[0]; got.Status != model.CriterionFailed || got.Note != "" {
		t.Fatalf("cleared criterion = %q/%q, want failed/empty note", got.Status, got.Note)
	}

	removed, err := c.RemoveCriterion(ctx, task.ID, crit.ID[:7])
	if err != nil {
		t.Fatalf("RemoveCriterion: %v", err)
	}
	if len(removed.Criteria) != 0 {
		t.Errorf("after remove, criteria = %d, want 0", len(removed.Criteria))
	}
}

func TestValidateTask(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	task := mustTask(t, c, notes.TaskSpec{Title: "validate", Branch: "main"})
	pass, err := c.AddCriterion(ctx, task.ID, "passes", "exit 0")
	if err != nil {
		t.Fatalf("AddCriterion pass: %v", err)
	}
	if _, err := c.AddCriterion(ctx, task.ID, "fails", "exit 1"); err != nil {
		t.Fatalf("AddCriterion fail: %v", err)
	}
	_ = pass

	loaded, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("Task: %v", err)
	}
	var scripted []model.Criterion
	for _, crit := range loaded.Criteria {
		if crit.Script != "" {
			scripted = append(scripted, crit)
		}
	}
	if len(scripted) != 2 {
		t.Fatalf("scripted criteria = %d, want 2", len(scripted))
	}

	verdicts := map[string]model.CriterionStatus{}
	validated, err := c.ValidateTask(ctx, task.ID, scripted, 30*time.Second, func(crit model.Criterion, status model.CriterionStatus) error {
		verdicts[crit.Text] = status
		return nil
	})
	if err != nil {
		t.Fatalf("ValidateTask: %v", err)
	}
	if verdicts["passes"] != model.CriterionMet || verdicts["fails"] != model.CriterionFailed {
		t.Fatalf("onVerdict = %v, want passes met / fails failed", verdicts)
	}
	statuses := map[string]model.CriterionStatus{}
	for _, crit := range validated.Criteria {
		statuses[crit.Text] = crit.Status
	}
	if statuses["passes"] != model.CriterionMet || statuses["fails"] != model.CriterionFailed {
		t.Fatalf("folded statuses = %v, want passes met / fails failed", statuses)
	}
}

func TestStartTaskDegrade(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	// Genuinely unresolvable HEAD: no trunk, advanced past the sole bookmark.
	gittest.Git(t, dir, "checkout", "-q", "-b", "wip")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c1")
	task := mustTask(t, c, notes.TaskSpec{Title: "detached start", Backlog: true})
	gittest.Git(t, dir, "checkout", "-q", "--detach")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c2")

	started, err := c.StartTask(ctx, task.ID, "")
	if err != nil {
		t.Fatalf("StartTask: %v", err)
	}
	if started.BranchSet {
		t.Error("BranchSet = true on unresolvable HEAD, want false")
	}
	if started.Task.Branch != "" || started.Task.Assignee != model.Actor(testActor) {
		t.Errorf("started task branch/assignee = %q/%q, want empty/%s", started.Task.Branch, started.Task.Assignee, testActor)
	}
}

// TestStartTaskResolveFailureNoMutation pins the resolve-before-mutate
// atomicity: when branch resolution hard-errors (a corrupt origin/HEAD pointing
// at a missing ref, distinct from the ErrDetachedHead degrade), StartTask must
// return the error without having claimed the task.
func TestStartTaskResolveFailureNoMutation(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c1")
	task := mustTask(t, c, notes.TaskSpec{Title: "no mutation", Backlog: true})

	// Detach, then point origin/HEAD at a remote branch that does not exist:
	// TrunkBranch resolves the name but no ref backs it, so branch resolution
	// fails hard rather than degrading.
	gittest.Git(t, dir, "checkout", "-q", "--detach")
	gittest.Git(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/nonexistent")

	_, err := c.StartTask(ctx, task.ID, "")
	if err == nil {
		t.Fatal("StartTask on unresolvable trunk = nil, want a hard error")
	}
	if isConflict(err) || errors.Is(err, notes.ErrDetachedHead) {
		t.Fatalf("StartTask error = %v, want a non-conflict, non-detached hard error", err)
	}

	reloaded, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if reloaded.Status != model.StatusOpen || reloaded.Assignee != "" {
		t.Fatalf("after failed StartTask, task = status %q assignee %q, want open/unassigned (no Claim)", reloaded.Status, reloaded.Assignee)
	}
}

// TestCreateTaskBacklogOverridesBranch pins the contract that Backlog puts a
// task on the backlog unconditionally, even when Branch is set.
func TestCreateTaskBacklogOverridesBranch(t *testing.T) {
	c, _ := newClient(t)
	created, err := c.CreateTask(t.Context(), notes.TaskSpec{Title: "override", Branch: "main", Backlog: true})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if created.Task.Branch != "" {
		t.Fatalf("task branch = %q, want empty (Backlog overrides Branch)", created.Task.Branch)
	}
}

// TestCreateTaskReusedSuppressesDegrade pins that a create that dedupes onto an
// existing task never also reports Degraded: it rooted nothing, so it degraded
// nothing.
func TestCreateTaskReusedSuppressesDegrade(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	first := mustTask(t, c, notes.TaskSpec{Title: "dup detach", Backlog: true})

	// Advance to a genuinely unresolvable HEAD, then re-create the identical
	// task with an empty branch: it degrades to the backlog and dedupes onto
	// first.
	gittest.Git(t, dir, "checkout", "-q", "-b", "wip")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c1")
	gittest.Git(t, dir, "checkout", "-q", "--detach")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c2")

	again, err := c.CreateTask(ctx, notes.TaskSpec{Title: "dup detach"})
	if err != nil {
		t.Fatalf("second CreateTask: %v", err)
	}
	if !again.Reused || again.Task.ID != first.ID {
		t.Fatalf("second CreateTask = reused %v id %s, want reused onto %s", again.Reused, again.Task.ID, first.ID)
	}
	if again.Degraded {
		t.Fatal("Reused create reported Degraded = true; a converged create rooted nothing on the backlog")
	}
}

// TestValidateTaskVerdictErrorNoPersist pins that an onVerdict error aborts the
// run before any verdict is written — matching the pre-migration CLI, whose
// result-output failure returned ahead of the append.
func TestValidateTaskVerdictErrorNoPersist(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	task := mustTask(t, c, notes.TaskSpec{Title: "abort", Branch: "main"})
	added, err := c.AddCriterion(ctx, task.ID, "passes", "exit 0")
	if err != nil {
		t.Fatalf("AddCriterion: %v", err)
	}
	scripted := []model.Criterion{added.Criteria[0]}

	sentinel := errors.New("output failed")
	_, err = c.ValidateTask(ctx, task.ID, scripted, 30*time.Second, func(model.Criterion, model.CriterionStatus) error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("ValidateTask = %v, want the onVerdict error", err)
	}

	reloaded, err := c.Task(ctx, task.ID)
	if err != nil {
		t.Fatalf("reload task: %v", err)
	}
	if got := reloaded.Criteria[0].Status; got != model.CriterionPending {
		t.Fatalf("criterion status = %q, want pending (no verdict persisted after the abort)", got)
	}
}

func TestCommentTask(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	task := mustTask(t, c, notes.TaskSpec{Title: "chatty", Branch: "main"})
	commented, err := c.CommentTask(ctx, task.ID, "looks good")
	if err != nil {
		t.Fatalf("CommentTask: %v", err)
	}
	if len(commented.Comments) != 1 || commented.Comments[0].Body != "looks good" {
		t.Fatalf("comments = %+v, want one 'looks good'", commented.Comments)
	}
}
