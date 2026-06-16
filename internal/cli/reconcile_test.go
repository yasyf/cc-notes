package cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
)

// reconcileJSON mirrors the reconcile output DTO for round-trip assertions.
type reconcileJSON struct {
	Into     string `json:"into"`
	Scanned  int    `json:"scanned"`
	Merged   int    `json:"merged"`
	Promoted int    `json:"promoted"`
	Branches []struct {
		Branch string   `json:"branch"`
		Merged bool     `json:"merged"`
		Reason string   `json:"reason"`
		Tasks  []string `json:"tasks"`
	} `json:"branches"`
}

// mergedFeature sets up a repo whose feature/x branch is merged into main and
// carries one open task, returning the repo dir and the task's full id.
func mergedFeature(t *testing.T) (dir, taskID string) {
	t.Helper()
	dir = initRepo(t)
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	mustGit(t, dir, "checkout", "-q", "-b", "feature/x")
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "work")
	task := addTask(t, dir, "open task", "--branch", "feature/x")
	mustGit(t, dir, "checkout", "-q", "main")
	mustGit(t, dir, "merge", "-q", "--no-ff", "-m", "merge", "feature/x")
	return dir, task.ID
}

func TestReconcileLean(t *testing.T) {
	dir, taskID := mergedFeature(t)
	out := mustRun(t, dir, "reconcile")
	want := fmt.Sprintf("scanned: 1\nmerged: 1\npromoted: 1\ninto: main\nfeature/x:\n%s\topen\tP2\t-\topen task\n", taskID[:7])
	if out != want {
		t.Fatalf("reconcile output = %q, want %q", out, want)
	}
	// The task now lives on main and no longer on feature/x.
	if listed := mustRun(t, dir, "task", "list", "--branch", "main"); !strings.Contains(listed, taskID[:7]) {
		t.Errorf("task list --branch main = %q, want the promoted task", listed)
	}
	if listed := mustRun(t, dir, "task", "list", "--branch", "feature/x"); strings.TrimSpace(listed) != "" {
		t.Errorf("task list --branch feature/x = %q, want empty", listed)
	}
}

func TestReconcileJSON(t *testing.T) {
	dir, taskID := mergedFeature(t)
	dto := mustJSON[reconcileJSON](t, mustRun(t, dir, "reconcile", "--json"))
	if dto.Into != "main" || dto.Scanned != 1 || dto.Merged != 1 || dto.Promoted != 1 {
		t.Fatalf("reconcile DTO tallies = %+v, want into=main scanned/merged/promoted=1", dto)
	}
	if len(dto.Branches) != 1 {
		t.Fatalf("branches = %+v, want one", dto.Branches)
	}
	br := dto.Branches[0]
	if br.Branch != "feature/x" || !br.Merged || br.Reason != "" {
		t.Errorf("branch entry = %+v, want feature/x merged with no reason", br)
	}
	if len(br.Tasks) != 1 || br.Tasks[0] != taskID {
		t.Errorf("branch tasks = %v, want the full-hex id %q", br.Tasks, taskID)
	}
}

func TestReconcileDryRunWritesNothing(t *testing.T) {
	dir, taskID := mergedFeature(t)
	dto := mustJSON[reconcileJSON](t, mustRun(t, dir, "reconcile", "--dry-run", "--json"))
	if dto.Promoted != 1 {
		t.Fatalf("dry-run Promoted = %d, want 1 (the plan)", dto.Promoted)
	}
	if listed := mustRun(t, dir, "task", "list", "--branch", "feature/x"); !strings.Contains(listed, taskID[:7]) {
		t.Errorf("after dry-run, feature/x list = %q, want the task still there", listed)
	}
	if listed := mustRun(t, dir, "task", "list", "--branch", "main"); strings.TrimSpace(listed) != "" {
		t.Errorf("after dry-run, main list = %q, want empty", listed)
	}
}

func TestReconcileForceRequiresFrom(t *testing.T) {
	dir := initRepo(t)
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	_, _, err := runCLI(t, dir, "reconcile", "--force")
	if cli.ExitCode(err) != 2 {
		t.Fatalf("reconcile --force err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}
	if !strings.Contains(err.Error(), "--from") {
		t.Errorf("error %q does not name --from", err)
	}
}

func TestReconcileFromEqualsInto(t *testing.T) {
	dir := initRepo(t)
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	_, _, err := runCLI(t, dir, "reconcile", "--into", "main", "--from", "main")
	if cli.ExitCode(err) != 2 {
		t.Fatalf("reconcile --from main --into main err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}
	if !strings.Contains(err.Error(), "main") {
		t.Errorf("error %q does not name the offending branch", err)
	}
}

func TestReconcileDetachedHead(t *testing.T) {
	dir := initRepo(t)
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	mustGit(t, dir, "checkout", "-q", "--detach")
	_, _, err := runCLI(t, dir, "reconcile")
	if err == nil {
		t.Fatal("reconcile on detached HEAD: want error")
	}
	if !strings.Contains(err.Error(), "--into") {
		t.Errorf("detached-HEAD error %q does not name --into", err)
	}
}

func TestReconcileMissingIntoBranch(t *testing.T) {
	dir := initRepo(t)
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	_, _, err := runCLI(t, dir, "reconcile", "--into", "ghost")
	if cli.ExitCode(err) != 3 {
		t.Fatalf("reconcile --into ghost err = %v (exit %d), want ErrRefNotFound exit 3", err, cli.ExitCode(err))
	}
}
