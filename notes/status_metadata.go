package notes

import (
	"context"
	"slices"
	"time"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

func (c *Client) statusFromMetadata(ctx context.Context, rows []store.StatusRecord, branch model.Branch, now time.Time, ttl time.Duration) (StatusReport, error) {
	byKind := make(map[model.Kind][]store.StatusRecord, len(statusKinds))
	for _, row := range rows {
		if row.Deleted {
			continue
		}
		byKind[row.Kind] = append(byKind[row.Kind], row)
	}
	report := StatusReport{Branch: branch, Blocking: make(map[model.EntityID][]model.EntityID)}
	states := make(map[model.EntityID]model.Status, len(byKind[model.KindTask]))
	for _, row := range byKind[model.KindTask] {
		states[row.ID] = row.TaskStatus
		for _, dep := range row.BlockedBy {
			report.Blocking[dep] = append(report.Blocking[dep], row.ID)
		}
	}
	for id := range report.Blocking {
		slices.Sort(report.Blocking[id])
	}
	var tasks, ready []model.Task
	for _, row := range byKind[model.KindTask] {
		if row.TaskStatus != model.StatusInProgress &&
			(row.TaskStatus != model.StatusOpen || row.Branch != "" && (branch == "" || row.Branch != branch)) {
			continue
		}
		snapshot, err := c.s.LoadAt(ctx, row.Tip)
		if err != nil {
			return StatusReport{}, err
		}
		task := snapshot.(model.Task)
		tasks = append(tasks, task)
		if row.Branch == "" && row.TaskStatus == model.StatusOpen && row.Assignee == "" &&
			blockersResolved(row.BlockedBy, func(id model.EntityID) (model.Status, bool) {
				state, ok := states[id]
				return state, ok
			}) {
			ready = append(ready, task)
		}
	}
	var runbooks []model.Runbook
	for _, row := range byKind[model.KindRunbook] {
		if !row.HasRunningRun {
			continue
		}
		snapshot, err := c.s.LoadAt(ctx, row.Tip)
		if err != nil {
			return StatusReport{}, err
		}
		runbooks = append(runbooks, snapshot.(model.Runbook))
	}
	staleAfter, err := c.NoteStaleAfter(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	head, err := c.head(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	judge := memoAncestry(c.judgeVia(c.s.Git.ResolveCommit))
	for _, item := range []struct {
		kind    model.Kind
		summary *SummaryCount
	}{
		{model.KindNote, &report.Notes},
		{model.KindDoc, &report.Docs},
		{model.KindAnswer, &report.Answers},
	} {
		summary, err := c.statusReviewSummary(ctx, byKind[item.kind], head, staleAfter, judge)
		if err != nil {
			return StatusReport{}, err
		}
		*item.summary = summary
	}
	for _, row := range rows {
		if row.Deleted || len(row.SupersededBy) != 0 {
			continue
		}
		report.SkippedOps += row.SkippedOps
		switch row.Kind {
		case model.KindLog:
			report.Logs++
			if slices.Contains(row.Labels, PapercutTag) {
				report.Papercuts += row.LogEntryCount
			}
		case model.KindInvestigation:
			if nonTerminalInvestigation(row.InvestigationStatus) {
				if row.InvestigationStatus == model.InvestigationFixed {
					report.Investigations.AwaitingConfirm++
				} else {
					report.Investigations.Open++
				}
				report.Investigations.OpenFindings += row.OpenFindingCount
			}
		case model.KindPlan:
			if nonTerminalPlan(row.PlanStatus) {
				report.Plans++
			}
		}
	}
	report.Runs = inFlightRuns(runbooks, now, ttl)
	fillTaskBuckets(&report, tasks, ready, now, ttl)
	if err := ctx.Err(); err != nil {
		return StatusReport{}, err
	}
	if err := c.s.CheckRecords(); err != nil {
		return StatusReport{}, err
	}
	return report, nil
}

func (c *Client) statusReviewSummary(ctx context.Context, rows []store.StatusRecord, head model.SHA, staleAfter time.Duration, judge ancestryJudge) (SummaryCount, error) {
	now := time.Now()
	exists := existsSet(rows, func(row store.StatusRecord) model.EntityID { return row.ID })
	var result SummaryCount
	for _, row := range rows {
		if len(row.SupersededBy) > 0 {
			if supersedeDangling(row.SupersededBy, exists) {
				result.NeedsReview++
			}
			continue
		}
		result.Total++
		verdict, err := c.verdictOf(ctx, head, freshDocument{
			Anchors: row.Anchors, Witness: row.Witness, VerifiedAt: row.VerifiedAt,
			StaleAt: row.StaleAt, SupersededBy: row.SupersededBy,
		}, now, staleAfter, false, judge)
		if err != nil {
			return SummaryCount{}, err
		}
		if verdict != "" && verdict != VerdictHistoryUnavailable {
			result.NeedsReview++
		}
	}
	return result, nil
}
