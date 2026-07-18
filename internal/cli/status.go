package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func newStatusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:     "status",
		Aliases: []string{"board"},
		Short:   "Orient: the backlog, your branch, in-progress across branches, notes, and investigations",
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			c, err := openClient()
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
	for _, t := range report.Backlog {
		fmt.Fprintf(&b, "  %s\n", leanTaskLine(t))
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
			flag := "fresh"
			if st.Stale {
				flag = "STALE"
			}
			fmt.Fprintf(&b, "  %s\t%s\t%s\n", grp.Assignee, st.Task.ID.Short(), flag)
		}
	}
	fmt.Fprintf(&b, "notes: %d total, %d need review\n", report.Notes.Total, report.Notes.NeedsReview)
	fmt.Fprintf(&b, "docs: %d total, %d need review\n", report.Docs.Total, report.Docs.NeedsReview)
	fmt.Fprintf(&b, "logs: %d total\n", report.Logs)
	fmt.Fprintf(&b, "investigations: %d open, %d awaiting confirmation\n", report.Investigations.Open, report.Investigations.AwaitingConfirm)
	_, err := fmt.Fprint(cmd.OutOrStdout(), b.String())
	return err
}

func printStatusJSON(cmd *cobra.Command, c *notes.Client, report notes.StatusReport) error {
	blocking, err := c.TasksBlockingIndex(cmd.Context())
	if err != nil {
		return err
	}
	dto := statusDTO{
		Branch:     string(report.Branch),
		Backlog:    taskDTOs(report.Backlog, blocking),
		YourBranch: taskDTOs(report.YourBranch, blocking),
		InProgress: make([]statusAssigneeDTO, 0, len(report.InProgress)),
		Notes:      statusNotesDTO{Total: report.Notes.Total, NeedsReview: report.Notes.NeedsReview},
		Docs:       statusNotesDTO{Total: report.Docs.Total, NeedsReview: report.Docs.NeedsReview},
		Logs:       statusLogsDTO{Total: report.Logs},
		Investigations: statusInvestigationsDTO{
			Open:            report.Investigations.Open,
			AwaitingConfirm: report.Investigations.AwaitingConfirm,
		},
	}
	for _, grp := range report.InProgress {
		staleDTOs := make([]statusStaleDTO, len(grp.Tasks))
		for i, st := range grp.Tasks {
			staleDTOs[i] = statusStaleDTO{taskDTO: newTaskDTO(st.Task, blocking[st.Task.ID]), Stale: st.Stale}
		}
		dto.InProgress = append(dto.InProgress, statusAssigneeDTO{Assignee: string(grp.Assignee), Tasks: staleDTOs})
	}
	return printJSON(cmd.OutOrStdout(), dto)
}

// taskDTOs maps tasks to their JSON DTOs, indexing each task's derived blocks
// from the reverse dependency index.
func taskDTOs(tasks []model.Task, blocking map[model.EntityID][]model.EntityID) []taskDTO {
	dtos := make([]taskDTO, len(tasks))
	for i, t := range tasks {
		dtos[i] = newTaskDTO(t, blocking[t.ID])
	}
	return dtos
}

// statusDTO fixes the JSON field order for a status report: the current
// branch, the backlog and your-branch task slices, the in-progress tasks
// grouped by assignee, and the note, doc, log, and investigation summaries.
type statusDTO struct {
	Branch         string                  `json:"branch"`
	Backlog        []taskDTO               `json:"backlog"`
	YourBranch     []taskDTO               `json:"your_branch"`
	InProgress     []statusAssigneeDTO     `json:"in_progress"`
	Notes          statusNotesDTO          `json:"notes"`
	Docs           statusNotesDTO          `json:"docs"`
	Logs           statusLogsDTO           `json:"logs"`
	Investigations statusInvestigationsDTO `json:"investigations"`
}

// statusAssigneeDTO groups one assignee's in-progress tasks.
type statusAssigneeDTO struct {
	Assignee string           `json:"assignee"`
	Tasks    []statusStaleDTO `json:"tasks"`
}

// statusStaleDTO embeds a taskDTO, inlining its fields, plus the reader-side
// stale verdict.
type statusStaleDTO struct {
	taskDTO
	Stale bool `json:"stale"`
}

// statusNotesDTO is the note summary: total notes and the count needing review.
type statusNotesDTO struct {
	Total       int `json:"total"`
	NeedsReview int `json:"needs_review"`
}

// statusLogsDTO is the log summary: total logs. Logs have no freshness
// lifecycle, so there is no needs_review count.
type statusLogsDTO struct {
	Total int `json:"total"`
}

// statusInvestigationsDTO is the active-investigation summary.
type statusInvestigationsDTO struct {
	Open            int `json:"open"`
	AwaitingConfirm int `json:"awaiting_confirm"`
}
