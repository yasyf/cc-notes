// MCP server tests: connect the server to an in-memory SDK client and drive the
// same cobra tree the stdio server would, against a real git repository. No
// t.Parallel — every test chdirs into its own temp repo.
package mcpserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/mcpserver"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	//nolint:gosec // G204: test helper shells out to git with a fixed argv[0] and test-controlled args.
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// initRepo creates a repository on main with one seed commit and chdirs into it,
// with the git environment scrubbed and the actor frozen.
func initRepo(t *testing.T) string {
	t.Helper()
	for _, k := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
		"CC_NOTES_ACTOR",
	} {
		if v, ok := os.LookupEnv(k); ok {
			t.Setenv(k, v)
			_ = os.Unsetenv(k)
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "config", "user.name", "Test User")
	runGit(t, dir, "config", "user.email", "test@example.com")
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	runGit(t, dir, "add", "seed.txt")
	runGit(t, dir, "commit", "-q", "-m", "seed")
	t.Setenv("CC_NOTES_ACTOR", "Agent A <a@example.com>")
	t.Chdir(dir)
	return dir
}

// connect wires an mcpserver to an SDK client over in-memory transports.
func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := t.Context()
	srv := mcpserver.New(mcpserver.Config{Version: "test", NewRoot: cli.NewRootCmd, Label: cli.Label})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// toolText joins every text block, for asserting against error results.
func toolText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			b.WriteString(tc.Text)
		}
	}
	return b.String()
}

// primaryText returns the first content block: the JSON DTO on success.
func primaryText(res *mcp.CallToolResult) string {
	if len(res.Content) == 0 {
		return ""
	}
	tc, _ := res.Content[0].(*mcp.TextContent)
	if tc == nil {
		return ""
	}
	return tc.Text
}

// call runs a tool and fails on a protocol error or an error result, returning
// the primary (JSON) content block.
func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) string {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("tool %s returned an error result: %s", name, toolText(res))
	}
	return primaryText(res)
}

func decode[T any](t *testing.T, raw string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("unmarshal %q: %v", raw, err)
	}
	return v
}

type docOut struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	When        string   `json:"when"`
	Tags        []string `json:"tags"`
	VerifiedAt  *string  `json:"verified_at"`
	VerifiedBy  *string  `json:"verified_by"`
	Attachments []struct {
		Name string `json:"name"`
	} `json:"attachments"`
}

type noteOut struct {
	ID         string  `json:"id"`
	Body       string  `json:"body"`
	VerifiedAt *string `json:"verified_at"`
	VerifiedBy *string `json:"verified_by"`
}

type taskOut struct {
	ID       string  `json:"id"`
	Status   string  `json:"status"`
	Assignee *string `json:"assignee"`
}

type runbookOut struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
	Steps  []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"steps"`
	Runs []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Steps  []struct {
			Step   string `json:"step"`
			Status string `json:"status"`
			Note   string `json:"note"`
		} `json:"steps"`
	} `json:"runs"`
}

func TestDocAddShowRoundTrip(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	att := filepath.Join(t.TempDir(), "artifact.txt")
	if err := os.WriteFile(att, []byte("attached bytes\n"), 0o600); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	added := decode[docOut](t, call(t, cs, "doc_add", map[string]any{
		"title":  "Handoff",
		"body":   "the long body",
		"when":   "resuming the cutover",
		"labels": []string{"design"},
		"attach": []string{att},
	}))
	if added.Title != "Handoff" || added.Body != "the long body" || added.When != "resuming the cutover" {
		t.Fatalf("added = %+v, want the prefilled fields", added)
	}
	if added.VerifiedAt == nil || added.VerifiedBy == nil || *added.VerifiedBy != "Agent A <a@example.com>" {
		t.Fatalf("doc not born-verified: verified_at=%v verified_by=%v", added.VerifiedAt, added.VerifiedBy)
	}
	if len(added.Attachments) != 1 || added.Attachments[0].Name != "artifact.txt" {
		t.Fatalf("attachments = %+v, want one artifact.txt", added.Attachments)
	}

	shown := decode[docOut](t, call(t, cs, "doc_show", map[string]any{"id": added.ID}))
	if shown.ID != added.ID || shown.Body != "the long body" {
		t.Fatalf("show = %+v, want the same doc", shown)
	}
	if len(shown.Attachments) != 1 || shown.Attachments[0].Name != "artifact.txt" {
		t.Fatalf("show attachments = %+v, want artifact.txt listed", shown.Attachments)
	}
}

func TestNoteAddVerify(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	added := decode[noteOut](t, call(t, cs, "note_add", map[string]any{"title": "Fact", "body": "v1"}))
	if added.VerifiedBy == nil || *added.VerifiedBy != "Agent A <a@example.com>" {
		t.Fatalf("note not born-verified: %+v", added)
	}
	verified := decode[noteOut](t, call(t, cs, "note_verify", map[string]any{"id": added.ID}))
	if verified.ID != added.ID || verified.VerifiedAt == nil {
		t.Fatalf("verify = %+v, want the same note re-verified", verified)
	}
}

func TestTaskLifecycle(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	// no_validation_criteria is required when no criteria are given.
	added := decode[taskOut](t, call(t, cs, "task_add", map[string]any{
		"title":                  "Wire the layer",
		"no_validation_criteria": true,
	}))
	if added.Status != "open" || added.Assignee != nil {
		t.Fatalf("added = %+v, want open and unassigned", added)
	}

	claimed := decode[taskOut](t, call(t, cs, "task_claim", map[string]any{"id": added.ID}))
	if claimed.Status != "in_progress" || claimed.Assignee == nil {
		t.Fatalf("claimed = %+v, want in_progress and assigned", claimed)
	}
	done := decode[taskOut](t, call(t, cs, "task_done", map[string]any{"id": added.ID}))
	if done.Status != "done" {
		t.Fatalf("done = %+v, want done", done)
	}

	// The criteria path also creates cleanly.
	withCrit := decode[taskOut](t, call(t, cs, "task_add", map[string]any{
		"title":    "Has criteria",
		"criteria": []string{"compiles clean"},
	}))
	if withCrit.Status != "open" {
		t.Fatalf("with-criteria add = %+v, want open", withCrit)
	}
}

// TestRunbookRunLoop drives create → run start → done/skip → finish → show
// through the MCP tools, proving the hand-typed argv resolves semantically.
func TestRunbookRunLoop(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	added := decode[runbookOut](t, call(t, cs, "runbook_add", map[string]any{
		"title": "Deploy",
		"steps": []string{"build", "ship"},
	}))
	if added.Status != "active" {
		t.Fatalf("added status = %q, want active", added.Status)
	}
	if len(added.Steps) != 2 || added.Steps[0].Text != "build" || added.Steps[1].Text != "ship" {
		t.Fatalf("steps = %+v, want build then ship in order", added.Steps)
	}

	started := decode[runbookOut](t, call(t, cs, "runbook_run_start", map[string]any{"id": added.ID}))
	if len(started.Runs) != 1 || started.Runs[0].Status != "running" {
		t.Fatalf("runs = %+v, want one running run", started.Runs)
	}

	// done/skip omit run: default-run resolution picks the sole running run.
	call(t, cs, "runbook_run_done", map[string]any{"id": added.ID, "step": added.Steps[0].ID, "note": "built clean"})
	call(t, cs, "runbook_run_skip", map[string]any{"id": added.ID, "step": added.Steps[1].ID})

	finished := decode[runbookOut](t, call(t, cs, "runbook_run_finish", map[string]any{"id": added.ID}))
	if len(finished.Runs) != 1 {
		t.Fatalf("finished runs = %+v, want exactly one", finished.Runs)
	}
	run := finished.Runs[0]
	if run.Status != "succeeded" {
		t.Fatalf("run status = %q, want succeeded (a skip is not a failure)", run.Status)
	}
	if run.Steps[0].Status != "done" || run.Steps[0].Note != "built clean" {
		t.Fatalf("run step[0] = %+v, want done with note", run.Steps[0])
	}
	if run.Steps[1].Status != "skipped" {
		t.Fatalf("run step[1] = %+v, want skipped", run.Steps[1])
	}

	shown := decode[runbookOut](t, call(t, cs, "runbook_show", map[string]any{"id": added.ID}))
	if len(shown.Runs) != 1 || shown.Runs[0].Status != "succeeded" {
		t.Fatalf("show runs = %+v, want one succeeded run", shown.Runs)
	}
}

func TestErrorMappingCarriesLabel(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "doc_show", Arguments: map[string]any{"id": "deadbeef"}})
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !res.IsError {
		t.Fatalf("bogus doc_show did not return an error result: %s", toolText(res))
	}
	if got := toolText(res); !strings.Contains(got, "not-found") {
		t.Fatalf("error text = %q, want it to carry the not-found label", got)
	}
}

func TestListToolsInventory(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	names := map[string]bool{}
	for _, tool := range res.Tools {
		names[tool.Name] = true
	}

	const wantCount = 84
	if len(names) != wantCount {
		t.Errorf("tool count = %d, want %d; got %v", len(names), wantCount, sortedKeys(names))
	}
	for _, want := range []string{
		"status", "relevant", "sync", "reconcile", "history", "blame",
		"note_add", "note_review", "doc_add", "doc_supersede", "log_append",
		"task_add", "task_claim", "task_done", "task_criterion_met", "task_criterion_pending", "task_criterion_script", "task_validate",
		"sprint_add", "sprint_activate", "project_add", "project_archive",
		"runbook_add", "runbook_list", "runbook_show", "runbook_step_add",
		"runbook_run_start", "runbook_run_done", "runbook_run_skip", "runbook_run_fail", "runbook_run_finish",
		"attachment_path", "attachment_get",
	} {
		if !names[want] {
			t.Errorf("tool %q missing from the inventory", want)
		}
	}
	for _, absent := range []string{
		"mcp", "mount", "mount_holder", "init", "gc", "compact", "version", "viz",
		"skills", "hooks", "workflows", "doc_checkout", "note_apply",
		"task_move", "task_criterion_reset", "sprint_start",
	} {
		if names[absent] {
			t.Errorf("excluded tool %q was registered", absent)
		}
	}
}

func TestMarkerLifecycle(t *testing.T) {
	dir := t.TempDir()
	if err := mcpserver.WriteMarker(dir); err != nil {
		t.Fatalf("WriteMarker: %v", err)
	}
	self := filepath.Join(dir, fmt.Sprintf("%d.json", os.Getpid()))
	if _, err := os.Stat(self); err != nil {
		t.Fatalf("own marker missing after WriteMarker: %v", err)
	}

	// A marker for a dead pid is swept on the next WriteMarker; ours survives.
	deadPID := reapedPID(t)
	dead := filepath.Join(dir, fmt.Sprintf("%d.json", deadPID))
	data, _ := json.Marshal(mcpserver.Marker{PID: deadPID, StartedAt: 1})
	if err := os.WriteFile(dead, data, 0o600); err != nil {
		t.Fatalf("write dead marker: %v", err)
	}
	if err := mcpserver.WriteMarker(dir); err != nil {
		t.Fatalf("WriteMarker (sweep): %v", err)
	}
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("dead-pid marker %d survived the sweep", deadPID)
	}
	if _, err := os.Stat(self); err != nil {
		t.Fatalf("own marker swept in error: %v", err)
	}

	mcpserver.RemoveMarker(dir)
	if _, err := os.Stat(self); !os.IsNotExist(err) {
		t.Fatalf("own marker not removed by RemoveMarker")
	}
}

// TestServeSignalStopExitsClean pins the shutdown contract: cancelling the
// context (what a SIGTERM does in main) makes Serve return nil — a requested
// stop is a clean exit — and removes the liveness marker.
func TestServeSignalStopExitsClean(t *testing.T) {
	dir := initRepo(t)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		_ = w.Close()
		_ = r.Close()
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- mcpserver.Serve(ctx, dir, mcpserver.Config{Version: "test", NewRoot: cli.NewRootCmd, Label: cli.Label})
	}()

	marker := filepath.Join(dir, ".git", "cc-notes", "mcp", fmt.Sprintf("%d.json", os.Getpid()))
	waitFor(t, "liveness marker", func() bool {
		_, err := os.Stat(marker)
		return err == nil
	})
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve after cancel = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after context cancel")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("marker still present after shutdown: %v", err)
	}
}

// waitFor polls an observable condition until it holds or the deadline lapses.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// reapedPID starts and waits on a trivial process, returning its now-dead pid.
func reapedPID(t *testing.T) int {
	t.Helper()
	//nolint:gosec // G204: fixed argv, a test helper to obtain a reaped (dead) pid.
	cmd := exec.Command("sh", "-c", "exit 0")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start throwaway process: %v", err)
	}
	pid := cmd.Process.Pid
	_ = cmd.Wait()
	return pid
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
