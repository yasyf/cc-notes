package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// spProjectCreatedAt reads a project's creation stamp, which lives with "show":
// a listing carries only the update stamp.
func spProjectCreatedAt(t *testing.T, dir, id string) string {
	t.Helper()
	return spJSON[projectDTO](t, spMust(t, dir, "project", "show", id, "--json")).CreatedAt
}

// spProjectDescription reads back the description of the project an add
// acknowledgement identifies.
func spProjectDescription(t *testing.T, dir, ack string) string {
	t.Helper()
	return spJSON[projectDTO](t, spMust(t, dir, "project", "show", spID(t, ack), "--json")).Description
}

func TestProjectAddShow(t *testing.T) {
	dir := spInitRepo(t)
	out := spMust(t, dir, "project", "add", "Platform", "--body", "the platform",
		"--label", "x", "--label", "a", "--json")
	if !strings.HasPrefix(out, `{"id":"`) {
		t.Fatalf("project JSON does not lead with id: %q", out)
	}
	for _, frag := range []string{`"sprints"`, `"tasks"`, `"closed_at"`, `"description"`, `"labels"`} {
		if strings.Contains(out, frag) {
			t.Errorf("project add ack %q carries %q; a write acknowledgement is a summary", out, frag)
		}
	}
	added := spJSON[projectSummaryDTO](t, out)
	if added.Title != "Platform" || added.Status != "active" {
		t.Errorf("ack title/status = %q/%q, want Platform/active", added.Title, added.Status)
	}

	shown := spJSON[projectDTO](t, spMust(t, dir, "project", "show", added.ID, "--json"))
	if shown.ID != added.ID || len(shown.ID) != 40 {
		t.Errorf("show id = %q, want %q (40 hex)", shown.ID, added.ID)
	}
	if shown.Description != "the platform" {
		t.Errorf("show description = %q, want the platform", shown.Description)
	}
	if strings.Join(shown.Labels, ",") != "a,x" {
		t.Errorf("labels = %v, want [a x]", shown.Labels)
	}
	if shown.Author != spActor {
		t.Errorf("author = %q, want %q", shown.Author, spActor)
	}
	if shown.ClosedAt != nil {
		t.Errorf("closed_at = %v, want absent", *shown.ClosedAt)
	}
	if len(shown.Sprints) != 0 || len(shown.Tasks) != 0 {
		t.Errorf("sprints/tasks = %v/%v, want empty", shown.Sprints, shown.Tasks)
	}
	lean := spMust(t, dir, "project", "show", added.ID)
	if !strings.HasPrefix(lean, "id: "+added.ID+"\ntitle: Platform\nstatus: active\n") {
		t.Errorf("lean show header order broken: %q", lean)
	}
}

func TestProjectListAndStatusFilter(t *testing.T) {
	dir := spInitRepo(t)
	aID := spID(t, spMust(t, dir, "project", "add", "A", "--json"))
	bID := spID(t, spMust(t, dir, "project", "add", "B", "--json"))
	spMust(t, dir, "project", "complete", aID)

	all := spJSON[[]projectSummaryDTO](t, spMust(t, dir, "project", "list", "--json"))
	if len(all) != 2 {
		t.Fatalf("list --json returned %d projects, want 2", len(all))
	}
	pairs := make([][2]string, len(all))
	for i, p := range all {
		pairs[i] = [2]string{spProjectCreatedAt(t, dir, p.ID), p.ID}
	}
	spAssertSorted(t, pairs)

	completed := spJSON[[]projectSummaryDTO](t, spMust(t, dir, "project", "list", "--status", "completed", "--json"))
	if len(completed) != 1 || completed[0].ID != aID {
		t.Errorf("list --status completed = %v, want only %s", completed, aID)
	}
	active := spJSON[[]projectSummaryDTO](t, spMust(t, dir, "project", "list", "--status", "active", "--json"))
	if len(active) != 1 || active[0].ID != bID {
		t.Errorf("list --status active = %v, want only %s", active, bID)
	}

	if _, _, err := spRun(t, dir, "", "project", "list", "--status", "bogus"); err == nil || ExitCode(err) != 1 {
		t.Errorf("list --status bogus err = %v (exit %d), want exit 1", err, ExitCode(err))
	}
}

func TestProjectEdit(t *testing.T) {
	dir := spInitRepo(t)
	id := spID(t, spMust(t, dir, "project", "add", "P", "--label", "keep", "--label", "drop", "--json"))

	ack := spJSON[projectSummaryDTO](t, spMust(t, dir, "project", "edit", id,
		"--title", "P2", "--body", "new", "--add-label", "new", "--rm-label", "drop", "--json"))
	if ack.Title != "P2" {
		t.Errorf("edit ack title = %q, want P2", ack.Title)
	}
	edited := spJSON[projectDTO](t, spMust(t, dir, "project", "show", id, "--json"))
	if edited.Title != "P2" || edited.Description != "new" {
		t.Errorf("title/desc = %q/%q, want P2/new", edited.Title, edited.Description)
	}
	if strings.Join(edited.Labels, ",") != "keep,new" {
		t.Errorf("labels = %v, want [keep new]", edited.Labels)
	}

	if _, _, err := spRun(t, dir, "", "project", "edit", id); !isUsage(err) {
		t.Errorf("edit with no flags err = %v, want UsageError exit 2", err)
	}
}

// TestProjectAddBodyForms proves "project add" resolves the description from a
// positional BODY, --body, or - (stdin), and rejects two sources.
func TestProjectAddBodyForms(t *testing.T) {
	dir := spInitRepo(t)

	if pos := spProjectDescription(t, dir, spMust(t, dir, "project", "add", "P1", "positional desc", "--json")); pos != "positional desc" {
		t.Errorf("positional desc = %q, want %q", pos, "positional desc")
	}
	if flag := spProjectDescription(t, dir, spMust(t, dir, "project", "add", "P2", "--body", "flag desc", "--json")); flag != "flag desc" {
		t.Errorf("--body desc = %q, want %q", flag, "flag desc")
	}
	out, _, err := spRun(t, dir, "stdin desc\n", "project", "add", "P3", "-", "--json")
	if err != nil {
		t.Fatalf("project add - : %v", err)
	}
	if got := spProjectDescription(t, dir, out); got != "stdin desc" {
		t.Errorf("stdin desc = %q, want %q", got, "stdin desc")
	}
	if _, _, err := spRun(t, dir, "", "project", "add", "P4", "pos", "--body", "flag"); !isUsage(err) {
		t.Errorf("positional+--body err = %v (exit %d), want UsageError exit 2", err, ExitCode(err))
	}
}

// TestProjectCommentBodyForms proves "project comment" resolves the comment text
// from a positional BODY, --body, or - (stdin), requires exactly one source, and
// persists it.
func TestProjectCommentBodyForms(t *testing.T) {
	dir := spInitRepo(t)
	id := spID(t, spMust(t, dir, "project", "add", "P", "--json"))

	spMust(t, dir, "project", "comment", id, "positional comment")
	spMust(t, dir, "project", "comment", id, "--body", "flag comment")
	if _, _, err := spRun(t, dir, "stdin comment\n", "project", "comment", id, "-"); err != nil {
		t.Fatalf("project comment - : %v", err)
	}

	if _, _, err := spRun(t, dir, "", "project", "comment", id); !isUsage(err) {
		t.Errorf("comment with no text err = %v (exit %d), want UsageError exit 2", err, ExitCode(err))
	}
	if _, _, err := spRun(t, dir, "", "project", "comment", id, "pos", "--body", "flag"); !isUsage(err) {
		t.Errorf("comment positional+--body err = %v (exit %d), want UsageError exit 2", err, ExitCode(err))
	}

	shown := spJSON[projectDTO](t, spMust(t, dir, "project", "show", id, "--json"))
	got := make([]string, len(shown.Comments))
	for i, c := range shown.Comments {
		got[i] = c.Body
	}
	want := []string{"positional comment", "flag comment", "stdin comment"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("comments = %v, want %v", got, want)
	}
}

func TestProjectStatusTransitions(t *testing.T) {
	dir := spInitRepo(t)
	id := spID(t, spMust(t, dir, "project", "add", "P", "--json"))
	if fresh := spJSON[projectDTO](t, spMust(t, dir, "project", "show", id, "--json")); fresh.Status != "active" || fresh.ClosedAt != nil {
		t.Fatalf("fresh project = %+v, want active with no closed_at", fresh)
	}

	if got := spJSON[projectSummaryDTO](t, spMust(t, dir, "project", "complete", id, "--json")).Status; got != "completed" {
		t.Fatalf("complete ack status = %q, want completed", got)
	}
	if completed := spJSON[projectDTO](t, spMust(t, dir, "project", "show", id, "--json")); completed.ClosedAt == nil {
		t.Fatalf("completed = %+v, want closed_at set", completed)
	}

	_, _, err := spRun(t, dir, "", "project", "archive", id)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || ExitCode(err) != 4 {
		t.Fatalf("archive on completed err = %v (exit %d), want ConflictError exit 4", err, ExitCode(err))
	}
	if want := id[:7] + " already completed"; conflict.Msg != want {
		t.Fatalf("archive msg = %q, want %q", conflict.Msg, want)
	}

	for _, tc := range []struct {
		verb   string
		status string
	}{
		{"archive", "archived"},
		{"cancel", "cancelled"},
	} {
		freshID := spID(t, spMust(t, dir, "project", "add", tc.verb, "--json"))
		if got := spJSON[projectSummaryDTO](t, spMust(t, dir, "project", tc.verb, freshID, "--json")).Status; got != tc.status {
			t.Fatalf("%s ack status = %q, want %s", tc.verb, got, tc.status)
		}
		if got := spJSON[projectDTO](t, spMust(t, dir, "project", "show", freshID, "--json")); got.ClosedAt == nil {
			t.Fatalf("%s = %+v, want closed_at set", tc.verb, got)
		}
	}
}

// TestProjectActivateRoundTrip proves activate un-archives a project (add →
// archive → activate → active in list), refuses a no-op activate on an active
// project, and refuses activating a terminal (completed or cancelled) project
// with a *ConflictError naming the actual status — only archived reactivates.
func TestProjectActivateRoundTrip(t *testing.T) {
	dir := spInitRepo(t)
	id := spID(t, spMust(t, dir, "project", "add", "P", "--json"))

	archived := spJSON[projectSummaryDTO](t, spMust(t, dir, "project", "archive", id, "--json"))
	if archived.Status != "archived" {
		t.Fatalf("archive = %+v, want archived", archived)
	}

	reactivated := spJSON[projectSummaryDTO](t, spMust(t, dir, "project", "activate", id, "--json"))
	if reactivated.Status != "active" {
		t.Fatalf("activate = %+v, want active", reactivated)
	}

	active := spJSON[[]projectSummaryDTO](t, spMust(t, dir, "project", "list", "--status", "active", "--json"))
	if len(active) != 1 || active[0].ID != id {
		t.Fatalf("list --status active = %v, want the reactivated %s", active, id)
	}

	// activate on an already-active project is a no-op refusal.
	_, _, err := spRun(t, dir, "", "project", "activate", id)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || ExitCode(err) != 4 {
		t.Fatalf("activate on active err = %v (exit %d), want ConflictError exit 4", err, ExitCode(err))
	}
	if want := id[:7] + " already active"; conflict.Msg != want {
		t.Fatalf("activate-on-active msg = %q, want %q", conflict.Msg, want)
	}

	// Terminal projects (completed, cancelled) cannot be activated; the refusal
	// is a ConflictError (exit 4) whose message names the actual status. Only an
	// archived project reactivates.
	for _, tc := range []struct {
		verb, status string
	}{
		{"complete", "completed"},
		{"cancel", "cancelled"},
	} {
		termID := spID(t, spMust(t, dir, "project", "add", "Term-"+tc.verb, "--json"))
		spMust(t, dir, "project", tc.verb, termID)
		_, _, err := spRun(t, dir, "", "project", "activate", termID)
		if !errors.As(err, &conflict) || ExitCode(err) != 4 {
			t.Fatalf("activate on %s err = %v (exit %d), want ConflictError exit 4", tc.status, err, ExitCode(err))
		}
		if want := termID[:7] + " already " + tc.status; conflict.Msg != want {
			t.Fatalf("activate-on-%s msg = %q, want %q", tc.status, conflict.Msg, want)
		}
	}
}

func TestProjectReverseIndex(t *testing.T) {
	dir := spInitRepo(t)
	id := spID(t, spMust(t, dir, "project", "add", "P", "--json"))
	sprintID := spID(t, spMust(t, dir, "sprint", "add", "S", "--project", id, "--json"))

	direct := spID(t, spMust(t, dir, "task", "add", "Direct", "--no-validation-criteria", "--json"))
	spSetTaskProject(t, dir, direct, id)
	viaSprint := spID(t, spMust(t, dir, "task", "add", "ViaSprint", "--no-validation-criteria", "--json"))
	spSetTaskSprint(t, dir, viaSprint, sprintID)
	spMust(t, dir, "task", "add", "Unrelated", "--no-validation-criteria", "--json")

	shown := spJSON[projectDTO](t, spMust(t, dir, "project", "show", id, "--json"))
	if len(shown.Sprints) != 1 || shown.Sprints[0] != sprintID {
		t.Fatalf("sprints = %v, want [%s]", shown.Sprints, sprintID)
	}
	want := []string{direct, viaSprint}
	slices.Sort(want)
	if !slices.Equal(shown.Tasks, want) {
		t.Fatalf("tasks = %v, want %v (direct ∪ via-sprint, no outsider)", shown.Tasks, want)
	}
	lean := spMust(t, dir, "project", "show", id)
	if !strings.Contains(lean, "sprints: "+sprintID[:7]+"\n") {
		t.Errorf("lean show missing sprints header for %s: %q", sprintID[:7], lean)
	}
}
