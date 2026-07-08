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
