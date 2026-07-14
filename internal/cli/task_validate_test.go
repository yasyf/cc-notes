package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/store"
)

// withFileStdin points os.Stdin at an empty regular file for the test's
// lifetime, so task validate's interactivity probe (which inspects os.Stdin's
// mode) deterministically sees a non-terminal regardless of how the suite is
// invoked. The package runs its tests sequentially, so the global swap is safe.
func withFileStdin(t *testing.T) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatalf("write stdin file: %v", err)
	}
	//nolint:gosec // G304: opens the stdin file under the test's own temp dir.
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open stdin file: %v", err)
	}
	saved := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = saved
		_ = f.Close()
	})
}

func TestTaskAddCriteriaRequired(t *testing.T) {
	dir := spInitRepo(t)

	_, _, err := spRun(t, dir, "", "task", "add", "Needs criteria")
	if !isUsage(err) {
		t.Fatalf("task add without criteria err = %v, want UsageError exit 2", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "--criterion") || !strings.Contains(msg, "--no-validation-criteria") {
		t.Errorf("error %q must name both --criterion and --no-validation-criteria", msg)
	}

	_, _, err = spRun(t, dir, "", "task", "add", "Both", "--criterion", "x", "--no-validation-criteria")
	if !isUsage(err) {
		t.Fatalf("task add --criterion + --no-validation-criteria err = %v, want UsageError exit 2", err)
	}

	added := spJSON[taskDTO](t, spMust(t, dir, "task", "add", "Free", "--no-validation-criteria", "--json"))
	if len(added.Criteria) != 0 {
		t.Errorf("criteria = %v, want empty under --no-validation-criteria", added.Criteria)
	}
}

func TestTaskAddCriteriaAndMembership(t *testing.T) {
	dir := spInitRepo(t)
	proj := spID(t, spMust(t, dir, "project", "add", "P", "--json"))
	sp := spID(t, spMust(t, dir, "sprint", "add", "S", "--json"))

	task := spJSON[taskDTO](t, spMust(t, dir, "task", "add", "Work",
		"--criterion", "first", "--criterion", "second",
		"--sprint", sp, "--project", proj, "--json"))

	if len(task.Criteria) != 2 || task.Criteria[0].Text != "first" || task.Criteria[1].Text != "second" {
		t.Fatalf("criteria = %+v, want [first second] in order", task.Criteria)
	}
	for _, c := range task.Criteria {
		if c.Status != "pending" || c.Script != "" {
			t.Errorf("criterion %+v, want pending with empty script", c)
		}
		if len(c.ID) != 32 {
			t.Errorf("criterion id %q len = %d, want 32 (nonce)", c.ID, len(c.ID))
		}
	}
	if task.Sprint == nil || *task.Sprint != sp {
		t.Errorf("sprint = %v, want %s", task.Sprint, sp)
	}
	if task.Project == nil || *task.Project != proj {
		t.Errorf("project = %v, want %s", task.Project, proj)
	}
}

func TestTaskDoneCriteriaGate(t *testing.T) {
	dir := spInitRepo(t)
	task := spJSON[taskDTO](t, spMust(t, dir, "task", "add", "Gated", "--criterion", "must pass", "--json"))
	crit := task.Criteria[0]

	_, _, err := spRun(t, dir, "", "task", "done", task.ID)
	if !isUsage(err) {
		t.Fatalf("done with unmet criterion err = %v, want UsageError exit 2", err)
	}
	if msg := err.Error(); !strings.Contains(msg, crit.ID[:7]) || !strings.Contains(msg, "must pass") || !strings.Contains(msg, "--force") {
		t.Errorf("gate error %q must list the criterion (%s / text) and instruct --force", msg, crit.ID[:7])
	}
	shown := spJSON[taskDTO](t, spMust(t, dir, "task", "show", task.ID, "--json"))
	if shown.Status != "open" {
		t.Fatalf("status = %q after blocked done, want open (nothing written)", shown.Status)
	}

	forced := spJSON[taskDTO](t, spMust(t, dir, "task", "done", task.ID, "--force", "--json"))
	if forced.Status != "done" || !forced.ClosedForced {
		t.Fatalf("forced done = status %q closed_forced %v, want done/true", forced.Status, forced.ClosedForced)
	}

	met := spJSON[taskDTO](t, spMust(t, dir, "task", "add", "Met", "--criterion", "c", "--json"))
	spMust(t, dir, "task", "criterion", "met", met.ID, met.Criteria[0].ID[:7])
	done := spJSON[taskDTO](t, spMust(t, dir, "task", "done", met.ID, "--json"))
	if done.Status != "done" || done.ClosedForced {
		t.Fatalf("done with met criterion = status %q closed_forced %v, want done/false", done.Status, done.ClosedForced)
	}
}

func TestTaskCriterionRoundTrip(t *testing.T) {
	dir := spInitRepo(t)
	task := spJSON[taskDTO](t, spMust(t, dir, "task", "add", "T", "--no-validation-criteria", "--json"))

	added := spJSON[taskDTO](t, spMust(t, dir, "task", "criterion", "add", task.ID, "first criterion", "--json"))
	if len(added.Criteria) != 1 || added.Criteria[0].Text != "first criterion" || added.Criteria[0].Status != "pending" {
		t.Fatalf("after add criteria = %+v, want one pending 'first criterion'", added.Criteria)
	}
	cid := added.Criteria[0].ID

	for _, tc := range []struct {
		verb, want string
	}{
		{"met", "met"},
		{"failed", "failed"},
		{"pending", "pending"},
	} {
		out := spJSON[taskDTO](t, spMust(t, dir, "task", "criterion", tc.verb, task.ID, cid[:7], "--json"))
		if out.Criteria[0].Status != tc.want {
			t.Errorf("criterion %s -> status %q, want %q", tc.verb, out.Criteria[0].Status, tc.want)
		}
	}

	scriptFile := filepath.Join(dir, "check.sh")
	if err := os.WriteFile(scriptFile, []byte("exit 0"), 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	scripted := spJSON[taskDTO](t, spMust(t, dir, "task", "criterion", "script", task.ID, cid[:7], scriptFile, "--json"))
	if scripted.Criteria[0].Script != "exit 0" {
		t.Errorf("script = %q, want %q", scripted.Criteria[0].Script, "exit 0")
	}
	cleared := spJSON[taskDTO](t, spMust(t, dir, "task", "criterion", "script", task.ID, cid[:7], "--clear", "--json"))
	if cleared.Criteria[0].Script != "" {
		t.Errorf("script = %q after --clear, want empty", cleared.Criteria[0].Script)
	}

	listed := spJSON[[]criterionDTO](t, spMust(t, dir, "task", "criterion", "list", task.ID, "--json"))
	if len(listed) != 1 || listed[0].ID != cid {
		t.Fatalf("criterion list = %+v, want one with id %s", listed, cid)
	}
	lean := spMust(t, dir, "task", "criterion", "list", task.ID)
	if want := cid[:7] + "\tpending\tfirst criterion\n"; lean != want {
		t.Errorf("lean list = %q, want %q", lean, want)
	}

	if _, _, err := spRun(t, dir, "", "task", "criterion", "met", task.ID, "zzzzzzz"); !errors.Is(err, store.ErrNotFound) || ExitCode(err) != 3 {
		t.Errorf("unknown criterion prefix err = %v (exit %d), want ErrNotFound exit 3", err, ExitCode(err))
	}

	removed := spJSON[taskDTO](t, spMust(t, dir, "task", "criterion", "rm", task.ID, cid[:7], "--json"))
	if len(removed.Criteria) != 0 {
		t.Errorf("criteria = %+v after rm, want empty", removed.Criteria)
	}
}

func TestTaskValidate(t *testing.T) {
	dir := spInitRepo(t)
	task := spJSON[taskDTO](t, spMust(t, dir, "task", "add", "Validate me",
		"--criterion", "passes", "--criterion", "fails", "--json"))
	pass, fail := task.Criteria[0].ID, task.Criteria[1].ID

	passFile := filepath.Join(dir, "pass.sh")
	failFile := filepath.Join(dir, "fail.sh")
	if err := os.WriteFile(passFile, []byte("exit 0"), 0o600); err != nil {
		t.Fatalf("write pass script: %v", err)
	}
	if err := os.WriteFile(failFile, []byte("exit 1"), 0o600); err != nil {
		t.Fatalf("write fail script: %v", err)
	}
	spMust(t, dir, "task", "criterion", "script", task.ID, pass[:7], passFile)
	spMust(t, dir, "task", "criterion", "script", task.ID, fail[:7], failFile)

	withFileStdin(t)
	_, _, err := spRun(t, dir, "", "task", "validate", task.ID)
	if err == nil || !strings.Contains(err.Error(), "refusing to run") {
		t.Fatalf("validate without --yes on non-terminal stdin err = %v, want the refusal error", err)
	}
	before := spJSON[[]criterionDTO](t, spMust(t, dir, "task", "criterion", "list", task.ID, "--json"))
	for _, c := range before {
		if c.Status != "pending" {
			t.Fatalf("criterion %s status = %q after refused validate, want pending (nothing ran)", c.ID[:7], c.Status)
		}
	}

	validated := spJSON[taskDTO](t, spMust(t, dir, "task", "validate", task.ID, "--yes", "--json"))
	got := map[string]string{}
	for _, c := range validated.Criteria {
		got[c.ID] = c.Status
	}
	if got[pass] != "met" {
		t.Errorf("passing criterion status = %q, want met", got[pass])
	}
	if got[fail] != "failed" {
		t.Errorf("failing criterion status = %q, want failed", got[fail])
	}

	none := spJSON[taskDTO](t, spMust(t, dir, "task", "add", "No scripts", "--criterion", "manual", "--json"))
	if _, stderr, err := spRun(t, dir, "", "task", "validate", none.ID); err != nil {
		t.Fatalf("validate with no scripted criteria err = %v (stderr %q)", err, stderr)
	}
	out, _, _ := spRun(t, dir, "", "task", "validate", none.ID)
	if want := "no criteria have validation scripts\n"; out != want {
		t.Errorf("validate with no scripts out = %q, want %q", out, want)
	}
}

func TestTaskEditSprintProject(t *testing.T) {
	dir := spInitRepo(t)
	proj := spID(t, spMust(t, dir, "project", "add", "P", "--json"))
	sp := spID(t, spMust(t, dir, "sprint", "add", "S", "--json"))
	task := spJSON[taskDTO](t, spMust(t, dir, "task", "add", "T", "--no-validation-criteria", "--json"))

	edited := spJSON[taskDTO](t, spMust(t, dir, "task", "edit", task.ID, "--sprint", sp, "--project", proj, "--json"))
	if edited.Sprint == nil || *edited.Sprint != sp {
		t.Errorf("sprint = %v, want %s", edited.Sprint, sp)
	}
	if edited.Project == nil || *edited.Project != proj {
		t.Errorf("project = %v, want %s", edited.Project, proj)
	}

	cleared := spJSON[taskDTO](t, spMust(t, dir, "task", "edit", task.ID, "--no-sprint", "--no-project", "--json"))
	if cleared.Sprint != nil {
		t.Errorf("sprint = %v after --no-sprint, want null", *cleared.Sprint)
	}
	if cleared.Project != nil {
		t.Errorf("project = %v after --no-project, want null", *cleared.Project)
	}

	// Cobra flag-group exclusions: exit 2, not the *UsageError type.
	for _, args := range [][]string{
		{"task", "edit", task.ID, "--sprint", sp, "--no-sprint"},
		{"task", "edit", task.ID, "--project", proj, "--no-project"},
	} {
		if _, _, err := spRun(t, dir, "", args...); ExitCode(err) != 2 {
			t.Errorf("%v err = %v, want exit 2", args, err)
		}
	}
}
