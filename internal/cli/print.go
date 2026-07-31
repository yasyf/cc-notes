package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// writeAck carries the per-write facts a summary DTO must not: a create that
// reused a dedupe survivor, and a task claim left without a branch. Printers
// take it as an optional trailing argument; its zero value marshals to nothing,
// so an ordinary ack stays byte-identical to the bare summary.
type writeAck struct {
	Reused    bool  `json:"reused,omitempty"`
	BranchSet *bool `json:"branch_set,omitempty"`
}

// branchAck stamps a claim that left the task without a branch. Absence of the
// "branch" field alone is ambiguous — a backlog task carries none either — so
// the failure is reported, while a claim that set a branch stamps nothing.
func branchAck(branchSet bool) writeAck {
	if branchSet {
		return writeAck{}
	}
	return writeAck{BranchSet: &branchSet}
}

// ackOf collapses a printer's optional trailing write-fact argument.
func ackOf(ack []writeAck) writeAck {
	if len(ack) == 0 {
		return writeAck{}
	}
	return ack[0]
}

// warnDuplicate reports on stderr that Create's best-effort duplicate guard
// reused an existing entity of kind (identified by its short id) instead of
// writing a twin. The caller still emits the reused entity on stdout.
func warnDuplicate(cmd *cobra.Command, kind string, id model.EntityID) {
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: exact duplicate of %s %s; reusing the existing %s (nothing created)\n", kind, id.Short(), kind)
}

// createEntity roots the entity ops describe, warning and returning the existing survivor as reused on a *store.DuplicateError.
func createEntity(ctx context.Context, cmd *cobra.Command, s *store.Store, ops []model.Op) (model.Snapshot, bool, error) {
	snap, err := s.Create(ctx, ops)
	var dup *store.DuplicateError
	if errors.As(err, &dup) {
		warnDuplicate(cmd, string(dup.Kind), dup.Existing.EntityID())
		return dup.Existing, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	return snap, false, nil
}

// The write acks: each embeds the summary DTO its printer emits and stamps the
// write facts onto it, as the show DTOs embed a full DTO to add elision counts.
type (
	noteAckDTO struct {
		noteSummaryDTO
		writeAck
	}
	docAckDTO struct {
		docSummaryDTO
		writeAck
	}
	logAckDTO struct {
		logSummaryDTO
		writeAck
	}
	taskAckDTO struct {
		taskSummaryDTO
		writeAck
	}
	sprintAckDTO struct {
		sprintSummaryDTO
		writeAck
	}
	projectAckDTO struct {
		projectSummaryDTO
		writeAck
	}
	runbookAckDTO struct {
		runbookSummaryDTO
		writeAck
	}
	investigationAckDTO struct {
		investigationSummaryDTO
		writeAck
	}
	planAckDTO struct {
		planSummaryDTO
		writeAck
	}
)

// printNote writes n as its JSON summary DTO — carrying the drift verdict
// computed against live content — or its lean line. The body stays with
// "note show".
func printNote(cmd *cobra.Command, c *notes.Client, n model.Note, jsonOut bool, ack ...writeAck) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanNoteLine(n))
		return err
	}
	ctx := cmd.Context()
	staleAfter, err := c.NoteStaleAfter(ctx)
	if err != nil {
		return err
	}
	verdict, err := c.NoteVerdict(ctx, n, staleAfter, false)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), noteAckDTO{noteSummaryDTO: newNoteSummaryDTO(n, string(verdict)), writeAck: ackOf(ack)})
}

// printDoc writes d as its JSON summary DTO — carrying the drift verdict
// computed against live content — or its lean line. The body stays with
// "doc show".
func printDoc(cmd *cobra.Command, c *notes.Client, d model.Doc, jsonOut bool, ack ...writeAck) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanDocLine(d))
		return err
	}
	ctx := cmd.Context()
	staleAfter, err := c.NoteStaleAfter(ctx)
	if err != nil {
		return err
	}
	verdict, err := c.DocVerdict(ctx, d, staleAfter, false)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), docAckDTO{docSummaryDTO: newDocSummaryDTO(d, string(verdict)), writeAck: ackOf(ack)})
}

// printLog writes l as its JSON summary DTO — carrying the entry tally, not the
// entries — or its lean line.
func printLog(cmd *cobra.Command, _ *notes.Client, l model.Log, jsonOut bool, ack ...writeAck) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanLogLine(l))
		return err
	}
	return printJSON(cmd.OutOrStdout(), logAckDTO{logSummaryDTO: newLogSummaryDTO(l), writeAck: ackOf(ack)})
}

// printTask writes t as its JSON summary DTO — carrying the tasks it blocks,
// resolved against the live listing — or its lean line.
func printTask(cmd *cobra.Command, c *notes.Client, t model.Task, jsonOut bool, ack ...writeAck) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanTaskLine(t))
		return err
	}
	blocks, err := c.TasksBlocking(cmd.Context(), t.ID)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), taskAckDTO{taskSummaryDTO: newTaskSummaryDTO(t, blocks), writeAck: ackOf(ack)})
}

// printSprint writes sprint as its JSON summary DTO or its lean line.
func printSprint(cmd *cobra.Command, _ *notes.Client, sprint model.Sprint, jsonOut bool, ack ...writeAck) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanSprintLine(sprint))
		return err
	}
	return printJSON(cmd.OutOrStdout(), sprintAckDTO{sprintSummaryDTO: newSprintSummaryDTO(sprint), writeAck: ackOf(ack)})
}

// printProject writes project as its JSON summary DTO or its lean line.
func printProject(cmd *cobra.Command, _ *notes.Client, project model.Project, jsonOut bool, ack ...writeAck) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanProjectLine(project))
		return err
	}
	return printJSON(cmd.OutOrStdout(), projectAckDTO{projectSummaryDTO: newProjectSummaryDTO(project), writeAck: ackOf(ack)})
}

// printRunbook writes runbook as its JSON summary DTO — carrying the step and
// run tallies, not the steps or runs — or its lean line.
func printRunbook(cmd *cobra.Command, rb model.Runbook, jsonOut bool, ack ...writeAck) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanRunbookLine(rb))
		return err
	}
	return printJSON(cmd.OutOrStdout(), runbookAckDTO{runbookSummaryDTO: newRunbookSummaryDTO(rb), writeAck: ackOf(ack)})
}

// printInvestigation writes inv as its JSON summary DTO or its structural-status
// lean line.
func printInvestigation(cmd *cobra.Command, _ *notes.Client, inv model.Investigation, jsonOut bool, ack ...writeAck) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanInvestigationLine(inv))
		return err
	}
	return printJSON(cmd.OutOrStdout(), investigationAckDTO{investigationSummaryDTO: newInvestigationSummaryDTO(inv), writeAck: ackOf(ack)})
}

// printPlan writes plan as its JSON summary DTO or its lean line. The recorded
// body, the outcome, and the task roll-up stay with "plan show".
func printPlan(cmd *cobra.Command, _ *notes.Client, plan model.Plan, jsonOut bool, ack ...writeAck) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanPlanLine(plan))
		return err
	}
	return printJSON(cmd.OutOrStdout(), planAckDTO{planSummaryDTO: newPlanSummaryDTO(plan), writeAck: ackOf(ack)})
}
