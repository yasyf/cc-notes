package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

func newProjectCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Projects: long-lived groupings of sprints and tasks",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newProjectAddCmd(),
		newProjectListCmd(),
		newProjectShowCmd(),
		newProjectStatusCmd("complete", model.ProjectCompleted),
		newProjectStatusCmd("archive", model.ProjectArchived),
		newProjectStatusCmd("cancel", model.ProjectCancelled),
		newProjectEditCmd(),
		newProjectCommentCmd(),
		newProjectHistoryCmd(),
	)
	return cmd
}

func newProjectAddCmd() *cobra.Command {
	var body string
	var labels []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add TITLE",
		Short: "Create a project",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTitle(args[0], titleHintDesc); err != nil {
				return err
			}
			text, err := bodyArg(cmd, body)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			project, reused, err := c.CreateProject(ctx, notes.ProjectSpec{Title: args[0], Description: text, Labels: labels})
			if err != nil {
				return err
			}
			if reused {
				warnDuplicate(cmd, "project", project.ID)
			}
			return printProject(cmd, s, project, jsonOut)
		},
	}
	flags := cmd.Flags()
	bindBody(flags, &body, "project description; - reads stdin")
	flags.StringArrayVar(&labels, "label", nil, "label (repeatable)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newProjectListCmd() *cobra.Command {
	var statusCSV string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List projects",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			var statuses []model.ProjectStatus
			if cmd.Flags().Changed("status") {
				for _, part := range strings.Split(statusCSV, ",") {
					status, err := parseProjectStatus(part)
					if err != nil {
						return err
					}
					statuses = append(statuses, status)
				}
			}
			projects, err := c.Projects(ctx, notes.ProjectFilter{Statuses: statuses})
			if err != nil {
				return err
			}
			return printProjectList(cmd, s, projects, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&statusCSV, "status", "", "status filter, comma-separated (default all)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newProjectShowCmd() *cobra.Command {
	return projectSpec.showVerb("Show one project", showProject)
}

func newProjectStatusCmd(use string, status model.ProjectStatus) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   use + " ID",
		Short: "Mark a project " + string(status),
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveProject(ctx, args[0])
			if err != nil {
				return err
			}
			var project model.Project
			switch status {
			case model.ProjectCompleted:
				project, err = c.CompleteProject(ctx, id)
			case model.ProjectArchived:
				project, err = c.ArchiveProject(ctx, id)
			case model.ProjectCancelled:
				project, err = c.CancelProject(ctx, id)
			}
			if err != nil {
				return planningErr(err)
			}
			return printProject(cmd, s, project, jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newProjectEditCmd() *cobra.Command {
	var title, body string
	var addLabels, rmLabels []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Edit a project without transition checks",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			flags := cmd.Flags()
			var edit notes.ProjectEdit
			if flags.Changed("title") {
				if err := validateTitle(title, titleHintDesc); err != nil {
					return err
				}
				edit.Title = &title
			}
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if flags.Changed("body") {
				text, err := bodyArg(cmd, body)
				if err != nil {
					return err
				}
				edit.Description = &text
			}
			edit.AddLabels, edit.RemoveLabels = addLabels, rmLabels
			if projectEditEmpty(edit) {
				return &UsageError{Err: errors.New("project edit requires at least one flag")}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveProject(ctx, args[0])
			if err != nil {
				return err
			}
			project, err := c.EditProject(ctx, id, edit)
			if err != nil {
				return planningErr(err)
			}
			return printProject(cmd, s, project, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&title, "title", "", "new title")
	bindBody(flags, &body, "new description; - reads stdin")
	flags.StringArrayVar(&addLabels, "add-label", nil, "add label (repeatable)")
	flags.StringArrayVar(&rmLabels, "rm-label", nil, "remove label (repeatable)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newProjectCommentCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "comment ID BODY",
		Short: "Append a comment; BODY - reads stdin",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			body, err := bodyArg(cmd, args[1])
			if err != nil {
				return err
			}
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveProject(ctx, args[0])
			if err != nil {
				return err
			}
			project, err := c.CommentProject(ctx, id, body)
			if err != nil {
				return planningErr(err)
			}
			return printProject(cmd, s, project, jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

// projectEditEmpty reports whether a project edit mask sets nothing, the CLI's
// own arity guard mapping an empty edit to a usage error before the notes layer.
func projectEditEmpty(e notes.ProjectEdit) bool {
	return e.Title == nil && e.Description == nil && len(e.AddLabels) == 0 && len(e.RemoveLabels) == 0
}

// printProjectList writes projects as a JSON array of their DTOs — each carrying
// its reverse-index sprint and task ids — or one lean line per project.
func printProjectList(cmd *cobra.Command, s *store.Store, projects []model.Project, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		ctx := cmd.Context()
		sprints, err := s.ListSprints(ctx)
		if err != nil {
			return err
		}
		tasks, err := s.ListTasks(ctx)
		if err != nil {
			return err
		}
		dtos := make([]projectDTO, len(projects))
		for i, p := range projects {
			dtos[i] = newProjectDTO(p, sprintsInProject(sprints, p.ID), tasksInProject(tasks, sprints, p.ID))
		}
		return printJSON(out, dtos)
	}
	for _, p := range projects {
		if _, err := fmt.Fprintln(out, leanProjectLine(p)); err != nil {
			return err
		}
	}
	return nil
}
