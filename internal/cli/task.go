package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

var allStatuses = []model.Status{model.StatusOpen, model.StatusInProgress, model.StatusDone, model.StatusCancelled}

// taskErr maps notes-layer task errors to the CLI's presentation so exit codes
// and stderr bytes stay identical: a *notes.ConflictError becomes the CLI's
// *ConflictError (its Error() drops the "cc-notes: " layer prefix), and a
// *notes.UnmetCriteriaError becomes the done gate's UsageError with the full
// criterion detail and --force hint. Every other error passes through.
func taskErr(err error) error {
	var conflict *notes.ConflictError
	var unmet *notes.UnmetCriteriaError
	switch {
	case errors.As(err, &unmet):
		return unmetCriteriaUsage(unmet.ID, unmet.Unmet)
	case errors.As(err, &conflict):
		return &ConflictError{Msg: strings.TrimPrefix(conflict.Error(), "cc-notes: ")}
	default:
		return err
	}
}

// unmetCriteriaUsage renders the done-gate UsageError: every criterion not yet
// met, with the --force remediation.
func unmetCriteriaUsage(id model.EntityID, unmet []model.Criterion) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s has %d unmet criterion/criteria (pass --force to close anyway):", id.Short(), len(unmet))
	for _, c := range unmet {
		fmt.Fprintf(&b, "\n  %s [%s] %s", c.ID[:7], c.Status, sanitizeDisplay(c.Text, false))
	}
	return &UsageError{Err: errors.New(b.String())}
}

// editEmpty reports whether a task edit mask sets nothing, the CLI's guard for
// the "at least one flag" usage error before any refspec install.
func editEmpty(e notes.TaskEdit) bool {
	return e.Title == nil && e.Description == nil && e.Type == nil && e.Priority == nil &&
		e.Status == nil && e.Assignee == nil && e.Parent == nil && e.Sprint == nil &&
		e.Project == nil && e.Branch == nil && len(e.AddLabels) == 0 && len(e.RemoveLabels) == 0
}

// branchScopeFromFlags resolves the mutually-exclusive branch-scoping flags into
// a notes.BranchScope and branch name. No flag scopes to the current branch;
// --all-branches, --backlog, and --branch=X select the other scopes. The three
// are mutually exclusive, and an explicit --branch is validated against git's
// check-ref-format so a malformed name is a usage error before any read.
func branchScopeFromFlags(ctx context.Context, s *store.Store, cmd *cobra.Command, branch string, allBranches, backlog bool) (notes.BranchScope, model.Branch, error) {
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
		return 0, "", &UsageError{Err: errors.New("--branch, --all-branches, and --backlog are mutually exclusive")}
	}
	switch {
	case allBranches:
		return notes.ScopeAllBranches, "", nil
	case backlog:
		return notes.ScopeBacklog, "", nil
	case cmd.Flags().Changed("branch"):
		if err := s.Git.CheckRefFormat(ctx, branch); err != nil {
			return 0, "", &UsageError{Err: err}
		}
		return notes.ScopeNamed, model.Branch(branch), nil
	default:
		return notes.ScopeCurrentBranch, "", nil
	}
}

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
		newTaskCancelCmd(),
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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			var b model.Branch
			if !backlog && cmd.Flags().Changed("branch") {
				if b, _, err = resolveBranchOrBacklog(ctx, s, branch, true); err != nil {
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
			var blockers []model.EntityID
			for _, prefix := range blockedBy {
				blocker, _, err := resolveBlocker(ctx, s, prefix)
				if err != nil {
					return err
				}
				blockers = append(blockers, blocker)
			}
			created, err := c.CreateTask(ctx, notes.TaskSpec{
				Title:       args[0],
				Description: text,
				Type:        tt,
				Priority:    p,
				Branch:      b,
				Backlog:     backlog,
				Parent:      parentID,
				Sprint:      sprintID,
				Project:     projectID,
				Labels:      labels,
				Criteria:    criteria,
				BlockedBy:   blockers,
			})
			if err != nil {
				return err
			}
			if created.Reused {
				warnDuplicate(cmd, "task", created.Task.ID)
			}
			if created.Degraded {
				_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "cc-notes: detached HEAD with no resolvable branch; created on the backlog — pass --branch to set one")
			}
			return printTask(cmd, c, created.Task, jsonOut)
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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			scope, scopeBranch, err := branchScopeFromFlags(ctx, s, cmd, branch, allBranches, backlog)
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
			filter := notes.TaskFilter{
				Scope:       scope,
				Branch:      scopeBranch,
				Statuses:    statuses,
				Labels:      labels,
				Type:        tt,
				Assignee:    assignee,
				AssigneeSet: cmd.Flags().Changed("assignee"),
			}
			if !includeArchived {
				filter.ArchiveCutoff = now.Add(-notes.DefaultArchiveAge)
			}
			tasks, err := c.Tasks(ctx, filter)
			if err != nil {
				return err
			}
			return printTaskList(cmd, c, tasks, jsonOut)
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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			scope, scopeBranch, err := branchScopeFromFlags(ctx, s, cmd, branch, allBranches, backlog)
			if err != nil {
				return err
			}
			tasks, err := c.ReadyTasks(ctx, scope, scopeBranch)
			if err != nil {
				return err
			}
			return printTaskList(cmd, c, tasks, jsonOut)
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
	return taskSpec.showVerb("Show one task", showTask)
}

func newTaskStartCmd() *cobra.Command {
	var branch string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "start ID",
		Short: "Claim a task and move it onto your current branch",
		Args:  exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			var branchArg model.Branch
			if cmd.Flags().Changed("branch") {
				if branchArg, _, err = resolveBranchOrBacklog(ctx, s, branch, true); err != nil {
					return err
				}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveTask(ctx, args[0])
			if err != nil {
				return err
			}
			result, err := c.StartTask(ctx, id, branchArg)
			if err != nil {
				return taskErr(err)
			}
			if !result.BranchSet {
				_, _ = fmt.Fprintf(cmd.ErrOrStderr(), "cc-notes: detached HEAD with no resolvable branch; claimed %s without setting a branch — pass --branch to set one\n", result.Task.ID.Short())
			}
			return printTask(cmd, c, result.Task, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&branch, "branch", "", "branch to set (default: current branch)")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveTask(ctx, args[0])
			if err != nil {
				return err
			}
			switch {
			case steal:
				task, err := c.Task(ctx, id)
				if err != nil {
					return err
				}
				// Resolve the actor before the --steal status check so a malformed
				// CC_NOTES_ACTOR errors (exit 1) ahead of the usage guard.
				if _, err := c.Actor(ctx); err != nil {
					return err
				}
				if task.Status != model.StatusInProgress {
					return &UsageError{Err: errors.New("--steal requires an in-progress task")}
				}
				ttl, err := leaseTTL(ctx, s.Git)
				if err != nil {
					return err
				}
				stolen, err := c.StealTask(ctx, id, ttl)
				if err != nil {
					return taskErr(err)
				}
				return printTask(cmd, c, stolen, jsonOut)
			case syncRemote:
				claimed, err := c.ClaimTaskSync(ctx, id)
				if err != nil {
					return taskErr(err)
				}
				return printTask(cmd, c, claimed, jsonOut)
			default:
				claimed, err := c.ClaimTask(ctx, id)
				if err != nil {
					return taskErr(err)
				}
				return printTask(cmd, c, claimed, jsonOut)
			}
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	flags.BoolVar(&steal, "steal", false, "reclaim an in-progress task whose lease has expired")
	flags.BoolVar(&syncRemote, "sync", false, "claim, then sync and re-check, yielding if another agent won")
	return cmd
}

func newTaskRenewCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "renew ID",
		Short: "Refresh the lease heartbeat on a task you hold",
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
			id, err := c.ResolveTask(ctx, args[0])
			if err != nil {
				return err
			}
			task, err := c.RenewTask(ctx, id)
			if err != nil {
				return taskErr(err)
			}
			return printTask(cmd, c, task, jsonOut)
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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveTask(ctx, args[0])
			if err != nil {
				return err
			}
			task, err := c.DoneTask(ctx, id, force)
			if err != nil {
				return taskErr(err)
			}
			return printTask(cmd, c, task, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&force, "force", false, "close even with unmet criteria")
	flags.BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func newTaskCancelCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "cancel ID",
		Short: "Mark a task " + string(model.StatusCancelled),
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
			id, err := c.ResolveTask(ctx, args[0])
			if err != nil {
				return err
			}
			task, err := c.CancelTask(ctx, id)
			if err != nil {
				return taskErr(err)
			}
			return printTask(cmd, c, task, jsonOut)
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
			if flags.Changed("title") {
				if err := validateTitle(title, titleHintDesc); err != nil {
					return err
				}
			}
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			var edit notes.TaskEdit
			if flags.Changed("title") {
				edit.Title = &title
			}
			if flags.Changed("body") {
				text, err := bodyArg(cmd, body)
				if err != nil {
					return err
				}
				edit.Description = &text
			}
			if flags.Changed("type") {
				tt, err := parseTaskType(taskType)
				if err != nil {
					return err
				}
				edit.Type = &tt
			}
			if flags.Changed("priority") {
				p, err := validatePriority(priority)
				if err != nil {
					return err
				}
				edit.Priority = &p
			}
			if flags.Changed("status") {
				st, err := parseStatus(status)
				if err != nil {
					return err
				}
				edit.Status = &st
			}
			if flags.Changed("assignee") {
				a := model.Actor(assignee)
				edit.Assignee = &a
			}
			if noAssignee {
				var a model.Actor
				edit.Assignee = &a
			}
			edit.AddLabels = addLabels
			edit.RemoveLabels = rmLabels
			if flags.Changed("parent") {
				_, parentTask, err := taskSpec.load(ctx, s, parent)
				if err != nil {
					return err
				}
				edit.Parent = &parentTask.ID
			}
			if noParent {
				var p model.EntityID
				edit.Parent = &p
			}
			if flags.Changed("sprint") {
				_, sp, err := sprintSpec.load(ctx, s, sprint)
				if err != nil {
					return err
				}
				edit.Sprint = &sp.ID
			}
			if noSprint {
				var sp model.EntityID
				edit.Sprint = &sp
			}
			if flags.Changed("project") {
				_, proj, err := projectSpec.load(ctx, s, project)
				if err != nil {
					return err
				}
				edit.Project = &proj.ID
			}
			if noProject {
				var pr model.EntityID
				edit.Project = &pr
			}
			if flags.Changed("branch") {
				if err := s.Git.CheckRefFormat(ctx, target.branch); err != nil {
					return &UsageError{Err: err}
				}
				b := model.Branch(target.branch)
				edit.Branch = &b
			}
			if target.backlog {
				var b model.Branch
				edit.Branch = &b
			}
			if editEmpty(edit) {
				return &UsageError{Err: errors.New("task edit requires at least one flag")}
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveTask(ctx, args[0])
			if err != nil {
				return err
			}
			task, err := c.EditTask(ctx, id, edit)
			if err != nil {
				return err
			}
			return printTask(cmd, c, task, jsonOut)
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
			s, c, err := openStoreClient()
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
			id, err := c.ResolveTask(ctx, args[0])
			if err != nil {
				return err
			}
			task, err := c.CommentTask(ctx, id, body)
			if err != nil {
				return err
			}
			return printTask(cmd, c, task, jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveTask(ctx, args[0])
			if err != nil {
				return err
			}
			blocker, _, err := resolveBlocker(ctx, s, args[1])
			if err != nil {
				return err
			}
			task, err := c.AddDep(ctx, id, blocker)
			if err != nil {
				return err
			}
			return printTask(cmd, c, task, jsonOut)
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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveTask(ctx, args[0])
			if err != nil {
				return err
			}
			blocker, _, err := resolveBlocker(ctx, s, args[1])
			if err != nil {
				return err
			}
			task, err := c.RemoveDep(ctx, id, blocker)
			if err != nil {
				return err
			}
			return printTask(cmd, c, task, jsonOut)
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
			_, c, err := openStoreClient()
			if err != nil {
				return err
			}
			tasks, err := c.Tasks(ctx, notes.TaskFilter{Scope: notes.ScopeBacklog, Statuses: []model.Status{model.StatusOpen}})
			if err != nil {
				return err
			}
			return printTaskList(cmd, c, tasks, jsonOut)
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
			s, c, err := openStoreClient()
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
			tasks, err := c.StaleTasks(ctx, ttl)
			if err != nil {
				return err
			}
			return printStaleTaskList(cmd, c, tasks, now, jsonOut)
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
			_, c, err := openStoreClient()
			if err != nil {
				return err
			}
			cutoff := now.Add(-defaultArchiveAge)
			if cmd.Flags().Changed("closed-before") {
				if cutoff, err = parseWhen(closedBefore, now); err != nil {
					return err
				}
			}
			tasks, err := c.ArchivedTasks(ctx, cutoff)
			if err != nil {
				return err
			}
			return printTaskList(cmd, c, tasks, jsonOut)
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
			s, c, err := openStoreClient()
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
			id, err := c.ResolveTask(ctx, args[0])
			if err != nil {
				return err
			}
			task, err := c.AddCriterion(ctx, id, args[1], scriptText)
			if err != nil {
				return err
			}
			return printTask(cmd, c, task, jsonOut)
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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveTask(ctx, args[0])
			if err != nil {
				return err
			}
			task, err := c.RemoveCriterion(ctx, id, args[1])
			if err != nil {
				return err
			}
			return printTask(cmd, c, task, jsonOut)
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
			s, c, err := openStoreClient()
			if err != nil {
				return err
			}
			if err := autoInstall(ctx, cmd, s.Git); err != nil {
				return err
			}
			id, err := c.ResolveTask(ctx, args[0])
			if err != nil {
				return err
			}
			task, err := c.SetCriterionStatus(ctx, id, args[1], status, "")
			if err != nil {
				return err
			}
			return printTask(cmd, c, task, jsonOut)
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
			s, c, err := openStoreClient()
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
			id, err := c.ResolveTask(ctx, args[0])
			if err != nil {
				return err
			}
			task, err := c.SetCriterionScript(ctx, id, args[1], scriptText)
			if err != nil {
				return err
			}
			return printTask(cmd, c, task, jsonOut)
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
			_, c, err := openStoreClient()
			if err != nil {
				return err
			}
			id, err := c.ResolveTask(ctx, args[0])
			if err != nil {
				return err
			}
			task, err := c.Task(ctx, id)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if jsonOut {
				return printJSON(out, criterionDTOs(task.Criteria))
			}
			for _, crit := range task.Criteria {
				if _, err := fmt.Fprintf(out, "%s\t%s\t%s\n", crit.ID[:7], crit.Status, crit.Text); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "emit JSON")
	return cmd
}

func printTaskList(cmd *cobra.Command, c *notes.Client, tasks []model.Task, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		blocking, err := c.TasksBlockingIndex(cmd.Context())
		if err != nil {
			return err
		}
		return printJSON(out, taskDTOs(tasks, blocking))
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
func printStaleTaskList(cmd *cobra.Command, c *notes.Client, tasks []model.Task, now time.Time, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		blocking, err := c.TasksBlockingIndex(cmd.Context())
		if err != nil {
			return err
		}
		dtos := make([]staleTaskDTO, len(tasks))
		for i, t := range tasks {
			idle := now.Sub(time.Unix(taskHeartbeat(t), 0))
			dtos[i] = staleTaskDTO{taskDTO: newTaskDTO(t, blocking[t.ID]), IdleSeconds: int64(idle.Seconds())}
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

// staleTaskDTO embeds a taskDTO, inlining its fields, plus the idle duration in
// seconds for a stale task.
type staleTaskDTO struct {
	taskDTO
	IdleSeconds int64 `json:"idle_seconds"`
}
