package notes_test

import (
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// mustStatus runs Status and fails the test on error.
func mustStatus(t *testing.T, c *notes.Client) notes.StatusReport {
	t.Helper()
	rep, err := c.Status(t.Context())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	return rep
}

// taskIDs projects a task slice onto its ids for order-sensitive comparison.
func taskIDs(tasks []model.Task) []model.EntityID {
	ids := make([]model.EntityID, len(tasks))
	for i, t := range tasks {
		ids[i] = t.ID
	}
	return ids
}

// backlogIDs projects the backlog rows onto their ids, in report order.
func backlogIDs(rows []notes.StatusBacklogTask) []model.EntityID {
	ids := make([]model.EntityID, len(rows))
	for i, r := range rows {
		ids[i] = r.Task.ID
	}
	return ids
}

// readyBacklogIDs projects the backlog rows the dependency traversal marked
// claimable, in report order.
func readyBacklogIDs(rows []notes.StatusBacklogTask) []model.EntityID {
	var ids []model.EntityID
	for _, r := range rows {
		if r.Ready {
			ids = append(ids, r.Task.ID)
		}
	}
	return ids
}

func TestStatusBuckets(t *testing.T) {
	c, dir := newClient(t)
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")

	// Created high-priority first, low-priority second: sortTasks must reorder
	// the backlog by priority ascending, independent of creation order.
	backlogHi := mustTask(t, c, notes.TaskSpec{Title: "backlog-hi", Backlog: true, Priority: 3})
	backlogLo := mustTask(t, c, notes.TaskSpec{Title: "backlog-lo", Backlog: true, Priority: 0})
	mainOpen := mustTask(t, c, notes.TaskSpec{Title: "main-open", Branch: "main", Priority: 1})
	feature := mustTask(t, c, notes.TaskSpec{Title: "feature", Branch: "feature/x", Priority: 0})

	rep := mustStatus(t, c)

	if rep.Branch != "main" {
		t.Errorf("Branch = %q, want main", rep.Branch)
	}
	if got, want := backlogIDs(rep.Backlog), []model.EntityID{backlogLo.ID, backlogHi.ID}; !slices.Equal(got, want) {
		t.Errorf("Backlog ids = %v, want %v (priority-ascending)", got, want)
	}
	// Nothing blocks either backlog task, so both come back claimable.
	if got, want := readyBacklogIDs(rep.Backlog), []model.EntityID{backlogLo.ID, backlogHi.ID}; !slices.Equal(got, want) {
		t.Errorf("ready backlog ids = %v, want %v", got, want)
	}
	if got, want := taskIDs(rep.YourBranch), []model.EntityID{mainOpen.ID}; !slices.Equal(got, want) {
		t.Errorf("YourBranch ids = %v, want %v", got, want)
	}
	if len(rep.InProgress) != 0 {
		t.Errorf("InProgress = %v, want empty (no in-progress task)", rep.InProgress)
	}
	if len(rep.Runs) != 0 {
		t.Errorf("Runs = %v, want empty (no runbook run started)", rep.Runs)
	}
	// A task on another branch belongs to no bucket here.
	for _, id := range append(backlogIDs(rep.Backlog), taskIDs(rep.YourBranch)...) {
		if id == feature.ID {
			t.Errorf("feature/x task %s leaked into backlog or your-branch", feature.ID)
		}
	}
	if rep.Notes != (notes.SummaryCount{}) || rep.Docs != (notes.SummaryCount{}) || rep.Logs != 0 || rep.Papercuts != 0 {
		t.Errorf("summaries = notes %+v docs %+v logs %d papercuts %d, want all zero", rep.Notes, rep.Docs, rep.Logs, rep.Papercuts)
	}
	if rep.Investigations != (notes.InvestigationSummary{}) {
		t.Errorf("Investigations = %+v, want zero", rep.Investigations)
	}
}

// TestStatusBacklogReadiness pins the split the orientation surface exists for:
// a backlog task held by a live blocker is not claimable, and closing the
// blocker flips it — the dependency traversal ReadyTasks runs, projected per
// row. Drop the ReadyTasks call and every row reads ready.
func TestStatusBacklogReadiness(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")

	blocker := mustTask(t, c, notes.TaskSpec{Title: "blocker", Backlog: true, Priority: 0})
	blocked := mustTask(t, c, notes.TaskSpec{Title: "blocked", Backlog: true, Priority: 1})
	if _, err := c.AddDep(ctx, blocked.ID, blocker.ID); err != nil {
		t.Fatalf("AddDep: %v", err)
	}
	held := mustTask(t, c, notes.TaskSpec{Title: "held", Backlog: true, Priority: 2})
	if _, err := c.ClaimTask(ctx, held.ID); err != nil {
		t.Fatalf("ClaimTask held: %v", err)
	}

	rep := mustStatus(t, c)
	if got, want := backlogIDs(rep.Backlog), []model.EntityID{blocker.ID, blocked.ID, held.ID}; !slices.Equal(got, want) {
		t.Fatalf("Backlog ids = %v, want %v", got, want)
	}
	if got, want := readyBacklogIDs(rep.Backlog), []model.EntityID{blocker.ID}; !slices.Equal(got, want) {
		t.Fatalf("ready ids = %v, want %v (a live blocker and an existing hold are both unready)", got, want)
	}

	if _, err := c.DoneTask(ctx, blocker.ID, true); err != nil {
		t.Fatalf("DoneTask blocker: %v", err)
	}
	after := mustStatus(t, c)
	if got, want := readyBacklogIDs(after.Backlog), []model.EntityID{blocked.ID}; !slices.Equal(got, want) {
		t.Fatalf("ready ids after closing the blocker = %v, want %v", got, want)
	}
}

// TestStatusRunsInFlight pins the run board: only running runs appear, and their
// verdict follows the lease TTL measured from the last recorded step.
func TestStatusRunsInFlight(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	rb, _, err := c.CreateRunbook(ctx, notes.RunbookSpec{Title: "deploy", Steps: []string{"build", "ship"}})
	if err != nil {
		t.Fatalf("CreateRunbook: %v", err)
	}
	if _, err := c.StartRun(ctx, rb.ID, ""); err != nil {
		t.Fatalf("StartRun live: %v", err)
	}
	finished, err := c.StartRun(ctx, rb.ID, "")
	if err != nil {
		t.Fatalf("StartRun finished: %v", err)
	}
	done := finished.Runs[len(finished.Runs)-1]
	if _, err := c.FinishRun(ctx, rb.ID, done.ID, model.RunSucceeded); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	t.Setenv("CC_NOTES_LEASE_TTL", "24h")
	rep := mustStatus(t, c)
	if len(rep.Runs) != 1 {
		t.Fatalf("Runs = %+v, want only the still-running run", rep.Runs)
	}
	run := rep.Runs[0]
	if run.Runbook != rb.ID || run.Title != "deploy" {
		t.Errorf("Runs[0] = runbook %s title %q, want %s deploy", run.Runbook, run.Title, rb.ID)
	}
	if run.Run.ID == done.ID {
		t.Errorf("Runs[0] is the finished run %s, want the running one", done.ID)
	}
	if run.Stale {
		t.Errorf("run %s Stale = true under a 24h TTL, want fresh", run.Run.ID)
	}

	t.Setenv("CC_NOTES_LEASE_TTL", "1ns")
	stale := mustStatus(t, c)
	if len(stale.Runs) != 1 || !stale.Runs[0].Stale {
		t.Errorf("Runs under a 1ns TTL = %+v, want one stale run", stale.Runs)
	}
}

// TestStatusPapercutCount proves the count tallies complaint entries, not
// journals, and ignores an ordinary untagged log.
func TestStatusPapercutCount(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	journal, _, err := c.CreateLog(ctx, notes.LogSpec{Title: "papercuts", Tags: []string{notes.PapercutTag}})
	if err != nil {
		t.Fatalf("CreateLog journal: %v", err)
	}
	for _, text := range []string{"unquoted globs broke rg", "the doc link 404s"} {
		if _, err := c.AppendLog(ctx, journal.ID, notes.LogAppend{Text: text}); err != nil {
			t.Fatalf("AppendLog %q: %v", text, err)
		}
	}
	plain, _, err := c.CreateLog(ctx, notes.LogSpec{Title: "rollout"})
	if err != nil {
		t.Fatalf("CreateLog plain: %v", err)
	}
	if _, err := c.AppendLog(ctx, plain.ID, notes.LogAppend{Text: "not a complaint"}); err != nil {
		t.Fatalf("AppendLog plain: %v", err)
	}

	rep := mustStatus(t, c)
	if rep.Papercuts != 2 {
		t.Errorf("Papercuts = %d, want 2 (entries in the tagged journal only)", rep.Papercuts)
	}
	if rep.Logs != 2 {
		t.Errorf("Logs = %d, want 2 (the journal counts as a log too)", rep.Logs)
	}
}

// TestStatusOpenFindings counts undecided suspects across the non-terminal
// investigations only: a cleared finding drops out, and a terminal record's
// findings never count.
func TestStatusOpenFindings(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	live := driveTo(t, c, model.InvestigationOpen)
	open, err := c.AddFinding(ctx, live, "the pool rewrite")
	if err != nil {
		t.Fatalf("AddFinding open: %v", err)
	}
	if _, err := c.AddFinding(ctx, live, "the retry cap"); err != nil {
		t.Fatalf("AddFinding second: %v", err)
	}
	if _, err := c.SetFindingCleared(ctx, live, open.Findings[0].ID, "bisect exonerates it"); err != nil {
		t.Fatalf("SetFindingCleared: %v", err)
	}

	closed := driveTo(t, c, model.InvestigationOpen)
	if _, err := c.AddFinding(ctx, closed, "a suspect nobody decided"); err != nil {
		t.Fatalf("AddFinding on the abandoned record: %v", err)
	}
	if _, err := c.Abandon(ctx, closed, "walked away"); err != nil {
		t.Fatalf("Abandon: %v", err)
	}

	rep := mustStatus(t, c)
	if want := (notes.InvestigationSummary{Open: 1, OpenFindings: 1}); rep.Investigations != want {
		t.Errorf("Investigations = %+v, want %+v", rep.Investigations, want)
	}
}

func TestStatusInvestigationCounts(t *testing.T) {
	c, _ := newClient(t)

	// Open counts open + root_caused; AwaitingConfirm counts fixed; the three
	// terminal statuses are excluded entirely.
	driveTo(t, c, model.InvestigationOpen)
	driveTo(t, c, model.InvestigationRootCaused)
	driveTo(t, c, model.InvestigationFixed)
	driveTo(t, c, model.InvestigationConfirmed)
	driveTo(t, c, model.InvestigationExonerated)
	driveTo(t, c, model.InvestigationAbandoned)

	rep := mustStatus(t, c)
	if want := (notes.InvestigationSummary{Open: 2, AwaitingConfirm: 1}); rep.Investigations != want {
		t.Errorf("Investigations = %+v, want %+v", rep.Investigations, want)
	}
}

func TestStatusInProgress(t *testing.T) {
	const otherActor = "Aaa Agent <aaa@example.com>"
	c, dir := newClient(t)
	ctx := t.Context()
	gittest.Git(t, dir, "commit", "--allow-empty", "-q", "-m", "root")

	// One in-progress task per actor; "Aaa" sorts before the default test actor,
	// so a correctly sorted InProgress lists otherActor first.
	mine := mustTask(t, c, notes.TaskSpec{Title: "mine", Branch: "main"})
	if _, err := c.ClaimTask(ctx, mine.ID); err != nil {
		t.Fatalf("ClaimTask mine: %v", err)
	}
	t.Setenv("CC_NOTES_ACTOR", otherActor)
	theirs := mustTask(t, c, notes.TaskSpec{Title: "theirs", Branch: "main"})
	if _, err := c.ClaimTask(ctx, theirs.ID); err != nil {
		t.Fatalf("ClaimTask theirs: %v", err)
	}
	t.Setenv("CC_NOTES_ACTOR", testActor)

	// A generous TTL keeps just-claimed tasks fresh.
	t.Setenv("CC_NOTES_LEASE_TTL", "24h")
	rep := mustStatus(t, c)
	if len(rep.InProgress) != 2 {
		t.Fatalf("InProgress groups = %d, want 2: %+v", len(rep.InProgress), rep.InProgress)
	}
	if rep.InProgress[0].Assignee != model.Actor(otherActor) {
		t.Errorf("InProgress[0].Assignee = %q, want %q (sorted first)", rep.InProgress[0].Assignee, otherActor)
	}
	if rep.InProgress[1].Assignee != model.Actor(testActor) {
		t.Errorf("InProgress[1].Assignee = %q, want %q", rep.InProgress[1].Assignee, testActor)
	}
	for _, grp := range rep.InProgress {
		if len(grp.Tasks) != 1 {
			t.Fatalf("group %q has %d tasks, want 1", grp.Assignee, len(grp.Tasks))
		}
		if grp.Tasks[0].Stale {
			t.Errorf("task %s Stale = true under a 24h TTL, want fresh", grp.Tasks[0].Task.ID)
		}
	}

	// A sub-nanosecond TTL makes every in-progress lease read as stale.
	t.Setenv("CC_NOTES_LEASE_TTL", "1ns")
	stale := mustStatus(t, c)
	for _, grp := range stale.InProgress {
		if !grp.Tasks[0].Stale {
			t.Errorf("task %s Stale = false under a 1ns TTL, want stale", grp.Tasks[0].Task.ID)
		}
	}
}

func TestStatusReviewCounts(t *testing.T) {
	c, dir := newClient(t)
	ctx := t.Context()
	commitFile(t, dir, "a.go", "v1\n")

	// Notes and docs are born verified (fresh); expiring one flags it for review.
	if _, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "fresh-note", Body: "b"}); err != nil {
		t.Fatalf("CreateNote fresh: %v", err)
	}
	expiredNote, _, err := c.CreateNote(ctx, notes.NoteSpec{Title: "expired-note", Body: "b"})
	if err != nil {
		t.Fatalf("CreateNote expired: %v", err)
	}
	if _, err := c.ExpireNote(ctx, expiredNote.ID, "outdated"); err != nil {
		t.Fatalf("ExpireNote: %v", err)
	}

	if _, _, err := c.CreateDoc(ctx, notes.DocSpec{Title: "fresh-doc", Body: "b", When: "always"}); err != nil {
		t.Fatalf("CreateDoc fresh: %v", err)
	}
	expiredDoc, _, err := c.CreateDoc(ctx, notes.DocSpec{Title: "expired-doc", Body: "b", When: "always"})
	if err != nil {
		t.Fatalf("CreateDoc expired: %v", err)
	}
	if _, err := c.ExpireDoc(ctx, expiredDoc.ID, "outdated"); err != nil {
		t.Fatalf("ExpireDoc: %v", err)
	}

	if _, _, err := c.CreateLog(ctx, notes.LogSpec{Title: "a log"}); err != nil {
		t.Fatalf("CreateLog: %v", err)
	}

	rep := mustStatus(t, c)

	if want := (notes.SummaryCount{Total: 2, NeedsReview: 1}); rep.Notes != want {
		t.Errorf("Notes = %+v, want %+v", rep.Notes, want)
	}
	if want := (notes.SummaryCount{Total: 2, NeedsReview: 1}); rep.Docs != want {
		t.Errorf("Docs = %+v, want %+v", rep.Docs, want)
	}
	if rep.Logs != 1 {
		t.Errorf("Logs = %d, want 1", rep.Logs)
	}
}
