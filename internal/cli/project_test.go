package cli

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestProjectAddShow(t *testing.T) {
	dir := spInitRepo(t)
	out := spMust(t, dir, "project", "add", "Platform", "--body", "the platform",
		"--label", "x", "--label", "a", "--json")
	if !strings.HasPrefix(out, `{"id":"`) {
		t.Fatalf("project JSON does not lead with id: %q", out)
	}
	for _, frag := range []string{`"status":"active"`, `"sprints":[]`, `"tasks":[]`, `"closed_at":null`} {
		if !strings.Contains(out, frag) {
			t.Errorf("project JSON %q missing %q", out, frag)
		}
	}
	added := spJSON[projectDTO](t, out)
	if added.Title != "Platform" || added.Description != "the platform" {
		t.Errorf("title/desc = %q/%q", added.Title, added.Description)
	}
	if added.Status != "active" {
		t.Errorf("status = %q, want active", added.Status)
	}
	if strings.Join(added.Labels, ",") != "a,x" {
		t.Errorf("labels = %v, want [a x]", added.Labels)
	}
	if added.Author != spActor {
		t.Errorf("author = %q, want %q", added.Author, spActor)
	}
	if added.ClosedAt != nil {
		t.Errorf("closed_at = %v, want null", *added.ClosedAt)
	}
	if len(added.Sprints) != 0 || len(added.Tasks) != 0 {
		t.Errorf("sprints/tasks = %v/%v, want empty", added.Sprints, added.Tasks)
	}

	shown := spJSON[projectDTO](t, spMust(t, dir, "project", "show", added.ID, "--json"))
	if shown.ID != added.ID || len(shown.ID) != 40 {
		t.Errorf("show id = %q, want %q (40 hex)", shown.ID, added.ID)
	}
	lean := spMust(t, dir, "project", "show", added.ID)
	if !strings.HasPrefix(lean, "id: "+added.ID+"\ntitle: Platform\nstatus: active\n") {
		t.Errorf("lean show header order broken: %q", lean)
	}
}

func TestProjectListAndStatusFilter(t *testing.T) {
	dir := spInitRepo(t)
	a := spJSON[projectDTO](t, spMust(t, dir, "project", "add", "A", "--json"))
	b := spJSON[projectDTO](t, spMust(t, dir, "project", "add", "B", "--json"))
	spMust(t, dir, "project", "complete", a.ID)

	all := spJSON[[]projectDTO](t, spMust(t, dir, "project", "list", "--json"))
	if len(all) != 2 {
		t.Fatalf("list --json returned %d projects, want 2", len(all))
	}
	pairs := make([][2]string, len(all))
	for i, p := range all {
		pairs[i] = [2]string{p.CreatedAt, p.ID}
	}
	spAssertSorted(t, pairs)

	completed := spJSON[[]projectDTO](t, spMust(t, dir, "project", "list", "--status", "completed", "--json"))
	if len(completed) != 1 || completed[0].ID != a.ID {
		t.Errorf("list --status completed = %v, want only %s", completed, a.ID)
	}
	active := spJSON[[]projectDTO](t, spMust(t, dir, "project", "list", "--status", "active", "--json"))
	if len(active) != 1 || active[0].ID != b.ID {
		t.Errorf("list --status active = %v, want only %s", active, b.ID)
	}

	if _, _, err := spRun(t, dir, "", "project", "list", "--status", "bogus"); err == nil || ExitCode(err) != 1 {
		t.Errorf("list --status bogus err = %v (exit %d), want exit 1", err, ExitCode(err))
	}
}

func TestProjectEdit(t *testing.T) {
	dir := spInitRepo(t)
	p := spJSON[projectDTO](t, spMust(t, dir, "project", "add", "P", "--label", "keep", "--label", "drop", "--json"))

	edited := spJSON[projectDTO](t, spMust(t, dir, "project", "edit", p.ID,
		"--title", "P2", "--body", "new", "--add-label", "new", "--rm-label", "drop", "--json"))
	if edited.Title != "P2" || edited.Description != "new" {
		t.Errorf("title/desc = %q/%q, want P2/new", edited.Title, edited.Description)
	}
	if strings.Join(edited.Labels, ",") != "keep,new" {
		t.Errorf("labels = %v, want [keep new]", edited.Labels)
	}

	if _, _, err := spRun(t, dir, "", "project", "edit", p.ID); !isUsage(err) {
		t.Errorf("edit with no flags err = %v, want UsageError exit 2", err)
	}
}

// TestProjectAddBodyForms proves "project add" resolves the description from a
// positional BODY, --body, or - (stdin), and rejects two sources.
func TestProjectAddBodyForms(t *testing.T) {
	dir := spInitRepo(t)

	pos := spJSON[projectDTO](t, spMust(t, dir, "project", "add", "P1", "positional desc", "--json"))
	if pos.Description != "positional desc" {
		t.Errorf("positional desc = %q, want %q", pos.Description, "positional desc")
	}
	flag := spJSON[projectDTO](t, spMust(t, dir, "project", "add", "P2", "--body", "flag desc", "--json"))
	if flag.Description != "flag desc" {
		t.Errorf("--body desc = %q, want %q", flag.Description, "flag desc")
	}
	out, _, err := spRun(t, dir, "stdin desc\n", "project", "add", "P3", "-", "--json")
	if err != nil {
		t.Fatalf("project add - : %v", err)
	}
	if got := spJSON[projectDTO](t, out).Description; got != "stdin desc" {
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
	p := spJSON[projectDTO](t, spMust(t, dir, "project", "add", "P", "--json"))

	spMust(t, dir, "project", "comment", p.ID, "positional comment")
	spMust(t, dir, "project", "comment", p.ID, "--body", "flag comment")
	if _, _, err := spRun(t, dir, "stdin comment\n", "project", "comment", p.ID, "-"); err != nil {
		t.Fatalf("project comment - : %v", err)
	}

	if _, _, err := spRun(t, dir, "", "project", "comment", p.ID); !isUsage(err) {
		t.Errorf("comment with no text err = %v (exit %d), want UsageError exit 2", err, ExitCode(err))
	}
	if _, _, err := spRun(t, dir, "", "project", "comment", p.ID, "pos", "--body", "flag"); !isUsage(err) {
		t.Errorf("comment positional+--body err = %v (exit %d), want UsageError exit 2", err, ExitCode(err))
	}

	shown := spJSON[projectDTO](t, spMust(t, dir, "project", "show", p.ID, "--json"))
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
	p := spJSON[projectDTO](t, spMust(t, dir, "project", "add", "P", "--json"))
	if p.Status != "active" || p.ClosedAt != nil {
		t.Fatalf("fresh project = %+v, want active/null", p)
	}

	completed := spJSON[projectDTO](t, spMust(t, dir, "project", "complete", p.ID, "--json"))
	if completed.Status != "completed" || completed.ClosedAt == nil {
		t.Fatalf("completed = %+v, want completed with closed_at set", completed)
	}

	_, _, err := spRun(t, dir, "", "project", "archive", p.ID)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || ExitCode(err) != 4 {
		t.Fatalf("archive on completed err = %v (exit %d), want ConflictError exit 4", err, ExitCode(err))
	}
	if want := p.ID[:7] + " already completed"; conflict.Msg != want {
		t.Fatalf("archive msg = %q, want %q", conflict.Msg, want)
	}

	for _, tc := range []struct {
		verb   string
		status string
	}{
		{"archive", "archived"},
		{"cancel", "cancelled"},
	} {
		fresh := spJSON[projectDTO](t, spMust(t, dir, "project", "add", tc.verb, "--json"))
		got := spJSON[projectDTO](t, spMust(t, dir, "project", tc.verb, fresh.ID, "--json"))
		if got.Status != tc.status || got.ClosedAt == nil {
			t.Fatalf("%s = %+v, want %s with closed_at set", tc.verb, got, tc.status)
		}
	}
}

// TestProjectActivateRoundTrip proves activate un-archives a project (add →
// archive → activate → active in list), refuses a no-op activate on an active
// project, and refuses activating a terminal (completed or cancelled) project
// with a *ConflictError naming the actual status — only archived reactivates.
func TestProjectActivateRoundTrip(t *testing.T) {
	dir := spInitRepo(t)
	p := spJSON[projectDTO](t, spMust(t, dir, "project", "add", "P", "--json"))

	archived := spJSON[projectDTO](t, spMust(t, dir, "project", "archive", p.ID, "--json"))
	if archived.Status != "archived" {
		t.Fatalf("archive = %+v, want archived", archived)
	}

	reactivated := spJSON[projectDTO](t, spMust(t, dir, "project", "activate", p.ID, "--json"))
	if reactivated.Status != "active" {
		t.Fatalf("activate = %+v, want active", reactivated)
	}

	active := spJSON[[]projectDTO](t, spMust(t, dir, "project", "list", "--status", "active", "--json"))
	if len(active) != 1 || active[0].ID != p.ID {
		t.Fatalf("list --status active = %v, want the reactivated %s", active, p.ID)
	}

	// activate on an already-active project is a no-op refusal.
	_, _, err := spRun(t, dir, "", "project", "activate", p.ID)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || ExitCode(err) != 4 {
		t.Fatalf("activate on active err = %v (exit %d), want ConflictError exit 4", err, ExitCode(err))
	}
	if want := p.ID[:7] + " already active"; conflict.Msg != want {
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
		term := spJSON[projectDTO](t, spMust(t, dir, "project", "add", "Term-"+tc.verb, "--json"))
		spMust(t, dir, "project", tc.verb, term.ID)
		_, _, err := spRun(t, dir, "", "project", "activate", term.ID)
		if !errors.As(err, &conflict) || ExitCode(err) != 4 {
			t.Fatalf("activate on %s err = %v (exit %d), want ConflictError exit 4", tc.status, err, ExitCode(err))
		}
		if want := term.ID[:7] + " already " + tc.status; conflict.Msg != want {
			t.Fatalf("activate-on-%s msg = %q, want %q", tc.status, conflict.Msg, want)
		}
	}
}

func TestProjectReverseIndex(t *testing.T) {
	dir := spInitRepo(t)
	p := spJSON[projectDTO](t, spMust(t, dir, "project", "add", "P", "--json"))
	sp := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "add", "S", "--project", p.ID, "--json"))

	direct := spID(t, spMust(t, dir, "task", "add", "Direct", "--no-validation-criteria", "--json"))
	spSetTaskProject(t, dir, direct, p.ID)
	viaSprint := spID(t, spMust(t, dir, "task", "add", "ViaSprint", "--no-validation-criteria", "--json"))
	spSetTaskSprint(t, dir, viaSprint, sp.ID)
	spMust(t, dir, "task", "add", "Unrelated", "--no-validation-criteria", "--json")

	shown := spJSON[projectDTO](t, spMust(t, dir, "project", "show", p.ID, "--json"))
	if len(shown.Sprints) != 1 || shown.Sprints[0] != sp.ID {
		t.Fatalf("sprints = %v, want [%s]", shown.Sprints, sp.ID)
	}
	want := []string{direct, viaSprint}
	slices.Sort(want)
	if !slices.Equal(shown.Tasks, want) {
		t.Fatalf("tasks = %v, want %v (direct ∪ via-sprint, no outsider)", shown.Tasks, want)
	}
	lean := spMust(t, dir, "project", "show", p.ID)
	if !strings.Contains(lean, "sprints: "+sp.ID[:7]+"\n") {
		t.Errorf("lean show missing sprints header for %s: %q", sp.ID[:7], lean)
	}
}
