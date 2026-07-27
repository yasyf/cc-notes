package notes

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/yasyf/cc-notes/model"
)

// StatusReport is the orientation snapshot Status returns. Branch is empty on a
// detached HEAD or the backlog. Backlog and YourBranch are ordered by priority
// then creation time then id; InProgress by assignee then the same task order;
// Runs by start time then runbook then run id.
type StatusReport struct {
	Branch         model.Branch
	Backlog        []StatusBacklogTask
	YourBranch     []model.Task
	InProgress     []StatusAssignee
	Runs           []StatusRun
	Notes          SummaryCount
	Docs           SummaryCount
	Logs           int
	Papercuts      int
	Investigations InvestigationSummary
}

// InvestigationSummary is the orientation count of open investigations: Open
// tallies the still-triaging records (open + root_caused), AwaitingConfirm the
// fixed-but-unconfirmed ones, and OpenFindings the still-undecided suspects
// across both. Terminal records — confirmed, exonerated, abandoned — are
// excluded; only non-terminal investigations need attention.
type InvestigationSummary struct {
	Open            int
	AwaitingConfirm int
	OpenFindings    int
}

// StatusBacklogTask pairs one backlog task with the dependency verdict
// ReadyTasks computes: Ready is true when the task is claimable right now —
// open, unheld, and every blocker done or cancelled — and false when a live
// blocker or an existing hold keeps it off the ready set.
type StatusBacklogTask struct {
	Task  model.Task
	Ready bool
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

// StatusRun is one runbook run still in flight, paired with the runbook that
// owns it — a run id is unique only within its runbook — and the same
// lease-style verdict an in-progress task carries: Stale is true when the run
// has been idle longer than the lease TTL, measured from its most recent step
// result or, when no step has landed, from its start.
type StatusRun struct {
	Runbook model.EntityID
	Title   string
	Run     model.RunbookRun
	Stale   bool
}

// SummaryCount summarizes a note or doc set: the total live entities and the
// count needing review.
type SummaryCount struct {
	Total       int
	NeedsReview int
}

// Status aggregates the orientation view in one fold per entity kind. The
// current branch degrades to empty on a detached HEAD.
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
	ready, err := c.ReadyTasks(ctx, ScopeBacklog, "")
	if err != nil {
		return StatusReport{}, err
	}
	runbooks, err := c.s.ListRunbooks(ctx)
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

	readySet := make(map[model.EntityID]bool, len(ready))
	for _, t := range ready {
		readySet[t.ID] = true
	}

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
		if !nonTerminalInvestigation(inv.Status) {
			continue
		}
		switch inv.Status {
		case model.InvestigationFixed:
			invSummary.AwaitingConfirm++
		default:
			invSummary.Open++
		}
		for _, f := range inv.Findings {
			if f.Status == model.FindingOpen {
				invSummary.OpenFindings++
			}
		}
	}

	papercuts := 0
	for _, l := range logList {
		if slices.Contains(l.Tags, PapercutTag) {
			papercuts += len(l.Entries)
		}
	}

	report := StatusReport{
		Branch:         branch,
		Backlog:        make([]StatusBacklogTask, len(backlog)),
		YourBranch:     yourBranch,
		InProgress:     make([]StatusAssignee, 0, len(assignees)),
		Runs:           inFlightRuns(runbooks, now, ttl),
		Notes:          SummaryCount{Total: len(noteList), NeedsReview: len(noteReviews)},
		Docs:           SummaryCount{Total: len(docList), NeedsReview: len(docReviews)},
		Logs:           len(logList),
		Papercuts:      papercuts,
		Investigations: invSummary,
	}
	for i, t := range backlog {
		report.Backlog[i] = StatusBacklogTask{Task: t, Ready: readySet[t.ID]}
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

// inFlightRuns collects every still-running run across runbooks with its
// lease-style stale verdict, oldest start first, ties broken by runbook then run
// id.
func inFlightRuns(runbooks []model.Runbook, now time.Time, ttl time.Duration) []StatusRun {
	var runs []StatusRun
	for _, rb := range runbooks {
		for _, run := range rb.Runs {
			if run.Status != model.RunRunning {
				continue
			}
			runs = append(runs, StatusRun{Runbook: rb.ID, Title: rb.Title, Run: run, Stale: runStale(run, now, ttl)})
		}
	}
	slices.SortFunc(runs, func(a, b StatusRun) int {
		if c := cmp.Compare(a.Run.StartedAt, b.Run.StartedAt); c != 0 {
			return c
		}
		if c := cmp.Compare(a.Runbook, b.Runbook); c != 0 {
			return c
		}
		return cmp.Compare(a.Run.ID, b.Run.ID)
	})
	return runs
}

// runStale reports a run idle past ttl, measured from its most recent step
// result or, when no step has landed, from its start — the run-side analogue of
// a task's lease heartbeat.
func runStale(run model.RunbookRun, now time.Time, ttl time.Duration) bool {
	last := run.StartedAt
	for _, r := range run.Results {
		if r.TS > last {
			last = r.TS
		}
	}
	return now.Sub(time.Unix(last, 0)) > ttl
}
