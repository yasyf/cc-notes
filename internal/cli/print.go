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

// warnDuplicate reports on stderr that Create's best-effort duplicate guard
// reused an existing entity of kind (identified by its short id) instead of
// writing a twin. The caller still emits the reused entity on stdout.
func warnDuplicate(cmd *cobra.Command, kind string, id model.EntityID) {
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: exact duplicate of %s %s; reusing the existing %s (nothing created)\n", kind, id.Short(), kind)
}

// createEntity roots the entity ops describe, warning and returning the existing survivor on a *store.DuplicateError.
func createEntity(ctx context.Context, cmd *cobra.Command, s *store.Store, ops []model.Op) (model.Snapshot, error) {
	snap, err := s.Create(ctx, ops)
	var dup *store.DuplicateError
	if errors.As(err, &dup) {
		warnDuplicate(cmd, string(dup.Kind), dup.Existing.EntityID())
		return dup.Existing, nil
	}
	if err != nil {
		return nil, err
	}
	return snap, nil
}

// printNote writes n as its JSON DTO or its lean line. A mutation echo carries
// no drift verdict.
func printNote(cmd *cobra.Command, c *notes.Client, n model.Note, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanNoteLine(n))
		return err
	}
	infos, err := c.AttachmentInfos(cmd.Context(), n.Attachments)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newNoteDTO(n, "", attachmentInfoDTOs(infos)))
}

// printDoc writes d as its JSON DTO carrying the drift verdict, or its lean
// line. A mutation echo passes an empty drift.
func printDoc(cmd *cobra.Command, c *notes.Client, d model.Doc, drift string, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanDocLine(d))
		return err
	}
	infos, err := c.AttachmentInfos(cmd.Context(), d.Attachments)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newDocDTO(d, drift, attachmentInfoDTOs(infos)))
}

// printLog writes l as its JSON DTO or its lean line.
func printLog(cmd *cobra.Command, c *notes.Client, l model.Log, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanLogLine(l))
		return err
	}
	infos, err := c.AttachmentInfos(cmd.Context(), l.Attachments)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newLogDTO(l, attachmentInfoDTOs(infos)))
}

// printTask writes t as its JSON DTO — with the derived blocks index — or
// its lean line.
func printTask(cmd *cobra.Command, c *notes.Client, t model.Task, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanTaskLine(t))
		return err
	}
	blocks, err := c.TasksBlocking(cmd.Context(), t.ID)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newTaskDTO(t, blocks))
}

// printSprint writes sprint as its JSON DTO — carrying the reverse-index ids of
// its tasks — or its lean line.
func printSprint(cmd *cobra.Command, c *notes.Client, sprint model.Sprint, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanSprintLine(sprint))
		return err
	}
	tasks, err := c.SprintTasks(cmd.Context(), sprint.ID)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newSprintDTO(sprint, tasks))
}

// printProject writes project as its JSON DTO — carrying the reverse-index ids
// of its sprints and tasks — or its lean line.
func printProject(cmd *cobra.Command, c *notes.Client, project model.Project, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanProjectLine(project))
		return err
	}
	ctx := cmd.Context()
	sprints, err := c.ProjectSprints(ctx, project.ID)
	if err != nil {
		return err
	}
	tasks, err := c.ProjectTasks(ctx, project.ID)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newProjectDTO(project, sprints, tasks))
}

// attachmentInfoDTOs maps client attachment-presence infos to the CLI's
// fixed-shape attachment DTOs, always non-nil so JSON serializes an empty list
// rather than null.
func attachmentInfoDTOs(infos []notes.AttachmentInfo) []attachmentDTO {
	out := make([]attachmentDTO, len(infos))
	for i, a := range infos {
		out[i] = attachmentDTO{Name: a.Name, OID: a.OID, Size: a.Size, Present: a.Present}
	}
	return out
}

// printRunbook writes runbook as its JSON DTO or its lean line.
func printRunbook(cmd *cobra.Command, rb model.Runbook, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanRunbookLine(rb))
		return err
	}
	return printJSON(cmd.OutOrStdout(), newRunbookDTO(rb))
}
