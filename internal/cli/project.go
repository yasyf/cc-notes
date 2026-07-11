package cli

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
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
	cmd := projectSpec.createVerb("Create a project", &jsonOut, func(_ context.Context, cmd *cobra.Command, _ *store.Store, title string) ([]model.Op, error) {
		text, err := bodyArg(cmd, body)
		if err != nil {
			return nil, err
		}
		return []model.Op{model.CreateProject{
			Nonce:       model.NewNonce(),
			Title:       title,
			Description: text,
			Labels:      labels,
		}}, nil
	})
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
			s, err := openStore()
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
			projects, err := s.ListProjects(ctx)
			if err != nil {
				return err
			}
			if len(statuses) > 0 {
				projects = slices.DeleteFunc(projects, func(p model.Project) bool {
					return !slices.Contains(statuses, p.Status)
				})
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
	return projectSpec.statusVerb(use, string(status), guardProjectTransition, model.SetProjectStatus{Status: status})
}

// guardProjectTransition allows a status change only from active; a project
// already closed refuses further transitions.
func guardProjectTransition(project model.Project) error {
	if project.Status != model.ProjectActive {
		return &ConflictError{Msg: fmt.Sprintf("%s already %s", project.ID.Short(), project.Status)}
	}
	return nil
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
			var ops []model.Op
			if flags.Changed("title") {
				if err := validateTitle(title, titleHintDesc); err != nil {
					return err
				}
				ops = append(ops, model.SetTitle{Title: title})
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			if flags.Changed("body") {
				text, err := bodyArg(cmd, body)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetDescription{Description: text})
			}
			for _, label := range addLabels {
				ops = append(ops, model.AddLabel{Label: label})
			}
			for _, label := range rmLabels {
				ops = append(ops, model.RemoveLabel{Label: label})
			}
			if len(ops) == 0 {
				return &UsageError{Err: errors.New("project edit requires at least one flag")}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, _, err := projectSpec.load(ctx, s, args[0])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, ops)
			if err != nil {
				return err
			}
			return printProject(cmd, s, snapshot.(model.Project), jsonOut)
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
	return projectSpec.commentVerb(nil)
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
