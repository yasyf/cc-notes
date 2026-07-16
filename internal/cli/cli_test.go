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
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/store"
)

const (
	actorA = "Agent A <a@example.com>"
	actorB = "Agent B <b@example.com>"
)

// maxTitleTestBytes mirrors the internal maxTitleBytes cap (unexported), so the
// boundary tests break loudly if the two ever diverge.
const maxTitleTestBytes = 256

// noteJSON mirrors the note output DTO for round-trip assertions.
type noteJSON struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Tags    []string `json:"tags"`
	Anchors []struct {
		Kind    string  `json:"kind"`
		Value   string  `json:"value"`
		Witness *string `json:"witness"`
	} `json:"anchors"`
	Author       string           `json:"author"`
	CreatedAt    string           `json:"created_at"`
	UpdatedAt    string           `json:"updated_at"`
	VerifiedAt   *string          `json:"verified_at"`
	VerifiedBy   *string          `json:"verified_by"`
	SupersededBy *string          `json:"superseded_by"`
	Drift        *string          `json:"drift"`
	Deleted      bool             `json:"deleted"`
	StaleAt      *string          `json:"stale_at"`
	StaleBy      *string          `json:"stale_by"`
	StaleReason  *string          `json:"stale_reason"`
	Attachments  []attachmentJSON `json:"attachments"`
}

// attachmentJSON mirrors the attachment output DTO for round-trip assertions.
type attachmentJSON struct {
	Name    string `json:"name"`
	OID     string `json:"oid"`
	Size    int64  `json:"size"`
	Present bool   `json:"present"`
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
	Commits []string `json:"commits"`
	Lease   struct {
		Holder    *string `json:"holder"`
		Heartbeat *string `json:"heartbeat"`
	} `json:"lease"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	StartedAt *string `json:"started_at"`
	ClosedAt  *string `json:"closed_at"`
	Sprint    *string `json:"sprint"`
	Project   *string `json:"project"`
	Criteria  []struct {
		ID     string `json:"id"`
		Text   string `json:"text"`
		Script string `json:"script"`
		Status string `json:"status"`
	} `json:"criteria"`
	ClosedForced bool `json:"closed_forced"`
}

// gitEnvKeys are the environment knobs that could leak host git state into a
// subprocess; binEnv strips them from the environment it builds for the
// built cc-notes binary.
var gitEnvKeys = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY", "GIT_NAMESPACE", "GIT_CEILING_DIRECTORIES",
	"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
	"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE",
	"GIT_EDITOR", "EMAIL", "CC_NOTES_ACTOR", "CC_NOTES_SESSION_ID",
	"CLAUDE_CODE_SESSION_ID",
}

// initRepo creates a repository on branch main with a local identity and
// freezes the cc-notes actor to actorA.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := gittest.InitRepo(t)
	// Isolate HOME so a test that spawns or drives a mount holder writes its
	// state under ~/.cc-notes in a temp dir, never the real home.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CC_NOTES_ACTOR", actorA)
	return dir
}

// commitFile writes path under dir with content, commits it, and returns the
// new HEAD sha. It gives note tests real anchored content to witness and drift.
func commitFile(t *testing.T, dir, path, content string) string {
	t.Helper()
	full := filepath.Join(dir, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	gittest.Git(t, dir, "add", path)
	gittest.Git(t, dir, "commit", "-q", "-m", "commit "+path)
	return gittest.Git(t, dir, "rev-parse", "HEAD")
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
	args := append([]string{"task", "add", title, "--no-validation-criteria", "--json"}, extra...)
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
	added := mustRun(t, dir, "note", "add", "Fix parser", "--label", "parser", "--label", "bug")
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
	full := commitFile(t, dir, "seed.go", "package main")
	short := full[:8]
	out := mustRun(t, dir, "note", "add", "Design", "--body", "decisions", "--label", "arch",
		"--commit", short, "--path", "internal/cli", "--dir", "internal/auth", "--branch", "main", "--json")
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
	wantAnchors := []string{"branch=main", "commit=" + full, "dir=internal/auth", "path=internal/cli"}
	gotAnchors := make([]string, len(shown.Anchors))
	for i, a := range shown.Anchors {
		gotAnchors[i] = a.Kind + "=" + a.Value
	}
	if strings.Join(gotAnchors, " ") != strings.Join(wantAnchors, " ") {
		t.Errorf("anchors = %v, want %v", gotAnchors, wantAnchors)
	}
	for _, a := range shown.Anchors {
		switch a.Kind {
		case "commit":
			if len(a.Value) != 40 {
				t.Errorf("commit anchor value = %q, want the full 40-char sha (short %q expanded)", a.Value, short)
			}
			if a.Witness == nil || *a.Witness != a.Value {
				t.Errorf("commit anchor witness = %v, want its own oid %q", a.Witness, a.Value)
			}
		default:
			if a.Witness != nil {
				t.Errorf("%s anchor witness = %v, want null for an absent path/dir/branch", a.Kind, *a.Witness)
			}
		}
	}
	if shown.Author != actorA {
		t.Errorf("author = %q, want %q", shown.Author, actorA)
	}
	if _, err := time.Parse(time.RFC3339, shown.CreatedAt); err != nil {
		t.Errorf("created_at %q: %v", shown.CreatedAt, err)
	}
	if shown.VerifiedAt == nil || *shown.VerifiedAt == "" {
		t.Error("verified_at = null, want a born-verified timestamp")
	}
	if shown.VerifiedBy == nil || *shown.VerifiedBy != actorA {
		t.Errorf("verified_by = %v, want %q", shown.VerifiedBy, actorA)
	}
	if shown.Drift != nil {
		t.Errorf("drift = %v, want null on a fresh note", *shown.Drift)
	}
	if shown.SupersededBy != nil {
		t.Errorf("superseded_by = %v, want null", *shown.SupersededBy)
	}
	if shown.Deleted {
		t.Error("deleted = true, want false")
	}
	ref := "refs/cc-notes/notes/" + added.ID
	if got := gittest.Git(t, dir, "rev-list", "--count", ref); got != "2" {
		t.Errorf("note chain has %s commits, want 2 (create + born-verified)", got)
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
	edited := mustJSON[noteJSON](t, mustRun(t, dir, "note", "edit", dto.ID, "--add-label", "x", "--title", "M", "--json"))
	if edited.Title != "M" || strings.Join(edited.Tags, ",") != "x" {
		t.Fatalf("edited title/tags = %q/%v, want M/[x]", edited.Title, edited.Tags)
	}
}

func TestNoteRmAndSearch(t *testing.T) {
	dir := initRepo(t)
	parser := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Parser bug", "--body", "the Tokenizer breaks", "--label", "bug", "--json"))
	mustRun(t, dir, "note", "add", "Other", "--label", "misc")
	//nolint:gosec // G101: test fixture, not a credential — "PARSER" is a search query string.
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
	out := mustRun(t, dir, "task", "add", "Main", "--body", "Body text", "--type", "bug",
		"--priority", "1", "--label", "x", "--label", "a", "--no-validation-criteria",
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

	reopened := mustJSON[taskJSON](t, mustRun(t, dir, "task", "edit", task.ID, "--status", "open", "--no-assignee", "--json"))
	if reopened.Status != "open" || reopened.Assignee != nil || reopened.ClosedAt != nil {
		t.Fatalf("reopen = %+v, want open/unassigned/closed_at null", reopened)
	}
}

func TestTaskClaimStealRequiresInProgress(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "Work")

	_, _, err := runCLI(t, dir, "task", "claim", task.ID, "--steal")
	var usage *cli.UsageError
	if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
		t.Fatalf("steal of open task err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}
	if want := "--steal requires an in-progress task"; usage.Err.Error() != want {
		t.Fatalf("usage msg = %q, want %q", usage.Err.Error(), want)
	}

	shown := mustJSON[taskJSON](t, mustRun(t, dir, "task", "show", task.ID, "--json"))
	if shown.Status != "open" || shown.Assignee != nil {
		t.Fatalf("after rejected steal status/assignee = %q/%v, want open/null", shown.Status, shown.Assignee)
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
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c")
	gittest.Git(t, dir, "checkout", "-q", "--detach")

	// Detached at main's tip — the jj colocation norm. CurrentBranch resolves
	// main, so an unflagged list scopes to main and finds the task, silently.
	out, stderr, err := runCLI(t, dir, "task", "list")
	if err != nil {
		t.Fatalf("detached task list err = %v (stderr %q), want nil", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("read command must degrade silently; stderr = %q", stderr)
	}
	if !strings.Contains(out, task.ID[:7]) {
		t.Fatalf("detached task list = %q, want %s (scoped to main)", out, task.ID[:7])
	}
	if out := mustRun(t, dir, "task", "list", "--branch", "main"); !strings.Contains(out, task.ID[:7]) {
		t.Fatalf("task list --branch main = %q, want %s", out, task.ID[:7])
	}
	if out := mustRun(t, dir, "task", "list", "--all-branches"); !strings.Contains(out, task.ID[:7]) {
		t.Fatalf("task list --all-branches = %q, want %s", out, task.ID[:7])
	}
	// Single-task commands resolve by id globally, so they work from a
	// detached HEAD with no branch flag.
	if out := mustRun(t, dir, "task", "show", task.ID); !strings.Contains(out, "id: "+task.ID+"\n") {
		t.Fatalf("task show = %q, want the task", out)
	}
	if out := mustRun(t, dir, "task", "comment", task.ID, "from detached"); !strings.HasPrefix(out, task.ID[:7]+"\t") {
		t.Fatalf("task comment = %q, want the lean line", out)
	}
	mustRun(t, dir, "note", "list")
}

// TestTaskListAmbiguousHead pins the read-command degrade: on a genuinely
// unresolvable HEAD (no trunk, advanced past the sole bookmark) an unflagged
// list silently falls back to the backlog view.
func TestTaskListAmbiguousHead(t *testing.T) {
	dir := initRepo(t)
	backlogTask := addTask(t, dir, "Backlog task", "--backlog")
	// No trunk: rename the unborn main to wip so main never exists.
	gittest.Git(t, dir, "checkout", "-q", "-b", "wip")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c1")
	branchTask := addTask(t, dir, "On wip")
	gittest.Git(t, dir, "checkout", "-q", "--detach")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c2")

	out, stderr, err := runCLI(t, dir, "task", "list")
	if err != nil {
		t.Fatalf("ambiguous task list err = %v, want nil", err)
	}
	if stderr != "" {
		t.Fatalf("read command must degrade silently; stderr = %q", stderr)
	}
	if !strings.Contains(out, backlogTask.ID[:7]) {
		t.Fatalf("ambiguous list = %q, want backlog task %s", out, backlogTask.ID[:7])
	}
	if strings.Contains(out, branchTask.ID[:7]) {
		t.Fatalf("ambiguous list = %q, want the wip task %s excluded (backlog view)", out, branchTask.ID[:7])
	}
}

// TestClaimStealBogusTTLExit2 pins that a malformed CC_NOTES_LEASE_TTL on a
// --steal claim is a usage error (exit 2) carrying the invalid-duration text,
// not the exit 1 a plain error would map to.
func TestClaimStealBogusTTLExit2(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "Work")
	mustRun(t, dir, "task", "claim", task.ID)
	t.Setenv("CC_NOTES_LEASE_TTL", "bogus")
	_, _, err := runCLI(t, dir, "task", "claim", task.ID, "--steal")
	if cli.ExitCode(err) != 2 || cli.Label(err) != "usage" {
		t.Fatalf("steal with bogus TTL = exit %d label %q (err %v), want 2/usage", cli.ExitCode(err), cli.Label(err), err)
	}
	if err.Error() != `invalid duration "bogus"` {
		t.Fatalf("err = %q, want `invalid duration \"bogus\"`", err.Error())
	}
}

// TestClaimStealMalformedActorExit1 pins that --steal resolves the actor before
// the in-progress guard: a malformed CC_NOTES_ACTOR errors (exit 1) ahead of
// the exit-2 usage guard, matching the pre-migration order.
func TestClaimStealMalformedActorExit1(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "Work")
	t.Setenv("CC_NOTES_ACTOR", "malformed")
	_, _, err := runCLI(t, dir, "task", "claim", task.ID, "--steal")
	if cli.ExitCode(err) != 1 {
		t.Fatalf("steal with malformed actor = exit %d (err %v), want 1", cli.ExitCode(err), err)
	}
	if !strings.Contains(err.Error(), "CC_NOTES_ACTOR") {
		t.Fatalf("err = %q, want a CC_NOTES_ACTOR malformed error", err.Error())
	}
}

// TestStartCancelledMalformedActorExit4 pins that start guards the task's
// claimability before resolving the actor: starting a cancelled task reports
// the conflict (exit 4), not the malformed-actor error the old order surfaced.
func TestStartCancelledMalformedActorExit4(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "Work")
	mustRun(t, dir, "task", "cancel", task.ID)
	t.Setenv("CC_NOTES_ACTOR", "malformed")
	_, _, err := runCLI(t, dir, "task", "start", task.ID)
	if cli.ExitCode(err) != 4 {
		t.Fatalf("start cancelled with malformed actor = exit %d (err %v), want 4 conflict", cli.ExitCode(err), err)
	}
	if !strings.Contains(err.Error(), "not open (cancelled)") {
		t.Fatalf("err = %q, want 'not open (cancelled)'", err.Error())
	}
}

// TestEditEmptyTitleOutsideRepoExit2 pins that task edit validates --title
// before opening the store: an empty title outside a repository is the title
// usage error (exit 2), not the store-open error (exit 1).
func TestEditEmptyTitleOutsideRepoExit2(t *testing.T) {
	gittest.ScrubEnv(t)
	dir := t.TempDir()
	t.Setenv("CC_NOTES_ACTOR", actorA)
	_, _, err := runCLI(t, dir, "task", "edit", "deadbeef", "--title", "")
	if cli.ExitCode(err) != 2 || cli.Label(err) != "usage" {
		t.Fatalf("edit --title='' outside repo = exit %d label %q (err %v), want 2/usage", cli.ExitCode(err), cli.Label(err), err)
	}
	if !strings.Contains(err.Error(), "title is empty") {
		t.Fatalf("err = %q, want a title-empty usage error", err.Error())
	}
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
	over := strings.Repeat("x", maxTitleTestBytes+1)
	for _, args := range [][]string{
		{"frobnicate"},
		{"note", "frobnicate"},
		{"task", "frobnicate"},
		{"note", "list", "--bogus"},
		{"note", "show"},
		{"task", "comment", "abc"},
		{"task", "list", "--branch", "feat ure"},
		{"task", "add", "T", "--no-validation-criteria", "--branch", "../evil"},
		// Over-cap title on every title-taking add rejects before any write.
		{"note", "add", over},
		{"doc", "add", over, "--body", "b"},
		{"log", "add", over},
		{"task", "add", over, "--no-validation-criteria"},
		{"sprint", "add", over},
		{"project", "add", over},
		// Over-cap title on a rename UsageErrors without resolving the id — validation
		// fires before the nonexistent "x" resolves, on every noun's edit.
		{"note", "edit", "x", "--title", over},
		{"doc", "edit", "x", "--title", over},
		{"log", "edit", "x", "--title", over},
		{"task", "edit", "x", "--title", over},
		{"sprint", "edit", "x", "--title", over},
		{"project", "edit", "x", "--title", over},
		// Empty title, and a doc created with no body and no --attach.
		{"note", "add", ""},
		{"doc", "add", "Handoff", "--when", "w"},
	} {
		_, _, err := runCLI(t, dir, args...)
		var usage *cli.UsageError
		if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
			t.Errorf("cc-notes %s err = %v (exit %d), want UsageError exit 2", strings.Join(args, " "), err, cli.ExitCode(err))
		}
	}
	if _, _, err := runCLI(t, dir, "task", "edit", "x", "--assignee", "a", "--no-assignee"); cli.ExitCode(err) != 2 {
		t.Errorf("conflicting edit flags err = %v, want exit 2", err)
	}
}

func TestTitleCap(t *testing.T) {
	dir := initRepo(t)
	at := strings.Repeat("x", maxTitleTestBytes)
	over := strings.Repeat("x", maxTitleTestBytes+1)

	// The byte cap is the boundary: 256 bytes passes on note add and doc
	// add-with-body, preserving the title verbatim.
	note := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", at, "--json"))
	if note.Title != at {
		t.Fatalf("note title = %d bytes, want the %d-byte title verbatim", len(note.Title), maxTitleTestBytes)
	}
	doc := mustJSON[docJSON](t, mustRun(t, dir, "doc", "add", at, "--body", "b", "--json"))
	if doc.Title != at {
		t.Fatalf("doc title = %d bytes, want the %d-byte title verbatim", len(doc.Title), maxTitleTestBytes)
	}

	// 257 bytes fails with a UsageError whose teaching text names every escape
	// hatch, on both note add and doc add.
	for _, args := range [][]string{
		{"note", "add", over},
		{"doc", "add", over, "--body", "b"},
	} {
		_, _, err := runCLI(t, dir, args...)
		var usage *cli.UsageError
		if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
			t.Fatalf("cc-notes %s err = %v (exit %d), want UsageError exit 2", strings.Join(args, " "), err, cli.ExitCode(err))
		}
		msg := err.Error()
		for _, want := range []string{"257 bytes", "max 256", "--body", "--checkout", "--attach"} {
			if !strings.Contains(msg, want) {
				t.Errorf("cc-notes %s over-cap message %q missing %q", strings.Join(args, " "), msg, want)
			}
		}
	}

	// note/doc edit now carry --attach too, so the rename hint names every content
	// escape hatch that exists on edit: --body, --checkout, and --attach.
	for _, args := range [][]string{
		{"note", "edit", "x", "--title", over},
		{"doc", "edit", "x", "--title", over},
	} {
		_, _, err := runCLI(t, dir, args...)
		var usage *cli.UsageError
		if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
			t.Fatalf("cc-notes %s err = %v (exit %d), want UsageError exit 2", strings.Join(args, " "), err, cli.ExitCode(err))
		}
		msg := err.Error()
		for _, want := range []string{"257 bytes", "max 256", "--body", "--checkout", "--attach"} {
			if !strings.Contains(msg, want) {
				t.Errorf("cc-notes %s over-cap message %q missing %q", strings.Join(args, " "), msg, want)
			}
		}
	}

	// task/sprint/project now carry the content in --body, so their over-cap hint
	// names --body — the flag that actually exists on the command — and never the
	// removed --desc.
	_, _, err := runCLI(t, dir, "task", "add", over, "--no-validation-criteria")
	var usage *cli.UsageError
	if !errors.As(err, &usage) || cli.ExitCode(err) != 2 {
		t.Fatalf("task add over-cap err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}
	msg := err.Error()
	if !strings.Contains(msg, "--body") {
		t.Errorf("task over-cap message %q, want it to name --body", msg)
	}
	if strings.Contains(msg, "--desc") {
		t.Errorf("task over-cap message %q must not name the removed --desc", msg)
	}
}

func TestNoteVerify(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "f.go", "v1\n")
	added := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Note", "--path", "f.go", "--json"))
	ref := "refs/cc-notes/notes/" + added.ID
	if got := gittest.Git(t, dir, "rev-list", "--count", ref); got != "2" {
		t.Fatalf("after add: %s commits, want 2 (create + born-verified)", got)
	}
	verified := mustJSON[noteJSON](t, mustRun(t, dir, "note", "verify", added.ID, "--json"))
	if verified.ID != added.ID {
		t.Fatalf("verify id = %q, want %q (stable)", verified.ID, added.ID)
	}
	if verified.VerifiedAt == nil || verified.VerifiedBy == nil || *verified.VerifiedBy != actorA {
		t.Fatalf("verify fields = %v/%v, want set and %q", verified.VerifiedAt, verified.VerifiedBy, actorA)
	}
	if got := gittest.Git(t, dir, "rev-list", "--count", ref); got != "3" {
		t.Fatalf("after verify: %s commits, want 3", got)
	}
}

func TestNoteReviewDrift(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "auth.go", "v1\n")
	added := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Auth note", "--path", "auth.go", "--label", "design", "--json"))
	if out := mustRun(t, dir, "note", "review"); out != "" {
		t.Fatalf("review of a fresh note = %q, want empty", out)
	}

	commitFile(t, dir, "auth.go", "v2\n")
	review := mustRun(t, dir, "note", "review")
	if !strings.HasPrefix(review, added.ID[:7]+"\t") || !strings.HasSuffix(review, "\tDRIFTED\n") {
		t.Fatalf("review = %q, want %s...DRIFTED", review, added.ID[:7])
	}
	if out := mustRun(t, dir, "note", "review", "--drift"); !strings.HasSuffix(out, "\tDRIFTED\n") {
		t.Fatalf("review --drift = %q, want the drifted note", out)
	}
	if out := mustRun(t, dir, "note", "review", "--unverified"); out != "" {
		t.Fatalf("review --unverified = %q, want empty", out)
	}
	dj := mustJSON[[]noteJSON](t, mustRun(t, dir, "note", "review", "--json"))
	if len(dj) != 1 || dj[0].Drift == nil || *dj[0].Drift != "DRIFTED" {
		t.Fatalf("review --json = %+v, want one DRIFTED note", dj)
	}

	mustRun(t, dir, "note", "verify", added.ID)
	if out := mustRun(t, dir, "note", "review"); out != "" {
		t.Fatalf("review after verify = %q, want empty", out)
	}
}

func TestNoteReviewStale(t *testing.T) {
	dir := initRepo(t)
	added := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Old note", "--json"))

	t.Setenv("CC_NOTES_NOTE_STALE_AFTER", "1ns")
	review := mustRun(t, dir, "note", "review")
	if !strings.HasPrefix(review, added.ID[:7]+"\t") || !strings.HasSuffix(review, "\tSTALE\n") {
		t.Fatalf("review = %q, want STALE", review)
	}
	if out := mustRun(t, dir, "note", "review", "--stale-after", "8760h"); out != "" {
		t.Fatalf("review --stale-after 8760h = %q, want empty", out)
	}
}

func TestNoteCommitAnchorDrift(t *testing.T) {
	dir := initRepo(t)
	base := commitFile(t, dir, "main.go", "base\n")
	gittest.Git(t, dir, "checkout", "-q", "-b", "side")
	side := commitFile(t, dir, "side.go", "side\n")
	gittest.Git(t, dir, "checkout", "-q", "main")

	drifted := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Side note", "--commit", side, "--json"))
	mustRun(t, dir, "note", "add", "Base note", "--commit", base)

	review := mustRun(t, dir, "note", "review")
	if strings.Count(review, "\n") != 1 || !strings.HasPrefix(review, drifted.ID[:7]+"\t") || !strings.HasSuffix(review, "\tDRIFTED\n") {
		t.Fatalf("review = %q, want only the non-ancestor commit anchor DRIFTED", review)
	}
}

func TestNoteSupersede(t *testing.T) {
	dir := initRepo(t)
	old := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Old decision", "--label", "design", "--json"))
	neu := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "New decision", "--label", "design", "--json"))

	if _, _, err := runCLI(t, dir, "note", "supersede", old.ID); err == nil {
		t.Fatal("supersede without --by, want UsageError")
	} else {
		var usage *cli.UsageError
		if !errors.As(err, &usage) {
			t.Fatalf("supersede without --by err = %v, want UsageError", err)
		}
	}

	echo := mustRun(t, dir, "note", "supersede", old.ID, "--by", neu.ID)
	if !strings.HasPrefix(echo, old.ID[:7]+"\t") {
		t.Fatalf("supersede echo = %q, want the mutated OLD note line", echo)
	}

	if list := mustRun(t, dir, "note", "list"); strings.Contains(list, old.ID[:7]) || !strings.Contains(list, neu.ID[:7]) {
		t.Fatalf("list = %q, want only NEW", list)
	}
	if out := mustRun(t, dir, "note", "search", "decision"); strings.Contains(out, old.ID[:7]) {
		t.Fatalf("search = %q, want OLD dropped", out)
	}
	if out := mustRun(t, dir, "note", "list", "--include-superseded"); !strings.Contains(out, old.ID[:7]) {
		t.Fatalf("list --include-superseded = %q, want OLD present", out)
	}

	shownOld := mustJSON[noteJSON](t, mustRun(t, dir, "note", "show", old.ID, "--json"))
	if shownOld.SupersededBy == nil || *shownOld.SupersededBy != neu.ID {
		t.Fatalf("OLD superseded_by = %v, want %s", shownOld.SupersededBy, neu.ID)
	}
	if out := mustRun(t, dir, "note", "show", neu.ID); !strings.Contains(out, "supersedes: "+old.ID[:7]) {
		t.Fatalf("show NEW = %q, want a supersedes line for %s", out, old.ID[:7])
	}

	mustRun(t, dir, "note", "supersede", old.ID, "--by", neu.ID, "--clear")
	if out := mustRun(t, dir, "note", "list"); !strings.Contains(out, old.ID[:7]) {
		t.Fatalf("list after --clear = %q, want OLD restored", out)
	}

	mustRun(t, dir, "note", "supersede", old.ID, "--by", neu.ID)
	mustRun(t, dir, "note", "rm", neu.ID)
	review := mustRun(t, dir, "note", "review")
	if !strings.Contains(review, old.ID[:7]) || !strings.HasSuffix(review, "\tDANGLING\n") {
		t.Fatalf("review = %q, want OLD flagged DANGLING after NEW is tombstoned", review)
	}
}

func TestNoteSupersedeChainNotDangling(t *testing.T) {
	dir := initRepo(t)
	a := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "A decision", "--label", "design", "--json"))
	b := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "B decision", "--label", "design", "--json"))
	c := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "C decision", "--label", "design", "--json"))

	mustRun(t, dir, "note", "supersede", a.ID, "--by", b.ID)
	mustRun(t, dir, "note", "supersede", b.ID, "--by", c.ID)

	review := mustRun(t, dir, "note", "review")
	if strings.Contains(review, "DANGLING") {
		t.Fatalf("review = %q, want no DANGLING for a live supersede chain A->B->C", review)
	}
	if strings.Contains(review, a.ID[:7]) || strings.Contains(review, b.ID[:7]) {
		t.Fatalf("review = %q, want neither A nor B flagged while B and C are live", review)
	}
}

func TestNoteJSONContract(t *testing.T) {
	dir := initRepo(t)
	base := commitFile(t, dir, "auth.go", "code\n")
	added := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "Auth note", "--body", "details",
		"--label", "design", "--path", "auth.go", "--commit", base, "--branch", "main", "--json"))

	raw := mustRun(t, dir, "note", "show", added.ID, "--json")
	if !strings.HasSuffix(raw, "\n") || strings.Count(raw, "\n") != 1 {
		t.Fatalf("raw = %q, want one compact document with one trailing newline", raw)
	}
	if !strings.HasPrefix(raw, `{"id":"`) {
		t.Fatalf("raw = %q, want id first", raw)
	}
	shown := mustJSON[noteJSON](t, raw)
	remarshaled, err := json.Marshal(shown)
	if err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	if string(remarshaled)+"\n" != raw {
		t.Fatalf("note JSON shape drifted from the DTO contract:\n got  %q\n want %q", raw, string(remarshaled)+"\n")
	}

	for _, a := range shown.Anchors {
		switch a.Kind {
		case "path", "commit":
			if a.Witness == nil {
				t.Errorf("%s anchor witness = null, want a content oid", a.Kind)
			}
		case "branch":
			if a.Witness != nil {
				t.Errorf("branch anchor witness = %v, want null", *a.Witness)
			}
		}
	}
	for _, frag := range []string{`"verified_at":"`, `"verified_by":"`, `"superseded_by":null`, `"drift":null`} {
		if !strings.Contains(raw, frag) {
			t.Errorf("note JSON %q missing %q", raw, frag)
		}
	}
}
