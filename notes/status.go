package notes

import (
	"context"
	"slices"
	"time"

	"github.com/yasyf/cc-notes/model"
)

// StatusReport is the orientation snapshot Status returns: the current branch
// (empty on a detached HEAD or the backlog), the backlog and current-branch open
// or in-progress task slices, the in-progress tasks grouped by assignee, and the
// note, doc, and log summaries. Backlog and YourBranch are ordered by priority
// then creation time then id; InProgress is ordered by assignee then the same
// task order.
type StatusReport struct {
	Branch         model.Branch
	Backlog        []model.Task
	YourBranch     []model.Task
	InProgress     []StatusAssignee
	Notes          SummaryCount
	Docs           SummaryCount
	Logs           int
	Investigations InvestigationSummary
}

// InvestigationSummary is the orientation count of open investigations: Open
// tallies the still-triaging records (open + root_caused) and AwaitingConfirm the
// fixed-but-unconfirmed ones. Terminal records — confirmed, exonerated,
// abandoned — are excluded; only non-terminal investigations need attention.
type InvestigationSummary struct {
	Open            int
	AwaitingConfirm int
}

// StatusAssignee groups one assignee's in-progress tasks, each paired with its
// reader-side stale verdict.
type StatusAssignee struct {
	Assignee model.Actor
	Tasks    []StatusTask
}

// StatusTask pairs an in-progress task with its stale flag: true when the task's
// lease heartbeat has been idle longer than the lease TTL.
type StatusTask struct {
	Task  model.Task
	Stale bool
}

// SummaryCount summarizes a note or doc set: the total live entities and the
// count needing review.
type SummaryCount struct {
	Total       int
	NeedsReview int
}

// Status aggregates the orientation view: the backlog, the current branch's open
// and in-progress tasks, every in-progress task grouped by assignee with a stale
// verdict, and the note, doc, and log summaries. The current branch degrades to
// empty on a detached HEAD.
func (c *Client) Status(ctx context.Context) (StatusReport, error) {
	now := time.Now()
	ttl, err := c.LeaseTTL(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	branch, _, err := c.currentBranchOrBacklog(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	tasks, err := c.s.ListTasks(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	noteList, err := c.s.ListNotes(ctx, false, false)
	if err != nil {
		return StatusReport{}, err
	}
	docList, err := c.s.ListDocs(ctx, false, false)
	if err != nil {
		return StatusReport{}, err
	}
	logList, err := c.s.ListLogs(ctx, false)
	if err != nil {
		return StatusReport{}, err
	}
	invList, err := c.s.ListInvestigations(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	staleAfter, err := c.NoteStaleAfter(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	noteReviews, err := c.ReviewNotes(ctx, staleAfter)
	if err != nil {
		return StatusReport{}, err
	}
	docReviews, err := c.ReviewDocs(ctx, staleAfter)
	if err != nil {
		return StatusReport{}, err
	}

	var backlog, yourBranch, inProgress []model.Task
	for _, t := range tasks {
		if t.Branch == "" && (t.Status == model.StatusOpen || t.Status == model.StatusInProgress) {
			backlog = append(backlog, t)
		}
		if branch != "" && t.Branch == branch && (t.Status == model.StatusOpen || t.Status == model.StatusInProgress) {
			yourBranch = append(yourBranch, t)
		}
		if t.Status == model.StatusInProgress {
			inProgress = append(inProgress, t)
		}
	}
	sortTasks(backlog)
	sortTasks(yourBranch)

	groups := map[model.Actor][]model.Task{}
	for _, t := range inProgress {
		groups[t.Assignee] = append(groups[t.Assignee], t)
	}
	assignees := make([]model.Actor, 0, len(groups))
	for a := range groups {
		assignees = append(assignees, a)
	}
	slices.Sort(assignees)
	for _, a := range assignees {
		sortTasks(groups[a])
	}

	var invSummary InvestigationSummary
	for _, inv := range invList {
		switch inv.Status {
		case model.InvestigationOpen, model.InvestigationRootCaused:
			invSummary.Open++
		case model.InvestigationFixed:
			invSummary.AwaitingConfirm++
		}
	}

	report := StatusReport{
		Branch:         branch,
		Backlog:        backlog,
		YourBranch:     yourBranch,
		InProgress:     make([]StatusAssignee, 0, len(assignees)),
		Notes:          SummaryCount{Total: len(noteList), NeedsReview: len(noteReviews)},
		Docs:           SummaryCount{Total: len(docList), NeedsReview: len(docReviews)},
		Logs:           len(logList),
		Investigations: invSummary,
	}
	for _, a := range assignees {
		grp := groups[a]
		staleTasks := make([]StatusTask, len(grp))
		for i, t := range grp {
			staleTasks[i] = StatusTask{Task: t, Stale: isStale(t, now, ttl)}
		}
		report.InProgress = append(report.InProgress, StatusAssignee{Assignee: a, Tasks: staleTasks})
	}
	return report, nil
}
