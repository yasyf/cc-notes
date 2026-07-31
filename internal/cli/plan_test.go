package cli

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/store"
)

const (
	planTitle    = "fix monorepo read performance"
	planFileBody = "## Context\ncc-notes is slow in a monorepo.\n\n## Approach\n1. stop reopening the pack\n"
)

// planFile writes body to a file under t.TempDir() and returns its path, the
// shape `plan add --body-file` reads.
func planFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plan.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write plan file: %v", err)
	}
	return path
}

// TestPlanLifecycleStory drives one plan through the whole CLI lifecycle —
// recorded from a file already approved, executed, closed with an outcome, then
// superseded — asserting the lean line, the show view, and the task roll-up at
// each step.
func TestPlanLifecycleStory(t *testing.T) {
	dir := spInitRepo(t)
	file := planFile(t, planFileBody)

	added := spMust(t, dir, "plan", "add", planTitle, "--body-file", file, "--approved", "--path", "internal/gitobj", "--label", "perf", "--json")
	id := spID(t, added)
	summary := spJSON[planSummaryDTO](t, added)
	if summary.Status != "approved" || summary.Title != planTitle {
		t.Fatalf("add summary = %+v, want approved %q", summary, planTitle)
	}

	lean := spMust(t, dir, "plan", "list")
	if want := id[:7] + "\tapproved\t" + planTitle + "\n"; lean != want {
		t.Errorf("plan list = %q, want %q", lean, want)
	}

	shown := spJSON[planDTO](t, spMust(t, dir, "plan", "show", id, "--json"))
	if shown.Body != strings.TrimRight(planFileBody, "\n") {
		t.Errorf("body = %q, want the file's bytes with the trailing newline trimmed", shown.Body)
	}
	if len(shown.Anchors) != 1 || shown.Anchors[0].Kind != "dir" && shown.Anchors[0].Kind != "path" {
		t.Errorf("anchors = %+v, want the one path anchor", shown.Anchors)
	}
	if shown.Anchors[0].Witness != nil {
		t.Errorf("anchor witness = %+v, want none: a plan carries no witness", shown.Anchors[0].Witness)
	}

	task := spJSON[taskSummaryDTO](t, spMust(t, dir, "task", "add", "step one", "--no-validation-criteria", "--plan", id, "--json"))
	if task.Plan != id {
		t.Fatalf("task plan = %q, want %q", task.Plan, id)
	}

	spMust(t, dir, "plan", "start", id)
	shown = spJSON[planDTO](t, spMust(t, dir, "plan", "show", id, "--json"))
	if shown.Status != "executing" || shown.StartedAt == nil {
		t.Fatalf("after start: status %q started_at %v, want executing with a stamp", shown.Status, shown.StartedAt)
	}
	if !slices.Equal(shown.Tasks, []string{task.ID}) {
		t.Errorf("tasks = %v, want the roll-up [%s] inverted from the task pointer", shown.Tasks, task.ID)
	}

	spMust(t, dir, "plan", "done", id, "--outcome", "landed as PR 1")
	shown = spJSON[planDTO](t, spMust(t, dir, "plan", "show", id, "--json"))
	if shown.Status != "done" || shown.Outcome != "landed as PR 1" || shown.ClosedAt == nil || shown.ClosedBy == nil {
		t.Fatalf("after done: %+v, want done with an outcome and a close stamp", shown)
	}

	next := spID(t, spMust(t, dir, "plan", "add", "fix it properly", "--body", "the v2 plan", "--json"))
	spMust(t, dir, "plan", "supersede", id, "--by", next)
	shown = spJSON[planDTO](t, spMust(t, dir, "plan", "show", id, "--json"))
	if !slices.Equal(shown.SupersededBy, []string{next}) {
		t.Errorf("superseded_by = %v, want [%s]", shown.SupersededBy, next)
	}
	if shown.Status != "done" {
		t.Errorf("status after supersede = %q, want done: supersession is an edge, not a status", shown.Status)
	}

	text := spMust(t, dir, "plan", "show", next)
	for _, want := range []string{"status: draft", "tasks: -", "outcome:"} {
		if !strings.Contains(text, want) {
			t.Errorf("plan show text is missing %q:\n%s", want, text)
		}
	}
}

func TestPlanTransitionGuards(t *testing.T) {
	dir := spInitRepo(t)
	id := spID(t, spMust(t, dir, "plan", "add", "gated", "--body", "the plan", "--json"))

	if _, _, err := spRun(t, dir, "", "plan", "start", id); ExitCode(err) != 4 {
		t.Fatalf("start on a draft = exit %d (%v), want 4", ExitCode(err), err)
	}
	spMust(t, dir, "plan", "approve", id)
	spMust(t, dir, "plan", "start", id)

	_, _, err := spRun(t, dir, "", "plan", "start", id)
	if ExitCode(err) != 4 || Label(err) != "conflict" {
		t.Fatalf("second start = exit %d %q (%v), want 4 conflict", ExitCode(err), Label(err), err)
	}

	spMust(t, dir, "plan", "abandon", id, "--outcome", "not worth it")
	spMust(t, dir, "plan", "reopen", id)
	if got := spJSON[planSummaryDTO](t, spMust(t, dir, "plan", "show", id, "--json")).Status; got != "executing" {
		t.Errorf("status after reopen = %q, want executing", got)
	}
}

func TestPlanAddBodySources(t *testing.T) {
	dir := spInitRepo(t)
	file := planFile(t, planFileBody)

	cases := []struct {
		name     string
		args     []string
		stdin    string
		wantBody string
		wantCode int
	}{
		{"positional", []string{"plan", "add", "a", "body one"}, "", "body one", 0},
		{"flag", []string{"plan", "add", "b", "--body", "body two"}, "", "body two", 0},
		{"stdin", []string{"plan", "add", "c", "--body", "-"}, "body three\n", "body three", 0},
		{"file", []string{"plan", "add", "d", "--body-file", file}, "", strings.TrimRight(planFileBody, "\n"), 0},
		{"no body", []string{"plan", "add", "e"}, "", "", 2},
		{"file and positional", []string{"plan", "add", "f", "inline", "--body-file", file}, "", "", 2},
		{"file and flag", []string{"plan", "add", "g", "--body", "inline", "--body-file", file}, "", "", 2},
		{"missing file", []string{"plan", "add", "h", "--body-file", filepath.Join(dir, "absent.md")}, "", "", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stdout, _, err := spRun(t, dir, tc.stdin, append(slices.Clone(tc.args), "--json")...)
			if got := ExitCode(err); got != tc.wantCode {
				t.Fatalf("exit = %d (%v), want %d", got, err, tc.wantCode)
			}
			if tc.wantCode != 0 {
				return
			}
			shown := spJSON[planDTO](t, spMust(t, dir, "plan", "show", spID(t, stdout), "--json"))
			if shown.Body != tc.wantBody {
				t.Errorf("body = %q, want %q", shown.Body, tc.wantBody)
			}
			if shown.Status != "draft" {
				t.Errorf("status = %q, want draft without --approved", shown.Status)
			}
		})
	}
}

func TestPlanListFiltersEditsAndSearch(t *testing.T) {
	dir := spInitRepo(t)
	perf := spID(t, spMust(t, dir, "plan", "add", "perf work", "--body", "walk the pack once", "--label", "perf", "--json"))
	kindPlan := spID(t, spMust(t, dir, "plan", "add", "the plan kind", "--body", "a ninth entity kind", "--approved", "--json"))

	// Two plans created in the same second share an UpdatedAt, so the listing's
	// second sort key (id ascending) decides the order; assert the set.
	want := []string{kindPlan[:7], perf[:7]}
	slices.Sort(want)
	if got := planIDs(spMust(t, dir, "plan", "list")); !slices.Equal(slices.Sorted(slices.Values(got)), want) {
		t.Errorf("plan list = %v, want both in-flight plans %v", got, want)
	}
	if got := planIDs(spMust(t, dir, "plan", "list", "--status", "approved")); !slices.Equal(got, []string{kindPlan[:7]}) {
		t.Errorf("plan list --status approved = %v, want [%s]", got, kindPlan[:7])
	}
	if got := planIDs(spMust(t, dir, "plan", "list", "--label", "perf")); !slices.Equal(got, []string{perf[:7]}) {
		t.Errorf("plan list --label perf = %v, want [%s]", got, perf[:7])
	}
	if _, _, err := spRun(t, dir, "", "plan", "list", "--status", "shipped"); ExitCode(err) != 1 {
		t.Errorf("plan list --status shipped = exit %d, want 1 for an unknown status", ExitCode(err))
	}

	spMust(t, dir, "plan", "abandon", perf)
	if got := planIDs(spMust(t, dir, "plan", "list")); !slices.Equal(got, []string{kindPlan[:7]}) {
		t.Errorf("plan list after abandon = %v, want only the in-flight plan", got)
	}
	if got := planIDs(spMust(t, dir, "plan", "list", "--all")); len(got) != 2 {
		t.Errorf("plan list --all = %v, want both", got)
	}

	if _, _, err := spRun(t, dir, "", "plan", "edit", kindPlan); ExitCode(err) != 2 {
		t.Errorf("flagless plan edit = exit %d, want 2", ExitCode(err))
	}
	spMust(t, dir, "plan", "edit", kindPlan, "--body", "a ninth entity kind, revised", "--add-label", "model")
	spMust(t, dir, "plan", "comment", kindPlan, "approved as written")
	shown := spJSON[planDTO](t, spMust(t, dir, "plan", "show", kindPlan, "--json"))
	if shown.Body != "a ninth entity kind, revised" {
		t.Errorf("body = %q, want the revision: Body is LWW", shown.Body)
	}
	if !slices.Equal(shown.Labels, []string{"model"}) {
		t.Errorf("labels = %v, want [model]", shown.Labels)
	}
	if len(shown.Comments) != 1 || shown.Comments[0].Body != "approved as written" {
		t.Errorf("comments = %+v, want the one comment", shown.Comments)
	}

	if got := planIDs(spMust(t, dir, "plan", "search", "ninth entity")); !slices.Equal(got, []string{kindPlan[:7]}) {
		t.Errorf("plan search = %v, want [%s]", got, kindPlan[:7])
	}
	if got := spMust(t, dir, "plan", "history", kindPlan); !strings.Contains(got, "body") {
		t.Errorf("plan history = %q, want the body edit named", got)
	}

	spMust(t, dir, "plan", "rm", kindPlan)
	if got := spJSON[planDTO](t, spMust(t, dir, "plan", "show", kindPlan, "--json")); !got.Deleted {
		t.Error("plan rm left deleted=false; the tombstone must survive as a soft delete")
	}
}

// TestPlanEditRefusesEmptyBody pins the invariant `plan add` already enforces on
// the one write path that used to leak: an empty --body is a usage error (exit
// 2), not a silent blanking of the approved text, whether it arrives inline or
// over stdin. --title "" and --outcome "" bracket it: a blank title is refused
// the same way, a blank outcome is a legal clear.
func TestPlanEditRefusesEmptyBody(t *testing.T) {
	dir := spInitRepo(t)
	id := spID(t, spMust(t, dir, "plan", "add", "the plan", "--body", "the approved text", "--approved", "--json"))

	cases := []struct {
		name  string
		args  []string
		stdin string
	}{
		{"inline", []string{"plan", "edit", id, "--body", ""}, ""},
		{"stdin", []string{"plan", "edit", id, "--body", "-"}, "\n"},
		{"empty title", []string{"plan", "edit", id, "--title", ""}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := spRun(t, dir, tc.stdin, tc.args...)
			if ExitCode(err) != 2 || Label(err) != "usage" {
				t.Fatalf("%v = exit %d %q (%v), want 2 usage", tc.args, ExitCode(err), Label(err), err)
			}
		})
	}

	spMust(t, dir, "plan", "edit", id, "--outcome", "")
	shown := spJSON[planDTO](t, spMust(t, dir, "plan", "show", id, "--json"))
	if shown.Body != "the approved text" {
		t.Errorf("body = %q, want the approved text intact", shown.Body)
	}
	if shown.Outcome != "" {
		t.Errorf("outcome = %q, want an outcome still clearable", shown.Outcome)
	}
}

// planIDs reads the short ids out of a lean plan listing.
func planIDs(out string) []string {
	var ids []string
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		ids = append(ids, strings.SplitN(line, "\t", 2)[0])
	}
	return ids
}

// TestPlanShowRejectsAnotherKindsID pins that the plan noun is kind-checked: a
// task id resolves nowhere under plans, and the miss carries the cross-kind
// hint pointing at the kind that owns it.
func TestPlanShowRejectsAnotherKindsID(t *testing.T) {
	dir := spInitRepo(t)
	task := spID(t, spMust(t, dir, "task", "add", "unrelated", "--no-validation-criteria", "--json"))

	_, _, err := spRun(t, dir, "", "plan", "show", task)
	if ExitCode(err) != 3 || !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("plan show of a task id = exit %d (%v), want 3 not-found", ExitCode(err), err)
	}
	var hint *notFoundHintError
	if !errors.As(err, &hint) || !strings.Contains(hint.hintLine(), "is a task") {
		t.Errorf("error = %v, want the cross-kind hint naming the task kind", err)
	}
}
