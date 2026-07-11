package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
)

var allStatuses = []model.Status{model.StatusOpen, model.StatusInProgress, model.StatusDone, model.StatusCancelled}

func newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task",
		Short: "Tasks with claiming, deps, lifecycle, and a branch attribute",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newTaskAddCmd(),
		newTaskListCmd(),
		newTaskReadyCmd(),
		newTaskBacklogCmd(),
		newTaskShowCmd(),
		newTaskStartCmd(),
		newTaskClaimCmd(),
		newTaskRenewCmd(),
		newTaskDoneCmd(),
		newTaskStatusCmd("cancel", model.StatusCancelled),
		newTaskEditCmd(),
		newTaskCommentCmd(),
		newTaskDepCmd(),
		newTaskUndepCmd(),
		newTaskStaleCmd(),
		newTaskArchivedCmd(),
		newTaskCriterionCmd(),
		newTaskValidateCmd(),
		newTaskHistoryCmd(),
	)
	return cmd
}

func newTaskAddCmd() *cobra.Command {
	var body, taskType, parent, branch, sprint, project string
	var priority int
	var labels, blockedBy, criteria []string
	var backlog, noValidation, jsonOut bool
	cmd := &cobra.Command{
		Use:   "add TITLE",
		Short: "Create a task",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateTitle(args[0], titleHintDesc); err != nil {
				return err
			}
			ctx := cmd.Context()
			if backlog && cmd.Flags().Changed("branch") {
				return &UsageError{Err: errors.New("--backlog and --branch are mutually exclusive")}
			}
			if len(criteria) > 0 && noValidation {
				return &UsageError{Err: errors.New("--criterion and --no-validation-criteria are mutually exclusive")}
			}
			if !noValidation && len(criteria) == 0 {
				return &UsageError{Err: errors.New("at least one --criterion is required (or pass --no-validation-criteria)")}
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			var b model.Branch
			if !backlog {
				if b, err = resolveBranch(ctx, s, "branch", branch); err != nil {
					return err
				}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			text, err := bodyArg(cmd, body)
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
				_, parentTask, err := taskSpec.load(ctx, s, parent)
				if err != nil {
					return err
				}
				parentID = parentTask.ID
			}
			var sprintID model.EntityID
			if sprint != "" {
				_, sp, err := sprintSpec.load(ctx, s, sprint)
				if err != nil {
					return err
				}
				sprintID = sp.ID
			}
			var projectID model.EntityID
			if project != "" {
				_, proj, err := projectSpec.load(ctx, s, project)
				if err != nil {
					return err
				}
				projectID = proj.ID
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
			if sprintID != "" {
				ops = append(ops, model.SetSprint{Sprint: sprintID})
			}
			if projectID != "" {
				ops = append(ops, model.SetProject{Project: projectID})
			}
			for _, c := range criteria {
				ops = append(ops, model.AddCriterion{ID: model.NewNonce(), Text: c})
			}
			for _, prefix := range blockedBy {
				blocker, _, err := resolveBlocker(ctx, s, prefix)
				if err != nil {
					return err
				}
				ops = append(ops, model.AddDep{ID: blocker})
			}
			snapshot, err := createEntity(ctx, cmd, s, ops)
			if err != nil {
				return err
			}
			return printTask(cmd, s, snapshot.(model.Task), jsonOut)
		},
	}
	flags := cmd.Flags()
	bindBody(flags, &body, "task description; - reads stdin")
	flags.StringVar(&taskType, "type", "task", "task type (task|bug|epic|question)")
	flags.IntVar(&priority, "priority", 2, "priority 0-3 (0 most urgent)")
	flags.StringArrayVar(&labels, "label", nil, "label (repeatable)")
	flags.StringArrayVar(&criteria, "criterion", nil, "acceptance criterion text (repeatable, required unless --no-validation-criteria)")
	flags.BoolVar(&noValidation, "no-validation-criteria", false, "create with no acceptance criteria")
	flags.StringVar(&parent, "parent", "", "parent task id prefix")
	flags.StringVar(&sprint, "sprint", "", "sprint id prefix")
	flags.StringVar(&project, "project", "", "project id prefix")
	flags.StringArrayVar(&blockedBy, "blocked-by", nil, "blocker task id prefix (repeatable, resolved globally)")
	flags.StringVar(&branch, "branch", "", "task branch (default: current branch)")
	flags.BoolVar(&backlog, "backlog", false, "create on the backlog (no branch)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskListCmd() *cobra.Command {
	var statusCSV, assignee, taskType, branch string
	var labels []string
	var all, allBranches, backlog, includeArchived, jsonOut bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tasks, scoped to the current branch by default",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			now := time.Now()
			s, err := openStore()
			if err != nil {
				return err
			}
			inBranch, err := branchFilter(ctx, s, cmd, branch, allBranches, backlog)
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
			tasks, err := s.ListTasks(ctx)
			if err != nil {
				return err
			}
			assigneeSet := cmd.Flags().Changed("assignee")
			tasks = slices.DeleteFunc(tasks, func(t model.Task) bool {
				return !inBranch(t) ||
					!slices.Contains(statuses, t.Status) ||
					!hasAll(t.Labels, labels) ||
					(assigneeSet && string(t.Assignee) != assignee) ||
					(taskType != "" && t.Type != tt)
			})
			if !includeArchived {
				cutoff := now.Add(-defaultArchiveAge)
				tasks = slices.DeleteFunc(tasks, func(t model.Task) bool {
					return isArchived(t, cutoff)
				})
			}
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
	flags.StringVar(&branch, "branch", "", "filter to branch (default: current branch)")
	flags.BoolVar(&allBranches, "all-branches", false, "every branch")
	flags.BoolVar(&backlog, "backlog", false, "only backlog tasks (no branch)")
	flags.BoolVar(&includeArchived, "include-archived", false, "include archived (old done/cancelled) tasks")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskReadyCmd() *cobra.Command {
	var branch string
	var allBranches, backlog, jsonOut bool
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
			inBranch, err := branchFilter(ctx, s, cmd, branch, allBranches, backlog)
			if err != nil {
				return err
			}
			tasks, err := s.ListTasks(ctx)
			if err != nil {
				return err
			}
			live, err := allTasks(ctx, s)
			if err != nil {
				return err
			}
			tasks = slices.DeleteFunc(tasks, func(t model.Task) bool {
				return !inBranch(t) || t.Status != model.StatusOpen || t.Assignee != "" || !unblocked(live, t)
			})
			sortTasks(tasks)
			return printTaskList(cmd, s, tasks, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&branch, "branch", "", "filter to branch (default: current branch)")
	flags.BoolVar(&allBranches, "all-branches", false, "every branch")
	flags.BoolVar(&backlog, "backlog", false, "only backlog tasks (no branch)")
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
			s, err := openStore()
			if err != nil {
				return err
			}
			return showTask(cmd, s, args[0], jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskStartCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "start ID",
		Short: "Claim a task and move it onto your current branch",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			headBranch, err := s.Git.HeadBranch(ctx)
			if errors.Is(err, gitcmd.ErrDetachedHead) {
				return errors.New("detached HEAD; cannot start a task here")
			}
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, task, err := taskSpec.load(ctx, s, args[0])
			if err != nil {
				return err
			}
			if err := claimable(task); err != nil {
				return err
			}
			me, err := s.Actor(ctx)
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.Claim{Assignee: me}})
			if err != nil {
				return err
			}
			task = snapshot.(model.Task)
			if task.Assignee != me {
				return &ConflictError{Msg: fmt.Sprintf("%s already claimed by %s (%s)", task.ID.Short(), task.Assignee, task.Status)}
			}
			snapshot, err = s.Append(ctx, ref, []model.Op{model.SetBranch{Branch: headBranch}})
			if err != nil {
				return err
			}
			return printTask(cmd, s, snapshot.(model.Task), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskClaimCmd() *cobra.Command {
	var jsonOut, steal, syncRemote bool
	cmd := &cobra.Command{
		Use:   "claim ID",
		Short: "Claim an open, unassigned task",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if steal && syncRemote {
				return &UsageError{Err: errors.New("--steal and --sync are mutually exclusive")}
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, task, err := taskSpec.load(ctx, s, args[0])
			if err != nil {
				return err
			}
			me, err := s.Actor(ctx)
			if err != nil {
				return err
			}
			if steal && task.Status != model.StatusInProgress {
				return &UsageError{Err: errors.New("--steal requires an in-progress task")}
			}
			if steal && task.Status == model.StatusInProgress {
				return claimSteal(cmd, s, ref, task, me, jsonOut)
			}
			if err := claimable(task); err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.Claim{Assignee: me}})
			if err != nil {
				return err
			}
			task = snapshot.(model.Task)
			if task.Assignee != me {
				return &ConflictError{Msg: fmt.Sprintf("%s already claimed by %s (%s)", task.ID.Short(), task.Assignee, task.Status)}
			}
			if syncRemote {
				return claimSyncYield(cmd, s, task, me, jsonOut)
			}
			return printTask(cmd, s, task, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	flags.BoolVar(&steal, "steal", false, "reclaim an in-progress task whose lease has expired")
	flags.BoolVar(&syncRemote, "sync", false, "claim, then sync and re-check, yielding if another agent won")
	return cmd
}

// claimSteal reclaims an in-progress task whose lease is stale. A fresh lease
// is refused with the remaining time; a holder who renewed past the observed
// heartbeat (or a stealer who lost the race) makes the Reclaim a fold no-op,
// surfaced as a conflict after the reload.
func claimSteal(cmd *cobra.Command, s *store.Store, ref string, task model.Task, me model.Actor, jsonOut bool) error {
	ctx := cmd.Context()
	now := time.Now()
	ttl, err := leaseTTL(ctx, s.Git)
	if err != nil {
		return err
	}
	if !isStale(task, now, ttl) {
		remaining := ttl - now.Sub(time.Unix(taskHeartbeat(task), 0))
		return &ConflictError{Msg: fmt.Sprintf("%s lease held by %s, %s remaining", task.ID.Short(), task.Assignee, remaining.Round(time.Second))}
	}
	snapshot, err := s.Append(ctx, ref, []model.Op{model.Reclaim{Assignee: me, From: task.Assignee, AfterLamport: task.HeartbeatLamport}})
	if err != nil {
		return err
	}
	task = snapshot.(model.Task)
	if task.Assignee != me {
		return &ConflictError{Msg: fmt.Sprintf("%s held by %s (renewed in time or lost steal race)", task.ID.Short(), task.Assignee)}
	}
	return printTask(cmd, s, task, jsonOut)
}

// claimSyncYield syncs the default remote after a local claim, then reloads and
// yields to the remote winner if another agent's claim linearized first.
func claimSyncYield(cmd *cobra.Command, s *store.Store, task model.Task, me model.Actor, jsonOut bool) error {
	ctx := cmd.Context()
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("working directory: %w", err)
	}
	if _, err := ccsync.Sync(ctx, dir, defaultRemote, false); err != nil {
		return err
	}
	if _, task, err = taskSpec.load(ctx, s, string(task.ID)); err != nil {
		return err
	}
	if task.Assignee != me {
		return &ConflictError{Msg: fmt.Sprintf("%s claimed by %s", task.ID.Short(), task.Assignee)}
	}
	return printTask(cmd, s, task, jsonOut)
}

func newTaskRenewCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "renew ID",
		Short: "Refresh the lease heartbeat on a task you hold",
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
			ref, task, err := taskSpec.load(ctx, s, args[0])
			if err != nil {
				return err
			}
			me, err := s.Actor(ctx)
			if err != nil {
				return err
			}
			if task.Assignee != me {
				return &ConflictError{Msg: fmt.Sprintf("%s held by %s, not you", task.ID.Short(), orDash(string(task.Assignee)))}
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.Renew{}})
			if err != nil {
				return err
			}
			return printTask(cmd, s, snapshot.(model.Task), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskDoneCmd() *cobra.Command {
	var force, jsonOut bool
	cmd := &cobra.Command{
		Use:   "done ID",
		Short: "Mark a task done and anchor your HEAD commit onto it",
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
			ref, task, err := taskSpec.load(ctx, s, args[0])
			if err != nil {
				return err
			}
			switch task.Status {
			case model.StatusOpen, model.StatusInProgress:
			default:
				return &ConflictError{Msg: fmt.Sprintf("%s already %s", task.ID.Short(), task.Status)}
			}
			if !force {
				if err := unmetCriteriaErr(task); err != nil {
					return err
				}
			}
			head, err := resolveHead(ctx, s)
			if err != nil {
				return err
			}
			ops := []model.Op{model.SetStatus{Status: model.StatusDone}}
			if head != "" {
				ops = append(ops, model.LinkCommit{SHA: head})
			}
			snapshot, err := s.Append(ctx, ref, ops)
			if err != nil {
				return err
			}
			return printTask(cmd, s, snapshot.(model.Task), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&force, "force", false, "close even with unmet criteria")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

// unmetCriteriaErr returns a UsageError listing every criterion not yet met,
// instructing --force, or nil when every criterion is met. The done gate uses
// it to refuse closing a task whose acceptance criteria have not all passed.
func unmetCriteriaErr(task model.Task) error {
	var unmet []model.Criterion
	for _, c := range task.Criteria {
		if c.Status != model.CriterionMet {
			unmet = append(unmet, c)
		}
	}
	if len(unmet) == 0 {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s has %d unmet criterion/criteria (pass --force to close anyway):", task.ID.Short(), len(unmet))
	for _, c := range unmet {
		fmt.Fprintf(&b, "\n  %s [%s] %s", c.ID[:7], c.Status, sanitizeDisplay(c.Text, false))
	}
	return &UsageError{Err: errors.New(b.String())}
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
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, task, err := taskSpec.load(ctx, s, args[0])
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
	var title, body, taskType, status, assignee, parent, sprint, project string
	var priority int
	var noAssignee, noParent, noSprint, noProject bool
	var addLabels, rmLabels []string
	var target branchTarget
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "edit ID",
		Short: "Edit a task without transition checks",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			flags := cmd.Flags()
			if flags.Changed("assignee") && noAssignee {
				return &UsageError{Err: errors.New("--assignee and --no-assignee are mutually exclusive")}
			}
			if flags.Changed("parent") && noParent {
				return &UsageError{Err: errors.New("--parent and --no-parent are mutually exclusive")}
			}
			if flags.Changed("sprint") && noSprint {
				return &UsageError{Err: errors.New("--sprint and --no-sprint are mutually exclusive")}
			}
			if flags.Changed("project") && noProject {
				return &UsageError{Err: errors.New("--project and --no-project are mutually exclusive")}
			}
			if err := target.validate(flags.Changed("branch")); err != nil {
				return err
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
			if noAssignee {
				ops = append(ops, model.SetAssignee{})
			}
			for _, label := range addLabels {
				ops = append(ops, model.AddLabel{Label: label})
			}
			for _, label := range rmLabels {
				ops = append(ops, model.RemoveLabel{Label: label})
			}
			if flags.Changed("parent") {
				_, parentTask, err := taskSpec.load(ctx, s, parent)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetParent{Parent: parentTask.ID})
			}
			if noParent {
				ops = append(ops, model.SetParent{})
			}
			if flags.Changed("sprint") {
				_, sp, err := sprintSpec.load(ctx, s, sprint)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetSprint{Sprint: sp.ID})
			}
			if noSprint {
				ops = append(ops, model.SetSprint{})
			}
			if flags.Changed("project") {
				_, proj, err := projectSpec.load(ctx, s, project)
				if err != nil {
					return err
				}
				ops = append(ops, model.SetProject{Project: proj.ID})
			}
			if noProject {
				ops = append(ops, model.SetProject{})
			}
			if flags.Changed("branch") {
				if err := s.Git.CheckRefFormat(ctx, target.branch); err != nil {
					return &UsageError{Err: err}
				}
				ops = append(ops, model.SetBranch{Branch: model.Branch(target.branch)})
			}
			if target.backlog {
				ops = append(ops, model.SetBranch{})
			}
			if len(ops) == 0 {
				return &UsageError{Err: errors.New("task edit requires at least one flag")}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, _, err := taskSpec.load(ctx, s, args[0])
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
	bindBody(flags, &body, "new description; - reads stdin")
	flags.StringVar(&taskType, "type", "", "new type (task|bug|epic|question)")
	flags.IntVar(&priority, "priority", 0, "new priority 0-3")
	flags.StringVar(&status, "status", "", "new status (open|in_progress|done|cancelled)")
	flags.StringVar(&assignee, "assignee", "", "new assignee")
	flags.BoolVar(&noAssignee, "no-assignee", false, "clear the assignee")
	flags.StringArrayVar(&addLabels, "add-label", nil, "add label (repeatable)")
	flags.StringArrayVar(&rmLabels, "rm-label", nil, "remove label (repeatable)")
	flags.StringVar(&parent, "parent", "", "new parent task id prefix")
	flags.BoolVar(&noParent, "no-parent", false, "clear the parent")
	flags.StringVar(&sprint, "sprint", "", "new sprint id prefix")
	flags.BoolVar(&noSprint, "no-sprint", false, "clear the sprint")
	flags.StringVar(&project, "project", "", "new project id prefix")
	flags.BoolVar(&noProject, "no-project", false, "clear the project")
	target.bind(flags)
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
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			body, err := bodyArg(cmd, args[1])
			if err != nil {
				return err
			}
			ref, _, err := taskSpec.load(ctx, s, args[0])
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
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, task, err := taskSpec.load(ctx, s, args[0])
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
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, _, err := taskSpec.load(ctx, s, args[0])
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

func newTaskBacklogCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "backlog",
		Short: "List the open, branch-less backlog",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			tasks, err := s.ListTasks(ctx)
			if err != nil {
				return err
			}
			tasks = slices.DeleteFunc(tasks, func(t model.Task) bool {
				return t.Branch != "" || t.Status != model.StatusOpen
			})
			sortTasks(tasks)
			return printTaskList(cmd, s, tasks, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskStaleCmd() *cobra.Command {
	var idleAfter string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "stale",
		Short: "List in-progress tasks idle past the lease threshold",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			now := time.Now()
			s, err := openStore()
			if err != nil {
				return err
			}
			var ttl time.Duration
			if cmd.Flags().Changed("idle-after") {
				if ttl, err = parseDuration(idleAfter); err != nil {
					return err
				}
			} else if ttl, err = leaseTTL(ctx, s.Git); err != nil {
				return err
			}
			tasks, err := s.ListTasks(ctx)
			if err != nil {
				return err
			}
			tasks = slices.DeleteFunc(tasks, func(t model.Task) bool {
				return !isStale(t, now, ttl)
			})
			sortTasks(tasks)
			return printStaleTaskList(cmd, s, tasks, now, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&idleAfter, "idle-after", "", "idle threshold (default cc-notes.leaseTTL / CC_NOTES_LEASE_TTL or 1h)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskArchivedCmd() *cobra.Command {
	var closedBefore string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "archived",
		Short: "List done and cancelled tasks closed before the threshold",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			now := time.Now()
			s, err := openStore()
			if err != nil {
				return err
			}
			cutoff := now.Add(-defaultArchiveAge)
			if cmd.Flags().Changed("closed-before") {
				if cutoff, err = parseWhen(closedBefore, now); err != nil {
					return err
				}
			}
			tasks, err := s.ListTasks(ctx)
			if err != nil {
				return err
			}
			tasks = slices.DeleteFunc(tasks, func(t model.Task) bool {
				return !isArchived(t, cutoff)
			})
			sortTasks(tasks)
			return printTaskList(cmd, s, tasks, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&closedBefore, "closed-before", "", "archive cutoff (Go duration relative, or RFC3339 absolute; default 720h)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskCriterionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "criterion",
		Short: "Structured acceptance criteria on a task",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(
		newCriterionAddCmd(),
		newCriterionRemoveCmd(),
		newCriterionStatusCmd("met", model.CriterionMet),
		newCriterionStatusCmd("failed", model.CriterionFailed),
		newCriterionStatusCmd("pending", model.CriterionPending),
		newCriterionScriptCmd(),
		newCriterionListCmd(),
	)
	return cmd
}

func newCriterionAddCmd() *cobra.Command {
	var script string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "add TASK TEXT",
		Short: "Add an acceptance criterion to a task",
		Args:  exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			var scriptText string
			if cmd.Flags().Changed("script") {
				if scriptText, err = readScript(script); err != nil {
					return err
				}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, _, err := taskSpec.load(ctx, s, args[0])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.AddCriterion{ID: model.NewNonce(), Text: args[1], Script: scriptText}})
			if err != nil {
				return err
			}
			return printTask(cmd, s, snapshot.(model.Task), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&script, "script", "", "validation script file; its contents become the criterion's check command")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newCriterionRemoveCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "rm TASK CRIT",
		Short: "Remove an acceptance criterion from a task",
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
			ref, task, err := taskSpec.load(ctx, s, args[0])
			if err != nil {
				return err
			}
			crit, err := resolveCriterion(task, args[1])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.RemoveCriterion{ID: crit.ID}})
			if err != nil {
				return err
			}
			return printTask(cmd, s, snapshot.(model.Task), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newCriterionStatusCmd(use string, status model.CriterionStatus) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   use + " TASK CRIT",
		Short: "Mark a criterion " + string(status),
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
			ref, task, err := taskSpec.load(ctx, s, args[0])
			if err != nil {
				return err
			}
			crit, err := resolveCriterion(task, args[1])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.SetCriterionStatus{ID: crit.ID, Status: status}})
			if err != nil {
				return err
			}
			return printTask(cmd, s, snapshot.(model.Task), jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newCriterionScriptCmd() *cobra.Command {
	var clearFlag, jsonOut bool
	cmd := &cobra.Command{
		Use:   "script TASK CRIT FILE",
		Short: "Set or clear a criterion's validation script",
		Args:  maxArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			switch {
			case clearFlag && len(args) != 2:
				return &UsageError{Err: errors.New("--clear takes TASK CRIT with no FILE")}
			case !clearFlag && len(args) != 3:
				return &UsageError{Err: errors.New("script requires TASK CRIT FILE (or --clear)")}
			}
			s, err := openStore()
			if err != nil {
				return err
			}
			var scriptText string
			if !clearFlag {
				if scriptText, err = readScript(args[2]); err != nil {
					return err
				}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			ref, task, err := taskSpec.load(ctx, s, args[0])
			if err != nil {
				return err
			}
			crit, err := resolveCriterion(task, args[1])
			if err != nil {
				return err
			}
			snapshot, err := s.Append(ctx, ref, []model.Op{model.SetCriterionScript{ID: crit.ID, Script: scriptText}})
			if err != nil {
				return err
			}
			return printTask(cmd, s, snapshot.(model.Task), jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&clearFlag, "clear", false, "clear the criterion's validation script")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newCriterionListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "list TASK",
		Short: "List a task's acceptance criteria",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, err := openStore()
			if err != nil {
				return err
			}
			_, task, err := taskSpec.load(ctx, s, args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(out, criterionDTOs(task.Criteria))
			}
			for _, c := range task.Criteria {
				if _, err := fmt.Fprintf(out, "%s\t%s\t%s\n", c.ID[:7], c.Status, c.Text); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

// branchFilter resolves the branch-scoping flags into a predicate over a
// task's folded branch. With no flag it scopes to HEAD; --all-branches
// matches every task, --backlog the empty branch, and --branch=X branch X.
// The three flags are mutually exclusive.
func branchFilter(ctx context.Context, s *store.Store, cmd *cobra.Command, branch string, allBranches, backlog bool) (func(model.Task) bool, error) {
	set := 0
	if cmd.Flags().Changed("branch") {
		set++
	}
	if allBranches {
		set++
	}
	if backlog {
		set++
	}
	if set > 1 {
		return nil, &UsageError{Err: errors.New("--branch, --all-branches, and --backlog are mutually exclusive")}
	}
	switch {
	case allBranches:
		return func(model.Task) bool { return true }, nil
	case backlog:
		return func(t model.Task) bool { return t.Branch == "" }, nil
	default:
		b, err := resolveBranch(ctx, s, "branch", branch)
		if err != nil {
			return nil, err
		}
		return func(t model.Task) bool { return t.Branch == b }, nil
	}
}

func printTaskList(cmd *cobra.Command, s *store.Store, tasks []model.Task, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		live, err := allTasks(cmd.Context(), s)
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

// printStaleTaskList writes stale tasks as their JSON DTOs — each carrying the
// idle duration in seconds — or one lean line per task with a trailing idle
// marker.
func printStaleTaskList(cmd *cobra.Command, s *store.Store, tasks []model.Task, now time.Time, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		live, err := allTasks(cmd.Context(), s)
		if err != nil {
			return err
		}
		dtos := make([]staleTaskDTO, len(tasks))
		for i, t := range tasks {
			idle := now.Sub(time.Unix(taskHeartbeat(t), 0))
			dtos[i] = staleTaskDTO{taskDTO: newTaskDTO(t, blocksFor(live, t.ID)), IdleSeconds: int64(idle.Seconds())}
		}
		return printJSON(out, dtos)
	}
	for _, t := range tasks {
		idle := now.Sub(time.Unix(taskHeartbeat(t), 0))
		if _, err := fmt.Fprintf(out, "%s\t%s\n", leanTaskLine(t), formatIdle(idle)); err != nil {
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

// staleTaskDTO embeds a taskDTO, inlining its fields, plus the idle duration in
// seconds for a stale task.
type staleTaskDTO struct {
	taskDTO
	IdleSeconds int64 `json:"idle_seconds"`
}
