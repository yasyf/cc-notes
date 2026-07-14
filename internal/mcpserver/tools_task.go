package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type taskAddArgs struct {
	Title                string   `json:"title" jsonschema:"short handle for the task"`
	Body                 string   `json:"body,omitempty" jsonschema:"task body (markdown; echoed as 'description' in the task DTO)"`
	Type                 string   `json:"type,omitempty" jsonschema:"task type: task|bug|epic|question (default task)"`
	Priority             *int     `json:"priority,omitempty" jsonschema:"priority 0-3, 0 most urgent (default 2)"`
	Labels               []string `json:"labels,omitempty" jsonschema:"labels"`
	Criteria             []string `json:"criteria,omitempty" jsonschema:"acceptance criterion text (required unless no_validation_criteria)"`
	NoValidationCriteria bool     `json:"no_validation_criteria,omitempty" jsonschema:"create with no acceptance criteria"`
	Parent               string   `json:"parent,omitempty" jsonschema:"parent task id prefix"`
	Sprint               string   `json:"sprint,omitempty" jsonschema:"sprint id prefix"`
	Project              string   `json:"project,omitempty" jsonschema:"project id prefix"`
	BlockedBy            []string `json:"blocked_by,omitempty" jsonschema:"blocker task id prefixes (resolved globally)"`
	Branch               string   `json:"branch,omitempty" jsonschema:"task branch (default: current branch)"`
	Backlog              bool     `json:"backlog,omitempty" jsonschema:"create on the shared backlog (no branch)"`
}

type taskListArgs struct {
	Status          string   `json:"status,omitempty" jsonschema:"status filter, comma-separated (default open,in_progress)"`
	All             bool     `json:"all,omitempty" jsonschema:"every status"`
	Labels          []string `json:"labels,omitempty" jsonschema:"require every label (ANDed)"`
	Assignee        string   `json:"assignee,omitempty" jsonschema:"require assignee"`
	Type            string   `json:"type,omitempty" jsonschema:"require type"`
	Branch          string   `json:"branch,omitempty" jsonschema:"filter to branch (default: current branch)"`
	AllBranches     bool     `json:"all_branches,omitempty" jsonschema:"every branch"`
	Backlog         bool     `json:"backlog,omitempty" jsonschema:"only backlog tasks (no branch)"`
	IncludeArchived bool     `json:"include_archived,omitempty" jsonschema:"include archived (old done/cancelled) tasks"`
}

type taskBranchArgs struct {
	Branch      string `json:"branch,omitempty" jsonschema:"filter to branch (default: current branch)"`
	AllBranches bool   `json:"all_branches,omitempty" jsonschema:"every branch"`
	Backlog     bool   `json:"backlog,omitempty" jsonschema:"only backlog tasks (no branch)"`
}

type taskClaimArgs struct {
	ID    string `json:"id" jsonschema:"task id prefix"`
	Steal bool   `json:"steal,omitempty" jsonschema:"reclaim an in-progress task whose lease has expired"`
	Sync  bool   `json:"sync,omitempty" jsonschema:"claim, then sync and re-check, yielding if another agent won"`
}

type taskStartArgs struct {
	ID     string `json:"id" jsonschema:"task id prefix"`
	Branch string `json:"branch,omitempty" jsonschema:"branch to set (default: current branch)"`
}

type taskDoneArgs struct {
	ID    string `json:"id" jsonschema:"task id prefix"`
	Force bool   `json:"force,omitempty" jsonschema:"close even with unmet criteria"`
}

type taskEditArgs struct {
	ID         string   `json:"id" jsonschema:"task id prefix"`
	Title      string   `json:"title,omitempty" jsonschema:"new title"`
	Body       string   `json:"body,omitempty" jsonschema:"new body (echoed as 'description' in the task DTO)"`
	Type       string   `json:"type,omitempty" jsonschema:"new type: task|bug|epic|question"`
	Priority   *int     `json:"priority,omitempty" jsonschema:"new priority 0-3"`
	Status     string   `json:"status,omitempty" jsonschema:"new status: open|in_progress|done|cancelled"`
	Assignee   string   `json:"assignee,omitempty" jsonschema:"new assignee"`
	NoAssignee bool     `json:"no_assignee,omitempty" jsonschema:"clear the assignee"`
	AddLabels  []string `json:"add_labels,omitempty" jsonschema:"labels to add"`
	RmLabels   []string `json:"rm_labels,omitempty" jsonschema:"labels to remove"`
	Parent     string   `json:"parent,omitempty" jsonschema:"new parent task id prefix"`
	NoParent   bool     `json:"no_parent,omitempty" jsonschema:"clear the parent"`
	Sprint     string   `json:"sprint,omitempty" jsonschema:"new sprint id prefix"`
	NoSprint   bool     `json:"no_sprint,omitempty" jsonschema:"clear the sprint"`
	Project    string   `json:"project,omitempty" jsonschema:"new project id prefix"`
	NoProject  bool     `json:"no_project,omitempty" jsonschema:"clear the project"`
	Branch     string   `json:"branch,omitempty" jsonschema:"reassign to this branch"`
	Backlog    bool     `json:"backlog,omitempty" jsonschema:"move to the shared backlog (clear branch)"`
}

type taskDepArgs struct {
	ID      string `json:"id" jsonschema:"task id prefix"`
	Blocker string `json:"blocker" jsonschema:"blocker task id prefix"`
}

type taskStaleArgs struct {
	IdleAfter string `json:"idle_after,omitempty" jsonschema:"idle threshold (Go duration; default lease TTL)"`
}

type taskArchivedArgs struct {
	ClosedBefore string `json:"closed_before,omitempty" jsonschema:"archive cutoff (Go duration relative or RFC3339 absolute; default 720h)"`
}

type taskValidateArgs struct {
	Task    string `json:"task" jsonschema:"task id prefix"`
	Yes     bool   `json:"yes,omitempty" jsonschema:"run the stored validation scripts without a confirmation prompt (executes untrusted content)"`
	Timeout string `json:"timeout,omitempty" jsonschema:"per-script timeout (Go duration; default 5m)"`
}

type criterionAddArgs struct {
	Task   string `json:"task" jsonschema:"task id prefix"`
	Text   string `json:"text" jsonschema:"acceptance criterion text"`
	Script string `json:"script,omitempty" jsonschema:"path to a validation script file; its contents become the check command"`
}

type criterionRefArgs struct {
	Task string `json:"task" jsonschema:"task id prefix"`
	Crit string `json:"crit" jsonschema:"criterion id prefix"`
}

type criterionResultArgs struct {
	Task string `json:"task" jsonschema:"task id prefix"`
	Crit string `json:"crit" jsonschema:"criterion id prefix"`
	Note string `json:"note,omitempty" jsonschema:"evidence recorded with the verdict (a later status change clears it)"`
}

type criterionScriptArgs struct {
	Task  string `json:"task" jsonschema:"task id prefix"`
	Crit  string `json:"crit" jsonschema:"criterion id prefix"`
	File  string `json:"file,omitempty" jsonschema:"path to a validation script file (omit with clear)"`
	Clear bool   `json:"clear,omitempty" jsonschema:"clear the criterion's validation script"`
}

type criterionListArgs struct {
	Task string `json:"task" jsonschema:"task id prefix"`
}

func registerTask(srv *mcp.Server, b *bridge) {
	mcp.AddTool(srv, &mcp.Tool{Name: "task_add", Description: "Create a task (durable, cross-agent). Provide acceptance criteria or set no_validation_criteria."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in taskAddArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			flags = optStr(flags, "--type", in.Type)
			flags = optInt(flags, "--priority", in.Priority)
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optRepeated(flags, "--criterion", in.Criteria)
			flags = optBool(flags, "--no-validation-criteria", in.NoValidationCriteria)
			flags = optStr(flags, "--parent", in.Parent)
			flags = optStr(flags, "--sprint", in.Sprint)
			flags = optStr(flags, "--project", in.Project)
			flags = optRepeated(flags, "--blocked-by", in.BlockedBy)
			flags = optStr(flags, "--branch", in.Branch)
			flags = optBool(flags, "--backlog", in.Backlog)
			return b.run(ctx, argvFor([]string{"task", "add"}, flags, in.Title)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "task_list", Description: "List tasks, filtered by status, label, assignee, type, and branch scope."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in taskListArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--status", in.Status)
			flags = optBool(flags, "--all", in.All)
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optStr(flags, "--assignee", in.Assignee)
			flags = optStr(flags, "--type", in.Type)
			flags = optStr(flags, "--branch", in.Branch)
			flags = optBool(flags, "--all-branches", in.AllBranches)
			flags = optBool(flags, "--backlog", in.Backlog)
			flags = optBool(flags, "--include-archived", in.IncludeArchived)
			return b.run(ctx, argvFor([]string{"task", "list"}, flags)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "task_ready", Description: "List unblocked, unclaimed tasks ready to pick up."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in taskBranchArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--branch", in.Branch)
			flags = optBool(flags, "--all-branches", in.AllBranches)
			flags = optBool(flags, "--backlog", in.Backlog)
			return b.run(ctx, argvFor([]string{"task", "ready"}, flags)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "task_backlog", Description: "List the shared backlog tasks (no branch)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ statusArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, "task", "backlog", "--json")
		})

	idTool(srv, b, "task_show", "Show one task with its full detail and derived blocks index.", "task", "show")

	mcp.AddTool(srv, &mcp.Tool{Name: "task_start", Description: "Claim a task and set its branch to the current HEAD branch or an explicit branch."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in taskStartArgs) (*mcp.CallToolResult, any, error) {
			flags := optStr([]string{"--json"}, "--branch", in.Branch)
			return b.run(ctx, argvFor([]string{"task", "start"}, flags, in.ID)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "task_claim", Description: "Claim a task (lease it). Use steal for an expired lease, sync to yield if another agent won."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in taskClaimArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optBool(flags, "--steal", in.Steal)
			flags = optBool(flags, "--sync", in.Sync)
			return b.run(ctx, argvFor([]string{"task", "claim"}, flags, in.ID)...)
		})

	idTool(srv, b, "task_renew", "Renew the lease on a task you hold.", "task", "renew")

	mcp.AddTool(srv, &mcp.Tool{Name: "task_done", Description: "Close a task as done (refuses with unmet criteria unless force); links the current commit."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in taskDoneArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optBool(flags, "--force", in.Force)
			return b.run(ctx, argvFor([]string{"task", "done"}, flags, in.ID)...)
		})

	idTool(srv, b, "task_cancel", "Cancel a task (from open or in-progress).", "task", "cancel")

	mcp.AddTool(srv, &mcp.Tool{Name: "task_edit", Description: "Edit a task's title, body, type, priority, status, assignee, branch, labels, and hierarchy."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in taskEditArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--title", in.Title)
			flags, err := freeTextFlag(flags, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			flags = optStr(flags, "--type", in.Type)
			flags = optInt(flags, "--priority", in.Priority)
			flags = optStr(flags, "--status", in.Status)
			flags = optStr(flags, "--assignee", in.Assignee)
			flags = optBool(flags, "--no-assignee", in.NoAssignee)
			flags = optRepeated(flags, "--add-label", in.AddLabels)
			flags = optRepeated(flags, "--rm-label", in.RmLabels)
			flags = optStr(flags, "--parent", in.Parent)
			flags = optBool(flags, "--no-parent", in.NoParent)
			flags = optStr(flags, "--sprint", in.Sprint)
			flags = optBool(flags, "--no-sprint", in.NoSprint)
			flags = optStr(flags, "--project", in.Project)
			flags = optBool(flags, "--no-project", in.NoProject)
			flags = optStr(flags, "--branch", in.Branch)
			flags = optBool(flags, "--backlog", in.Backlog)
			return b.run(ctx, argvFor([]string{"task", "edit"}, flags, in.ID)...)
		})

	commentTool(srv, b, "task")

	mcp.AddTool(srv, &mcp.Tool{Name: "task_dep", Description: "Add a dependency: ID is blocked by BLOCKER (rejects cycles)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in taskDepArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"task", "dep"}, []string{"--json"}, in.ID, in.Blocker)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "task_undep", Description: "Remove a dependency edge between ID and BLOCKER."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in taskDepArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"task", "undep"}, []string{"--json"}, in.ID, in.Blocker)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "task_stale", Description: "List in-progress tasks whose lease has gone idle past the threshold."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in taskStaleArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--idle-after", in.IdleAfter)
			return b.run(ctx, argvFor([]string{"task", "stale"}, flags)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "task_archived", Description: "List old done/cancelled tasks past the archive cutoff."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in taskArchivedArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--closed-before", in.ClosedBefore)
			return b.run(ctx, argvFor([]string{"task", "archived"}, flags)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "task_validate", Description: "Run a task's stored validation scripts. Requires yes to execute untrusted script content."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in taskValidateArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optBool(flags, "--yes", in.Yes)
			flags = optStr(flags, "--timeout", in.Timeout)
			return b.run(ctx, argvFor([]string{"task", "validate"}, flags, in.Task)...)
		})

	registerCriterion(srv, b)
}

func registerCriterion(srv *mcp.Server, b *bridge) {
	mcp.AddTool(srv, &mcp.Tool{Name: "task_criterion_add", Description: "Add an acceptance criterion to a task, optionally with a validation script file."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in criterionAddArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--body", in.Text)
			if err != nil {
				return nil, nil, err
			}
			flags = optStr(flags, "--script", in.Script)
			return b.run(ctx, argvFor([]string{"task", "criterion", "add"}, flags, in.Task)...)
		})

	for _, verb := range []string{"rm", "pending"} {
		mcp.AddTool(srv, &mcp.Tool{Name: "task_criterion_" + verb, Description: criterionVerbDescription(verb)},
			func(ctx context.Context, _ *mcp.CallToolRequest, in criterionRefArgs) (*mcp.CallToolResult, any, error) {
				return b.run(ctx, argvFor([]string{"task", "criterion", verb}, []string{"--json"}, in.Task, in.Crit)...)
			})
	}

	for _, verb := range []string{"met", "failed"} {
		mcp.AddTool(srv, &mcp.Tool{Name: "task_criterion_" + verb, Description: criterionVerbDescription(verb)},
			func(ctx context.Context, _ *mcp.CallToolRequest, in criterionResultArgs) (*mcp.CallToolResult, any, error) {
				flags, err := freeTextFlag([]string{"--json"}, "--note", in.Note)
				if err != nil {
					return nil, nil, err
				}
				return b.run(ctx, argvFor([]string{"task", "criterion", verb}, flags, in.Task, in.Crit)...)
			})
	}

	mcp.AddTool(srv, &mcp.Tool{Name: "task_criterion_script", Description: "Set or clear a criterion's validation script."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in criterionScriptArgs) (*mcp.CallToolResult, any, error) {
			flags := optBool([]string{"--json"}, "--clear", in.Clear)
			positionals := []string{in.Task, in.Crit}
			if in.File != "" {
				positionals = append(positionals, in.File)
			}
			return b.run(ctx, argvFor([]string{"task", "criterion", "script"}, flags, positionals...)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "task_criterion_list", Description: "List a task's acceptance criteria and their status."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in criterionListArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"task", "criterion", "list"}, []string{"--json"}, in.Task)...)
		})
}

func criterionVerbDescription(verb string) string {
	switch verb {
	case "rm":
		return "Remove an acceptance criterion from a task."
	case "met":
		return "Mark a criterion as met."
	case "failed":
		return "Mark a criterion as failed."
	default: // pending
		return "Reset a criterion to pending."
	}
}
