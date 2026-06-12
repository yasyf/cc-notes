// CLI tests: every test executes the cobra tree in-process against a real
// git repository in t.TempDir(), with the git environment scrubbed and the
// actor frozen via CC_NOTES_ACTOR.
package cli_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/store"
)

const (
	actorA = "Agent A <a@example.com>"
	actorB = "Agent B <b@example.com>"
)

// noteJSON mirrors the note output DTO for round-trip assertions.
type noteJSON struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Tags    []string `json:"tags"`
	Anchors []struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	} `json:"anchors"`
	Author    string `json:"author"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
	Deleted   bool   `json:"deleted"`
}

// taskJSON mirrors the task output DTO for round-trip assertions.
type taskJSON struct {
	ID          string   `json:"id"`
	Branch      string   `json:"branch"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Status      string   `json:"status"`
	Priority    int      `json:"priority"`
	Assignee    *string  `json:"assignee"`
	Labels      []string `json:"labels"`
	BlockedBy   []string `json:"blocked_by"`
	Blocks      []string `json:"blocks"`
	Parent      *string  `json:"parent"`
	Comments    []struct {
		Author string `json:"author"`
		TS     string `json:"ts"`
		Body   string `json:"body"`
	} `json:"comments"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	StartedAt *string `json:"started_at"`
	ClosedAt  *string `json:"closed_at"`
}

// gitEnvKeys are the environment knobs that could leak host git state into a
// test; scrubGitEnv clears them in-process and binEnv strips them from
// subprocess environments.
var gitEnvKeys = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY", "GIT_NAMESPACE", "GIT_CEILING_DIRECTORIES",
	"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
	"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE",
	"GIT_EDITOR", "EMAIL", "CC_NOTES_ACTOR",
}

// scrubGitEnv clears every git environment knob that could leak host state
// into a test and pins global/system config to /dev/null.
func scrubGitEnv(t *testing.T) {
	t.Helper()
	for _, key := range gitEnvKeys {
		if value, ok := os.LookupEnv(key); ok {
			t.Setenv(key, value)
			os.Unsetenv(key)
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// initRepo creates a repository on branch main with a local identity and
// freezes the cc-notes actor to actorA.
func initRepo(t *testing.T) string {
	t.Helper()
	scrubGitEnv(t)
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "-b", "main")
	mustGit(t, dir, "config", "user.name", "Test User")
	mustGit(t, dir, "config", "user.email", "test@example.com")
	t.Setenv("CC_NOTES_ACTOR", actorA)
	return dir
}

func runCLI(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	return runCLIIn(t, dir, "", args...)
}

func runCLIIn(t *testing.T, dir, stdin string, args ...string) (string, string, error) {
	t.Helper()
	t.Chdir(dir)
	root := cli.NewRootCmd()
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err := root.ExecuteContext(t.Context())
	return stdout.String(), stderr.String(), err
}

func mustRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, err := runCLI(t, dir, args...)
	if err != nil {
		t.Fatalf("cc-notes %s: %v (stderr %q)", strings.Join(args, " "), err, stderr)
	}
	return stdout
}

func mustJSON[T any](t *testing.T, raw string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	return v
}

func addTask(t *testing.T, dir, title string, extra ...string) taskJSON {
	t.Helper()
	args := append([]string{"task", "add", title, "--json"}, extra...)
	return mustJSON[taskJSON](t, mustRun(t, dir, args...))
}

func dateOf(t *testing.T, rfc string) string {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		t.Fatalf("parse %q: %v", rfc, err)
	}
	return ts.UTC().Format("2006-01-02")
}

func TestNoteAddLeanLine(t *testing.T) {
	dir := initRepo(t)
	added := mustRun(t, dir, "note", "add", "Fix parser", "--tag", "parser", "--tag", "bug")
	listed := mustRun(t, dir, "note", "list")
	if listed != added {
		t.Fatalf("note list = %q, want the line note add printed %q", listed, added)
	}
	dto := mustJSON[[]noteJSON](t, mustRun(t, dir, "note", "list", "--json"))[0]
	want := fmt.Sprintf("%s\t%s\tbug,parser\tFix parser\n", dto.ID[:7], dateOf(t, dto.UpdatedAt))
	if added != want {
		t.Fatalf("note add output = %q, want %q", added, want)
	}
}

func TestNoteJSONRoundTrip(t *testing.T) {
	dir := initRepo(t)
	out := mustRun(t, dir, "note", "add", "Design", "--body", "decisions", "--tag", "arch",
		"--commit", "abc123", "--path", "internal/cli", "--branch", "main", "--json")
	if !strings.HasPrefix(out, `{"id":"`) {
		t.Fatalf("note JSON does not lead with id: %q", out)
	}
	added := mustJSON[noteJSON](t, out)
	shown := mustJSON[noteJSON](t, mustRun(t, dir, "note", "show", added.ID, "--json"))
	if shown.ID != added.ID {
		t.Fatalf("show id = %q, want %q", shown.ID, added.ID)
	}
	if len(shown.ID) != 40 {
		t.Errorf("id length = %d, want 40", len(shown.ID))
	}
	if shown.Title != "Design" || shown.Body != "decisions" {
		t.Errorf("title/body = %q/%q", shown.Title, shown.Body)
	}
	if got := strings.Join(shown.Tags, ","); got != "arch" {
		t.Errorf("tags = %q, want arch", got)
	}
	wantAnchors := []string{"branch=main", "commit=abc123", "path=internal/cli"}
	gotAnchors := make([]string, len(shown.Anchors))
	for i, a := range shown.Anchors {
		gotAnchors[i] = a.Kind + "=" + a.Value
	}
	if strings.Join(gotAnchors, " ") != strings.Join(wantAnchors, " ") {
		t.Errorf("anchors = %v, want %v", gotAnchors, wantAnchors)
	}
	if shown.Author != actorA {
		t.Errorf("author = %q, want %q", shown.Author, actorA)
	}
	if _, err := time.Parse(time.RFC3339, shown.CreatedAt); err != nil {
		t.Errorf("created_at %q: %v", shown.CreatedAt, err)
	}
	if shown.UpdatedAt != shown.CreatedAt {
		t.Errorf("updated_at = %q, want created_at %q", shown.UpdatedAt, shown.CreatedAt)
	}
	if shown.Deleted {
		t.Error("deleted = true, want false")
	}
}

func TestNoteEditRequiresFlag(t *testing.T) {
	dir := initRepo(t)
	dto := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "N", "--json"))
	_, _, err := runCLI(t, dir, "note", "edit", dto.ID)
	var usage *cli.UsageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want UsageError", err)
	}
	if code := cli.ExitCode(err); code != 2 {
		t.Fatalf("ExitCode = %d, want 2", code)
	}
	edited := mustJSON[noteJSON](t, mustRun(t, dir, "note", "edit", dto.ID, "--add-tag", "x", "--title", "M", "--json"))
	if edited.Title != "M" || strings.Join(edited.Tags, ",") != "x" {
		t.Fatalf("edited title/tags = %q/%v, want M/[x]", edited.Title, edited.Tags)
	}
}

func TestNoteRmAndSearch(t *testing.T) {
	dir := initRepo(t)
	parser := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Parser bug", "--body", "the Tokenizer breaks", "--tag", "bug", "--json"))
	mustRun(t, dir, "note", "add", "Other", "--tag", "misc")
	for query, wantTitle := range map[string]string{"PARSER": "Parser bug", "tokenizer": "Parser bug", "misc": "Other"} {
		out := mustRun(t, dir, "note", "search", query)
		if !strings.Contains(out, wantTitle) || strings.Count(out, "\n") != 1 {
			t.Errorf("search %q = %q, want one line containing %q", query, out, wantTitle)
		}
	}
	rm := mustRun(t, dir, "note", "rm", parser.ID)
	tombstoned := mustJSON[noteJSON](t, mustRun(t, dir, "note", "show", parser.ID, "--json"))
	if !tombstoned.Deleted {
		t.Fatalf("note after rm = %+v, want deleted", tombstoned)
	}
	if want := fmt.Sprintf("%s\t%s\tbug\tParser bug\n", parser.ID[:7], dateOf(t, tombstoned.UpdatedAt)); rm != want {
		t.Fatalf("rm output = %q, want the post-append lean line %q", rm, want)
	}
	if out := mustRun(t, dir, "note", "search", "parser"); out != "" {
		t.Fatalf("search after rm = %q, want empty", out)
	}
	if out := mustRun(t, dir, "note", "list"); strings.Count(out, "\n") != 1 {
		t.Fatalf("list after rm = %q, want one line", out)
	}
	if out := mustRun(t, dir, "note", "list", "--all"); strings.Count(out, "\n") != 2 {
		t.Fatalf("list --all = %q, want two lines", out)
	}
}

func TestTaskJSONRoundTrip(t *testing.T) {
	dir := initRepo(t)
	blocker := addTask(t, dir, "Blocker")
	out := mustRun(t, dir, "task", "add", "Main", "--desc", "Body text", "--type", "bug",
		"--priority", "1", "--label", "x", "--label", "a",
		"--parent", blocker.ID, "--blocked-by", blocker.ID, "--json")
	if !strings.HasPrefix(out, `{"id":"`) || !strings.Contains(out, `","branch":"main",`) {
		t.Fatalf("task JSON field order broken: %q", out)
	}
	added := mustJSON[taskJSON](t, out)
	if c := mustRun(t, dir, "task", "comment", added.ID, "hello"); c != added.ID[:7]+"\topen\tP1\t-\tMain\n" {
		t.Fatalf("comment output = %q, want the post-append lean line", c)
	}
	shown := mustJSON[taskJSON](t, mustRun(t, dir, "task", "show", added.ID, "--json"))
	if shown.ID != added.ID || len(shown.ID) != 40 {
		t.Errorf("id = %q, want %q (40 hex)", shown.ID, added.ID)
	}
	if shown.Branch != "main" || shown.Title != "Main" || shown.Description != "Body text" {
		t.Errorf("branch/title/desc = %q/%q/%q", shown.Branch, shown.Title, shown.Description)
	}
	if shown.Type != "bug" || shown.Status != "open" || shown.Priority != 1 {
		t.Errorf("type/status/priority = %q/%q/%d", shown.Type, shown.Status, shown.Priority)
	}
	if shown.Assignee != nil || shown.StartedAt != nil || shown.ClosedAt != nil {
		t.Errorf("assignee/started/closed not null: %v/%v/%v", shown.Assignee, shown.StartedAt, shown.ClosedAt)
	}
	if strings.Join(shown.Labels, ",") != "a,x" {
		t.Errorf("labels = %v, want [a x]", shown.Labels)
	}
	if len(shown.BlockedBy) != 1 || shown.BlockedBy[0] != blocker.ID {
		t.Errorf("blocked_by = %v, want [%s]", shown.BlockedBy, blocker.ID)
	}
	if len(shown.Blocks) != 0 {
		t.Errorf("blocks = %v, want []", shown.Blocks)
	}
	if shown.Parent == nil || *shown.Parent != blocker.ID {
		t.Errorf("parent = %v, want %s", shown.Parent, blocker.ID)
	}
	if len(shown.Comments) != 1 || shown.Comments[0].Author != actorA || shown.Comments[0].Body != "hello" {
		t.Errorf("comments = %+v", shown.Comments)
	}
	if _, err := time.Parse(time.RFC3339, shown.Comments[0].TS); err != nil {
		t.Errorf("comment ts %q: %v", shown.Comments[0].TS, err)
	}
	blockerShown := mustJSON[taskJSON](t, mustRun(t, dir, "task", "show", blocker.ID, "--json"))
	if len(blockerShown.Blocks) != 1 || blockerShown.Blocks[0] != added.ID {
		t.Errorf("blocker blocks = %v, want [%s]", blockerShown.Blocks, added.ID)
	}
	lean := mustRun(t, dir, "task", "show", blocker.ID)
	if !strings.Contains(lean, "blocks: "+added.ID[:7]+"\n") {
		t.Errorf("lean show missing blocks header: %q", lean)
	}
	if !strings.HasPrefix(lean, "id: "+blocker.ID+"\nbranch: main\n") {
		t.Errorf("lean show header order broken: %q", lean)
	}
}

func TestTaskLifecycleConflicts(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "Work")
	claimed := mustRun(t, dir, "task", "claim", task.ID)
	if want := fmt.Sprintf("%s\tin_progress\tP2\t%s\tWork\n", task.ID[:7], actorA); claimed != want {
		t.Fatalf("claim output = %q, want %q", claimed, want)
	}

	t.Setenv("CC_NOTES_ACTOR", actorB)
	_, _, err := runCLI(t, dir, "task", "claim", task.ID)
	var conflict *cli.ConflictError
	if !errors.As(err, &conflict) || cli.ExitCode(err) != 4 {
		t.Fatalf("second claim err = %v (exit %d), want ConflictError exit 4", err, cli.ExitCode(err))
	}
	if want := fmt.Sprintf("%s already claimed by %s (in_progress)", task.ID[:7], actorA); conflict.Msg != want {
		t.Fatalf("conflict msg = %q, want %q", conflict.Msg, want)
	}

	done := mustRun(t, dir, "task", "done", task.ID)
	if want := fmt.Sprintf("%s\tdone\tP2\t%s\tWork\n", task.ID[:7], actorA); done != want {
		t.Fatalf("done output = %q, want %q", done, want)
	}
	for _, verb := range []string{"done", "cancel"} {
		_, _, err := runCLI(t, dir, "task", verb, task.ID)
		if !errors.As(err, &conflict) || cli.ExitCode(err) != 4 {
			t.Fatalf("%s on done err = %v, want ConflictError exit 4", verb, err)
		}
		if want := task.ID[:7] + " already done"; conflict.Msg != want {
			t.Fatalf("%s msg = %q, want %q", verb, conflict.Msg, want)
		}
	}

	reopened := mustJSON[taskJSON](t, mustRun(t, dir, "task", "edit", task.ID, "--status", "open", "--unassign", "--json"))
	if reopened.Status != "open" || reopened.Assignee != nil || reopened.ClosedAt != nil {
		t.Fatalf("reopen = %+v, want open/unassigned/closed_at null", reopened)
	}
}

func TestTaskListFiltersAndOrder(t *testing.T) {
	dir := initRepo(t)
	urgent := addTask(t, dir, "Urgent", "--priority", "0")
	labeled := addTask(t, dir, "Labeled", "--label", "x")
	finished := addTask(t, dir, "Finished")
	mustRun(t, dir, "task", "done", finished.ID)

	out := mustRun(t, dir, "task", "list")
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], urgent.ID[:7]+"\t") {
		t.Fatalf("default list = %q, want 2 lines with %s first", out, urgent.ID[:7])
	}
	if out := mustRun(t, dir, "task", "list", "--all"); strings.Count(out, "\n") != 3 {
		t.Fatalf("list --all = %q, want 3 lines", out)
	}
	if out := mustRun(t, dir, "task", "list", "--status", "done"); !strings.HasPrefix(out, finished.ID[:7]+"\t") || strings.Count(out, "\n") != 1 {
		t.Fatalf("list --status done = %q, want only %s", out, finished.ID[:7])
	}
	if out := mustRun(t, dir, "task", "list", "--label", "x"); !strings.HasPrefix(out, labeled.ID[:7]+"\t") || strings.Count(out, "\n") != 1 {
		t.Fatalf("list --label x = %q, want only %s", out, labeled.ID[:7])
	}
	if _, _, err := runCLI(t, dir, "task", "list", "--status", "bogus"); err == nil || cli.ExitCode(err) != 1 {
		t.Fatalf("list --status bogus err = %v, want exit 1", err)
	}
}

func TestTaskReady(t *testing.T) {
	dir := initRepo(t)
	blocker := addTask(t, dir, "Blocker")
	dependent := addTask(t, dir, "Dependent", "--blocked-by", blocker.ID)
	missing := addTask(t, dir, "Missing dep")
	mustRun(t, dir, "task", "dep", missing.ID, blocker.ID)
	mustRun(t, dir, "task", "undep", missing.ID, blocker.ID)

	out := mustRun(t, dir, "task", "ready")
	if strings.Contains(out, dependent.ID[:7]) || !strings.Contains(out, blocker.ID[:7]) || !strings.Contains(out, missing.ID[:7]) {
		t.Fatalf("ready = %q, want blocker+missing, not dependent", out)
	}
	mustRun(t, dir, "task", "done", blocker.ID)
	if out := mustRun(t, dir, "task", "ready"); !strings.Contains(out, dependent.ID[:7]) {
		t.Fatalf("ready after blocker done = %q, want %s", out, dependent.ID[:7])
	}
	mustRun(t, dir, "task", "claim", dependent.ID)
	if out := mustRun(t, dir, "task", "ready"); strings.Contains(out, dependent.ID[:7]) {
		t.Fatalf("ready after claim = %q, want %s gone", out, dependent.ID[:7])
	}
}

func TestTaskDepCycle(t *testing.T) {
	dir := initRepo(t)
	one := addTask(t, dir, "One")
	two := addTask(t, dir, "Two")
	mustRun(t, dir, "task", "dep", one.ID, two.ID)
	for _, pair := range [][2]string{{two.ID, one.ID}, {one.ID, one.ID}} {
		_, _, err := runCLI(t, dir, "task", "dep", pair[0], pair[1])
		if err == nil || !strings.Contains(err.Error(), "dependency cycle") || cli.ExitCode(err) != 1 {
			t.Fatalf("dep %s %s err = %v, want dependency cycle exit 1", pair[0][:7], pair[1][:7], err)
		}
	}
}

func TestDetachedHead(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "On main")
	mustGit(t, dir, "commit", "-q", "--allow-empty", "-m", "c")
	mustGit(t, dir, "checkout", "-q", "--detach")

	_, _, err := runCLI(t, dir, "task", "list")
	if err == nil || err.Error() != "detached HEAD; pass --branch" || cli.ExitCode(err) != 1 {
		t.Fatalf("detached task list err = %v, want detached HEAD; pass --branch (exit 1)", err)
	}
	if out := mustRun(t, dir, "task", "list", "--branch", "main"); !strings.Contains(out, task.ID[:7]) {
		t.Fatalf("task list --branch main = %q, want %s", out, task.ID[:7])
	}
	if out := mustRun(t, dir, "task", "show", task.ID, "--branch", "main"); !strings.Contains(out, "id: "+task.ID+"\n") {
		t.Fatalf("task show --branch main = %q, want the task", out)
	}
	if out := mustRun(t, dir, "task", "comment", task.ID, "from detached", "--branch", "main"); !strings.HasPrefix(out, task.ID[:7]+"\t") {
		t.Fatalf("task comment --branch main = %q, want the lean line", out)
	}
	mustRun(t, dir, "note", "list")
}

func TestStdinBody(t *testing.T) {
	dir := initRepo(t)
	if _, stderr, err := runCLIIn(t, dir, "line1\nline2\n", "note", "add", "Piped", "--body", "-"); err != nil {
		t.Fatalf("note add --body -: %v (stderr %q)", err, stderr)
	}
	dto := mustJSON[[]noteJSON](t, mustRun(t, dir, "note", "list", "--json"))[0]
	if dto.Body != "line1\nline2" {
		t.Fatalf("body = %q, want %q", dto.Body, "line1\nline2")
	}
	shown := mustRun(t, dir, "note", "show", dto.ID)
	if !strings.HasSuffix(shown, "\n\nline1\nline2\n") {
		t.Fatalf("show = %q, want blank line then body", shown)
	}

	task := addTask(t, dir, "Commented")
	if _, stderr, err := runCLIIn(t, dir, "from stdin\n", "task", "comment", task.ID, "-"); err != nil {
		t.Fatalf("task comment -: %v (stderr %q)", err, stderr)
	}
	commented := mustRun(t, dir, "task", "show", task.ID)
	want := fmt.Sprintf("\n-- %s ", actorA)
	if !strings.Contains(commented, want) || !strings.HasSuffix(commented, "\nfrom stdin\n") {
		t.Fatalf("show after comment = %q, want %q block ending in body", commented, want)
	}
}

func TestNotFoundAndAmbiguous(t *testing.T) {
	dir := initRepo(t)
	_, _, err := runCLI(t, dir, "note", "show", "deadbeef")
	if !errors.Is(err, store.ErrNotFound) || cli.ExitCode(err) != 3 {
		t.Fatalf("note show err = %v (exit %d), want ErrNotFound exit 3", err, cli.ExitCode(err))
	}
	one := addTask(t, dir, "One")
	two := addTask(t, dir, "Two")
	_, _, err = runCLI(t, dir, "task", "show", "")
	var ambiguous *store.AmbiguousError
	if !errors.As(err, &ambiguous) || cli.ExitCode(err) != 5 {
		t.Fatalf("task show \"\" err = %v (exit %d), want AmbiguousError exit 5", err, cli.ExitCode(err))
	}
	msg := err.Error()
	if !strings.Contains(msg, one.ID[:7]) || !strings.Contains(msg, two.ID[:7]) {
		t.Fatalf("ambiguous msg %q missing candidate short ids %s/%s", msg, one.ID[:7], two.ID[:7])
	}
}

func TestUsageErrors(t *testing.T) {
	dir := initRepo(t)
	for _, args := range [][]string{
		{"frobnicate"},
		{"note", "frobnicate"},
		{"task", "frobnicate"},
		{"note", "list", "--bogus"},
		{"note", "show"},
		{"task", "comment", "abc"},
		{"task", "list", "--branch", "feat ure"},
		{"task", "add", "T", "--branch", "../evil"},
	} {
		_, _, err := runCLI(t, dir, args...)
		var usage *cli.UsageError
		if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
			t.Errorf("cc-notes %s err = %v (exit %d), want UsageError exit 2", strings.Join(args, " "), err, cli.ExitCode(err))
		}
	}
	if _, _, err := runCLI(t, dir, "task", "edit", "x", "--assignee", "a", "--unassign"); cli.ExitCode(err) != 2 {
		t.Errorf("conflicting edit flags err = %v, want exit 2", err)
	}
}
