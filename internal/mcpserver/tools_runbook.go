package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type runbookAddArgs struct {
	Title  string   `json:"title" jsonschema:"short handle for the runbook"`
	Body   string   `json:"body,omitempty" jsonschema:"runbook description (echoed as 'description' in the runbook DTO)"`
	Labels []string `json:"labels,omitempty" jsonschema:"labels"`
	anchorSetArgs
	Steps []string `json:"steps,omitempty" jsonschema:"initial step texts, in order"`
}

type runbookListArgs struct {
	Labels []string `json:"labels,omitempty" jsonschema:"require every label (ANDed; echoed as 'tags' in the DTO)"`
	Path   string   `json:"path,omitempty" jsonschema:"require path anchor"`
	Commit string   `json:"commit,omitempty" jsonschema:"require commit anchor"`
	Dir    string   `json:"dir,omitempty" jsonschema:"require directory anchor"`
	Branch string   `json:"branch,omitempty" jsonschema:"require branch anchor"`
	All    bool     `json:"all,omitempty" jsonschema:"include archived runbooks (default active only)"`
}

type runbookEditArgs struct {
	ID        string   `json:"id" jsonschema:"runbook id prefix"`
	Title     string   `json:"title,omitempty" jsonschema:"new title"`
	Body      string   `json:"body,omitempty" jsonschema:"new description"`
	AddLabels []string `json:"add_labels,omitempty" jsonschema:"labels to add"`
	RmLabels  []string `json:"rm_labels,omitempty" jsonschema:"labels to remove"`
	anchorEditArgs
}

type runbookSearchArgs struct {
	Query  string   `json:"query" jsonschema:"search query (matches title, labels, description, and step text)"`
	Labels []string `json:"labels,omitempty" jsonschema:"require every label (ANDed; echoed as 'tags' in the DTO)"`
	Limit  *int     `json:"limit,omitempty" jsonschema:"maximum results (0 = all; default 20)"`
	Author string   `json:"author,omitempty" jsonschema:"require author"`
	Path   string   `json:"path,omitempty" jsonschema:"require path anchor"`
	Commit string   `json:"commit,omitempty" jsonschema:"require commit anchor"`
	Dir    string   `json:"dir,omitempty" jsonschema:"require directory anchor"`
	Branch string   `json:"branch,omitempty" jsonschema:"require branch anchor"`
}

type runbookStepPlacementArgs struct {
	First  bool   `json:"first,omitempty" jsonschema:"place before all steps"`
	Last   bool   `json:"last,omitempty" jsonschema:"place after all steps (default for add)"`
	Before string `json:"before,omitempty" jsonschema:"place before this step id prefix"`
	After  string `json:"after,omitempty" jsonschema:"place after this step id prefix"`
}

type runbookStepAddArgs struct {
	ID      string `json:"id" jsonschema:"runbook id prefix"`
	Text    string `json:"text" jsonschema:"step text"`
	Command string `json:"command,omitempty" jsonschema:"shell command for the step"`
	runbookStepPlacementArgs
}

type runbookStepEditArgs struct {
	ID        string `json:"id" jsonschema:"runbook id prefix"`
	Step      string `json:"step" jsonschema:"step id prefix"`
	Text      string `json:"text,omitempty" jsonschema:"new step text"`
	Command   string `json:"command,omitempty" jsonschema:"new step command"`
	NoCommand bool   `json:"no_command,omitempty" jsonschema:"clear the step command"`
}

type runbookStepRefArgs struct {
	ID   string `json:"id" jsonschema:"runbook id prefix"`
	Step string `json:"step" jsonschema:"step id prefix"`
}

type runbookStepMoveArgs struct {
	ID   string `json:"id" jsonschema:"runbook id prefix"`
	Step string `json:"step" jsonschema:"step id prefix"`
	runbookStepPlacementArgs
}

type runbookStepListArgs struct {
	ID string `json:"id" jsonschema:"runbook id prefix"`
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

type runbookRunListArgs struct {
	ID string `json:"id" jsonschema:"runbook id prefix"`
}

type runbookRunShowArgs struct {
	ID  string `json:"id" jsonschema:"runbook id prefix"`
	Run string `json:"run" jsonschema:"run id prefix"`
}

func runbookStepPlacementFlags(flags []string, in runbookStepPlacementArgs) []string {
	flags = optBool(flags, "--first", in.First)
	flags = optBool(flags, "--last", in.Last)
	flags = optStr(flags, "--before", in.Before)
	flags = optStr(flags, "--after", in.After)
	return flags
}

func registerRunbook(srv *mcp.Server, b *bridge) {
	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_add", Description: "Create a runbook (a repeatable step-by-step operational procedure), optionally with its first steps."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookAddArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			flags = optRepeated(flags, "--label", in.Labels)
			flags = anchorSetFlags(flags, in.anchorSetArgs)
			flags = optRepeated(flags, "--step", in.Steps)
			return b.run(ctx, argvFor([]string{"runbook", "add"}, flags, in.Title)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_list", Description: "List runbooks, optionally filtered by label and anchors (active only unless all is set)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookListArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optStr(flags, "--path", in.Path)
			flags = optStr(flags, "--commit", in.Commit)
			flags = optStr(flags, "--dir", in.Dir)
			flags = optStr(flags, "--branch", in.Branch)
			flags = optBool(flags, "--all", in.All)
			return b.run(ctx, argvFor([]string{"runbook", "list"}, flags)...)
		})

	idTool(srv, b, "runbook_show", "Show one runbook with its steps and runs.", "runbook", "show")

	statusTools(srv, b, "runbook", "activate", "archive")

	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_edit", Description: "Edit a runbook's title, description, labels, and anchors."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookEditArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--title", in.Title)
			flags, err := freeTextFlag(flags, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			flags = optRepeated(flags, "--add-label", in.AddLabels)
			flags = optRepeated(flags, "--rm-label", in.RmLabels)
			flags = anchorEditFlags(flags, in.anchorEditArgs)
			return b.run(ctx, argvFor([]string{"runbook", "edit"}, flags, in.ID)...)
		})

	idTool(srv, b, "runbook_rm", "Tombstone a runbook.", "runbook", "rm")

	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_search", Description: "Ranked search across runbook titles, labels, descriptions, and step text."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookSearchArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optInt(flags, "--limit", in.Limit)
			flags = optStr(flags, "--author", in.Author)
			flags = optStr(flags, "--path", in.Path)
			flags = optStr(flags, "--commit", in.Commit)
			flags = optStr(flags, "--dir", in.Dir)
			flags = optStr(flags, "--branch", in.Branch)
			return b.run(ctx, argvFor([]string{"runbook", "search"}, flags, in.Query)...)
		})

	commentTool(srv, b, "runbook")

	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_step_add", Description: "Add a positioned step to a runbook's procedure (default last)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookStepAddArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--command", in.Command)
			flags, err := freeTextFlag(flags, "--text", in.Text)
			if err != nil {
				return nil, nil, err
			}
			flags = runbookStepPlacementFlags(flags, in.runbookStepPlacementArgs)
			return b.run(ctx, argvFor([]string{"runbook", "step", "add"}, flags, in.ID)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_step_rm", Description: "Remove a step from a runbook."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookStepRefArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"runbook", "step", "rm"}, []string{"--json"}, in.ID, in.Step)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_step_edit", Description: "Edit a runbook step's text or command."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookStepEditArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--text", in.Text)
			if err != nil {
				return nil, nil, err
			}
			flags = optStr(flags, "--command", in.Command)
			flags = optBool(flags, "--no-command", in.NoCommand)
			return b.run(ctx, argvFor([]string{"runbook", "step", "edit"}, flags, in.ID, in.Step)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_step_move", Description: "Reorder a step within a runbook."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookStepMoveArgs) (*mcp.CallToolResult, any, error) {
			flags := runbookStepPlacementFlags([]string{"--json"}, in.runbookStepPlacementArgs)
			return b.run(ctx, argvFor([]string{"runbook", "step", "move"}, flags, in.ID, in.Step)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_step_list", Description: "List a runbook's ordered steps."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookStepListArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"runbook", "step", "list"}, []string{"--json"}, in.ID)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_run_start", Description: "Start a tracked run of a runbook, optionally linked to a task."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookRunStartArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--task", in.Task)
			return b.run(ctx, argvFor([]string{"runbook", "run", "start"}, flags, in.ID)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_run_list", Description: "List a runbook's tracked runs."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookRunListArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"runbook", "run", "list"}, []string{"--json"}, in.ID)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "runbook_run_show", Description: "Show one run's per-step results."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in runbookRunShowArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"runbook", "run", "show"}, []string{"--json"}, in.ID, in.Run)...)
		})

	for _, verb := range []string{"done", "skip", "fail"} {
		mcp.AddTool(srv, &mcp.Tool{Name: "runbook_run_" + verb, Description: "Record a step result (" + verb + ") in a run; re-mark to correct a wrong result."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in runbookRunStepArgs) (*mcp.CallToolResult, any, error) {
				flags, err := freeTextFlag([]string{"--json"}, "--note", in.Note)
				if err != nil {
					return nil, nil, err
				}
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
