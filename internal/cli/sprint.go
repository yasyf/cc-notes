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
			var projectID model.EntityID
			if project != "" {
				projectID, err = c.ResolveProject(ctx, project)
				if err != nil {
					return err
				}
			}
			spec := notes.SprintSpec{Title: args[0], Description: text, Project: projectID, Labels: labels}
			if cmd.Flags().Changed("start") {
				spec.StartDate, err = parseDate(start)
				if err != nil {
					return err
				}
			}
			if cmd.Flags().Changed("end") {
				spec.EndDate, err = parseDate(end)
				if err != nil {
					return err
				}
			}
			sprint, reused, err := c.CreateSprint(ctx, spec)
			if err != nil {
				return err
			}
			if reused {
				warnDuplicate(cmd, "sprint", sprint.ID)
			}
			return printSprint(cmd, c, sprint, jsonOut)
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
			s, c, err := openStoreClient()
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
				projectID, err = c.ResolveProject(ctx, project)
				if err != nil {
					return err
				}
			}
			sprints, err := c.Sprints(ctx, notes.SprintFilter{Project: projectID, Statuses: statuses})
			if err != nil {
				return err
			}
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
	return sprintSpec.showVerb("Show one sprint", showSprint)
}

func newSprintStatusCmd(use string, status model.SprintStatus) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   use + " ID",
		Short: "Mark a sprint " + string(status),
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
			id, err := c.ResolveSprint(ctx, args[0])
			if err != nil {
				return err
			}
			var sprint model.Sprint
			switch status {
			case model.SprintActive:
				sprint, err = c.ActivateSprint(ctx, id)
			case model.SprintCompleted:
				sprint, err = c.CompleteSprint(ctx, id)
			case model.SprintCancelled:
				sprint, err = c.CancelSprint(ctx, id)
			}
			if err != nil {
				return planningErr(err)
			}
			return printSprint(cmd, c, sprint, jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
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
			var edit notes.SprintEdit
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
			if flags.Changed("project") {
				id, err := c.ResolveProject(ctx, project)
				if err != nil {
					return err
				}
				edit.Project = &id
			}
			if noProject {
				empty := model.EntityID("")
				edit.Project = &empty
			}
			if flags.Changed("start") {
				date, err := parseDate(start)
				if err != nil {
					return err
				}
				edit.StartDate = &date
			}
			if noStart {
				zero := int64(0)
				edit.StartDate = &zero
			}
			if flags.Changed("end") {
				date, err := parseDate(end)
				if err != nil {
					return err
				}
				edit.EndDate = &date
			}
			if noEnd {
				zero := int64(0)
				edit.EndDate = &zero
			}
			edit.AddLabels, edit.RemoveLabels = addLabels, rmLabels
			if sprintEditEmpty(edit) {
				return &UsageError{Err: errors.New("sprint edit requires at least one flag")}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveSprint(ctx, args[0])
			if err != nil {
				return err
			}
			sprint, err := c.EditSprint(ctx, id, edit)
			if err != nil {
				return planningErr(err)
			}
			return printSprint(cmd, c, sprint, jsonOut)
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
			id, err := c.ResolveSprint(ctx, args[0])
			if err != nil {
				return err
			}
			sprint, err := c.CommentSprint(ctx, id, body)
			if err != nil {
				return planningErr(err)
			}
			return printSprint(cmd, c, sprint, jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

// sprintEditEmpty reports whether a sprint edit mask sets nothing, the CLI's own
// arity guard mapping an empty edit to a usage error before the notes layer.
func sprintEditEmpty(e notes.SprintEdit) bool {
	return e.Title == nil && e.Description == nil && e.Project == nil &&
		e.StartDate == nil && e.EndDate == nil &&
		len(e.AddLabels) == 0 && len(e.RemoveLabels) == 0
}

// planningErr maps a *notes.ConflictError from a sprint or project transition to
// the CLI's own *ConflictError, so its stderr bytes match the pre-migration CLI;
// every other error passes through untouched.
func planningErr(err error) error {
	var conflict *notes.ConflictError
	if errors.As(err, &conflict) {
		return &ConflictError{Msg: strings.TrimPrefix(conflict.Error(), "cc-notes: ")}
	}
	return err
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
