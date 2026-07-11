package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
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
func printNote(cmd *cobra.Command, s *store.Store, n model.Note, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanNoteLine(n))
		return err
	}
	atts, err := entityAttachments(cmd.Context(), s, n.Attachments)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newNoteDTO(n, "", atts))
}

// printDoc writes d as its JSON DTO carrying the drift verdict, or its lean
// line. A mutation echo passes an empty drift.
func printDoc(cmd *cobra.Command, s *store.Store, d model.Doc, drift string, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanDocLine(d))
		return err
	}
	atts, err := entityAttachments(cmd.Context(), s, d.Attachments)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newDocDTO(d, drift, atts))
}

// printLog writes l as its JSON DTO or its lean line.
func printLog(cmd *cobra.Command, s *store.Store, l model.Log, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanLogLine(l))
		return err
	}
	atts, err := entityAttachments(cmd.Context(), s, l.Attachments)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newLogDTO(l, atts))
}

// printTask writes t as its JSON DTO — with the derived blocks index — or
// its lean line.
func printTask(cmd *cobra.Command, s *store.Store, t model.Task, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanTaskLine(t))
		return err
	}
	live, err := allTasks(cmd.Context(), s)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newTaskDTO(t, blocksFor(live, t.ID)))
}

// printSprint writes sprint as its JSON DTO — carrying the reverse-index ids of
// its tasks — or its lean line.
func printSprint(cmd *cobra.Command, s *store.Store, sprint model.Sprint, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanSprintLine(sprint))
		return err
	}
	tasks, err := s.ListTasks(cmd.Context())
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newSprintDTO(sprint, tasksInSprint(tasks, sprint.ID)))
}

// printProject writes project as its JSON DTO — carrying the reverse-index ids
// of its sprints and tasks — or its lean line.
func printProject(cmd *cobra.Command, s *store.Store, project model.Project, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanProjectLine(project))
		return err
	}
	ctx := cmd.Context()
	sprints, err := s.ListSprints(ctx)
	if err != nil {
		return err
	}
	tasks, err := s.ListTasks(ctx)
	if err != nil {
		return err
	}
	return printJSON(cmd.OutOrStdout(), newProjectDTO(project, sprintsInProject(sprints, project.ID), tasksInProject(tasks, sprints, project.ID)))
}

// printRunbook writes runbook as its JSON DTO or its lean line.
func printRunbook(cmd *cobra.Command, rb model.Runbook, jsonOut bool) error {
	if !jsonOut {
		_, err := fmt.Fprintln(cmd.OutOrStdout(), leanRunbookLine(rb))
		return err
	}
	return printJSON(cmd.OutOrStdout(), newRunbookDTO(rb))
}
