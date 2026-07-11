package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type runbookAddArgs struct {
	Title  string   `json:"title" jsonschema:"short handle for the runbook"`
	Body   string   `json:"body,omitempty" jsonschema:"runbook description (echoed as 'description' in the runbook DTO)"`
	Labels []string `json:"labels,omitempty" jsonschema:"labels"`
	Steps  []string `json:"steps,omitempty" jsonschema:"initial step texts, in order"`
}

type runbookListArgs struct {
	All bool `json:"all,omitempty" jsonschema:"include archived runbooks (default active only)"`
}

type runbookStepAddArgs struct {
	ID      string `json:"id" jsonschema:"runbook id prefix"`
	Text    string `json:"text" jsonschema:"step text"`
	Command string `json:"command,omitempty" jsonschema:"shell command for the step"`
}

type runbookRunStartArgs struct {
	ID   string `json:"id" jsonschema:"runbook id prefix"`
	Task string `json:"task,omitempty" jsonschema:"task id prefix this run serves"`
}

type runbookRunStepArgs struct {
	ID   string `json:"id" jsonschema:"runbook id prefix"`
	Step string `json:"step" jsonschema:"step id prefix"`
	Note string `json:"note,omitempty" jsonschema:"context note (error output, skip reason)"`
	Run  string `json:"run,omitempty" jsonschema:"run id prefix (default: the sole running run)"`
}

type runbookRunFinishArgs struct {
	ID        string `json:"id" jsonschema:"runbook id prefix"`
	Run       string `json:"run,omitempty" jsonschema:"run id prefix (default: the sole running run)"`
	Failed    bool   `json:"failed,omitempty" jsonschema:"finish as failed"`
	Abandoned bool   `json:"abandoned,omitempty" jsonschema:"finish as abandoned"`
}

func registerRunbook(srv *mcp.Server, b *bridge) {
	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_add", Description: "Create a runbook (a repeatable step-by-step operational procedure), optionally with its first steps."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookAddArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--body", in.Body)
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optRepeated(flags, "--step", in.Steps)
			return b.run(ctx, argvFor([]string{"runbook", "add"}, flags, in.Title)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_list", Description: "List runbooks (active only unless all is set)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookListArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optBool(flags, "--all", in.All)
			return b.run(ctx, argvFor([]string{"runbook", "list"}, flags)...)
		})

	idTool(srv, b, "runbook_show", "Show one runbook with its steps and runs.", "runbook", "show")

	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_step_add", Description: "Append a step to a runbook's procedure."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookStepAddArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--command", in.Command)
			return b.run(ctx, argvFor([]string{"runbook", "step", "add"}, flags, in.ID, in.Text)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_run_start", Description: "Start a tracked run of a runbook, optionally linked to a task."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookRunStartArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--task", in.Task)
			return b.run(ctx, argvFor([]string{"runbook", "run", "start"}, flags, in.ID)...)
		})

	for _, verb := range []string{"done", "skip", "fail"} {
		mcp.AddTool(srv, &mcp.Tool{Name: "runbook_run_" + verb, Description: "Record a step result (" + verb + ") in a run; re-mark to correct a wrong result."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in runbookRunStepArgs) (*mcp.CallToolResult, any, error) {
				flags := []string{"--json"}
				flags = optStr(flags, "--note", in.Note)
				flags = optStr(flags, "--run", in.Run)
				return b.run(ctx, argvFor([]string{"runbook", "run", verb}, flags, in.ID, in.Step)...)
			})
	}

	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_run_finish", Description: "Finish a run (default succeeded, or failed if any step failed)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookRunFinishArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--run", in.Run)
			flags = optBool(flags, "--failed", in.Failed)
			flags = optBool(flags, "--abandoned", in.Abandoned)
			return b.run(ctx, argvFor([]string{"runbook", "run", "finish"}, flags, in.ID)...)
		})
}
