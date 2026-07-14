package cli

import (
	"errors"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// rbStepID returns the id of the step with the given text in rb, failing if
// none matches.
func rbStepID(t *testing.T, rb runbookDTO, text string) string {
	t.Helper()
	for _, st := range rb.Steps {
		if st.Text == text {
			return st.ID
		}
	}
	t.Fatalf("no step with text %q in %+v", text, rb.Steps)
	return ""
}

// rbStepTexts returns rb's step texts in folded order.
func rbStepTexts(rb runbookDTO) []string {
	out := make([]string, len(rb.Steps))
	for i, st := range rb.Steps {
		out[i] = st.Text
	}
	return out
}

func TestRunbookAddWithSteps(t *testing.T) {
	dir := spInitRepo(t)
	out := spMust(t, dir, "runbook", "add", "Deploy", "--body", "how to deploy",
		"--label", "ops", "--step", "build", "--step", "test", "--step", "ship", "--json")
	rb := spJSON[runbookDTO](t, out)

	if rb.Title != "Deploy" || rb.Description != "how to deploy" {
		t.Errorf("title/desc = %q/%q", rb.Title, rb.Description)
	}
	if rb.Status != "active" {
		t.Errorf("status = %q, want active", rb.Status)
	}
	if got := rbStepTexts(rb); strings.Join(got, ",") != "build,test,ship" {
		t.Errorf("step order = %v, want [build test ship]", got)
	}
	if rb.Steps[0].Position >= rb.Steps[1].Position || rb.Steps[1].Position >= rb.Steps[2].Position {
		t.Errorf("positions not strictly increasing: %q %q %q", rb.Steps[0].Position, rb.Steps[1].Position, rb.Steps[2].Position)
	}
	if strings.Join(rb.Labels, ",") != "ops" {
		t.Errorf("labels = %v, want [ops]", rb.Labels)
	}
	if rb.Runs == nil || len(rb.Runs) != 0 {
		t.Errorf("runs = %v, want empty non-nil", rb.Runs)
	}
	if rb.Author != spActor {
		t.Errorf("author = %q, want %q", rb.Author, spActor)
	}
	if len(rb.ID) != 40 {
		t.Errorf("id = %q, want 40 hex", rb.ID)
	}

	lean := spMust(t, dir, "runbook", "show", rb.ID)
	if !strings.HasPrefix(lean, "id: "+rb.ID+"\ntitle: Deploy\nstatus: active\n") {
		t.Errorf("lean show header wrong: %q", lean)
	}
}

func TestRunbookListActiveVsAll(t *testing.T) {
	dir := spInitRepo(t)
	a := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "A", "--json"))
	b := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "B", "--json"))
	spMust(t, dir, "runbook", "archive", b.ID)

	active := spJSON[[]runbookDTO](t, spMust(t, dir, "runbook", "list", "--json"))
	if len(active) != 1 || active[0].ID != a.ID {
		t.Fatalf("list --json = %v, want only active %s", active, a.ID)
	}
	all := spJSON[[]runbookDTO](t, spMust(t, dir, "runbook", "list", "--all", "--json"))
	if len(all) != 2 {
		t.Fatalf("list --all --json = %d, want 2", len(all))
	}
	lean := spMust(t, dir, "runbook", "list")
	if strings.Contains(lean, b.ID[:7]) {
		t.Errorf("default list leaked archived runbook %s: %q", b.ID[:7], lean)
	}
}

func TestRunbookShowRendersStepsAndRuns(t *testing.T) {
	dir := spInitRepo(t)
	rb := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "Deploy", "--step", "build", "--step", "ship", "--json"))
	spMust(t, dir, "runbook", "run", "start", rb.ID)
	spMust(t, dir, "runbook", "run", "done", rb.ID, rbStepID(t, rb, "build")[:8])
	spMust(t, dir, "runbook", "run", "finish", rb.ID)

	lean := spMust(t, dir, "runbook", "show", rb.ID)
	if !strings.Contains(lean, "steps:") || !strings.Contains(lean, "build") || !strings.Contains(lean, "ship") {
		t.Fatalf("plain show omits steps (renderTaskShow gotcha):\n%s", lean)
	}
	if !strings.Contains(lean, "runs:") || !strings.Contains(lean, "succeeded by "+spActor) {
		t.Fatalf("plain show omits runs:\n%s", lean)
	}
}

func TestRunbookStepPlacementAndMove(t *testing.T) {
	dir := spInitRepo(t)
	rb := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "R", "--json"))
	first := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "step", "add", rb.ID, "first", "--json"))
	spMust(t, dir, "runbook", "step", "add", rb.ID, "third", "--json") // appends last
	firstID := rbStepID(t, first, "first")
	after := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "step", "add", rb.ID, "second", "--after", firstID[:8], "--json"))
	if got := rbStepTexts(after); strings.Join(got, ",") != "first,second,third" {
		t.Fatalf("after --after = %v, want [first second third]", got)
	}
	zeroed := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "step", "add", rb.ID, "zero", "--first", "--json"))
	if got := rbStepTexts(zeroed); strings.Join(got, ",") != "zero,first,second,third" {
		t.Fatalf("after --first = %v, want [zero first second third]", got)
	}
	thirdID := rbStepID(t, zeroed, "third")
	beforeThird := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "step", "add", rb.ID, "pre", "--before", thirdID[:8], "--json"))
	if got := rbStepTexts(beforeThird); strings.Join(got, ",") != "zero,first,second,pre,third" {
		t.Fatalf("after --before = %v, want [zero first second pre third]", got)
	}
	moved := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "step", "move", rb.ID, firstID[:8], "--last", "--json"))
	if got := rbStepTexts(moved); strings.Join(got, ",") != "zero,second,pre,third,first" {
		t.Fatalf("after move --last = %v, want [zero second pre third first]", got)
	}

	// move requires exactly one placement flag (cobra one-required); add rejects
	// two (cobra mutually-exclusive). Both surface as exit 2, though the cobra
	// flag-group error is not a *UsageError.
	if _, _, err := spRun(t, dir, "", "runbook", "step", "move", rb.ID, firstID[:8]); ExitCode(err) != 2 {
		t.Errorf("move with no placement flag err = %v (exit %d), want exit 2", err, ExitCode(err))
	}
	if _, _, err := spRun(t, dir, "", "runbook", "step", "add", rb.ID, "x", "--first", "--last"); ExitCode(err) != 2 {
		t.Errorf("add --first --last err = %v (exit %d), want exit 2", err, ExitCode(err))
	}
}

func TestRunbookStepEditRemove(t *testing.T) {
	dir := spInitRepo(t)
	rb := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "R", "--step", "a", "--json"))
	id := rbStepID(t, rb, "a")

	edited := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "step", "edit", rb.ID, id[:8], "--text", "A2", "--command", "echo hi", "--json"))
	if edited.Steps[0].Text != "A2" || edited.Steps[0].Command != "echo hi" {
		t.Fatalf("edit = %+v, want text A2 command 'echo hi'", edited.Steps[0])
	}
	cleared := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "step", "edit", rb.ID, id[:8], "--no-command", "--json"))
	if cleared.Steps[0].Command != "" {
		t.Fatalf("command = %q after --no-command, want empty", cleared.Steps[0].Command)
	}

	// A no-flag edit is a CLI usage guard; --command with --no-command is a cobra
	// mutually-exclusive violation — both exit 2.
	for _, args := range [][]string{
		{"runbook", "step", "edit", rb.ID, id[:8]},
		{"runbook", "step", "edit", rb.ID, id[:8], "--command", "x", "--no-command"},
	} {
		if _, _, err := spRun(t, dir, "", args...); ExitCode(err) != 2 {
			t.Errorf("%v err = %v (exit %d), want exit 2", args, err, ExitCode(err))
		}
	}

	removed := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "step", "rm", rb.ID, id[:8], "--json"))
	if len(removed.Steps) != 0 {
		t.Fatalf("steps = %v after rm, want empty", removed.Steps)
	}
}

func TestRunbookRunLifecycle(t *testing.T) {
	dir := spInitRepo(t)
	rb := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "R", "--step", "a", "--step", "b", "--json"))
	task := spID(t, spMust(t, dir, "task", "add", "T", "--no-validation-criteria", "--json"))

	started := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "run", "start", rb.ID, "--task", task, "--json"))
	if len(started.Runs) != 1 || started.Runs[0].Status != "running" {
		t.Fatalf("runs = %+v, want one running", started.Runs)
	}
	if started.Runs[0].Task == nil || *started.Runs[0].Task != task {
		t.Fatalf("run task = %v, want %s", started.Runs[0].Task, task)
	}

	spMust(t, dir, "runbook", "run", "done", rb.ID, rbStepID(t, rb, "a")[:8], "--note", "built")
	skipped := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "run", "skip", rb.ID, rbStepID(t, rb, "b")[:8], "--json"))
	run := skipped.Runs[0]
	if run.Steps[0].Status != "done" || run.Steps[0].Note != "built" {
		t.Fatalf("step a = %+v, want done/built", run.Steps[0])
	}
	if run.Steps[1].Status != "skipped" {
		t.Fatalf("step b = %+v, want skipped", run.Steps[1])
	}

	finished := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "run", "finish", rb.ID, "--json"))
	if finished.Runs[0].Status != "succeeded" || finished.Runs[0].FinishedAt == nil {
		t.Fatalf("finished run = %+v, want succeeded with finish stamp", finished.Runs[0])
	}
}

func TestRunbookRunDefaultResolution(t *testing.T) {
	dir := spInitRepo(t)
	rb := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "R", "--step", "a", "--json"))
	step := rbStepID(t, rb, "a")[:8]

	// zero running runs → conflict.
	_, _, err := spRun(t, dir, "", "runbook", "run", "done", rb.ID, step)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || ExitCode(err) != 4 {
		t.Fatalf("done with no run err = %v (exit %d), want conflict", err, ExitCode(err))
	}
	if !strings.Contains(conflict.Msg, "no running run") {
		t.Fatalf("conflict msg = %q, want 'no running run'", conflict.Msg)
	}

	// two running runs → ambiguous, unless --run disambiguates.
	spMust(t, dir, "runbook", "run", "start", rb.ID)
	two := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "run", "start", rb.ID, "--json"))
	if _, _, err := spRun(t, dir, "", "runbook", "run", "done", rb.ID, step); !errors.Is(err, store.ErrAmbiguous) || ExitCode(err) != 5 {
		t.Fatalf("done with two runs err = %v (exit %d), want ambiguous", err, ExitCode(err))
	}
	runID := two.Runs[len(two.Runs)-1].ID
	spMust(t, dir, "runbook", "run", "done", rb.ID, step, "--run", runID[:8])
}

func TestRunbookFinishStatusAndFlags(t *testing.T) {
	dir := spInitRepo(t)
	rb := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "R", "--step", "a", "--json"))
	step := rbStepID(t, rb, "a")[:8]

	// a failed step defaults the finish to failed.
	spMust(t, dir, "runbook", "run", "start", rb.ID)
	spMust(t, dir, "runbook", "run", "fail", rb.ID, step)
	failed := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "run", "finish", rb.ID, "--json"))
	if failed.Runs[0].Status != "failed" {
		t.Fatalf("finish default = %q, want failed (a step failed)", failed.Runs[0].Status)
	}

	// explicit --abandoned overrides.
	spMust(t, dir, "runbook", "run", "start", rb.ID)
	ab := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "run", "finish", rb.ID, "--abandoned", "--json"))
	if ab.Runs[len(ab.Runs)-1].Status != "abandoned" {
		t.Fatalf("finish --abandoned = %q, want abandoned", ab.Runs[len(ab.Runs)-1].Status)
	}

	// mutually exclusive flags (cobra flag group → exit 2).
	spMust(t, dir, "runbook", "run", "start", rb.ID)
	if _, _, err := spRun(t, dir, "", "runbook", "run", "finish", rb.ID, "--failed", "--abandoned"); ExitCode(err) != 2 {
		t.Fatalf("finish --failed --abandoned err = %v (exit %d), want exit 2", err, ExitCode(err))
	}
	// finishing an already-finished run conflicts.
	if _, _, err := spRun(t, dir, "", "runbook", "run", "finish", rb.ID, "--run", failed.Runs[0].ID[:8]); ExitCode(err) != 4 {
		t.Fatalf("re-finish err = %v (exit %d), want conflict", err, ExitCode(err))
	}
}

func TestRunbookStatusConflicts(t *testing.T) {
	dir := spInitRepo(t)
	rb := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "R", "--json"))

	archived := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "archive", rb.ID, "--json"))
	if archived.Status != "archived" || archived.ArchivedAt == nil {
		t.Fatalf("archived = %+v, want archived with stamp", archived)
	}
	for _, verb := range []string{"archive"} {
		_, _, err := spRun(t, dir, "", "runbook", verb, rb.ID)
		var conflict *ConflictError
		if !errors.As(err, &conflict) || ExitCode(err) != 4 {
			t.Fatalf("%s already-archived err = %v (exit %d), want conflict", verb, err, ExitCode(err))
		}
		if want := rb.ID[:7] + " already archived"; conflict.Msg != want {
			t.Fatalf("%s msg = %q, want %q", verb, conflict.Msg, want)
		}
	}
	spMust(t, dir, "runbook", "activate", rb.ID)
	if _, _, err := spRun(t, dir, "", "runbook", "activate", rb.ID); ExitCode(err) != 4 {
		t.Fatalf("activate already-active err = %v (exit %d), want conflict", err, ExitCode(err))
	}
}

func TestRunbookArchivedWriteGating(t *testing.T) {
	dir := spInitRepo(t)
	rb := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "R", "--step", "a", "--json"))
	spMust(t, dir, "runbook", "archive", rb.ID)

	for _, args := range [][]string{
		{"runbook", "step", "add", rb.ID, "b"},
		{"runbook", "run", "start", rb.ID},
		{"runbook", "edit", rb.ID, "--title", "X"},
		{"runbook", "comment", rb.ID, "hi"},
	} {
		_, _, err := spRun(t, dir, "", args...)
		var conflict *ConflictError
		if !errors.As(err, &conflict) || ExitCode(err) != 4 {
			t.Fatalf("%v on archived err = %v (exit %d), want conflict", args, err, ExitCode(err))
		}
		if !strings.Contains(conflict.Msg, "archived") {
			t.Fatalf("%v conflict msg = %q, want 'archived'", args, conflict.Msg)
		}
	}

	// reactivating lifts the gate.
	spMust(t, dir, "runbook", "activate", rb.ID)
	spMust(t, dir, "runbook", "step", "add", rb.ID, "b")
}

func TestRunbookEditAndComment(t *testing.T) {
	dir := spInitRepo(t)
	rb := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "R", "--label", "keep", "--label", "drop", "--json"))

	edited := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "edit", rb.ID, "--title", "R2", "--body", "desc", "--add-label", "new", "--rm-label", "drop", "--json"))
	if edited.Title != "R2" || edited.Description != "desc" {
		t.Fatalf("edit = title %q desc %q", edited.Title, edited.Description)
	}
	if strings.Join(edited.Labels, ",") != "keep,new" {
		t.Fatalf("labels = %v, want [keep new]", edited.Labels)
	}
	if _, _, err := spRun(t, dir, "", "runbook", "edit", rb.ID); !isUsage(err) {
		t.Fatalf("edit with no flags err = %v, want usage", err)
	}

	spMust(t, dir, "runbook", "comment", rb.ID, "operational note")
	shown := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "show", rb.ID, "--json"))
	if len(shown.Comments) != 1 || shown.Comments[0].Body != "operational note" {
		t.Fatalf("show --json comments = %+v, want one operational-note comment", shown.Comments)
	}
	if plain := spMust(t, dir, "runbook", "show", rb.ID); !strings.Contains(plain, "operational note") {
		t.Fatalf("plain show omits the comment:\n%s", plain)
	}
	hist := spMust(t, dir, "runbook", "history", rb.ID)
	if !strings.Contains(hist, "created runbook") {
		t.Fatalf("history missing create verb:\n%s", hist)
	}
	if !strings.Contains(hist, "operational note") {
		t.Fatalf("history missing the comment:\n%s", hist)
	}
}

func TestResolveStep(t *testing.T) {
	rb := model.Runbook{Steps: []model.RunbookStep{
		{ID: "aaaa1111bbbb", Text: "build"},
		{ID: "aaaa2222cccc", Text: "test"},
		{ID: "bbbb3333dddd", Text: "ship"},
	}}
	tests := []struct {
		name    string
		prefix  string
		wantID  string
		wantErr error
	}{
		{"unique prefix", "bbbb3", "bbbb3333dddd", nil},
		{"case insensitive", "BBBB3", "bbbb3333dddd", nil},
		{"no match", "cccc", "", store.ErrNotFound},
		{"ambiguous prefix", "aaaa", "", store.ErrAmbiguous},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveStep(rb, tc.prefix)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want errors.Is %v", err, tc.wantErr)
				}
				return
			}
			if err != nil || got.ID != tc.wantID {
				t.Fatalf("got (%q, %v), want id %q", got.ID, err, tc.wantID)
			}
		})
	}
}

func TestResolveRun(t *testing.T) {
	rb := model.Runbook{Runs: []model.RunbookRun{
		{ID: "aaaa1111", Status: model.RunRunning, StartedAt: 1735689600},
		{ID: "aaaa2222", Status: model.RunSucceeded, StartedAt: 1735689600},
		{ID: "bbbb3333", Status: model.RunFailed, StartedAt: 1735689600},
	}}
	if got, err := resolveRun(rb, "bbbb"); err != nil || got.ID != "bbbb3333" {
		t.Fatalf("resolveRun bbbb = (%q, %v), want bbbb3333", got.ID, err)
	}
	if _, err := resolveRun(rb, "zzzz"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("resolveRun zzzz err = %v, want ErrNotFound", err)
	}
	_, err := resolveRun(rb, "aaaa")
	if !errors.Is(err, store.ErrAmbiguous) {
		t.Fatalf("resolveRun aaaa err = %v, want ErrAmbiguous", err)
	}
	if !strings.Contains(err.Error(), "running") || !strings.Contains(err.Error(), "succeeded") {
		t.Fatalf("ambiguity msg %q must list run statuses", err.Error())
	}
}

func TestResolveTargetRun(t *testing.T) {
	oneRunning := model.Runbook{ID: "deadbeefcafe", Runs: []model.RunbookRun{
		{ID: "run1aaaa", Status: model.RunRunning},
		{ID: "run2bbbb", Status: model.RunSucceeded},
	}}
	if got, err := resolveTargetRun(oneRunning, ""); err != nil || got.ID != "run1aaaa" {
		t.Fatalf("default = (%q, %v), want the sole running run1aaaa", got.ID, err)
	}
	// --run may target the finished run.
	if got, err := resolveTargetRun(oneRunning, "run2"); err != nil || got.ID != "run2bbbb" {
		t.Fatalf("--run run2 = (%q, %v), want run2bbbb", got.ID, err)
	}

	none := model.Runbook{ID: "deadbeefcafe", Runs: []model.RunbookRun{{ID: "x", Status: model.RunFailed}}}
	var conflict *ConflictError
	if _, err := resolveTargetRun(none, ""); !errors.As(err, &conflict) {
		t.Fatalf("no running run err = %v, want ConflictError", err)
	}

	many := model.Runbook{ID: "deadbeefcafe", Runs: []model.RunbookRun{
		{ID: "run1aaaa", Status: model.RunRunning},
		{ID: "run2bbbb", Status: model.RunRunning},
	}}
	if _, err := resolveTargetRun(many, ""); !errors.Is(err, store.ErrAmbiguous) {
		t.Fatalf("two running runs err = %v, want ErrAmbiguous", err)
	}
}

// TestRunbookFinishArchivedBeatsAmbiguousRuns pins the archived-first precedence
// of `run finish`: an archived runbook with several running runs reports the
// archived conflict (exit 4), never the ambiguous-run error (exit 5) that the
// pre-migration CLI's own target resolution would otherwise raise first.
func TestRunbookFinishArchivedBeatsAmbiguousRuns(t *testing.T) {
	dir := spInitRepo(t)
	rb := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "R", "--step", "a", "--json"))
	spMust(t, dir, "runbook", "run", "start", rb.ID)
	spMust(t, dir, "runbook", "run", "start", rb.ID) // two concurrent running runs
	spMust(t, dir, "runbook", "archive", rb.ID)

	_, _, err := spRun(t, dir, "", "runbook", "run", "finish", rb.ID)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || ExitCode(err) != 4 {
		t.Fatalf("finish archived w/ two running runs err = %v (exit %d), want archived conflict exit 4", err, ExitCode(err))
	}
	if !strings.Contains(conflict.Msg, "archived") {
		t.Fatalf("conflict msg = %q, want the archived message", conflict.Msg)
	}
}

// TestRunbookStartArchivedBeatsTaskResolution pins the archived-first precedence
// of `run start --task`: an archived runbook reports the archived conflict
// (exit 4) before the --task prefix is resolved, so a missing task never wins
// the not-found (exit 3) the pre-migration CLI would raise first.
func TestRunbookStartArchivedBeatsTaskResolution(t *testing.T) {
	dir := spInitRepo(t)
	rb := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "R", "--json"))
	spMust(t, dir, "runbook", "archive", rb.ID)

	_, _, err := spRun(t, dir, "", "runbook", "run", "start", rb.ID, "--task", "deadbeef")
	var conflict *ConflictError
	if !errors.As(err, &conflict) || ExitCode(err) != 4 {
		t.Fatalf("start archived w/ missing --task err = %v (exit %d), want archived conflict exit 4", err, ExitCode(err))
	}
	if !strings.Contains(conflict.Msg, "archived") {
		t.Fatalf("conflict msg = %q, want the archived message", conflict.Msg)
	}
}

// TestRunbookStepMoveSelfRelative pins the exit code of a self-relative step
// move: placing a step before or after itself is a usage error (exit 2), not
// the plain notes error (exit 1) the raw ErrSelfRelative would map to.
func TestRunbookStepMoveSelfRelative(t *testing.T) {
	dir := spInitRepo(t)
	rb := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "R", "--step", "a", "--step", "b", "--json"))
	aID := rbStepID(t, rb, "a")

	for _, flag := range []string{"--before", "--after"} {
		_, _, err := spRun(t, dir, "", "runbook", "step", "move", rb.ID, aID[:8], flag, aID[:8])
		if !isUsage(err) {
			t.Fatalf("step move %s self err = %v (exit %d), want usage exit 2", flag, err, ExitCode(err))
		}
		if !strings.Contains(err.Error(), "relative to itself") {
			t.Fatalf("step move %s self msg = %q, want 'relative to itself'", flag, err.Error())
		}
	}
}

// runbookIDs returns the ids of a runbook DTO slice.
func runbookIDs(rbs []runbookDTO) []string {
	out := make([]string, len(rbs))
	for i, rb := range rbs {
		out[i] = rb.ID
	}
	return out
}

// TestRunbookAddAnchorsRoundTrip pins that every anchor kind attached at add
// round-trips: the list filter matches only on the stored value, and a commit
// revision is resolved to its full sha (so --commit HEAD no longer matches).
func TestRunbookAddAnchorsRoundTrip(t *testing.T) {
	dir := spInitRepo(t)
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	sha := gittest.Git(t, dir, "rev-parse", "HEAD")

	rb := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "Deploy",
		"--commit", "HEAD", "--path", "scripts/deploy.sh", "--dir", "internal/auth", "--branch", "main", "--json"))
	// A second, anchorless runbook must never match an anchor filter.
	spMust(t, dir, "runbook", "add", "Bare", "--json")

	tests := []struct {
		name  string
		args  []string
		match bool
	}{
		{"commit resolved to sha", []string{"--commit", sha}, true},
		{"commit literal HEAD not stored", []string{"--commit", "HEAD"}, false},
		{"commit wrong sha", []string{"--commit", strings.Repeat("0", 40)}, false},
		{"path", []string{"--path", "scripts/deploy.sh"}, true},
		{"path wrong", []string{"--path", "other.sh"}, false},
		{"dir", []string{"--dir", "internal/auth"}, true},
		{"branch", []string{"--branch", "main"}, true},
		{"branch wrong", []string{"--branch", "dev"}, false},
		{"path AND dir both match", []string{"--path", "scripts/deploy.sh", "--dir", "internal/auth"}, true},
		{"path AND dir one wrong", []string{"--path", "scripts/deploy.sh", "--dir", "internal/other"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"runbook", "list", "--json"}, tc.args...)
			got := spJSON[[]runbookDTO](t, spMust(t, dir, args...))
			matched := len(got) == 1 && got[0].ID == rb.ID
			if matched != tc.match {
				t.Fatalf("list %v = %v, want match=%v", tc.args, runbookIDs(got), tc.match)
			}
		})
	}
}

// TestRunbookListLabelFilter pins runbook list's label/anchor filter, mirroring
// log list: multiple --label are ANDed (every one required), and --label
// composes conjunctively with an anchor filter (both must match). An OR label
// rule or a dropped anchor conjunction changes these counts.
func TestRunbookListLabelFilter(t *testing.T) {
	dir := spInitRepo(t)
	both := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "Both",
		"--label", "ops", "--label", "db", "--path", "deploy.sh", "--json"))
	spMust(t, dir, "runbook", "add", "OpsOnly", "--label", "ops", "--json")
	spMust(t, dir, "runbook", "add", "DbOnly", "--label", "db", "--json")

	// A single label matches every runbook carrying it.
	ops := spJSON[[]runbookDTO](t, spMust(t, dir, "runbook", "list", "--label", "ops", "--json"))
	if len(ops) != 2 {
		t.Fatalf("list --label ops = %v, want 2 (Both, OpsOnly)", runbookIDs(ops))
	}

	// Two labels are ANDed: only the runbook carrying both survives.
	and := spJSON[[]runbookDTO](t, spMust(t, dir, "runbook", "list", "--label", "ops", "--label", "db", "--json"))
	if len(and) != 1 || and[0].ID != both.ID {
		t.Fatalf("list --label ops --label db = %v, want only Both %s", runbookIDs(and), both.ID)
	}

	// --label composes with an anchor: both the label and the path must match.
	composed := spJSON[[]runbookDTO](t, spMust(t, dir, "runbook", "list", "--label", "ops", "--path", "deploy.sh", "--json"))
	if len(composed) != 1 || composed[0].ID != both.ID {
		t.Fatalf("list --label ops --path deploy.sh = %v, want only Both %s", runbookIDs(composed), both.ID)
	}

	// Label matches but anchor does not: the conjunction rejects it.
	if got := spJSON[[]runbookDTO](t, spMust(t, dir, "runbook", "list", "--label", "ops", "--path", "other.sh", "--json")); len(got) != 0 {
		t.Fatalf("list --label ops --path other.sh = %v, want empty (anchor unmatched)", runbookIDs(got))
	}

	// Anchor matches but label does not: still rejected.
	if got := spJSON[[]runbookDTO](t, spMust(t, dir, "runbook", "list", "--label", "absent", "--path", "deploy.sh", "--json")); len(got) != 0 {
		t.Fatalf("list --label absent --path deploy.sh = %v, want empty (label unmatched)", runbookIDs(got))
	}
}

// TestRunbookEditAnchors pins that --add-*/--rm-* anchor edits round-trip through
// the list filter and that an anchor-only edit is not an empty edit.
func TestRunbookEditAnchors(t *testing.T) {
	dir := spInitRepo(t)
	rb := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "R", "--path", "a.go", "--json"))

	byPath := spJSON[[]runbookDTO](t, spMust(t, dir, "runbook", "list", "--path", "a.go", "--json"))
	if len(byPath) != 1 || byPath[0].ID != rb.ID {
		t.Fatalf("pre-edit list --path a.go = %v, want [%s]", runbookIDs(byPath), rb.ID)
	}

	// an anchor-only edit is accepted (not an empty-edit usage error).
	spMust(t, dir, "runbook", "edit", rb.ID, "--add-dir", "internal/x", "--rm-path", "a.go")

	if got := spJSON[[]runbookDTO](t, spMust(t, dir, "runbook", "list", "--dir", "internal/x", "--json")); len(got) != 1 || got[0].ID != rb.ID {
		t.Fatalf("post-edit list --dir internal/x = %v, want [%s]", runbookIDs(got), rb.ID)
	}
	if got := spJSON[[]runbookDTO](t, spMust(t, dir, "runbook", "list", "--path", "a.go", "--json")); len(got) != 0 {
		t.Fatalf("post-edit list --path a.go = %v, want empty (path removed)", runbookIDs(got))
	}

	if _, _, err := spRun(t, dir, "", "runbook", "edit", rb.ID); !isUsage(err) {
		t.Fatalf("empty edit err = %v, want usage", err)
	}
}

// TestRunbookRm pins that rm tombstones a runbook: it vanishes from both the
// default and --all listings, and the tombstone is not a live dedupe target, so
// re-adding identical content roots a fresh runbook.
func TestRunbookRm(t *testing.T) {
	dir := spInitRepo(t)
	gone := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "Gone", "--json"))
	keep := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "Keep", "--json"))

	removed := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "rm", gone.ID[:8], "--json"))
	if removed.ID != gone.ID {
		t.Fatalf("rm returned %s, want tombstoned %s", removed.ID, gone.ID)
	}

	for _, listArgs := range [][]string{{"runbook", "list", "--json"}, {"runbook", "list", "--all", "--json"}} {
		got := spJSON[[]runbookDTO](t, spMust(t, dir, listArgs...))
		if len(got) != 1 || got[0].ID != keep.ID {
			t.Fatalf("%v after rm = %v, want only %s (tombstone hidden)", listArgs, runbookIDs(got), keep.ID)
		}
	}

	// dedupe-safe: the tombstone is not live, so identical content roots anew.
	re := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "Gone", "--json"))
	if re.ID == gone.ID {
		t.Fatalf("re-add converged on tombstoned runbook %s", gone.ID)
	}
}

// TestRunbookSearch pins the CLI search wiring: results rank title over label
// over step text, --limit caps (0 = all per bindLimit), and archived runbooks
// are excluded.
func TestRunbookSearch(t *testing.T) {
	dir := spInitRepo(t)
	byTitle := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "deploy service", "--json"))
	byLabel := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "rollout", "--label", "deploy", "--json"))
	byStep := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "release", "--step", "deploy the binary", "--json"))
	spMust(t, dir, "runbook", "add", "unrelated", "--json")

	ranked := spJSON[[]runbookDTO](t, spMust(t, dir, "runbook", "search", "deploy", "--json"))
	if want := []string{byTitle.ID, byLabel.ID, byStep.ID}; strings.Join(runbookIDs(ranked), ",") != strings.Join(want, ",") {
		t.Fatalf("search deploy = %v, want %v (title > label > step)", runbookIDs(ranked), want)
	}

	one := spJSON[[]runbookDTO](t, spMust(t, dir, "runbook", "search", "deploy", "--limit", "1", "--json"))
	if len(one) != 1 || one[0].ID != byTitle.ID {
		t.Fatalf("search --limit 1 = %v, want [%s]", runbookIDs(one), byTitle.ID)
	}
	all := spJSON[[]runbookDTO](t, spMust(t, dir, "runbook", "search", "deploy", "--limit", "0", "--json"))
	if len(all) != 3 {
		t.Fatalf("search --limit 0 = %v, want all 3 (0 = all)", runbookIDs(all))
	}
	if none := spJSON[[]runbookDTO](t, spMust(t, dir, "runbook", "search", "nomatch", "--json")); len(none) != 0 {
		t.Fatalf("search nomatch = %v, want empty", runbookIDs(none))
	}

	spMust(t, dir, "runbook", "archive", byTitle.ID)
	afterArchive := spJSON[[]runbookDTO](t, spMust(t, dir, "runbook", "search", "deploy", "--json"))
	if want := []string{byLabel.ID, byStep.ID}; strings.Join(runbookIDs(afterArchive), ",") != strings.Join(want, ",") {
		t.Fatalf("search after archive = %v, want %v (archived excluded)", runbookIDs(afterArchive), want)
	}
}

// TestRunbookFreeText pins the one-of positional/flag/stdin resolution freeText
// gives runbook add, comment, and step add, plus stdin on step edit --text.
func TestRunbookFreeText(t *testing.T) {
	dir := spInitRepo(t)

	// add: positional BODY.
	pos := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "P", "positional desc", "--json"))
	if pos.Description != "positional desc" {
		t.Fatalf("add positional desc = %q, want 'positional desc'", pos.Description)
	}
	// add: stdin via "-".
	out, stderr, err := spRun(t, dir, "piped desc\n", "runbook", "add", "S", "-", "--json")
	if err != nil {
		t.Fatalf("add stdin: %v (%s)", err, stderr)
	}
	if desc := spJSON[runbookDTO](t, out).Description; desc != "piped desc" {
		t.Fatalf("add stdin desc = %q, want 'piped desc'", desc)
	}
	// add: positional and --body together conflict.
	if _, _, err := spRun(t, dir, "", "runbook", "add", "C", "x", "--body", "y"); !isUsage(err) {
		t.Fatalf("add positional+--body err = %v, want usage", err)
	}

	rb := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "add", "R", "--json"))

	// comment: --body flag, then stdin, then the required-absence and conflict errors.
	commented := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "comment", rb.ID, "--body", "flag comment", "--json"))
	if len(commented.Comments) != 1 || commented.Comments[0].Body != "flag comment" {
		t.Fatalf("comment --body = %+v, want one 'flag comment'", commented.Comments)
	}
	out, stderr, err = spRun(t, dir, "stdin comment", "runbook", "comment", rb.ID, "-", "--json")
	if err != nil {
		t.Fatalf("comment stdin: %v (%s)", err, stderr)
	}
	if last := spJSON[runbookDTO](t, out).Comments; len(last) != 2 || last[1].Body != "stdin comment" {
		t.Fatalf("comment stdin = %+v, want 'stdin comment' second", last)
	}
	if _, _, err := spRun(t, dir, "", "runbook", "comment", rb.ID); !isUsage(err) {
		t.Fatalf("comment with no body err = %v, want usage", err)
	}
	if _, _, err := spRun(t, dir, "", "runbook", "comment", rb.ID, "x", "--body", "y"); !isUsage(err) {
		t.Fatalf("comment positional+--body err = %v, want usage", err)
	}

	// step add: --text flag, then the required-absence and conflict errors.
	viaText := spJSON[runbookDTO](t, spMust(t, dir, "runbook", "step", "add", rb.ID, "--text", "flag step", "--json"))
	if len(viaText.Steps) != 1 || viaText.Steps[0].Text != "flag step" {
		t.Fatalf("step add --text = %+v, want one 'flag step'", viaText.Steps)
	}
	if _, _, err := spRun(t, dir, "", "runbook", "step", "add", rb.ID); !isUsage(err) {
		t.Fatalf("step add with no text err = %v, want usage", err)
	}
	if _, _, err := spRun(t, dir, "", "runbook", "step", "add", rb.ID, "x", "--text", "y"); !isUsage(err) {
		t.Fatalf("step add positional+--text err = %v, want usage", err)
	}

	// step edit: --text - reads stdin.
	sid := rbStepID(t, viaText, "flag step")
	out, stderr, err = spRun(t, dir, "edited via stdin", "runbook", "step", "edit", rb.ID, sid[:8], "--text", "-", "--json")
	if err != nil {
		t.Fatalf("step edit stdin: %v (%s)", err, stderr)
	}
	if edited := spJSON[runbookDTO](t, out); edited.Steps[0].Text != "edited via stdin" {
		t.Fatalf("step edit --text - = %q, want 'edited via stdin'", edited.Steps[0].Text)
	}
}

// TestRunbookConstraintsInHelp pins that the MarkFlags* groups surface in --help
// via the shared Constraints template extension.
func TestRunbookConstraintsInHelp(t *testing.T) {
	dir := spInitRepo(t)
	moveHelp := spMust(t, dir, "runbook", "step", "move", "--help")
	for _, want := range []string{
		"Constraints:",
		"--first, --last, --before, --after are mutually exclusive",
		"one of --first, --last, --before, --after is required",
	} {
		if !strings.Contains(moveHelp, want) {
			t.Fatalf("step move --help missing %q:\n%s", want, moveHelp)
		}
	}
	finishHelp := spMust(t, dir, "runbook", "run", "finish", "--help")
	if !strings.Contains(finishHelp, "--failed, --abandoned are mutually exclusive") {
		t.Fatalf("run finish --help missing constraint:\n%s", finishHelp)
	}
}
