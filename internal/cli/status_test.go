package cli_test

import (
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
)

// statusBacklogJSON mirrors one backlog row: the task summary plus the
// ready-to-claim verdict.
type statusBacklogJSON struct {
	taskJSON
	Ready bool `json:"ready"`
}

// statusRunJSON mirrors one in-flight runbook run row.
type statusRunJSON struct {
	Runbook   string `json:"runbook"`
	Title     string `json:"title"`
	Run       string `json:"run"`
	Runner    string `json:"runner"`
	StartedAt string `json:"started_at"`
	Stale     bool   `json:"stale"`
}

// statusJSONShape mirrors the status --json DTO for round-trip assertions.
type statusJSONShape struct {
	Branch     string              `json:"branch"`
	Backlog    []statusBacklogJSON `json:"backlog"`
	YourBranch []taskJSON          `json:"your_branch"`
	InProgress []struct {
		Assignee string `json:"assignee"`
		Tasks    []struct {
			ID    string `json:"id"`
			Stale bool   `json:"stale"`
		} `json:"tasks"`
	} `json:"in_progress"`
	Runs  []statusRunJSON `json:"runs"`
	Notes struct {
		Total       int `json:"total"`
		NeedsReview int `json:"needs_review"`
	} `json:"notes"`
	Docs struct {
		Total       int `json:"total"`
		NeedsReview int `json:"needs_review"`
	} `json:"docs"`
	Logs struct {
		Total int `json:"total"`
	} `json:"logs"`
	Papercuts struct {
		Total int `json:"total"`
	} `json:"papercuts"`
	Investigations struct {
		Open            int `json:"open"`
		AwaitingConfirm int `json:"awaiting_confirm"`
		OpenFindings    int `json:"open_findings"`
	} `json:"investigations"`
}

// addInvestigation opens an investigation through the CLI and returns its id.
func addInvestigation(t *testing.T, dir, title string) string {
	t.Helper()
	inv := mustJSON[struct {
		ID string `json:"id"`
	}](t, mustRun(t, dir, "investigation", "open", title, "premise for "+title, "--json"))
	return inv.ID
}

func hasTaskID(tasks []taskJSON, id string) bool {
	for _, t := range tasks {
		if t.ID == id {
			return true
		}
	}
	return false
}

// backlogRow returns the status backlog row for id, failing when it is absent.
func backlogRow(t *testing.T, rows []statusBacklogJSON, id string) statusBacklogJSON {
	t.Helper()
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("task %s absent from backlog %+v", id, rows)
	return statusBacklogJSON{}
}

func TestStatusJSON(t *testing.T) {
	dir := initRepo(t)
	back := addTask(t, dir, "Backlog item", "--backlog")
	open := addTask(t, dir, "Open on branch")
	claimed := addTask(t, dir, "Claimed work")
	mustRun(t, dir, "task", "claim", claimed.ID)
	mustRun(t, dir, "note", "add", "A note")

	t.Setenv("CC_NOTES_LEASE_TTL", "8760h")
	st := mustJSON[statusJSONShape](t, mustRun(t, dir, "status", "--json"))
	if st.Branch != "main" {
		t.Fatalf("branch = %q, want main", st.Branch)
	}
	if len(st.Backlog) != 1 || st.Backlog[0].ID != back.ID {
		t.Fatalf("backlog = %+v, want only %s", st.Backlog, back.ID)
	}
	if !st.Backlog[0].Ready {
		t.Fatalf("backlog row %+v, want ready (nothing blocks it and nobody holds it)", st.Backlog[0])
	}
	if len(st.YourBranch) != 2 || !hasTaskID(st.YourBranch, open.ID) || !hasTaskID(st.YourBranch, claimed.ID) {
		t.Fatalf("your_branch = %+v, want %s and %s", st.YourBranch, open.ID, claimed.ID)
	}
	if len(st.InProgress) != 1 || st.InProgress[0].Assignee != actorA {
		t.Fatalf("in_progress = %+v, want one group for %s", st.InProgress, actorA)
	}
	grp := st.InProgress[0].Tasks
	if len(grp) != 1 || grp[0].ID != claimed.ID {
		t.Fatalf("in_progress tasks = %+v, want only %s", grp, claimed.ID)
	}
	if grp[0].Stale {
		t.Fatalf("stale = true under 8760h ttl, want fresh")
	}
	if st.Notes.Total != 1 || st.Notes.NeedsReview != 0 {
		t.Fatalf("notes = %+v, want total 1 needs_review 0", st.Notes)
	}
	if st.Docs.Total != 0 || st.Docs.NeedsReview != 0 {
		t.Fatalf("docs = %+v, want total 0 needs_review 0", st.Docs)
	}
	if st.Logs.Total != 0 {
		t.Fatalf("logs = %+v, want total 0", st.Logs)
	}

	t.Setenv("CC_NOTES_LEASE_TTL", "1ns")
	st2 := mustJSON[statusJSONShape](t, mustRun(t, dir, "status", "--json"))
	if !st2.InProgress[0].Tasks[0].Stale {
		t.Fatalf("stale = false under 1ns ttl, want STALE")
	}
}

func TestStatusText(t *testing.T) {
	dir := initRepo(t)
	back := addTask(t, dir, "Backlog item", "--backlog")
	claimed := addTask(t, dir, "Claimed work")
	mustRun(t, dir, "task", "claim", claimed.ID)
	mustRun(t, dir, "note", "add", "A note")

	t.Setenv("CC_NOTES_LEASE_TTL", "8760h")
	out := mustRun(t, dir, "status")
	for _, want := range []string{
		"backlog\n",
		"  " + back.ID[:7] + "\t",
		"\tready\n",
		"your branch (main)\n",
		"  " + claimed.ID[:7] + "\t",
		"in progress across branches\n",
		"  " + actorA + "\t" + claimed.ID[:7] + "\tfresh\n",
		"runs in flight\n",
		"notes: 1 total, 0 need review\n",
		"docs: 0 total, 0 need review\n",
		"logs: 0 total\n",
		"papercuts: 0 total\n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status text %q missing %q", out, want)
		}
	}
}

// TestStatusBacklogReadiness proves the board reports which backlog work is
// actually claimable: a task held by a live blocker reads blocked in both text
// and JSON, and closing the blocker flips it to ready.
func TestStatusBacklogReadiness(t *testing.T) {
	dir := initRepo(t)
	blocker := addTask(t, dir, "The blocker", "--backlog")
	blocked := addTask(t, dir, "The blocked one", "--backlog")
	mustRun(t, dir, "task", "dep", blocked.ID, blocker.ID)

	t.Setenv("CC_NOTES_LEASE_TTL", "8760h")
	st := mustJSON[statusJSONShape](t, mustRun(t, dir, "status", "--json"))
	if row := backlogRow(t, st.Backlog, blocker.ID); !row.Ready {
		t.Errorf("blocker row = %+v, want ready", row)
	}
	row := backlogRow(t, st.Backlog, blocked.ID)
	if row.Ready {
		t.Errorf("blocked row = %+v, want not ready", row)
	}
	// The blocked row's blockers ride the same TasksBlockingIndex-backed summary.
	if len(row.BlockedBy) != 1 || row.BlockedBy[0] != blocker.ID {
		t.Errorf("blocked row blocked_by = %v, want [%s]", row.BlockedBy, blocker.ID)
	}
	if out := mustRun(t, dir, "status"); !strings.Contains(out, "  "+blocked.ID[:7]+"\topen\tP2\t-\tThe blocked one\tblocked\n") {
		t.Errorf("status text %q missing the blocked backlog row", out)
	}

	mustRun(t, dir, "task", "done", blocker.ID, "--force")
	after := mustJSON[statusJSONShape](t, mustRun(t, dir, "status", "--json"))
	if row := backlogRow(t, after.Backlog, blocked.ID); !row.Ready {
		t.Errorf("row after closing the blocker = %+v, want ready", row)
	}
}

// TestStatusRunsAndPapercuts proves the two additions that make the board an
// orientation surface rather than a task list: a runbook run still in flight
// with its lease verdict, and the papercut tally.
func TestStatusRunsAndPapercuts(t *testing.T) {
	dir := initRepo(t)
	rb := mustJSON[struct {
		ID string `json:"id"`
	}](t, mustRun(t, dir, "runbook", "add", "Deploy", "--step", "build", "--json"))
	mustRun(t, dir, "runbook", "run", "start", rb.ID)
	mustRun(t, dir, "papercut", "unquoted globs broke rg")
	mustRun(t, dir, "papercut", "the doc link 404s")

	t.Setenv("CC_NOTES_LEASE_TTL", "8760h")
	st := mustJSON[statusJSONShape](t, mustRun(t, dir, "status", "--json"))
	if len(st.Runs) != 1 {
		t.Fatalf("runs = %+v, want the one started run", st.Runs)
	}
	run := st.Runs[0]
	if run.Runbook != rb.ID || run.Title != "Deploy" || run.Runner != actorA || run.Stale {
		t.Errorf("run = %+v, want runbook %s title Deploy runner %s fresh", run, rb.ID, actorA)
	}
	if st.Papercuts.Total != 2 {
		t.Errorf("papercuts = %+v, want total 2", st.Papercuts)
	}
	// The journal is an ordinary log, so it counts once there too.
	if st.Logs.Total != 1 {
		t.Errorf("logs = %+v, want total 1 (the papercut journal)", st.Logs)
	}

	out := mustRun(t, dir, "status")
	if !strings.Contains(out, "  "+rb.ID[:7]+"\t") || !strings.Contains(out, "\tDeploy\t"+actorA+"\tfresh\n") {
		t.Errorf("status text %q missing the in-flight run row", out)
	}
	if !strings.Contains(out, "papercuts: 2 total\n") {
		t.Errorf("status text %q missing the papercut tally", out)
	}

	t.Setenv("CC_NOTES_LEASE_TTL", "1ns")
	stale := mustJSON[statusJSONShape](t, mustRun(t, dir, "status", "--json"))
	if len(stale.Runs) != 1 || !stale.Runs[0].Stale {
		t.Errorf("runs under a 1ns TTL = %+v, want one stale run", stale.Runs)
	}
}

// TestStatusLogs proves the logs summary tracks real logs: logs have no
// freshness lifecycle, so the line and DTO carry only a total, no needs_review.
func TestStatusLogs(t *testing.T) {
	dir := initRepo(t)
	mustRun(t, dir, "log", "add", "Rollout timeline")
	mustRun(t, dir, "log", "add", "Incident A")

	t.Setenv("CC_NOTES_LEASE_TTL", "8760h")
	out := mustRun(t, dir, "status")
	if !strings.Contains(out, "logs: 2 total\n") {
		t.Fatalf("status text %q missing logs summary line", out)
	}

	st := mustJSON[statusJSONShape](t, mustRun(t, dir, "status", "--json"))
	if st.Logs.Total != 2 {
		t.Fatalf("logs = %+v, want total 2", st.Logs)
	}
}

// TestStatusDocs proves the docs summary tracks real docs: a freshly added doc
// is born-verified, so it counts toward the total without needing review.
func TestStatusDocs(t *testing.T) {
	dir := initRepo(t)
	mustRun(t, dir, "doc", "add", "A doc", "--body", "x", "--when", "editing the parser")

	t.Setenv("CC_NOTES_LEASE_TTL", "8760h")
	out := mustRun(t, dir, "status")
	if !strings.Contains(out, "docs: 1 total, 0 need review\n") {
		t.Fatalf("status text %q missing docs summary line", out)
	}

	st := mustJSON[statusJSONShape](t, mustRun(t, dir, "status", "--json"))
	if st.Docs.Total != 1 || st.Docs.NeedsReview != 0 {
		t.Fatalf("docs = %+v, want total 1 needs_review 0", st.Docs)
	}
}

// TestStatusInvestigations proves the investigation summary counts the
// still-triaging records (open + root_caused) as open and the fixed-but-unconfirmed
// as awaiting confirmation, in both JSON and text, with terminals excluded.
func TestStatusInvestigations(t *testing.T) {
	dir := initRepo(t)
	head := commitFile(t, dir, "seed.go", "package main")

	addInvestigation(t, dir, "still open") // open

	rc := addInvestigation(t, dir, "will be root-caused")
	mustRun(t, dir, "investigation", "root-cause", rc, "found the cause") // root_caused → open count

	fx := addInvestigation(t, dir, "will be fixed")
	mustRun(t, dir, "investigation", "root-cause", fx, "cause")
	mustRun(t, dir, "investigation", "fix", fx, "the fix", "--commit", head) // fixed → awaiting confirm

	cf := addInvestigation(t, dir, "will be confirmed")
	mustRun(t, dir, "investigation", "root-cause", cf, "cause")
	mustRun(t, dir, "investigation", "fix", cf, "the fix", "--commit", head)
	mustRun(t, dir, "investigation", "confirm", cf, "20 green runs") // confirmed → excluded

	t.Setenv("CC_NOTES_LEASE_TTL", "8760h")
	st := mustJSON[statusJSONShape](t, mustRun(t, dir, "status", "--json"))
	if st.Investigations.Open != 2 || st.Investigations.AwaitingConfirm != 1 || st.Investigations.OpenFindings != 0 {
		t.Fatalf("investigations = %+v, want open 2 awaiting_confirm 1 open_findings 0", st.Investigations)
	}

	out := mustRun(t, dir, "status")
	if !strings.Contains(out, "investigations: 2 open, 1 awaiting confirmation, 0 open findings\n") {
		t.Fatalf("status text %q missing investigation summary line", out)
	}

	// An undecided suspect on the still-open record is the count's whole point:
	// a confirm or exonerate refuses while it stands.
	mustRun(t, dir, "investigation", "finding", "add", rc, "the pool rewrite")
	withFinding := mustJSON[statusJSONShape](t, mustRun(t, dir, "status", "--json"))
	if withFinding.Investigations.OpenFindings != 1 {
		t.Fatalf("investigations = %+v, want open_findings 1", withFinding.Investigations)
	}
}

func TestStatusDetachedHead(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "On main")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c")
	gittest.Git(t, dir, "checkout", "-q", "--detach")

	// Detached at main's tip — the jj colocation norm. CurrentBranch resolves
	// main, so status shows the your-branch section for main.
	out, _, err := runCLI(t, dir, "status")
	if err != nil {
		t.Fatalf("status on detached HEAD err = %v, want nil", err)
	}
	if !strings.Contains(out, "your branch (main)") {
		t.Fatalf("status text %q must show the your-branch section for main", out)
	}
	st := mustJSON[statusJSONShape](t, mustRun(t, dir, "status", "--json"))
	if st.Branch != "main" {
		t.Fatalf("branch = %q, want main on detached-at-tip HEAD", st.Branch)
	}
	if !hasTaskID(st.YourBranch, task.ID) {
		t.Fatalf("your_branch = %+v, want the main task %s", st.YourBranch, task.ID[:7])
	}
}

// TestStatusAmbiguousHead pins the your-branch degrade: on a genuinely
// unresolvable HEAD (no trunk, advanced past the sole bookmark) status resolves
// the empty branch and omits the your-branch section.
func TestStatusAmbiguousHead(t *testing.T) {
	dir := initRepo(t)
	// No trunk: rename the unborn main to wip so main never exists.
	gittest.Git(t, dir, "checkout", "-q", "-b", "wip")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c1")
	addTask(t, dir, "On wip")
	gittest.Git(t, dir, "checkout", "-q", "--detach")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c2")

	out, _, err := runCLI(t, dir, "status")
	if err != nil {
		t.Fatalf("status on ambiguous HEAD err = %v, want nil", err)
	}
	if strings.Contains(out, "your branch") {
		t.Fatalf("status text %q must omit the your-branch section on an unresolvable HEAD", out)
	}
	st := mustJSON[statusJSONShape](t, mustRun(t, dir, "status", "--json"))
	if st.Branch != "" {
		t.Fatalf("branch = %q, want empty on an unresolvable HEAD", st.Branch)
	}
	if len(st.YourBranch) != 0 {
		t.Fatalf("your_branch = %+v, want empty on an unresolvable HEAD", st.YourBranch)
	}
}
