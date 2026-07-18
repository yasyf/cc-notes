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
	if got, want := taskIDs(rep.Backlog), []model.EntityID{backlogLo.ID, backlogHi.ID}; !slices.Equal(got, want) {
		t.Errorf("Backlog ids = %v, want %v (priority-ascending)", got, want)
	}
	if got, want := taskIDs(rep.YourBranch), []model.EntityID{mainOpen.ID}; !slices.Equal(got, want) {
		t.Errorf("YourBranch ids = %v, want %v", got, want)
	}
	if len(rep.InProgress) != 0 {
		t.Errorf("InProgress = %v, want empty (no in-progress task)", rep.InProgress)
	}
	// A task on another branch belongs to no bucket here.
	for _, id := range append(taskIDs(rep.Backlog), taskIDs(rep.YourBranch)...) {
		if id == feature.ID {
			t.Errorf("feature/x task %s leaked into backlog or your-branch", feature.ID)
		}
	}
	if rep.Notes != (notes.SummaryCount{}) || rep.Docs != (notes.SummaryCount{}) || rep.Logs != 0 {
		t.Errorf("summaries = notes %+v docs %+v logs %d, want all zero", rep.Notes, rep.Docs, rep.Logs)
	}
	if rep.Investigations != (notes.InvestigationSummary{}) {
		t.Errorf("Investigations = %+v, want zero", rep.Investigations)
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
