package notes

import (
	"cmp"
	"context"
	"slices"
	"time"

	"github.com/yasyf/cc-notes/model"
)

var statusKinds = []model.Kind{
	model.KindTask, model.KindRunbook, model.KindNote, model.KindDoc, model.KindAnswer,
	model.KindLog, model.KindInvestigation, model.KindSprint, model.KindProject, model.KindPlan,
}

// StatusReport is the orientation snapshot Status returns. Branch is empty on a
// detached HEAD or the backlog. Backlog and YourBranch are ordered by priority
// then creation time then id; InProgress by assignee then the same task order;
// Runs by start time then runbook then run id.
//
// Plans counts the plans still in flight — draft, approved, or executing —
// leaving out the done and abandoned ones.
//
// SkippedOps totals the ops this pass could not fold — history a newer
// cc-notes wrote. Every entity kind contributes, over the live records the
// report covers. Non-zero means upgrade.
//
// Blocking is TasksBlockingIndex over the same tasks the buckets come from:
// each task id mapped to the sorted ids of the live tasks it blocks.
type StatusReport struct {
	Branch         model.Branch
	Backlog        []StatusBacklogTask
	YourBranch     []model.Task
	InProgress     []StatusAssignee
	Runs           []StatusRun
	Notes          SummaryCount
	Docs           SummaryCount
	Answers        SummaryCount
	Logs           int
	Papercuts      int
	Investigations InvestigationSummary
	Plans          int
	SkippedOps     int
	Blocking       map[model.EntityID][]model.EntityID
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

// SummaryCount summarizes a note, doc, or answer set: the total live entities and the
// count needing review — every flagged entity except a HISTORY-UNAVAILABLE one,
// whose missing history is not a proven change.
type SummaryCount struct {
	Total       int
	NeedsReview int
}

// Status aggregates the orientation view in one fold per entity kind, every
// kind read from one ref query so the counts describe a single moment. The
// current branch degrades to empty on a detached HEAD. Sprints and projects
// feed nothing but SkippedOps, which states its verdict over the whole
// repository and so cannot scan only the kinds the view itself needs.
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
	pinned, err := c.s.PinnedKinds(ctx, statusKinds...)
	if err != nil {
		return StatusReport{}, err
	}
	v := &Client{s: pinned}
	tasks, err := v.s.ListTasks(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	ready, err := v.readyAmong(ctx, tasks, ScopeBacklog, "")
	if err != nil {
		return StatusReport{}, err
	}
	runbooks, err := v.s.ListRunbooks(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	noteSet, err := v.s.ListNotes(ctx, false, true)
	if err != nil {
		return StatusReport{}, err
	}
	docSet, err := v.s.ListDocs(ctx, false, true)
	if err != nil {
		return StatusReport{}, err
	}
	answerSet, err := v.s.ListAnswers(ctx, false, true)
	if err != nil {
		return StatusReport{}, err
	}
	logList, err := v.s.ListLogs(ctx, false)
	if err != nil {
		return StatusReport{}, err
	}
	invList, err := v.s.ListInvestigations(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	sprintList, err := v.s.ListSprints(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	projectList, err := v.s.ListProjects(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	planList, err := v.s.ListPlans(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	staleAfter, err := v.NoteStaleAfter(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	head, err := v.head(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	judge := memoAncestry(v.judgeVia(v.s.Git.ResolveCommit))
	noteReviews, err := v.reviewNotes(ctx, noteSet, head, staleAfter, judge)
	if err != nil {
		return StatusReport{}, err
	}
	docReviews, err := v.reviewDocs(ctx, docSet, head, staleAfter, judge)
	if err != nil {
		return StatusReport{}, err
	}
	answerReviews, err := v.reviewAnswers(ctx, answerSet, head, staleAfter, judge)
	if err != nil {
		return StatusReport{}, err
	}
	noteList, docList, answerList := unsuperseded(noteSet), unsuperseded(docSet), unsuperseded(answerSet)

	report := StatusReport{
		Branch:         branch,
		Runs:           inFlightRuns(runbooks, now, ttl),
		Notes:          SummaryCount{Total: len(noteList), NeedsReview: needsReview(noteReviews, func(r NoteReview) Verdict { return r.Verdict })},
		Docs:           SummaryCount{Total: len(docList), NeedsReview: needsReview(docReviews, func(r DocReview) Verdict { return r.Verdict })},
		Answers:        SummaryCount{Total: len(answerList), NeedsReview: needsReview(answerReviews, func(r AnswerReview) Verdict { return r.Verdict })},
		Logs:           len(logList),
		Papercuts:      papercutCount(logList),
		Investigations: investigationSummary(invList),
		Plans:          inFlightPlans(planList),
		SkippedOps: sumSkipped(tasks) + sumSkipped(runbooks) + sumSkipped(noteList) +
			sumSkipped(docList) + sumSkipped(logList) + sumSkipped(invList) +
			sumSkipped(sprintList) + sumSkipped(projectList) + sumSkipped(planList) +
			sumSkipped(answerList),
		Blocking: blockingIndex(tasks),
	}
	fillTaskBuckets(&report, tasks, ready, now, ttl)
	return report, nil
}

func needsReview[R any](reviews []R, verdict func(R) Verdict) int {
	n := 0
	for _, r := range reviews {
		if verdict(r) != VerdictHistoryUnavailable {
			n++
		}
	}
	return n
}

// TaskStatus is the task half of Status: the current branch, the backlog with
// each row's readiness, the branch's own tasks, and the in-progress leases with
// their stale verdicts, folding only tasks. It skips every other kind and the
// drift review behind the record counts, so every count and Runs stay zero and
// SkippedOps covers tasks alone.
func (c *Client) TaskStatus(ctx context.Context) (StatusReport, error) {
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
	ready, err := c.readyAmong(ctx, tasks, ScopeBacklog, "")
	if err != nil {
		return StatusReport{}, err
	}
	report := StatusReport{Branch: branch, SkippedOps: sumSkipped(tasks), Blocking: blockingIndex(tasks)}
	fillTaskBuckets(&report, tasks, ready, now, ttl)
	return report, nil
}

func fillTaskBuckets(report *StatusReport, tasks, ready []model.Task, now time.Time, ttl time.Duration) {
	branch := report.Branch
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

	report.Backlog = make([]StatusBacklogTask, len(backlog))
	report.YourBranch = yourBranch
	report.InProgress = make([]StatusAssignee, 0, len(assignees))
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
}

func investigationSummary(invList []model.Investigation) InvestigationSummary {
	var summary InvestigationSummary
	for _, inv := range invList {
		if !nonTerminalInvestigation(inv.Status) {
			continue
		}
		switch inv.Status {
		case model.InvestigationFixed:
			summary.AwaitingConfirm++
		default:
			summary.Open++
		}
		for _, f := range inv.Findings {
			if f.Status == model.FindingOpen {
				summary.OpenFindings++
			}
		}
	}
	return summary
}

func inFlightPlans(planList []model.Plan) int {
	count := 0
	for _, p := range planList {
		if nonTerminalPlan(p.Status) {
			count++
		}
	}
	return count
}

func papercutCount(logList []model.Log) int {
	count := 0
	for _, l := range logList {
		if slices.Contains(l.Tags, PapercutTag) {
			count += len(l.Entries)
		}
	}
	return count
}

func unsuperseded[T model.Snapshot](snaps []T) []T {
	return slices.DeleteFunc(slices.Clone(snaps), func(s T) bool { return s.Meta().Superseded })
}

// sumSkipped totals the ops the fold skipped across snaps.
func sumSkipped[T model.Snapshot](snaps []T) int {
	total := 0
	for _, s := range snaps {
		total += s.Meta().SkippedOps
	}
	return total
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
