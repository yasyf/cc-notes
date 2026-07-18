package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type investigationOpenArgs struct {
	Title    string   `json:"title" jsonschema:"short handle for the investigation; keep the verdict out of the title"`
	Premise  string   `json:"premise" jsonschema:"falsifiable suspicion being investigated"`
	Findings []string `json:"findings,omitempty" jsonschema:"initial finding texts"`
	Labels   []string `json:"labels,omitempty" jsonschema:"labels"`
	anchorSetArgs
	Attach []string `json:"attach,omitempty" jsonschema:"file paths to attach via git-lfs"`
}

type investigationListArgs struct {
	Status string   `json:"status,omitempty" jsonschema:"status filter, comma-separated (default open,root_caused,fixed; mutually exclusive with all)"`
	All    bool     `json:"all,omitempty" jsonschema:"every status (mutually exclusive with status)"`
	Labels []string `json:"labels,omitempty" jsonschema:"require every label (ANDed)"`
	Path   string   `json:"path,omitempty" jsonschema:"require path anchor"`
	Commit string   `json:"commit,omitempty" jsonschema:"require commit anchor"`
	Dir    string   `json:"dir,omitempty" jsonschema:"require directory anchor"`
	Branch string   `json:"branch,omitempty" jsonschema:"require branch anchor"`
}

type investigationAppendArgs struct {
	ID     string   `json:"id" jsonschema:"investigation id prefix"`
	Text   string   `json:"text,omitempty" jsonschema:"evidence text (required unless attach is given)"`
	Attach []string `json:"attach,omitempty" jsonschema:"file paths to attach via git-lfs"`
}

type investigationFindingAddArgs struct {
	ID   string `json:"id" jsonschema:"investigation id prefix"`
	Text string `json:"text" jsonschema:"finding text"`
}

type investigationFindingEditArgs struct {
	ID      string `json:"id" jsonschema:"investigation id prefix"`
	Finding string `json:"finding" jsonschema:"finding id prefix"`
	Text    string `json:"text" jsonschema:"new finding text"`
}

type investigationFindingDispositionArgs struct {
	ID      string `json:"id" jsonschema:"investigation id prefix"`
	Finding string `json:"finding" jsonschema:"finding id prefix"`
	Why     string `json:"why" jsonschema:"evidence supporting the finding disposition"`
}

type investigationFindingRefArgs struct {
	ID      string `json:"id" jsonschema:"investigation id prefix"`
	Finding string `json:"finding" jsonschema:"finding id prefix"`
}

type investigationFindingListArgs struct {
	ID string `json:"id" jsonschema:"investigation id prefix"`
}

type investigationTransitionArgs struct {
	ID   string `json:"id" jsonschema:"investigation id prefix"`
	Text string `json:"text" jsonschema:"evidence or reason for the transition"`
}

type investigationFixArgs struct {
	ID      string   `json:"id" jsonschema:"investigation id prefix"`
	Text    string   `json:"text,omitempty" jsonschema:"fix summary"`
	Commits []string `json:"commits" jsonschema:"fixing commits (at least one required)"`
}

type investigationAbandonArgs struct {
	ID   string `json:"id" jsonschema:"investigation id prefix"`
	Text string `json:"text,omitempty" jsonschema:"reason for abandoning the investigation"`
}

type investigationEditArgs struct {
	ID        string   `json:"id" jsonschema:"investigation id prefix"`
	Title     string   `json:"title,omitempty" jsonschema:"new title; keep the verdict out of the title"`
	Body      string   `json:"body,omitempty" jsonschema:"new resolution summary"`
	AddLabels []string `json:"add_labels,omitempty" jsonschema:"labels to add"`
	RmLabels  []string `json:"rm_labels,omitempty" jsonschema:"labels to remove"`
	anchorEditArgs
}

type investigationSearchArgs struct {
	Query  string   `json:"query" jsonschema:"search query (matches titles, premises, timelines, findings, and verdicts)"`
	Labels []string `json:"labels,omitempty" jsonschema:"require every label (ANDed)"`
	Limit  *int     `json:"limit,omitempty" jsonschema:"maximum results (0 = all; default 20)"`
	Author string   `json:"author,omitempty" jsonschema:"require author"`
	Path   string   `json:"path,omitempty" jsonschema:"require path anchor"`
	Commit string   `json:"commit,omitempty" jsonschema:"require commit anchor"`
	Dir    string   `json:"dir,omitempty" jsonschema:"require directory anchor"`
	Branch string   `json:"branch,omitempty" jsonschema:"require branch anchor"`
}

func investigationTextPositionals(id, text string, required bool) ([]string, error) {
	if text == "-" {
		return nil, errStdinDash
	}
	positionals := []string{id}
	if required || text != "" {
		positionals = append(positionals, text)
	}
	return positionals, nil
}

func registerInvestigation(ts *toolset, b *bridge) {
	addTool(ts, &mcp.Tool{Name: "investigation_open", Description: "Open an investigation around a falsifiable premise."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationOpenArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--body", in.Premise)
			if err != nil {
				return nil, nil, err
			}
			flags = optRepeated(flags, "--finding", in.Findings)
			flags = optRepeated(flags, "--label", in.Labels)
			flags = anchorSetFlags(flags, in.anchorSetArgs)
			flags = optRepeated(flags, "--attach", in.Attach)
			return b.run(ctx, argvFor([]string{"investigation", "open"}, flags, in.Title)...)
		})

	addTool(ts, &mcp.Tool{Name: "investigation_list", Description: "List investigations, optionally filtered by status, label, and anchors."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationListArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--status", in.Status)
			flags = optBool(flags, "--all", in.All)
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optStr(flags, "--path", in.Path)
			flags = optStr(flags, "--commit", in.Commit)
			flags = optStr(flags, "--dir", in.Dir)
			flags = optStr(flags, "--branch", in.Branch)
			return b.run(ctx, argvFor([]string{"investigation", "list"}, flags)...)
		})

	idTool(ts, b, "investigation_show", "Show one investigation with its findings and evidence timeline.", "investigation", "show")

	addTool(ts, &mcp.Tool{Name: "investigation_append", Description: "Append evidence from one investigation step, and/or attach files."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationAppendArgs) (*mcp.CallToolResult, any, error) {
			positionals, err := investigationTextPositionals(in.ID, in.Text, false)
			if err != nil {
				return nil, nil, err
			}
			flags := optRepeated([]string{"--json"}, "--attach", in.Attach)
			return b.run(ctx, argvFor([]string{"investigation", "append"}, flags, positionals...)...)
		})

	addTool(ts, &mcp.Tool{Name: "investigation_finding_add", Description: "Add an open finding to an investigation."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationFindingAddArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--body", in.Text)
			if err != nil {
				return nil, nil, err
			}
			return b.run(ctx, argvFor([]string{"investigation", "finding", "add"}, flags, in.ID)...)
		})

	addTool(ts, &mcp.Tool{Name: "investigation_finding_edit", Description: "Edit a finding's text."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationFindingEditArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--body", in.Text)
			if err != nil {
				return nil, nil, err
			}
			return b.run(ctx, argvFor([]string{"investigation", "finding", "edit"}, flags, in.ID, in.Finding)...)
		})

	addTool(ts, &mcp.Tool{Name: "investigation_finding_clear", Description: "Clear a finding with supporting evidence."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationFindingDispositionArgs) (*mcp.CallToolResult, any, error) {
			flags := optStr([]string{"--json"}, "--why", in.Why)
			return b.run(ctx, argvFor([]string{"investigation", "finding", "clear"}, flags, in.ID, in.Finding)...)
		})

	addTool(ts, &mcp.Tool{Name: "investigation_finding_confirm", Description: "Confirm a finding with supporting evidence."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationFindingDispositionArgs) (*mcp.CallToolResult, any, error) {
			flags := optStr([]string{"--json"}, "--why", in.Why)
			return b.run(ctx, argvFor([]string{"investigation", "finding", "confirm"}, flags, in.ID, in.Finding)...)
		})

	addTool(ts, &mcp.Tool{Name: "investigation_finding_rm", Description: "Remove a finding from an investigation."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationFindingRefArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"investigation", "finding", "rm"}, []string{"--json"}, in.ID, in.Finding)...)
		})

	addTool(ts, &mcp.Tool{Name: "investigation_finding_list", Description: "List an investigation's findings."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationFindingListArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"investigation", "finding", "list"}, []string{"--json"}, in.ID)...)
		})

	addTool(ts, &mcp.Tool{Name: "investigation_root_cause", Description: "Record the root cause with supporting evidence."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationTransitionArgs) (*mcp.CallToolResult, any, error) {
			positionals, err := investigationTextPositionals(in.ID, in.Text, true)
			if err != nil {
				return nil, nil, err
			}
			return b.run(ctx, argvFor([]string{"investigation", "root-cause"}, []string{"--json"}, positionals...)...)
		})

	addTool(ts, &mcp.Tool{Name: "investigation_fix", Description: "Record at least one fixing commit and mark an investigation fixed."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationFixArgs) (*mcp.CallToolResult, any, error) {
			positionals, err := investigationTextPositionals(in.ID, in.Text, false)
			if err != nil {
				return nil, nil, err
			}
			flags := optRepeated([]string{"--json"}, "--commit", in.Commits)
			return b.run(ctx, argvFor([]string{"investigation", "fix"}, flags, positionals...)...)
		})

	addTool(ts, &mcp.Tool{Name: "investigation_confirm", Description: "Confirm an investigation's fix with proof."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationTransitionArgs) (*mcp.CallToolResult, any, error) {
			positionals, err := investigationTextPositionals(in.ID, in.Text, true)
			if err != nil {
				return nil, nil, err
			}
			return b.run(ctx, argvFor([]string{"investigation", "confirm"}, []string{"--json"}, positionals...)...)
		})

	addTool(ts, &mcp.Tool{Name: "investigation_exonerate", Description: "Falsify the investigation premise with evidence."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationTransitionArgs) (*mcp.CallToolResult, any, error) {
			positionals, err := investigationTextPositionals(in.ID, in.Text, true)
			if err != nil {
				return nil, nil, err
			}
			return b.run(ctx, argvFor([]string{"investigation", "exonerate"}, []string{"--json"}, positionals...)...)
		})

	addTool(ts, &mcp.Tool{Name: "investigation_reopen", Description: "Reopen an investigation with a reason."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationTransitionArgs) (*mcp.CallToolResult, any, error) {
			positionals, err := investigationTextPositionals(in.ID, in.Text, true)
			if err != nil {
				return nil, nil, err
			}
			return b.run(ctx, argvFor([]string{"investigation", "reopen"}, []string{"--json"}, positionals...)...)
		})

	addTool(ts, &mcp.Tool{Name: "investigation_abandon", Description: "Abandon an investigation without a verdict."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationAbandonArgs) (*mcp.CallToolResult, any, error) {
			positionals, err := investigationTextPositionals(in.ID, in.Text, false)
			if err != nil {
				return nil, nil, err
			}
			return b.run(ctx, argvFor([]string{"investigation", "abandon"}, []string{"--json"}, positionals...)...)
		})

	addTool(ts, &mcp.Tool{Name: "investigation_edit", Description: "Edit an investigation's title, resolution, labels, and anchors."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationEditArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--title", in.Title)
			flags, err := freeTextFlag(flags, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			flags = optRepeated(flags, "--add-label", in.AddLabels)
			flags = optRepeated(flags, "--rm-label", in.RmLabels)
			flags = anchorEditFlags(flags, in.anchorEditArgs)
			return b.run(ctx, argvFor([]string{"investigation", "edit"}, flags, in.ID)...)
		})

	addTool(ts, &mcp.Tool{Name: "investigation_search", Description: "Ranked search across investigation titles, premises, timelines, findings, and verdicts."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in investigationSearchArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optInt(flags, "--limit", in.Limit)
			flags = optStr(flags, "--author", in.Author)
			flags = optStr(flags, "--path", in.Path)
			flags = optStr(flags, "--commit", in.Commit)
			flags = optStr(flags, "--dir", in.Dir)
			flags = optStr(flags, "--branch", in.Branch)
			return b.run(ctx, argvFor([]string{"investigation", "search"}, flags, in.Query)...)
		})

	idTool(ts, b, "investigation_rm", "Tombstone an investigation.", "investigation", "rm")
}
