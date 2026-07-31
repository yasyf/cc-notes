package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func newStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"board"},
		Short:   "Orient: the backlog by readiness, your branch, in-progress leases, runs in flight, and record counts",
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := openClient(cmd)
			if err != nil {
				return err
			}
			report, err := c.Status(ctx)
			if err != nil {
				return err
			}
			if jsonOut {
				return printStatusJSON(cmd, c, report)
			}
			return printStatusText(cmd, report)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func printStatusText(cmd *cobra.Command, report notes.StatusReport) error {
	var b strings.Builder
	b.WriteString("backlog\n")
	for _, bt := range report.Backlog {
		fmt.Fprintf(&b, "  %s\t%s\n", leanTaskLine(bt.Task), readyFlag(bt.Ready))
	}
	if report.Branch != "" {
		fmt.Fprintf(&b, "your branch (%s)\n", report.Branch)
		for _, t := range report.YourBranch {
			fmt.Fprintf(&b, "  %s\n", leanTaskLine(t))
		}
	}
	b.WriteString("in progress across branches\n")
	for _, grp := range report.InProgress {
		for _, st := range grp.Tasks {
			fmt.Fprintf(&b, "  %s\t%s\t%s\n", grp.Assignee, st.Task.ID.Short(), staleFlag(st.Stale))
		}
	}
	b.WriteString("runs in flight\n")
	for _, r := range report.Runs {
		fmt.Fprintf(&b, "  %s\t%s\t%s\t%s\t%s\n", r.Runbook.Short(), render.ShortWireID(r.Run.ID), r.Title, r.Run.Runner, staleFlag(r.Stale))
	}
	fmt.Fprintf(&b, "notes: %d total, %d need review\n", report.Notes.Total, report.Notes.NeedsReview)
	fmt.Fprintf(&b, "docs: %d total, %d need review\n", report.Docs.Total, report.Docs.NeedsReview)
	fmt.Fprintf(&b, "logs: %d total\n", report.Logs)
	fmt.Fprintf(&b, "papercuts: %d total\n", report.Papercuts)
	fmt.Fprintf(&b, "investigations: %d open, %d awaiting confirmation, %d open findings\n",
		report.Investigations.Open, report.Investigations.AwaitingConfirm, report.Investigations.OpenFindings)
	fmt.Fprintf(&b, "plans: %d in flight\n", report.Plans)
	if report.SkippedOps > 0 {
		fmt.Fprintf(&b, "skipped %d op(s) this cc-notes cannot fold; %s\n", report.SkippedOps, UpgradeRemedy)
	}
	_, err := fmt.Fprint(cmd.OutOrStdout(), b.String())
	return err
}

// readyFlag renders a backlog task's dependency verdict as the board's
// ready/blocked column.
func readyFlag(ready bool) string {
	if ready {
		return "ready"
	}
	return "blocked"
}

// staleFlag renders a lease verdict as the board's fresh/STALE column.
func staleFlag(stale bool) string {
	if stale {
		return "STALE"
	}
	return "fresh"
}

func printStatusJSON(cmd *cobra.Command, c *notes.Client, report notes.StatusReport) error {
	blocks, err := c.TasksBlockingIndex(cmd.Context())
	if err != nil {
		return err
	}
	dto := statusDTO{
		Branch:     string(report.Branch),
		Backlog:    statusBacklogDTOs(report.Backlog, blocks),
		YourBranch: taskSummaryDTOs(report.YourBranch, blocks),
		Runs:       make([]statusRunDTO, 0, len(report.Runs)),
		Notes:      statusNotesDTO{Total: report.Notes.Total, NeedsReview: report.Notes.NeedsReview},
		Docs:       statusNotesDTO{Total: report.Docs.Total, NeedsReview: report.Docs.NeedsReview},
		Logs:       statusLogsDTO{Total: report.Logs},
		Papercuts:  statusLogsDTO{Total: report.Papercuts},
		Investigations: statusInvestigationsDTO{
			Open:            report.Investigations.Open,
			AwaitingConfirm: report.Investigations.AwaitingConfirm,
			OpenFindings:    report.Investigations.OpenFindings,
		},
		Plans:      statusPlansDTO{InFlight: report.Plans},
		SkippedOps: report.SkippedOps,
	}
	for _, grp := range report.InProgress {
		staleDTOs := make([]statusStaleDTO, len(grp.Tasks))
		for i, st := range grp.Tasks {
			staleDTOs[i] = statusStaleDTO{taskSummaryDTO: newTaskSummaryDTO(st.Task, blocks[st.Task.ID]), Stale: st.Stale}
		}
		dto.InProgress = append(dto.InProgress, statusAssigneeDTO{Assignee: string(grp.Assignee), Tasks: staleDTOs})
	}
	for _, r := range report.Runs {
		dto.Runs = append(dto.Runs, statusRunDTO{
			Runbook:   string(r.Runbook),
			Title:     r.Title,
			Run:       r.Run.ID,
			Runner:    string(r.Run.Runner),
			StartedAt: render.RFC3339(r.Run.StartedAt),
			Stale:     r.Stale,
		})
	}
	return printJSON(cmd.OutOrStdout(), dto)
}

// taskSummaryDTOs maps tasks to their JSON summary DTOs against one
// TasksBlockingIndex pass, nil when there are none.
func taskSummaryDTOs(tasks []model.Task, blocks map[model.EntityID][]model.EntityID) []taskSummaryDTO {
	dtos := make([]taskSummaryDTO, 0, len(tasks))
	for _, t := range tasks {
		dtos = append(dtos, newTaskSummaryDTO(t, blocks[t.ID]))
	}
	return dtos
}

// statusBacklogDTOs maps backlog rows to their JSON DTOs against the same
// TasksBlockingIndex pass every other status slice reads.
func statusBacklogDTOs(backlog []notes.StatusBacklogTask, blocks map[model.EntityID][]model.EntityID) []statusBacklogDTO {
	dtos := make([]statusBacklogDTO, 0, len(backlog))
	for _, bt := range backlog {
		dtos = append(dtos, statusBacklogDTO{taskSummaryDTO: newTaskSummaryDTO(bt.Task, blocks[bt.Task.ID]), Ready: bt.Ready})
	}
	return dtos
}

// statusDTO fixes the JSON field order for a status report: the current
// branch, the backlog and your-branch task slices, the in-progress tasks
// grouped by assignee, the runs in flight, the note, doc, log, papercut,
// and investigation summaries, and the skipped-op count.
type statusDTO struct {
	Branch         string                  `json:"branch"`
	Backlog        []statusBacklogDTO      `json:"backlog,omitempty"`
	YourBranch     []taskSummaryDTO        `json:"your_branch,omitempty"`
	InProgress     []statusAssigneeDTO     `json:"in_progress,omitempty"`
	Runs           []statusRunDTO          `json:"runs,omitempty"`
	Notes          statusNotesDTO          `json:"notes"`
	Docs           statusNotesDTO          `json:"docs"`
	Logs           statusLogsDTO           `json:"logs"`
	Papercuts      statusLogsDTO           `json:"papercuts"`
	Investigations statusInvestigationsDTO `json:"investigations"`
	Plans          statusPlansDTO          `json:"plans"`
	SkippedOps     int                     `json:"skipped_ops"`
}

// statusBacklogDTO embeds a taskSummaryDTO, inlining its fields, plus the
// dependency verdict: whether the task is claimable right now.
type statusBacklogDTO struct {
	taskSummaryDTO
	Ready bool `json:"ready"`
}

// statusRunDTO is one runbook run still in flight: the owning runbook and its
// title, the run's identity and runner, its RFC3339 UTC start, and the
// lease-style stale verdict.
type statusRunDTO struct {
	Runbook   string `json:"runbook"`
	Title     string `json:"title"`
	Run       string `json:"run"`
	Runner    string `json:"runner"`
	StartedAt string `json:"started_at"`
	Stale     bool   `json:"stale"`
}

// statusAssigneeDTO groups one assignee's in-progress tasks.
type statusAssigneeDTO struct {
	Assignee string           `json:"assignee"`
	Tasks    []statusStaleDTO `json:"tasks"`
}

// statusStaleDTO embeds a taskSummaryDTO, inlining its fields, plus the
// reader-side stale verdict.
type statusStaleDTO struct {
	taskSummaryDTO
	Stale bool `json:"stale"`
}

// statusNotesDTO is the note summary: total notes and the count needing review.
type statusNotesDTO struct {
	Total       int `json:"total"`
	NeedsReview int `json:"needs_review"`
}

// statusLogsDTO is the log and papercut summary: a bare total. Neither carries
// a freshness lifecycle, so there is no needs_review count.
type statusLogsDTO struct {
	Total int `json:"total"`
}

// statusPlansDTO is the in-flight-plan summary: the plans still draft,
// approved, or executing. A plan goes out of date through its status machine
// rather than a freshness lifecycle, so there is no needs_review count.
type statusPlansDTO struct {
	InFlight int `json:"in_flight"`
}

// statusInvestigationsDTO is the active-investigation summary.
type statusInvestigationsDTO struct {
	Open            int `json:"open"`
	AwaitingConfirm int `json:"awaiting_confirm"`
	OpenFindings    int `json:"open_findings"`
}
