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
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/mcpserver"
)

// initRepo creates a repository on main with one seed commit and chdirs into it,
// with the git environment scrubbed and the actor frozen.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := gittest.InitRepo(t)
	t.Setenv("HOME", t.TempDir())
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o600); err != nil {
		t.Fatalf("write seed: %v", err)
	}
	gittest.Git(t, dir, "add", "seed.txt")
	gittest.Git(t, dir, "commit", "-q", "-m", "seed")
	t.Setenv("CC_NOTES_ACTOR", "Agent A <a@example.com>")
	t.Chdir(dir)
	return dir
}

// connect wires an mcpserver to an SDK client over in-memory transports.
func connect(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := t.Context()
	srv := mcpserver.New(mcpserver.Config{Version: "test", NewRoot: cli.NewRootCmd, Label: cli.Label, Message: cli.Message})
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

// ackID extracts the entity id a tool's summary acknowledgement carries.
func ackID(t *testing.T, raw string) string {
	t.Helper()
	return decode[struct {
		ID string `json:"id"`
	}](t, raw).ID
}

// show reads an entity back in full through its *_show tool: a mutation
// acknowledges with a summary, so bodies, findings, steps, and runs live here.
func show[T any](t *testing.T, cs *mcp.ClientSession, tool, id string) T {
	t.Helper()
	return decode[T](t, call(t, cs, tool, map[string]any{"id": id}))
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

// docSummaryOut is what every doc mutation acknowledges with: identity, the
// When trigger, and the drift verdict, but no body and no attachments.
type docSummaryOut struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	When  string   `json:"when"`
	Tags  []string `json:"tags"`
	Drift string   `json:"drift"`
	Body  string   `json:"body"`
}

type noteOut struct {
	ID         string  `json:"id"`
	Body       string  `json:"body"`
	VerifiedAt *string `json:"verified_at"`
	VerifiedBy *string `json:"verified_by"`
}

// noteSummaryOut is what every note mutation acknowledges with. Body and the
// verification stamps must stay absent; they live with note_show.
type noteSummaryOut struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Drift      string  `json:"drift"`
	Body       string  `json:"body"`
	VerifiedAt *string `json:"verified_at"`
	VerifiedBy *string `json:"verified_by"`
}

// taskOut is what every task mutation acknowledges with: the triage and claim
// fields, without the description, comments, or criteria.
type taskOut struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Status   string `json:"status"`
	Assignee string `json:"assignee"`
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

// runbookSummaryOut is what every runbook mutation acknowledges with: the step
// and run tallies stand in for the steps and runs themselves.
type runbookSummaryOut struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	StepCount int    `json:"step_count"`
	RunCount  int    `json:"run_count"`
	Steps     []struct {
		ID string `json:"id"`
	} `json:"steps"`
	Runs []struct {
		ID string `json:"id"`
	} `json:"runs"`
}

type investigationOut struct {
	ID         string   `json:"id"`
	Premise    string   `json:"premise"`
	Status     string   `json:"status"`
	RootCause  string   `json:"root_cause"`
	FixCommits []string `json:"fix_commits"`
	Findings   []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
		Note   string `json:"note"`
	} `json:"findings"`
	Entries []struct {
		Text string `json:"text"`
	} `json:"entries"`
}

// investigationSummaryOut is what every investigation mutation acknowledges
// with: the status plus the finding and timeline tallies, no premise or bodies.
type investigationSummaryOut struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Status       string `json:"status"`
	FindingCount int    `json:"finding_count"`
	EntryCount   int    `json:"entry_count"`
	Premise      string `json:"premise"`
	RootCause    string `json:"root_cause"`
	Findings     []struct {
		ID string `json:"id"`
	} `json:"findings"`
	Entries []struct {
		Text string `json:"text"`
	} `json:"entries"`
}

func TestDocAddShowRoundTrip(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	att := filepath.Join(t.TempDir(), "artifact.txt")
	if err := os.WriteFile(att, []byte("attached bytes\n"), 0o600); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	ack := decode[docSummaryOut](t, call(t, cs, "doc_add", map[string]any{
		"title":  "Handoff",
		"body":   "the long body",
		"when":   "resuming the cutover",
		"labels": []string{"design"},
		"attach": []string{att},
	}))
	if ack.Title != "Handoff" || ack.When != "resuming the cutover" {
		t.Fatalf("ack = %+v, want the title and the When trigger", ack)
	}
	if ack.Body != "" {
		t.Fatalf("doc_add ack carries the body %q; a write acknowledgement is a summary", ack.Body)
	}

	shown := show[docOut](t, cs, "doc_show", ack.ID)
	if shown.ID != ack.ID || shown.Body != "the long body" || shown.When != "resuming the cutover" {
		t.Fatalf("show = %+v, want the full doc", shown)
	}
	if shown.VerifiedAt == nil || shown.VerifiedBy == nil || *shown.VerifiedBy != "Agent A <a@example.com>" {
		t.Fatalf("doc not born-verified: verified_at=%v verified_by=%v", shown.VerifiedAt, shown.VerifiedBy)
	}
	if len(shown.Attachments) != 1 || shown.Attachments[0].Name != "artifact.txt" {
		t.Fatalf("show attachments = %+v, want artifact.txt listed", shown.Attachments)
	}
}

func TestNoteAddVerify(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	ack := decode[noteSummaryOut](t, call(t, cs, "note_add", map[string]any{"title": "Fact", "body": "v1"}))
	if ack.Title != "Fact" {
		t.Fatalf("ack = %+v, want the title", ack)
	}
	if ack.Body != "" || ack.VerifiedAt != nil || ack.VerifiedBy != nil {
		t.Fatalf("note_add ack = %+v, want a summary with no body and no verification stamps", ack)
	}

	added := show[noteOut](t, cs, "note_show", ack.ID)
	if added.Body != "v1" {
		t.Fatalf("show body = %q, want v1", added.Body)
	}
	if added.VerifiedBy == nil || *added.VerifiedBy != "Agent A <a@example.com>" {
		t.Fatalf("note not born-verified: %+v", added)
	}

	reverified := decode[noteSummaryOut](t, call(t, cs, "note_verify", map[string]any{"id": ack.ID}))
	if reverified.ID != ack.ID {
		t.Fatalf("verify ack = %+v, want the same note", reverified)
	}
	if verified := show[noteOut](t, cs, "note_show", ack.ID); verified.VerifiedAt == nil {
		t.Fatalf("verify = %+v, want the note re-verified", verified)
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
	if added.Status != "open" || added.Assignee != "" {
		t.Fatalf("added = %+v, want open and unassigned", added)
	}

	claimed := decode[taskOut](t, call(t, cs, "task_claim", map[string]any{"id": added.ID}))
	if claimed.Status != "in_progress" || claimed.Assignee == "" {
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

	ack := decode[runbookSummaryOut](t, call(t, cs, "runbook_add", map[string]any{
		"title": "Deploy",
		"steps": []string{"build", "ship"},
	}))
	if ack.Status != "active" || ack.StepCount != 2 {
		t.Fatalf("add ack = %+v, want active with step_count 2", ack)
	}
	if ack.Steps != nil {
		t.Fatalf("runbook_add ack carries the steps; a write acknowledgement is a summary")
	}

	added := show[runbookOut](t, cs, "runbook_show", ack.ID)
	if len(added.Steps) != 2 || added.Steps[0].Text != "build" || added.Steps[1].Text != "ship" {
		t.Fatalf("steps = %+v, want build then ship in order", added.Steps)
	}

	startAck := decode[runbookSummaryOut](t, call(t, cs, "runbook_run_start", map[string]any{"id": added.ID}))
	if startAck.RunCount != 1 || startAck.Runs != nil {
		t.Fatalf("run_start ack = %+v, want run_count 1 and no runs array", startAck)
	}
	started := show[runbookOut](t, cs, "runbook_show", added.ID)
	if len(started.Runs) != 1 || started.Runs[0].Status != "running" {
		t.Fatalf("runs = %+v, want one running run", started.Runs)
	}

	// done/skip omit run: default-run resolution picks the sole running run.
	call(t, cs, "runbook_run_done", map[string]any{"id": added.ID, "step": added.Steps[0].ID, "note": "built clean"})
	call(t, cs, "runbook_run_skip", map[string]any{"id": added.ID, "step": added.Steps[1].ID})

	call(t, cs, "runbook_run_finish", map[string]any{"id": added.ID})
	finished := show[runbookOut](t, cs, "runbook_show", added.ID)
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

	shown := show[runbookOut](t, cs, "runbook_show", added.ID)
	if len(shown.Runs) != 1 || shown.Runs[0].Status != "succeeded" {
		t.Fatalf("show runs = %+v, want one succeeded run", shown.Runs)
	}
}

func TestInvestigationLifecycle(t *testing.T) {
	dir := initRepo(t)
	fixCommit := gittest.Git(t, dir, "rev-parse", "HEAD")
	cs := connect(t)

	openAck := decode[investigationSummaryOut](t, call(t, cs, "investigation_open", map[string]any{
		"title":    "Suspect the parser",
		"premise":  "the parser drops quoted fields",
		"findings": []string{"quoted fields disappear"},
	}))
	if openAck.Status != "open" || openAck.FindingCount != 1 {
		t.Fatalf("open ack = %+v, want open with finding_count 1", openAck)
	}
	if openAck.Premise != "" || openAck.Findings != nil {
		t.Fatalf("investigation_open ack = %+v, want a summary with no premise and no findings array", openAck)
	}

	id := openAck.ID
	opened := show[investigationOut](t, cs, "investigation_show", id)
	if opened.Status != "open" || opened.Premise != "the parser drops quoted fields" || len(opened.Findings) != 1 {
		t.Fatalf("opened = %+v, want an open investigation with its premise and finding", opened)
	}

	if ack := decode[investigationSummaryOut](t, call(t, cs, "investigation_append", map[string]any{
		"id":   id,
		"text": "reproduced with a quoted fixture",
	})); ack.EntryCount != 1 || ack.Entries != nil {
		t.Fatalf("append ack = %+v, want entry_count 1 and no entries array", ack)
	}
	appended := show[investigationOut](t, cs, "investigation_show", id)
	if len(appended.Entries) != 1 || appended.Entries[0].Text != "reproduced with a quoted fixture" {
		t.Fatalf("entries = %+v, want the appended evidence", appended.Entries)
	}

	call(t, cs, "investigation_finding_clear", map[string]any{
		"id":      id,
		"finding": opened.Findings[0].ID,
		"why":     "the fixture was malformed",
	})
	cleared := show[investigationOut](t, cs, "investigation_show", id)
	if cleared.Findings[0].Status != "cleared" || cleared.Findings[0].Note != "the fixture was malformed" {
		t.Fatalf("finding = %+v, want cleared with its evidence", cleared.Findings[0])
	}

	if ack := decode[investigationSummaryOut](t, call(t, cs, "investigation_root_cause", map[string]any{
		"id":   id,
		"text": "the fixture escaped the delimiter twice",
	})); ack.Status != "root_caused" || ack.RootCause != "" {
		t.Fatalf("root-cause ack = %+v, want root_caused with no root_cause text", ack)
	}
	rooted := show[investigationOut](t, cs, "investigation_show", id)
	if rooted.RootCause != "the fixture escaped the delimiter twice" {
		t.Fatalf("root cause = %q, want the recorded verdict", rooted.RootCause)
	}

	if ack := decode[investigationSummaryOut](t, call(t, cs, "investigation_fix", map[string]any{
		"id":      id,
		"text":    "corrected fixture escaping",
		"commits": []string{fixCommit},
	})); ack.Status != "fixed" {
		t.Fatalf("fix ack = %+v, want fixed", ack)
	}
	fixed := show[investigationOut](t, cs, "investigation_show", id)
	if len(fixed.FixCommits) != 1 || fixed.FixCommits[0] != fixCommit {
		t.Fatalf("fixed = %+v, want the seed commit recorded", fixed)
	}

	if ack := decode[investigationSummaryOut](t, call(t, cs, "investigation_confirm", map[string]any{
		"id":   id,
		"text": "the quoted fixture now passes",
	})); ack.Status != "confirmed" {
		t.Fatalf("confirm ack = %+v, want confirmed", ack)
	}
	confirmed := show[investigationOut](t, cs, "investigation_show", id)
	if confirmed.Entries[len(confirmed.Entries)-1].Text != "the quoted fixture now passes" {
		t.Fatalf("confirmed = %+v, want the proof appended", confirmed)
	}
}

// TestInvestigationVerdictForceOverMCP pins that the verdict gate and its escape
// both reach the MCP surface: a verdict with a finding still open comes back as
// an error result naming the finding and the remediation, and force records it.
func TestInvestigationVerdictForceOverMCP(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	const suspect = "quoted fields disappear"
	id := ackID(t, call(t, cs, "investigation_open", map[string]any{
		"title":    "the parser drops rows",
		"premise":  "the parser drops quoted fields",
		"findings": []string{suspect},
	}))
	finding := show[investigationOut](t, cs, "investigation_show", id).Findings[0]

	args := map[string]any{"id": id, "text": "the fixture was malformed"}
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "investigation_exonerate", Arguments: args})
	if err != nil {
		t.Fatalf("call investigation_exonerate: %v", err)
	}
	if !res.IsError {
		t.Fatalf("exonerate with an open finding returned no error result: %s", toolText(res))
	}
	for _, fragment := range []string{"usage", "1 open finding/findings blocking exonerated", "pass --force", finding.ID[:7], suspect} {
		if got := toolText(res); !strings.Contains(got, fragment) {
			t.Errorf("error text omits %q: %s", fragment, got)
		}
	}
	if got := show[investigationOut](t, cs, "investigation_show", id); got.Status != "open" {
		t.Fatalf("status after the refused verdict = %q, want open", got.Status)
	}

	args["force"] = true
	if ack := decode[investigationSummaryOut](t, call(t, cs, "investigation_exonerate", args)); ack.Status != "exonerated" {
		t.Fatalf("forced exonerate ack = %+v, want exonerated", ack)
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

	const wantCount = 128
	if len(names) != wantCount {
		t.Errorf("tool count = %d, want %d; got %v", len(names), wantCount, sortedKeys(names))
	}
	for _, want := range []string{
		"status", "relevant", "sync", "reconcile", "history", "search", "show", "blame",
		"note_add", "note_review", "doc_add", "doc_supersede", "log_append",
		"papercut", "papercut_list",
		"task_add", "task_claim", "task_done", "task_criterion_met", "task_criterion_pending", "task_criterion_script", "task_validate",
		"sprint_add", "sprint_activate", "project_add", "project_activate", "project_archive",
		"runbook_add", "runbook_list", "runbook_show", "runbook_activate", "runbook_archive", "runbook_edit", "runbook_rm", "runbook_search", "runbook_comment",
		"runbook_step_add", "runbook_step_edit", "runbook_step_rm", "runbook_step_move", "runbook_step_list",
		"runbook_run_start", "runbook_run_list", "runbook_run_show", "runbook_run_done", "runbook_run_skip", "runbook_run_fail", "runbook_run_finish",
		"investigation_open", "investigation_list", "investigation_show", "investigation_append",
		"investigation_finding_add", "investigation_finding_edit", "investigation_finding_clear", "investigation_finding_confirm", "investigation_finding_rm", "investigation_finding_list",
		"investigation_root_cause", "investigation_fix", "investigation_confirm", "investigation_exonerate", "investigation_reopen", "investigation_abandon", "investigation_edit", "investigation_search", "investigation_rm",
		"attachment_path", "attachment_get",
	} {
		if !names[want] {
			t.Errorf("tool %q missing from the inventory", want)
		}
	}
	for _, absent := range []string{
		"mcp", "init", "service", "gc", "compact", "version", "viz",
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
		done <- mcpserver.Serve(ctx, dir, mcpserver.Config{Version: "test", NewRoot: cli.NewRootCmd, Label: cli.Label, Message: cli.Message})
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
