package cli_test

import (
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// loadCriterion reads a criterion straight from the notes client so the test
// witnesses the stored Note field directly, independent of the output DTO.
func loadCriterion(t *testing.T, dir, taskID, critID string) model.Criterion {
	t.Helper()
	c, err := notes.Open(dir)
	if err != nil {
		t.Fatalf("notes.Open: %v", err)
	}
	ctx := t.Context()
	id, err := c.ResolveTask(ctx, taskID)
	if err != nil {
		t.Fatalf("ResolveTask %s: %v", taskID, err)
	}
	task, err := c.Task(ctx, id)
	if err != nil {
		t.Fatalf("Task %s: %v", taskID, err)
	}
	for _, cr := range task.Criteria {
		if cr.ID == critID {
			return cr
		}
	}
	t.Fatalf("criterion %s not found on task %s", critID, taskID)
	return model.Criterion{}
}

// lastComment returns the body of the most recently appended comment.
func lastComment(t *testing.T, dir, taskID string) string {
	t.Helper()
	task := mustJSON[taskJSON](t, mustRun(t, dir, "task", "show", taskID, "--json"))
	if len(task.Comments) == 0 {
		t.Fatalf("task %s has no comments", taskID)
	}
	return task.Comments[len(task.Comments)-1].Body
}

func TestCriterionNoteRoundTrip(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "Ship it")
	withCrit := mustJSON[taskJSON](t, mustRun(t, dir, "task", "criterion", "add", task.ID, "all tests pass", "--json"))
	if len(withCrit.Criteria) != 1 {
		t.Fatalf("criteria after add = %d, want 1", len(withCrit.Criteria))
	}
	crit := withCrit.Criteria[0].ID

	mustRun(t, dir, "task", "criterion", "met", task.ID, crit, "--note", "go test: 12 passed")
	got := loadCriterion(t, dir, task.ID, crit)
	if got.Status != model.CriterionMet {
		t.Errorf("status after met = %q, want %q", got.Status, model.CriterionMet)
	}
	if got.Note != "go test: 12 passed" {
		t.Errorf("note after met = %q, want %q", got.Note, "go test: 12 passed")
	}

	// Re-marking met without --note clears the prior evidence (LWW): the empty
	// note overwrites, and it marshals omitempty to a bare snapshot.
	mustRun(t, dir, "task", "criterion", "met", task.ID, crit)
	got = loadCriterion(t, dir, task.ID, crit)
	if got.Status != model.CriterionMet {
		t.Errorf("status after re-met = %q, want %q", got.Status, model.CriterionMet)
	}
	if got.Note != "" {
		t.Errorf("note after re-met = %q, want cleared", got.Note)
	}

	// failed carries a note too.
	mustRun(t, dir, "task", "criterion", "failed", task.ID, crit, "--note", "flaky assertion")
	got = loadCriterion(t, dir, task.ID, crit)
	if got.Status != model.CriterionFailed || got.Note != "flaky assertion" {
		t.Errorf("failed status/note = %q/%q, want %q/%q", got.Status, got.Note, model.CriterionFailed, "flaky assertion")
	}
}

// TestCriterionNoteFlagScope pins that --note lives on met and failed but not on
// pending: a reset to pending clears evidence with no flag to carry one.
func TestCriterionNoteFlagScope(t *testing.T) {
	root := cli.NewRootCmd()
	for _, tc := range []struct {
		verb string
		want bool
	}{
		{"met", true},
		{"failed", true},
		{"pending", false},
	} {
		cmd, _, err := root.Find([]string{"task", "criterion", tc.verb})
		if err != nil {
			t.Fatalf("find task criterion %s: %v", tc.verb, err)
		}
		if got := cmd.Flags().Lookup("note") != nil; got != tc.want {
			t.Errorf("task criterion %s has --note = %v, want %v", tc.verb, got, tc.want)
		}
	}
}

func TestTaskAddBodyForms(t *testing.T) {
	dir := initRepo(t)

	pos := mustJSON[taskJSON](t, mustRun(t, dir, "task", "add", "Pos", "positional body", "--no-validation-criteria", "--json"))
	if pos.Description != "positional body" {
		t.Errorf("positional body desc = %q, want %q", pos.Description, "positional body")
	}

	flag := mustJSON[taskJSON](t, mustRun(t, dir, "task", "add", "Flag", "--body", "flag body", "--no-validation-criteria", "--json"))
	if flag.Description != "flag body" {
		t.Errorf("--body desc = %q, want %q", flag.Description, "flag body")
	}

	stdout, stderr, err := runCLIIn(t, dir, "stdin body\n", "task", "add", "Stdin", "-", "--no-validation-criteria", "--json")
	if err != nil {
		t.Fatalf("stdin add: %v (stderr %q)", err, stderr)
	}
	if s := mustJSON[taskJSON](t, stdout); s.Description != "stdin body" {
		t.Errorf("stdin desc = %q, want %q", s.Description, "stdin body")
	}

	none := mustJSON[taskJSON](t, mustRun(t, dir, "task", "add", "None", "--no-validation-criteria", "--json"))
	if none.Description != "" {
		t.Errorf("no-body desc = %q, want empty", none.Description)
	}

	// Positional BODY and --body together is a usage error, exit 2.
	if _, _, err := runCLI(t, dir, "task", "add", "Conf", "pos", "--body", "flag", "--no-validation-criteria"); err == nil || cli.ExitCode(err) != 2 {
		t.Fatalf("conflicting body sources err = %v (exit %d), want exit 2", err, cli.ExitCode(err))
	}
}

func TestTaskCommentBodyForms(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "Commented")

	mustRun(t, dir, "task", "comment", task.ID, "positional c")
	if got := lastComment(t, dir, task.ID); got != "positional c" {
		t.Errorf("positional comment = %q, want %q", got, "positional c")
	}

	mustRun(t, dir, "task", "comment", task.ID, "--body", "flag c")
	if got := lastComment(t, dir, task.ID); got != "flag c" {
		t.Errorf("--body comment = %q, want %q", got, "flag c")
	}

	if _, stderr, err := runCLIIn(t, dir, "stdin c\n", "task", "comment", task.ID, "-"); err != nil {
		t.Fatalf("stdin comment: %v (stderr %q)", err, stderr)
	}
	if got := lastComment(t, dir, task.ID); got != "stdin c" {
		t.Errorf("stdin comment = %q, want %q", got, "stdin c")
	}

	// Positional BODY and --body together is a usage error, exit 2.
	if _, _, err := runCLI(t, dir, "task", "comment", task.ID, "pos", "--body", "flag"); err == nil || cli.ExitCode(err) != 2 {
		t.Fatalf("conflicting comment sources err = %v (exit %d), want exit 2", err, cli.ExitCode(err))
	}
	// A comment with no text at all is a usage error, exit 2.
	if _, _, err := runCLI(t, dir, "task", "comment", task.ID); err == nil || cli.ExitCode(err) != 2 {
		t.Fatalf("empty comment err = %v (exit %d), want exit 2", err, cli.ExitCode(err))
	}
}

func TestCriterionAddTextForms(t *testing.T) {
	dir := initRepo(t)

	posTask := addTask(t, dir, "Pos")
	pos := mustJSON[taskJSON](t, mustRun(t, dir, "task", "criterion", "add", posTask.ID, "positional crit", "--json"))
	if pos.Criteria[0].Text != "positional crit" {
		t.Errorf("positional crit text = %q, want %q", pos.Criteria[0].Text, "positional crit")
	}

	flagTask := addTask(t, dir, "Flag")
	flag := mustJSON[taskJSON](t, mustRun(t, dir, "task", "criterion", "add", flagTask.ID, "--body", "flag crit", "--json"))
	if flag.Criteria[0].Text != "flag crit" {
		t.Errorf("--body crit text = %q, want %q", flag.Criteria[0].Text, "flag crit")
	}

	stdinTask := addTask(t, dir, "Stdin")
	stdout, stderr, err := runCLIIn(t, dir, "stdin crit\n", "task", "criterion", "add", stdinTask.ID, "-", "--json")
	if err != nil {
		t.Fatalf("stdin crit add: %v (stderr %q)", err, stderr)
	}
	if s := mustJSON[taskJSON](t, stdout); s.Criteria[0].Text != "stdin crit" {
		t.Errorf("stdin crit text = %q, want %q", s.Criteria[0].Text, "stdin crit")
	}

	confTask := addTask(t, dir, "Conf")
	if _, _, err := runCLI(t, dir, "task", "criterion", "add", confTask.ID, "pos", "--body", "flag"); err == nil || cli.ExitCode(err) != 2 {
		t.Fatalf("conflicting crit sources err = %v (exit %d), want exit 2", err, cli.ExitCode(err))
	}
	missTask := addTask(t, dir, "Miss")
	if _, _, err := runCLI(t, dir, "task", "criterion", "add", missTask.ID); err == nil || cli.ExitCode(err) != 2 {
		t.Fatalf("textless crit err = %v (exit %d), want exit 2", err, cli.ExitCode(err))
	}
}

// TestTaskFlagExclusionsExit2 pins that every cobra MarkFlagsMutuallyExclusive
// group on the task noun rejects both flags at once with exit 2.
func TestTaskFlagExclusionsExit2(t *testing.T) {
	dir := initRepo(t)
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"claim steal/sync", []string{"task", "claim", "deadbeef", "--steal", "--sync"}},
		{"edit assignee", []string{"task", "edit", "x", "--assignee", "a", "--no-assignee"}},
		{"edit parent", []string{"task", "edit", "x", "--parent", "p", "--no-parent"}},
		{"edit sprint", []string{"task", "edit", "x", "--sprint", "s", "--no-sprint"}},
		{"edit project", []string{"task", "edit", "x", "--project", "pr", "--no-project"}},
		{"edit branch/backlog", []string{"task", "edit", "x", "--branch", "b", "--backlog"}},
		{"add branch/backlog", []string{"task", "add", "T", "--branch", "b", "--backlog"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := runCLI(t, dir, tc.args...)
			if err == nil || cli.ExitCode(err) != 2 {
				t.Fatalf("cc-notes %s err = %v (exit %d), want exit 2", strings.Join(tc.args, " "), err, cli.ExitCode(err))
			}
		})
	}
}

// TestTaskEditLabelEdits pins that the labelEdits binder drives --add-label and
// --rm-label unchanged: an add and a remove in one edit converge on the sorted
// survivors.
func TestTaskEditLabelEdits(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "Labeled", "--label", "a", "--label", "b")
	edited := mustJSON[taskJSON](t, mustRun(t, dir, "task", "edit", task.ID, "--add-label", "c", "--rm-label", "a", "--json"))
	if got := strings.Join(edited.Labels, ","); got != "b,c" {
		t.Errorf("labels after add c / rm a = %q, want %q", got, "b,c")
	}
}
