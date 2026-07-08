package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

func newSprintCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sprint",
		Short: "Sprints: time-boxed groupings of tasks, optionally within a project",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newSprintAddCmd(),
		newSprintListCmd(),
		newSprintShowCmd(),
		newSprintStatusCmd("activate", model.SprintActive),
		newSprintStatusCmd("complete", model.SprintCompleted),
		newSprintStatusCmd("cancel", model.SprintCancelled),
		newSprintEditCmd(),
		newSprintCommentCmd(),
		newSprintHistoryCmd(),
	)
	return cmd
}

func newSprintAddCmd() *cobra.Command {
	var body, project, start, end string
	var labels []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add TITLE",
		Short: "Create a sprint",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTitle(args[0], titleHintDesc); err != nil {
				return err
			}
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			text, err := bodyArg(cmd, body)
			if err != nil {
				return err
			}
			var projectID model.EntityID
			if project != "" {
				_, proj, err := loadProject(ctx, s, project)
				if err != nil {
					return err
				}
				projectID = proj.ID
			}
			ops := []model.Op{model.CreateSprint{
				Nonce:       model.NewNonce(),
				Title:       args[0],
				Description: text,
				Project:     projectID,
				Labels:      labels,
			}}
			if cmd.Flags().Changed("start") {
				date, err := parseDate(start)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetStartDate{Date: date})
			}
			if cmd.Flags().Changed("end") {
				date, err := parseDate(end)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetEndDate{Date: date})
			}
			snapshot, deduped, err := s.Create(ctx, ops)
			if err != nil {
				return err
			}
			if deduped {
				warnDuplicate(cmd, "sprint", snapshot.EntityID())
			}
			return printSprint(cmd, s, snapshot.(model.Sprint), jsonOut)
		},
	}
	flags := cmd.Flags()
	bindBody(flags, &body, "sprint description; - reads stdin")
	flags.StringVar(&project, "project", "", "project id prefix")
	flags.StringArrayVar(&labels, "label", nil, "label (repeatable)")
	flags.StringVar(&start, "start", "", "start date YYYY-MM-DD")
	flags.StringVar(&end, "end", "", "end date YYYY-MM-DD")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newSprintListCmd() *cobra.Command {
	var project, statusCSV string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List sprints",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			var statuses []model.SprintStatus
			if cmd.Flags().Changed("status") {
				for _, part := range strings.Split(statusCSV, ",") {
					status, err := parseSprintStatus(part)
					if err != nil {
						return err
					}
					statuses = append(statuses, status)
				}
			}
			var projectID model.EntityID
			if project != "" {
				_, proj, err := loadProject(ctx, s, project)
				if err != nil {
					return err
				}
				projectID = proj.ID
			}
			sprints, err := s.ListSprints(ctx)
			if err != nil {
				return err
			}
			sprints = slices.DeleteFunc(sprints, func(sp model.Sprint) bool {
				return (projectID != "" && sp.Project != projectID) ||
					(len(statuses) > 0 && !slices.Contains(statuses, sp.Status))
			})
			return printSprintList(cmd, s, sprints, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&project, "project", "", "filter to project id prefix")
	flags.StringVar(&statusCSV, "status", "", "status filter, comma-separated (default all)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newSprintShowCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show one sprint",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStore()
			if err != nil {
				return err
			}
			return showSprint(cmd, s, args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newSprintStatusCmd(use string, status model.SprintStatus) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   use + " ID",
		Short: "Mark a sprint " + string(status),
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, sprint, err := loadSprint(ctx, s, args[0])
			if err != nil {
				return err
			}
			switch sprint.Status {
			case model.SprintPlanned, model.SprintActive:
			default:
				return &ConflictError{Msg: fmt.Sprintf("%s already %s", sprint.ID.Short(), sprint.Status)}
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.SetSprintStatus{Status: status}})
			if err != nil {
				return err
			}
			return printSprint(cmd, s, snapshot.(model.Sprint), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newSprintEditCmd() *cobra.Command {
	var title, body, project, start, end string
	var noProject, noStart, noEnd bool
	var addLabels, rmLabels []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Edit a sprint without transition checks",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			flags := cmd.Flags()
			if flags.Changed("project") && noProject {
				return &UsageError{Err: errors.New("--project and --no-project are mutually exclusive")}
			}
			if flags.Changed("start") && noStart {
				return &UsageError{Err: errors.New("--start and --no-start are mutually exclusive")}
			}
			if flags.Changed("end") && noEnd {
				return &UsageError{Err: errors.New("--end and --no-end are mutually exclusive")}
			}
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
			if flags.Changed("project") {
				_, proj, err := loadProject(ctx, s, project)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetProject{Project: proj.ID})
			}
			if noProject {
				ops = append(ops, model.SetProject{})
			}
			if flags.Changed("start") {
				date, err := parseDate(start)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetStartDate{Date: date})
			}
			if noStart {
				ops = append(ops, model.SetStartDate{})
			}
			if flags.Changed("end") {
				date, err := parseDate(end)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetEndDate{Date: date})
			}
			if noEnd {
				ops = append(ops, model.SetEndDate{})
			}
			for _, label := range addLabels {
				ops = append(ops, model.AddLabel{Label: label})
			}
			for _, label := range rmLabels {
				ops = append(ops, model.RemoveLabel{Label: label})
			}
			if len(ops) == 0 {
				return &UsageError{Err: errors.New("sprint edit requires at least one flag")}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, _, err := loadSprint(ctx, s, args[0])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, ops)
			if err != nil {
				return err
			}
			return printSprint(cmd, s, snapshot.(model.Sprint), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&title, "title", "", "new title")
	bindBody(flags, &body, "new description; - reads stdin")
	flags.StringVar(&project, "project", "", "new project id prefix")
	flags.BoolVar(&noProject, "no-project", false, "clear the project")
	flags.StringVar(&start, "start", "", "new start date YYYY-MM-DD")
	flags.BoolVar(&noStart, "no-start", false, "clear the start date")
	flags.StringVar(&end, "end", "", "new end date YYYY-MM-DD")
	flags.BoolVar(&noEnd, "no-end", false, "clear the end date")
	flags.StringArrayVar(&addLabels, "add-label", nil, "add label (repeatable)")
	flags.StringArrayVar(&rmLabels, "rm-label", nil, "remove label (repeatable)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newSprintCommentCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "comment ID BODY",
		Short: "Append a comment; BODY - reads stdin",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			body, err := bodyArg(cmd, args[1])
			if err != nil {
				return err
			}
			ref, _, err := loadSprint(ctx, s, args[0])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.AddComment{Body: body}})
			if err != nil {
				return err
			}
			return printSprint(cmd, s, snapshot.(model.Sprint), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

// printSprintList writes sprints as a JSON array of their DTOs — each carrying
// its reverse-index task ids — or one lean line per sprint.
func printSprintList(cmd *cobra.Command, s *store.Store, sprints []model.Sprint, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		tasks, err := s.ListTasks(cmd.Context())
		if err != nil {
			return err
		}
		dtos := make([]sprintDTO, len(sprints))
		for i, sp := range sprints {
			dtos[i] = newSprintDTO(sp, tasksInSprint(tasks, sp.ID))
		}
		return printJSON(out, dtos)
	}
	for _, sp := range sprints {
		if _, err := fmt.Fprintln(out, leanSprintLine(sp)); err != nil {
			return err
		}
	}
	return nil
}
