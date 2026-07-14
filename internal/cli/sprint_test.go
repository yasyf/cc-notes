package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// spActor is the frozen identity these sprint/project tests author every entity
// under, so author and comment assertions are deterministic.
const spActor = "Agent A <a@example.com>"

// spInitRepo inits a repo on main with a local identity, freezes the
// cc-notes actor to spActor, and returns the repo dir.
func spInitRepo(t *testing.T) string {
	t.Helper()
	dir := gittest.InitRepo(t)
	t.Setenv("CC_NOTES_ACTOR", spActor)
	return dir
}

// spRun executes the cobra tree in-process against dir and returns stdout,
// stderr, and the command error.
func spRun(t *testing.T, dir, stdin string, args ...string) (string, string, error) {
	t.Helper()
	t.Chdir(dir)
	root := NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.ExecuteContext(t.Context())
	return stdout.String(), stderr.String(), err
}

func spMust(t *testing.T, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, err := spRun(t, dir, "", args...)
	if err != nil {
		t.Fatalf("cc-notes %s: %v (stderr %q)", strings.Join(args, " "), err, stderr)
	}
	return stdout
}

func spJSON[T any](t *testing.T, raw string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	return v
}

// spID extracts the full hex id from an entity's JSON output.
func spID(t *testing.T, raw string) string {
	t.Helper()
	var v struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("unmarshal id %q: %v", raw, err)
	}
	return v.ID
}

// spSetTaskSprint stamps a task's sprint membership through the store directly,
// so reverse-index tests exercise the index independently of the task add path
// by seeding the LWW pointer with a real SetSprint op.
func spSetTaskSprint(t *testing.T, dir, taskID, sprintID string) {
	t.Helper()
	spAppendTask(t, dir, taskID, model.SetSprint{Sprint: model.EntityID(sprintID)})
}

// spSetTaskProject stamps a task's project membership through the store directly.
func spSetTaskProject(t *testing.T, dir, taskID, projectID string) {
	t.Helper()
	spAppendTask(t, dir, taskID, model.SetProject{Project: model.EntityID(projectID)})
}

func spAppendTask(t *testing.T, dir, taskID string, op model.Op) {
	t.Helper()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	ref, err := s.Resolve(t.Context(), model.KindTask, taskID)
	if err != nil {
		t.Fatalf("resolve task %s: %v", taskID, err)
	}
	if _, err := s.Append(t.Context(), ref, []model.Op{op}); err != nil {
		t.Fatalf("append %s: %v", op.OpKind(), err)
	}
}

// spAssertSorted fails unless pairs are non-decreasing by (created_at, id) — the
// order ListSprints/ListProjects document.
func spAssertSorted(t *testing.T, pairs [][2]string) {
	t.Helper()
	for i := 1; i < len(pairs); i++ {
		prev, cur := pairs[i-1], pairs[i]
		if prev[0] > cur[0] || (prev[0] == cur[0] && prev[1] > cur[1]) {
			t.Fatalf("entries not sorted by (created_at, id): %v before %v", prev, cur)
		}
	}
}

func TestSprintAddShow(t *testing.T) {
	dir := spInitRepo(t)
	proj := spID(t, spMust(t, dir, "project", "add", "Roadmap", "--json"))

	out := spMust(t, dir, "sprint", "add", "Sprint 1", "--body", "first sprint",
		"--project", proj, "--label", "x", "--label", "a",
		"--start", "2026-01-01", "--end", "2026-02-01", "--json")
	if !strings.HasPrefix(out, `{"id":"`) {
		t.Fatalf("sprint JSON does not lead with id: %q", out)
	}
	for _, frag := range []string{`"status":"planned"`, `"start_date":"2026-01-01T00:00:00Z"`, `"end_date":"2026-02-01T00:00:00Z"`, `"tasks":[]`} {
		if !strings.Contains(out, frag) {
			t.Errorf("sprint JSON %q missing %q", out, frag)
		}
	}
	added := spJSON[sprintDTO](t, out)
	if added.Title != "Sprint 1" || added.Description != "first sprint" {
		t.Errorf("title/desc = %q/%q", added.Title, added.Description)
	}
	if added.Project == nil || *added.Project != proj {
		t.Errorf("project = %v, want %s", added.Project, proj)
	}
	if added.Status != "planned" {
		t.Errorf("status = %q, want planned", added.Status)
	}
	if added.StartDate == nil || *added.StartDate != "2026-01-01T00:00:00Z" {
		t.Errorf("start_date = %v, want 2026-01-01T00:00:00Z", added.StartDate)
	}
	if added.EndDate == nil || *added.EndDate != "2026-02-01T00:00:00Z" {
		t.Errorf("end_date = %v, want 2026-02-01T00:00:00Z", added.EndDate)
	}
	if strings.Join(added.Labels, ",") != "a,x" {
		t.Errorf("labels = %v, want [a x]", added.Labels)
	}
	if added.Author != spActor {
		t.Errorf("author = %q, want %q", added.Author, spActor)
	}
	if added.StartedAt != nil || added.ClosedAt != nil {
		t.Errorf("started/closed = %v/%v, want null/null", added.StartedAt, added.ClosedAt)
	}
	if len(added.Tasks) != 0 {
		t.Errorf("tasks = %v, want empty", added.Tasks)
	}

	shown := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "show", added.ID, "--json"))
	if shown.ID != added.ID || len(shown.ID) != 40 {
		t.Errorf("show id = %q, want %q (40 hex)", shown.ID, added.ID)
	}
	lean := spMust(t, dir, "sprint", "show", added.ID)
	if !strings.HasPrefix(lean, "id: "+added.ID+"\nproject: "+proj[:7]+"\ntitle: Sprint 1\nstatus: planned\n") {
		t.Errorf("lean show header order broken: %q", lean)
	}
	if !strings.Contains(lean, "start_date: 2026-01-01T00:00:00Z\n") || !strings.Contains(lean, "end_date: 2026-02-01T00:00:00Z\n") {
		t.Errorf("lean show missing date headers: %q", lean)
	}
}

func TestSprintListOrderAndFilter(t *testing.T) {
	dir := spInitRepo(t)
	proj := spID(t, spMust(t, dir, "project", "add", "P", "--json"))
	a := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "add", "A", "--project", proj, "--json"))
	b := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "add", "B", "--json"))
	c := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "add", "C", "--json"))
	spMust(t, dir, "sprint", "activate", a.ID)

	all := spJSON[[]sprintDTO](t, spMust(t, dir, "sprint", "list", "--json"))
	if len(all) != 3 {
		t.Fatalf("list --json returned %d sprints, want 3", len(all))
	}
	pairs := make([][2]string, len(all))
	seen := map[string]bool{}
	for i, sp := range all {
		pairs[i] = [2]string{sp.CreatedAt, sp.ID}
		seen[sp.ID] = true
	}
	spAssertSorted(t, pairs)
	for _, id := range []string{a.ID, b.ID, c.ID} {
		if !seen[id] {
			t.Errorf("list missing sprint %s", id)
		}
	}

	active := spJSON[[]sprintDTO](t, spMust(t, dir, "sprint", "list", "--status", "active", "--json"))
	if len(active) != 1 || active[0].ID != a.ID {
		t.Errorf("list --status active = %v, want only %s", active, a.ID)
	}
	planned := spJSON[[]sprintDTO](t, spMust(t, dir, "sprint", "list", "--status", "planned", "--json"))
	if len(planned) != 2 {
		t.Errorf("list --status planned = %d sprints, want 2 (B,C)", len(planned))
	}
	for _, sp := range planned {
		if sp.ID == a.ID {
			t.Errorf("list --status planned included the active sprint %s", a.ID)
		}
	}
	both := spJSON[[]sprintDTO](t, spMust(t, dir, "sprint", "list", "--status", "planned,active", "--json"))
	if len(both) != 3 {
		t.Errorf("list --status planned,active = %d sprints, want 3", len(both))
	}
	inProj := spJSON[[]sprintDTO](t, spMust(t, dir, "sprint", "list", "--project", proj, "--json"))
	if len(inProj) != 1 || inProj[0].ID != a.ID {
		t.Errorf("list --project = %v, want only %s", inProj, a.ID)
	}

	if _, _, err := spRun(t, dir, "", "sprint", "list", "--status", "bogus"); err == nil || ExitCode(err) != 1 {
		t.Errorf("list --status bogus err = %v (exit %d), want exit 1", err, ExitCode(err))
	}
}

func TestSprintEdit(t *testing.T) {
	dir := spInitRepo(t)
	p1 := spID(t, spMust(t, dir, "project", "add", "P1", "--json"))
	p2 := spID(t, spMust(t, dir, "project", "add", "P2", "--json"))
	sp := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "add", "S", "--project", p1,
		"--start", "2026-01-01", "--end", "2026-02-01", "--label", "keep", "--label", "drop", "--json"))

	edited := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "edit", sp.ID,
		"--title", "S2", "--project", p2, "--start", "2026-03-01", "--no-end",
		"--add-label", "new", "--rm-label", "drop", "--json"))
	if edited.Title != "S2" {
		t.Errorf("title = %q, want S2", edited.Title)
	}
	if edited.Project == nil || *edited.Project != p2 {
		t.Errorf("project = %v, want %s", edited.Project, p2)
	}
	if edited.StartDate == nil || *edited.StartDate != "2026-03-01T00:00:00Z" {
		t.Errorf("start_date = %v, want 2026-03-01T00:00:00Z", edited.StartDate)
	}
	if edited.EndDate != nil {
		t.Errorf("end_date = %v, want null after --no-end", *edited.EndDate)
	}
	if strings.Join(edited.Labels, ",") != "keep,new" {
		t.Errorf("labels = %v, want [keep new]", edited.Labels)
	}

	cleared := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "edit", sp.ID, "--no-project", "--no-start", "--json"))
	if cleared.Project != nil {
		t.Errorf("project = %v, want null after --no-project", *cleared.Project)
	}
	if cleared.StartDate != nil {
		t.Errorf("start_date = %v, want null after --no-start", *cleared.StartDate)
	}

	if _, _, err := spRun(t, dir, "", "sprint", "edit", sp.ID); !isUsage(err) {
		t.Errorf("edit with no flags err = %v, want UsageError exit 2", err)
	}
	// The --x/--no-x pairs are cobra mutually-exclusive groups now: their error is
	// a flag-group error mapped to exit 2 by classify, not the concrete UsageError.
	for _, args := range [][]string{
		{"sprint", "edit", sp.ID, "--project", p1, "--no-project"},
		{"sprint", "edit", sp.ID, "--start", "2026-01-01", "--no-start"},
		{"sprint", "edit", sp.ID, "--end", "2026-01-01", "--no-end"},
	} {
		if _, _, err := spRun(t, dir, "", args...); ExitCode(err) != 2 || !isFlagGroupError(err) {
			t.Errorf("%v err = %v (exit %d), want flag-group usage error exit 2", args, err, ExitCode(err))
		}
	}
}

// TestSprintAddBodyForms proves "sprint add" resolves the description from a
// positional BODY, --body, or - (stdin), and rejects two sources.
func TestSprintAddBodyForms(t *testing.T) {
	dir := spInitRepo(t)

	pos := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "add", "S1", "positional desc", "--json"))
	if pos.Description != "positional desc" {
		t.Errorf("positional desc = %q, want %q", pos.Description, "positional desc")
	}
	flag := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "add", "S2", "--body", "flag desc", "--json"))
	if flag.Description != "flag desc" {
		t.Errorf("--body desc = %q, want %q", flag.Description, "flag desc")
	}
	out, _, err := spRun(t, dir, "stdin desc\n", "sprint", "add", "S3", "-", "--json")
	if err != nil {
		t.Fatalf("sprint add - : %v", err)
	}
	if got := spJSON[sprintDTO](t, out).Description; got != "stdin desc" {
		t.Errorf("stdin desc = %q, want %q", got, "stdin desc")
	}
	if _, _, err := spRun(t, dir, "", "sprint", "add", "S4", "pos", "--body", "flag"); !isUsage(err) {
		t.Errorf("positional+--body err = %v (exit %d), want UsageError exit 2", err, ExitCode(err))
	}
}

// TestSprintCommentBodyForms proves "sprint comment" resolves the comment text
// from a positional BODY, --body, or - (stdin), requires exactly one source, and
// persists it.
func TestSprintCommentBodyForms(t *testing.T) {
	dir := spInitRepo(t)
	sp := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "add", "S", "--json"))

	spMust(t, dir, "sprint", "comment", sp.ID, "positional comment")
	spMust(t, dir, "sprint", "comment", sp.ID, "--body", "flag comment")
	if _, _, err := spRun(t, dir, "stdin comment\n", "sprint", "comment", sp.ID, "-"); err != nil {
		t.Fatalf("sprint comment - : %v", err)
	}

	if _, _, err := spRun(t, dir, "", "sprint", "comment", sp.ID); !isUsage(err) {
		t.Errorf("comment with no text err = %v (exit %d), want UsageError exit 2", err, ExitCode(err))
	}
	if _, _, err := spRun(t, dir, "", "sprint", "comment", sp.ID, "pos", "--body", "flag"); !isUsage(err) {
		t.Errorf("comment positional+--body err = %v (exit %d), want UsageError exit 2", err, ExitCode(err))
	}

	shown := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "show", sp.ID, "--json"))
	got := make([]string, len(shown.Comments))
	for i, c := range shown.Comments {
		got[i] = c.Body
	}
	want := []string{"positional comment", "flag comment", "stdin comment"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("comments = %v, want %v", got, want)
	}
}

func TestSprintStatusTransitions(t *testing.T) {
	dir := spInitRepo(t)
	sp := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "add", "S", "--json"))
	if sp.Status != "planned" || sp.StartedAt != nil || sp.ClosedAt != nil {
		t.Fatalf("fresh sprint = %+v, want planned/null/null", sp)
	}

	started := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "activate", sp.ID, "--json"))
	if started.Status != "active" || started.StartedAt == nil || started.ClosedAt != nil {
		t.Fatalf("started = %+v, want active with started_at set, closed_at null", started)
	}

	completed := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "complete", sp.ID, "--json"))
	if completed.Status != "completed" || completed.ClosedAt == nil {
		t.Fatalf("completed = %+v, want completed with closed_at set", completed)
	}
	if completed.StartedAt == nil {
		t.Errorf("started_at = null after complete, want the active-transition stamp preserved")
	}

	for _, verb := range []string{"cancel", "activate"} {
		_, _, err := spRun(t, dir, "", "sprint", verb, sp.ID)
		var conflict *ConflictError
		if !errors.As(err, &conflict) || ExitCode(err) != 4 {
			t.Fatalf("%s on completed err = %v (exit %d), want ConflictError exit 4", verb, err, ExitCode(err))
		}
		if want := sp.ID[:7] + " already completed"; conflict.Msg != want {
			t.Fatalf("%s msg = %q, want %q", verb, conflict.Msg, want)
		}
	}

	sp2 := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "add", "S2", "--json"))
	cancelled := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "cancel", sp2.ID, "--json"))
	if cancelled.Status != "cancelled" || cancelled.ClosedAt == nil {
		t.Fatalf("cancelled = %+v, want cancelled with closed_at set", cancelled)
	}
}

func TestSprintReverseIndexTasks(t *testing.T) {
	dir := spInitRepo(t)
	sp := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "add", "S", "--json"))
	member := spID(t, spMust(t, dir, "task", "add", "Member", "--no-validation-criteria", "--json"))
	spSetTaskSprint(t, dir, member, sp.ID)
	spMust(t, dir, "task", "add", "Outsider", "--no-validation-criteria", "--json")

	shown := spJSON[sprintDTO](t, spMust(t, dir, "sprint", "show", sp.ID, "--json"))
	if len(shown.Tasks) != 1 || shown.Tasks[0] != member {
		t.Fatalf("tasks = %v, want [%s]", shown.Tasks, member)
	}
	lean := spMust(t, dir, "sprint", "show", sp.ID)
	if !strings.Contains(lean, "tasks: "+member[:7]+"\n") {
		t.Errorf("lean show missing tasks header for %s: %q", member[:7], lean)
	}
}

// isUsage reports whether err is a UsageError mapped to exit 2.
func isUsage(err error) bool {
	var usage *UsageError
	return errors.As(err, &usage) && ExitCode(err) == 2
}
