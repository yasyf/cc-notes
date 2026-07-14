package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sprintAddArgs struct {
	Title   string   `json:"title" jsonschema:"short handle for the sprint"`
	Body    string   `json:"body,omitempty" jsonschema:"sprint body (echoed as 'description' in the sprint DTO)"`
	Project string   `json:"project,omitempty" jsonschema:"project id prefix"`
	Labels  []string `json:"labels,omitempty" jsonschema:"labels"`
	Start   string   `json:"start,omitempty" jsonschema:"start date YYYY-MM-DD"`
	End     string   `json:"end,omitempty" jsonschema:"end date YYYY-MM-DD"`
}

type sprintEditArgs struct {
	ID        string   `json:"id" jsonschema:"sprint id prefix"`
	Title     string   `json:"title,omitempty" jsonschema:"new title"`
	Body      string   `json:"body,omitempty" jsonschema:"new body (echoed as 'description' in the sprint DTO)"`
	Project   string   `json:"project,omitempty" jsonschema:"new project id prefix"`
	NoProject bool     `json:"no_project,omitempty" jsonschema:"clear the project"`
	Start     string   `json:"start,omitempty" jsonschema:"new start date YYYY-MM-DD"`
	NoStart   bool     `json:"no_start,omitempty" jsonschema:"clear the start date"`
	End       string   `json:"end,omitempty" jsonschema:"new end date YYYY-MM-DD"`
	NoEnd     bool     `json:"no_end,omitempty" jsonschema:"clear the end date"`
	AddLabels []string `json:"add_labels,omitempty" jsonschema:"labels to add"`
	RmLabels  []string `json:"rm_labels,omitempty" jsonschema:"labels to remove"`
}

type sprintListArgs struct {
	Project string `json:"project,omitempty" jsonschema:"filter to project id prefix"`
	Status  string `json:"status,omitempty" jsonschema:"status filter, comma-separated (default all)"`
}

type projectAddArgs struct {
	Title  string   `json:"title" jsonschema:"short handle for the project"`
	Body   string   `json:"body,omitempty" jsonschema:"project body (echoed as 'description' in the project DTO)"`
	Labels []string `json:"labels,omitempty" jsonschema:"labels"`
}

type projectEditArgs struct {
	ID        string   `json:"id" jsonschema:"project id prefix"`
	Title     string   `json:"title,omitempty" jsonschema:"new title"`
	Body      string   `json:"body,omitempty" jsonschema:"new body (echoed as 'description' in the project DTO)"`
	AddLabels []string `json:"add_labels,omitempty" jsonschema:"labels to add"`
	RmLabels  []string `json:"rm_labels,omitempty" jsonschema:"labels to remove"`
}

type projectListArgs struct {
	Status string `json:"status,omitempty" jsonschema:"status filter, comma-separated (default all)"`
}

func registerPlanning(srv *mcp.Server, b *bridge) {
	mcp.AddTool(srv, &mcp.Tool{Name: "sprint_add", Description: "Create a sprint (a time-boxed grouping of tasks)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in sprintAddArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			flags = optStr(flags, "--project", in.Project)
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optStr(flags, "--start", in.Start)
			flags = optStr(flags, "--end", in.End)
			return b.run(ctx, argvFor([]string{"sprint", "add"}, flags, in.Title)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "sprint_edit", Description: "Edit a sprint's title, description, project, dates, and labels."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in sprintEditArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--title", in.Title)
			flags, err := freeTextFlag(flags, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			flags = optStr(flags, "--project", in.Project)
			flags = optBool(flags, "--no-project", in.NoProject)
			flags = optStr(flags, "--start", in.Start)
			flags = optBool(flags, "--no-start", in.NoStart)
			flags = optStr(flags, "--end", in.End)
			flags = optBool(flags, "--no-end", in.NoEnd)
			flags = optRepeated(flags, "--add-label", in.AddLabels)
			flags = optRepeated(flags, "--rm-label", in.RmLabels)
			return b.run(ctx, argvFor([]string{"sprint", "edit"}, flags, in.ID)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "sprint_list", Description: "List sprints, filtered by project and status."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in sprintListArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--project", in.Project)
			flags = optStr(flags, "--status", in.Status)
			return b.run(ctx, argvFor([]string{"sprint", "list"}, flags)...)
		})

	idTool(srv, b, "sprint_show", "Show one sprint with its tasks.", "sprint", "show")

	commentTool(srv, b, "sprint")

	statusTools(srv, b, "sprint", "activate", "complete", "cancel")

	mcp.AddTool(srv, &mcp.Tool{Name: "project_add", Description: "Create a project (a long-lived grouping of sprints and tasks)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in projectAddArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			flags = optRepeated(flags, "--label", in.Labels)
			return b.run(ctx, argvFor([]string{"project", "add"}, flags, in.Title)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "project_edit", Description: "Edit a project's title, description, and labels."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in projectEditArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--title", in.Title)
			flags, err := freeTextFlag(flags, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			flags = optRepeated(flags, "--add-label", in.AddLabels)
			flags = optRepeated(flags, "--rm-label", in.RmLabels)
			return b.run(ctx, argvFor([]string{"project", "edit"}, flags, in.ID)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "project_list", Description: "List projects, filtered by status."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in projectListArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--status", in.Status)
			return b.run(ctx, argvFor([]string{"project", "list"}, flags)...)
		})

	idTool(srv, b, "project_show", "Show one project with its sprints and tasks.", "project", "show")

	commentTool(srv, b, "project")

	statusTools(srv, b, "project", "activate", "complete", "archive", "cancel")
}
