package cli_test

import (
	"strings"
	"testing"
)

// staleTaskJSON mirrors the task stale --json DTO: a task plus its idle seconds.
type staleTaskJSON struct {
	taskJSON
	IdleSeconds int64 `json:"idle_seconds"`
}

func TestTaskStaleThreshold(t *testing.T) {
	dir := initRepo(t)
	claimed := addTask(t, dir, "Claimed work")
	mustRun(t, dir, "task", "claim", claimed.ID)

	if out := mustRun(t, dir, "task", "stale", "--idle-after", "8760h"); out != "" {
		t.Fatalf("stale --idle-after 8760h = %q, want empty", out)
	}

	out := mustRun(t, dir, "task", "stale", "--idle-after", "0s")
	if !strings.Contains(out, claimed.ID[:7]) || !strings.Contains(out, "\tidle ") {
		t.Fatalf("stale --idle-after 0s = %q, want %s with an idle marker", out, claimed.ID[:7])
	}

	dtos := mustJSON[[]staleTaskJSON](t, mustRun(t, dir, "task", "stale", "--idle-after", "0s", "--json"))
	if len(dtos) != 1 || dtos[0].ID != claimed.ID {
		t.Fatalf("stale json = %+v, want only %s", dtos, claimed.ID)
	}
	if dtos[0].IdleSeconds < 0 {
		t.Fatalf("idle_seconds = %d, want >= 0", dtos[0].IdleSeconds)
	}
}

func TestTaskStaleOnlyInProgress(t *testing.T) {
	dir := initRepo(t)
	addTask(t, dir, "Open task")
	done := addTask(t, dir, "Done task")
	mustRun(t, dir, "task", "done", done.ID)
	cancelled := addTask(t, dir, "Cancelled task")
	mustRun(t, dir, "task", "cancel", cancelled.ID)
	inProgress := addTask(t, dir, "In progress task")
	mustRun(t, dir, "task", "claim", inProgress.ID)

	out := mustRun(t, dir, "task", "stale", "--idle-after", "0s")
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, inProgress.ID[:7]) {
		t.Fatalf("stale = %q, want only the in-progress task %s", out, inProgress.ID[:7])
	}
}

func TestTaskArchivedThreshold(t *testing.T) {
	dir := initRepo(t)
	done := addTask(t, dir, "Done task")
	mustRun(t, dir, "task", "done", done.ID)

	out := mustRun(t, dir, "task", "archived", "--closed-before", "0s")
	if !strings.Contains(out, done.ID[:7]) || strings.Count(out, "\n") != 1 {
		t.Fatalf("archived --closed-before 0s = %q, want %s", out, done.ID[:7])
	}

	if out := mustRun(t, dir, "task", "archived", "--closed-before", "8760h"); out != "" {
		t.Fatalf("archived --closed-before 8760h = %q, want empty", out)
	}

	out = mustRun(t, dir, "task", "archived", "--closed-before", "2099-01-01T00:00:00Z")
	if !strings.Contains(out, done.ID[:7]) {
		t.Fatalf("archived --closed-before absolute = %q, want %s", out, done.ID[:7])
	}
}

func TestTaskListExcludesArchived(t *testing.T) {
	dir := initRepo(t)
	addTask(t, dir, "Open task")
	done := addTask(t, dir, "Done task")
	mustRun(t, dir, "task", "done", done.ID)

	all := mustRun(t, dir, "task", "list", "--all")
	if !strings.Contains(all, done.ID[:7]) {
		t.Fatalf("list --all = %q, want fresh done task %s (not yet archived)", all, done.ID[:7])
	}
	withArchived := mustRun(t, dir, "task", "list", "--all", "--include-archived")
	if !strings.Contains(withArchived, done.ID[:7]) {
		t.Fatalf("list --all --include-archived = %q, want %s", withArchived, done.ID[:7])
	}
}

func TestTaskBacklog(t *testing.T) {
	dir := initRepo(t)
	back := addTask(t, dir, "Backlog item", "--backlog")
	addTask(t, dir, "Branch item")
	closed := addTask(t, dir, "Closed backlog", "--backlog")
	mustRun(t, dir, "task", "done", closed.ID)

	out := mustRun(t, dir, "task", "backlog")
	if !strings.Contains(out, back.ID[:7]) || strings.Count(out, "\n") != 1 {
		t.Fatalf("task backlog = %q, want only the open backlog task %s", out, back.ID[:7])
	}
	if equiv := mustRun(t, dir, "task", "list", "--backlog", "--status", "open"); equiv != out {
		t.Fatalf("task backlog = %q, want it to equal task list --backlog --status open = %q", out, equiv)
	}
}
