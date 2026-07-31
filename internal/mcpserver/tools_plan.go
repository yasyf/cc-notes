package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type planAddArgs struct {
	Title    string   `json:"title" jsonschema:"short handle for the plan"`
	Body     string   `json:"body,omitempty" jsonschema:"the plan text, verbatim — context, approach, pitfalls, verification (required unless body_file is given)"`
	BodyFile string   `json:"body_file,omitempty" jsonschema:"read the plan text from this file instead of body"`
	Approved bool     `json:"approved,omitempty" jsonschema:"record the plan already approved instead of draft"`
	Labels   []string `json:"labels,omitempty" jsonschema:"labels"`
	anchorSetArgs
}

type planListArgs struct {
	Status string   `json:"status,omitempty" jsonschema:"status filter, comma-separated (default draft,approved,executing; mutually exclusive with all)"`
	All    bool     `json:"all,omitempty" jsonschema:"every status (mutually exclusive with status)"`
	Labels []string `json:"labels,omitempty" jsonschema:"require every label (ANDed)"`
	Path   string   `json:"path,omitempty" jsonschema:"require path anchor"`
	Commit string   `json:"commit,omitempty" jsonschema:"require commit anchor"`
	Dir    string   `json:"dir,omitempty" jsonschema:"require directory anchor"`
	Branch string   `json:"branch,omitempty" jsonschema:"require branch anchor"`
}

type planEditArgs struct {
	ID        string   `json:"id" jsonschema:"plan id prefix"`
	Title     string   `json:"title,omitempty" jsonschema:"new title"`
	Body      string   `json:"body,omitempty" jsonschema:"new plan text, verbatim (replaces the recorded text)"`
	Outcome   string   `json:"outcome,omitempty" jsonschema:"what executing the plan produced"`
	AddLabels []string `json:"add_labels,omitempty" jsonschema:"labels to add"`
	RmLabels  []string `json:"rm_labels,omitempty" jsonschema:"labels to remove"`
	anchorEditArgs
}

type planCloseArgs struct {
	ID      string `json:"id" jsonschema:"plan id prefix"`
	Outcome string `json:"outcome,omitempty" jsonschema:"what executing the plan produced"`
}

type planSupersedeArgs struct {
	ID    string `json:"id" jsonschema:"id prefix of the superseded (old) plan"`
	By    string `json:"by" jsonschema:"id prefix of the plan that replaces it"`
	Clear bool   `json:"clear,omitempty" jsonschema:"remove the supersede edge instead of adding it"`
}

type planSearchArgs struct {
	Query  string   `json:"query" jsonschema:"search query (matches titles, plan text, and outcomes)"`
	Labels []string `json:"labels,omitempty" jsonschema:"require every label (ANDed)"`
	Limit  *int     `json:"limit,omitempty" jsonschema:"maximum results (0 = all; default 20)"`
	Author string   `json:"author,omitempty" jsonschema:"require author"`
	Path   string   `json:"path,omitempty" jsonschema:"require path anchor"`
	Commit string   `json:"commit,omitempty" jsonschema:"require commit anchor"`
	Dir    string   `json:"dir,omitempty" jsonschema:"require directory anchor"`
	Branch string   `json:"branch,omitempty" jsonschema:"require branch anchor"`
}

func registerPlan(ts *toolset, b *bridge) {
	addTool(ts, &mcp.Tool{Name: "plan_add", Description: "Record an approved plan verbatim — context, approach, pitfalls, verification — as a durable record the next agent can execute. The ack is a summary; plan_show reads the text and the tasks pointing at it back."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in planAddArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			flags = optStr(flags, "--body-file", in.BodyFile)
			flags = optBool(flags, "--approved", in.Approved)
			flags = optRepeated(flags, "--label", in.Labels)
			flags = anchorSetFlags(flags, in.anchorSetArgs)
			return b.run(ctx, argvFor([]string{"plan", "add"}, flags, in.Title)...)
		})

	addTool(ts, &mcp.Tool{Name: "plan_list", Description: "List plans, optionally filtered by status, label, and anchors (in-flight only unless status or all is set). Returns summaries; plan_show reads the text and the tasks pointing at it back."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in planListArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--status", in.Status)
			flags = optBool(flags, "--all", in.All)
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optStr(flags, "--path", in.Path)
			flags = optStr(flags, "--commit", in.Commit)
			flags = optStr(flags, "--dir", in.Dir)
			flags = optStr(flags, "--branch", in.Branch)
			return b.run(ctx, argvFor([]string{"plan", "list"}, flags)...)
		})

	idTool(ts, b, "plan_show", "Show one plan with its recorded text, outcome, and the tasks pointing at it.", "plan", "show")

	addTool(ts, &mcp.Tool{Name: "plan_edit", Description: "Edit a plan's title, recorded text, outcome, labels, and anchors. Body is last-writer-wins, so re-approving a revised draft under the same title lands here and every earlier draft stays in history — plan_supersede is reserved for a genuine replan once execution has started."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in planEditArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--title", in.Title)
			flags, err := freeTextFlag(flags, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			flags, err = freeTextFlag(flags, "--outcome", in.Outcome)
			if err != nil {
				return nil, nil, err
			}
			flags = optRepeated(flags, "--add-label", in.AddLabels)
			flags = optRepeated(flags, "--rm-label", in.RmLabels)
			flags = anchorEditFlags(flags, in.anchorEditArgs)
			return b.run(ctx, argvFor([]string{"plan", "edit"}, flags, in.ID)...)
		})

	statusTools(ts, b, "plan", "approve")

	addTool(ts, &mcp.Tool{Name: "plan_start", Description: "Move a plan into executing. Legal from approved, done, or abandoned; plan_reopen is the same move named for a closed plan."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in entityIDArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"plan", "start"}, []string{"--json"}, in.ID)...)
		})

	addTool(ts, &mcp.Tool{Name: "plan_reopen", Description: "Resume executing a done or abandoned plan. The same move as plan_start, which is legal from approved, done, or abandoned alike."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in entityIDArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"plan", "reopen"}, []string{"--json"}, in.ID)...)
		})

	addTool(ts, &mcp.Tool{Name: "plan_done", Description: "Close an executing plan as done, recording what it produced."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in planCloseArgs) (*mcp.CallToolResult, any, error) {
			return planCloseCall(ctx, b, "done", in)
		})

	addTool(ts, &mcp.Tool{Name: "plan_abandon", Description: "Close a plan as abandoned, recording why."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in planCloseArgs) (*mcp.CallToolResult, any, error) {
			return planCloseCall(ctx, b, "abandon", in)
		})

	commentTool(ts, b, "plan")

	addTool(ts, &mcp.Tool{Name: "plan_supersede", Description: "Record that a newer plan replaces this one after a genuine replan, the way note_supersede does for notes — supersession is an edge, not a status. A plan revised before work starts is plan_edit'ed in place instead, with no edge."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in planSupersedeArgs) (*mcp.CallToolResult, any, error) {
			flags := optStr([]string{"--json"}, "--by", in.By)
			flags = optBool(flags, "--clear", in.Clear)
			return b.run(ctx, argvFor([]string{"plan", "supersede"}, flags, in.ID)...)
		})

	addTool(ts, &mcp.Tool{Name: "plan_search", Description: "Ranked search across plan titles, recorded text, and outcomes. Returns summaries; plan_show reads the text and the tasks pointing at it back."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in planSearchArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optInt(flags, "--limit", in.Limit)
			flags = optStr(flags, "--author", in.Author)
			flags = optStr(flags, "--path", in.Path)
			flags = optStr(flags, "--commit", in.Commit)
			flags = optStr(flags, "--dir", in.Dir)
			flags = optStr(flags, "--branch", in.Branch)
			return b.run(ctx, argvFor([]string{"plan", "search"}, flags, in.Query)...)
		})

	idTool(ts, b, "plan_rm", "Tombstone a plan.", "plan", "rm")
}

// planCloseCall runs a terminal plan verb, whose optional outcome lands in the
// same pack commit as the status.
func planCloseCall(ctx context.Context, b *bridge, verb string, in planCloseArgs) (*mcp.CallToolResult, any, error) {
	flags, err := freeTextFlag([]string{"--json"}, "--outcome", in.Outcome)
	if err != nil {
		return nil, nil, err
	}
	return b.run(ctx, argvFor([]string{"plan", verb}, flags, in.ID)...)
}
