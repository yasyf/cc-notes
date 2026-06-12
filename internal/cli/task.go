package cli

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
)

var allStatuses = []model.Status{model.StatusOpen, model.StatusInProgress, model.StatusDone, model.StatusCancelled}

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Branch-scoped tasks with claiming, deps, and lifecycle",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newTaskAddCmd(),
		newTaskListCmd(),
		newTaskReadyCmd(),
		newTaskShowCmd(),
		newTaskClaimCmd(),
		newTaskStatusCmd("done", model.StatusDone),
		newTaskStatusCmd("cancel", model.StatusCancelled),
		newTaskEditCmd(),
		newTaskCommentCmd(),
		newTaskDepCmd(),
		newTaskUndepCmd(),
		newTaskPromoteCmd(),
	)
	return cmd
}

func newTaskAddCmd() *cobra.Command {
	var desc, taskType, parent, branch string
	var priority int
	var labels, blockedBy []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add TITLE",
		Short: "Create a task",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			b, err := resolveBranch(ctx, s, branch)
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, s.Git); err != nil {
				return err
			}
			text, err := bodyArg(cmd, desc)
			if err != nil {
				return err
			}
			tt, err := parseTaskType(taskType)
			if err != nil {
				return err
			}
			p, err := validatePriority(priority)
			if err != nil {
				return err
			}
			var parentID model.EntityID
			if parent != "" {
				_, parentTask, err := loadTask(ctx, s, b, parent)
				if err != nil {
					return err
				}
				parentID = parentTask.ID
			}
			ops := []model.Op{model.CreateTask{
				Nonce:       model.NewNonce(),
				Title:       args[0],
				Description: text,
				Type:        tt,
				Priority:    p,
				Branch:      b,
				Parent:      parentID,
				Labels:      labels,
			}}
			for _, prefix := range blockedBy {
				blocker, _, err := resolveBlocker(ctx, s, prefix)
				if err != nil {
					return err
				}
				ops = append(ops, model.AddDep{ID: blocker})
			}
			snapshot, err := s.Create(ctx, ops)
			if err != nil {
				return err
			}
			return printTask(cmd, s, snapshot.(model.Task), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&desc, "desc", "", "task description; - reads stdin")
	flags.StringVar(&taskType, "type", "task", "task type (task|bug|epic|question)")
	flags.IntVar(&priority, "priority", 2, "priority 0-3 (0 most urgent)")
	flags.StringArrayVar(&labels, "label", nil, "label (repeatable)")
	flags.StringVar(&parent, "parent", "", "parent task id prefix")
	flags.StringArrayVar(&blockedBy, "blocked-by", nil, "blocker task id prefix (repeatable, resolved globally)")
	flags.StringVar(&branch, "branch", "", "task branch (default: current branch)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskListCmd() *cobra.Command {
	var statusCSV, assignee, taskType, branch string
	var labels []string
	var all, jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tasks on a branch",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			b, err := resolveBranch(ctx, s, branch)
			if err != nil {
				return err
			}
			statuses := []model.Status{model.StatusOpen, model.StatusInProgress}
			switch {
			case all:
				statuses = allStatuses
			case cmd.Flags().Changed("status"):
				statuses = statuses[:0]
				for _, part := range strings.Split(statusCSV, ",") {
					status, err := parseStatus(part)
					if err != nil {
						return err
					}
					statuses = append(statuses, status)
				}
			}
			var tt model.TaskType
			if taskType != "" {
				if tt, err = parseTaskType(taskType); err != nil {
					return err
				}
			}
			tasks, err := s.ListTasks(ctx, b)
			if err != nil {
				return err
			}
			assigneeSet := cmd.Flags().Changed("assignee")
			tasks = slices.DeleteFunc(tasks, func(t model.Task) bool {
				return !slices.Contains(statuses, t.Status) ||
					!hasAll(t.Labels, labels) ||
					(assigneeSet && string(t.Assignee) != assignee) ||
					(taskType != "" && t.Type != tt)
			})
			sortTasks(tasks)
			return printTaskList(cmd, s, tasks, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&statusCSV, "status", "", "status filter, comma-separated (default open,in_progress)")
	flags.BoolVar(&all, "all", false, "every status")
	flags.StringArrayVar(&labels, "label", nil, "require label (repeatable, ANDed)")
	flags.StringVar(&assignee, "assignee", "", "require assignee")
	flags.StringVar(&taskType, "type", "", "require type")
	flags.StringVar(&branch, "branch", "", "branch (default: current branch)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskReadyCmd() *cobra.Command {
	var branch string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "ready",
		Short: "List unblocked, unassigned open tasks",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			b, err := resolveBranch(ctx, s, branch)
			if err != nil {
				return err
			}
			tasks, err := s.ListTasks(ctx, b)
			if err != nil {
				return err
			}
			live, err := liveTasks(ctx, s)
			if err != nil {
				return err
			}
			tasks = slices.DeleteFunc(tasks, func(t model.Task) bool {
				return t.Status != model.StatusOpen || t.Assignee != "" || !unblocked(live, t)
			})
			sortTasks(tasks)
			return printTaskList(cmd, s, tasks, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&branch, "branch", "", "branch (default: current branch)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskShowCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show ID",
		Short: "Show one task",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			b, err := resolveBranch(ctx, s, "")
			if err != nil {
				return err
			}
			_, task, err := loadTask(ctx, s, b, args[0])
			if err != nil {
				return err
			}
			live, err := liveTasks(ctx, s)
			if err != nil {
				return err
			}
			blocks := blocksFor(live, task.ID)
			if jsonOut {
				return printJSON(cmd.OutOrStdout(), newTaskDTO(task, blocks))
			}
			_, err = fmt.Fprint(cmd.OutOrStdout(), renderTaskShow(task, blocks))
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskClaimCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "claim ID",
		Short: "Claim an open, unassigned task",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			b, err := resolveBranch(ctx, s, "")
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, s.Git); err != nil {
				return err
			}
			ref, task, err := loadTask(ctx, s, b, args[0])
			if err != nil {
				return err
			}
			if err := claimable(task); err != nil {
				return err
			}
			actor, err := s.Actor(ctx)
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.Claim{Assignee: actor}})
			if err != nil {
				return err
			}
			task = snapshot.(model.Task)
			if task.Assignee != actor {
				return &ConflictError{Msg: fmt.Sprintf("%s already claimed by %s (%s)", task.ID.Short(), task.Assignee, task.Status)}
			}
			return printTask(cmd, s, task, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskStatusCmd(use string, status model.Status) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   use + " ID",
		Short: "Mark a task " + string(status),
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			b, err := resolveBranch(ctx, s, "")
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, s.Git); err != nil {
				return err
			}
			ref, task, err := loadTask(ctx, s, b, args[0])
			if err != nil {
				return err
			}
			switch task.Status {
			case model.StatusOpen, model.StatusInProgress:
			default:
				return &ConflictError{Msg: fmt.Sprintf("%s already %s", task.ID.Short(), task.Status)}
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.SetStatus{Status: status}})
			if err != nil {
				return err
			}
			return printTask(cmd, s, snapshot.(model.Task), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskEditCmd() *cobra.Command {
	var title, desc, taskType, status, assignee, parent string
	var priority int
	var unassign, noParent bool
	var addLabels, rmLabels []string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Edit a task without transition checks",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			flags := cmd.Flags()
			if flags.Changed("assignee") && unassign {
				return &UsageError{Err: errors.New("--assignee and --unassign are mutually exclusive")}
			}
			if flags.Changed("parent") && noParent {
				return &UsageError{Err: errors.New("--parent and --no-parent are mutually exclusive")}
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			b, err := resolveBranch(ctx, s, "")
			if err != nil {
				return err
			}
			var ops []model.Op
			if flags.Changed("title") {
				ops = append(ops, model.SetTitle{Title: title})
			}
			if flags.Changed("desc") {
				text, err := bodyArg(cmd, desc)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetDescription{Description: text})
			}
			if flags.Changed("type") {
				tt, err := parseTaskType(taskType)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetType{Type: tt})
			}
			if flags.Changed("priority") {
				p, err := validatePriority(priority)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetPriority{Priority: p})
			}
			if flags.Changed("status") {
				st, err := parseStatus(status)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetStatus{Status: st})
			}
			if flags.Changed("assignee") {
				ops = append(ops, model.SetAssignee{Assignee: model.Actor(assignee)})
			}
			if unassign {
				ops = append(ops, model.SetAssignee{})
			}
			for _, label := range addLabels {
				ops = append(ops, model.AddLabel{Label: label})
			}
			for _, label := range rmLabels {
				ops = append(ops, model.RemoveLabel{Label: label})
			}
			if flags.Changed("parent") {
				_, parentTask, err := loadTask(ctx, s, b, parent)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetParent{Parent: parentTask.ID})
			}
			if noParent {
				ops = append(ops, model.SetParent{})
			}
			if len(ops) == 0 {
				return &UsageError{Err: errors.New("task edit requires at least one flag")}
			}
			if err := autoInstall(ctx, s.Git); err != nil {
				return err
			}
			ref, _, err := loadTask(ctx, s, b, args[0])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, ops)
			if err != nil {
				return err
			}
			return printTask(cmd, s, snapshot.(model.Task), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&title, "title", "", "new title")
	flags.StringVar(&desc, "desc", "", "new description; - reads stdin")
	flags.StringVar(&taskType, "type", "", "new type (task|bug|epic|question)")
	flags.IntVar(&priority, "priority", 0, "new priority 0-3")
	flags.StringVar(&status, "status", "", "new status (open|in_progress|done|cancelled)")
	flags.StringVar(&assignee, "assignee", "", "new assignee")
	flags.BoolVar(&unassign, "unassign", false, "clear the assignee")
	flags.StringArrayVar(&addLabels, "add-label", nil, "add label (repeatable)")
	flags.StringArrayVar(&rmLabels, "rm-label", nil, "remove label (repeatable)")
	flags.StringVar(&parent, "parent", "", "new parent task id prefix")
	flags.BoolVar(&noParent, "no-parent", false, "clear the parent")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskCommentCmd() *cobra.Command {
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
			b, err := resolveBranch(ctx, s, "")
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, s.Git); err != nil {
				return err
			}
			body, err := bodyArg(cmd, args[1])
			if err != nil {
				return err
			}
			ref, _, err := loadTask(ctx, s, b, args[0])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.AddComment{Body: body}})
			if err != nil {
				return err
			}
			return printTask(cmd, s, snapshot.(model.Task), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskDepCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "dep ID BLOCKER",
		Short: "Mark ID blocked by BLOCKER",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			b, err := resolveBranch(ctx, s, "")
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, s.Git); err != nil {
				return err
			}
			ref, task, err := loadTask(ctx, s, b, args[0])
			if err != nil {
				return err
			}
			blocker, live, err := resolveBlocker(ctx, s, args[1])
			if err != nil {
				return err
			}
			if hasPath(live, blocker, task.ID) {
				return fmt.Errorf("dependency cycle: %s already blocks %s", task.ID.Short(), blocker.Short())
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.AddDep{ID: blocker}})
			if err != nil {
				return err
			}
			return printTask(cmd, s, snapshot.(model.Task), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskUndepCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "undep ID BLOCKER",
		Short: "Remove a blocked-by dependency",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			b, err := resolveBranch(ctx, s, "")
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, s.Git); err != nil {
				return err
			}
			ref, _, err := loadTask(ctx, s, b, args[0])
			if err != nil {
				return err
			}
			blocker, _, err := resolveBlocker(ctx, s, args[1])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.RemoveDep{ID: blocker}})
			if err != nil {
				return err
			}
			return printTask(cmd, s, snapshot.(model.Task), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskPromoteCmd() *cobra.Command {
	var to, from string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "promote --to BRANCH [ID]...",
		Short: "Move tasks to another branch namespace",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if to == "" {
				return &UsageError{Err: errors.New("required flag --to not set")}
			}
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := s.Git.CheckRefFormat(ctx, to); err != nil {
				return &UsageError{Err: err}
			}
			fromBranch, err := resolveBranch(ctx, s, from)
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, s.Git); err != nil {
				return err
			}
			var ids []model.EntityID
			if len(args) > 0 {
				for _, prefix := range args {
					_, task, err := loadTask(ctx, s, fromBranch, prefix)
					if err != nil {
						return err
					}
					ids = append(ids, task.ID)
				}
			} else {
				tasks, err := s.ListTasks(ctx, fromBranch)
				if err != nil {
					return err
				}
				for _, task := range tasks {
					if task.Status == model.StatusOpen || task.Status == model.StatusInProgress {
						ids = append(ids, task.ID)
					}
				}
			}
			if err := ccsync.Promote(ctx, s, fromBranch, model.Branch(to), ids); err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				live, err := liveTasks(ctx, s)
				if err != nil {
					return err
				}
				dtos := make([]taskDTO, len(ids))
				for i, id := range ids {
					snapshot, err := s.Load(ctx, refs.Task(model.Branch(to), id))
					if err != nil {
						return err
					}
					dtos[i] = newTaskDTO(snapshot.(model.Task), blocksFor(live, id))
				}
				return printJSON(out, dtos)
			}
			for _, id := range ids {
				snapshot, err := s.Load(ctx, refs.Task(model.Branch(to), id))
				if err != nil {
					return err
				}
				if _, err := fmt.Fprintln(out, leanTaskLine(snapshot.(model.Task))); err != nil {
					return err
				}
			}
			return nil
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&to, "to", "", "destination branch (required)")
	flags.StringVar(&from, "from", "", "source branch (default: current branch)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func printTaskList(cmd *cobra.Command, s *store.Store, tasks []model.Task, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		live, err := liveTasks(cmd.Context(), s)
		if err != nil {
			return err
		}
		dtos := make([]taskDTO, len(tasks))
		for i, t := range tasks {
			dtos[i] = newTaskDTO(t, blocksFor(live, t.ID))
		}
		return printJSON(out, dtos)
	}
	for _, t := range tasks {
		if _, err := fmt.Fprintln(out, leanTaskLine(t)); err != nil {
			return err
		}
	}
	return nil
}

// unblocked reports whether every blocker of t resolves to a live task that
// is done or cancelled. A blocker id matching no live task does not count
// as resolved.
func unblocked(live map[model.EntityID]model.Task, t model.Task) bool {
	for _, dep := range t.BlockedBy {
		blocker, ok := live[dep]
		if !ok {
			return false
		}
		if blocker.Status != model.StatusDone && blocker.Status != model.StatusCancelled {
			return false
		}
	}
	return true
}

// claimable rejects a claim on a task that is assigned or not open.
func claimable(t model.Task) error {
	switch {
	case t.Assignee != "":
		return &ConflictError{Msg: fmt.Sprintf("%s already claimed by %s (%s)", t.ID.Short(), t.Assignee, t.Status)}
	case t.Status != model.StatusOpen:
		return &ConflictError{Msg: fmt.Sprintf("%s not open (%s)", t.ID.Short(), t.Status)}
	default:
		return nil
	}
}
