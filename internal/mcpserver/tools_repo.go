package mcpserver

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type statusArgs struct{}

type relevantArgs struct {
	Path     string `json:"path" jsonschema:"repository path to weigh notes, docs, and tasks against"`
	Branch   string `json:"branch,omitempty" jsonschema:"branch to weigh against (default: current HEAD branch)"`
	Base     string `json:"base,omitempty" jsonschema:"merge-base reference for cross-author signals (default: remote default branch)"`
	Limit    *int   `json:"limit,omitempty" jsonschema:"maximum results (negative: unlimited; default 10)"`
	Attached bool   `json:"attached,omitempty" jsonschema:"keep only entities anchored to the path or a parent directory"`
	Worktree bool   `json:"worktree,omitempty" jsonschema:"drift-check path anchors against uncommitted working-tree edits"`
}

type syncArgs struct {
	Remote string `json:"remote,omitempty" jsonschema:"remote to sync with (default: origin)"`
	Full   bool   `json:"full,omitempty" jsonschema:"force a whole-namespace reconcile scan"`
}

type reconcileArgs struct {
	Into   string   `json:"into,omitempty" jsonschema:"target branch (default: current branch)"`
	From   []string `json:"from,omitempty" jsonschema:"source branch to reconcile (repeatable; default: auto-discover)"`
	Force  bool     `json:"force,omitempty" jsonschema:"skip the merge-ancestry test (requires from)"`
	DryRun bool     `json:"dry_run,omitempty" jsonschema:"report what would change without writing"`
}

type historyArgs struct {
	ID      string `json:"id" jsonschema:"entity id prefix (resolved across note, doc, log, task, sprint, project)"`
	Reverse bool   `json:"reverse,omitempty" jsonschema:"oldest first (chronological); default is newest first"`
	Limit   *int   `json:"limit,omitempty" jsonschema:"show at most N most recent entries (0 = all)"`
}

type searchArgs struct {
	Query  string   `json:"query" jsonschema:"search query (matches titles, labels, bodies, log entries, and runbook steps)"`
	Labels []string `json:"labels,omitempty" jsonschema:"require every label (ANDed; echoed as 'tags' in entity DTOs)"`
	Limit  *int     `json:"limit,omitempty" jsonschema:"maximum results (0 = all; default 20)"`
	Path   string   `json:"path,omitempty" jsonschema:"require path anchor"`
	Commit string   `json:"commit,omitempty" jsonschema:"require commit anchor"`
	Dir    string   `json:"dir,omitempty" jsonschema:"require directory anchor"`
	Branch string   `json:"branch,omitempty" jsonschema:"require branch anchor"`
}

type blameArgs struct {
	SHA string `json:"sha" jsonschema:"commit sha (or prefix) to find the tasks that produced it"`
}

type attachmentPathArgs struct {
	ID   string `json:"id" jsonschema:"owning note, doc, or log id prefix"`
	Name string `json:"name" jsonschema:"attachment file name"`
}

type attachmentGetArgs struct {
	ID     string `json:"id" jsonschema:"owning note, doc, or log id prefix"`
	Name   string `json:"name" jsonschema:"attachment file name"`
	Output string `json:"output" jsonschema:"file path to write the attachment bytes to (required; binary never flows through the result)"`
}

func registerRepo(ts *toolset, b *bridge) {
	addTool(ts, &mcp.Tool{Name: "status", Description: "Orient on the backlog: tasks in flight, who holds what, and notes and docs needing attention."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ statusArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, "status", "--json")
		})

	addTool(ts, &mcp.Tool{Name: "relevant", Description: "Surface the notes, docs, and tasks anchored to a repository path — run before editing unfamiliar code."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in relevantArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--branch", in.Branch)
			flags = optStr(flags, "--base", in.Base)
			flags = optInt(flags, "--limit", in.Limit)
			flags = optBool(flags, "--attached", in.Attached)
			flags = optBool(flags, "--worktree", in.Worktree)
			return b.run(ctx, argvFor([]string{"relevant"}, flags, in.Path)...)
		})

	addTool(ts, &mcp.Tool{Name: "sync", Description: "Sync notes, tasks, and attachment content with the remote (moves refs AND lfs bytes)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in syncArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--remote", in.Remote)
			flags = optBool(flags, "--full", in.Full)
			return b.run(ctx, argvFor([]string{"sync"}, flags)...)
		})

	addTool(ts, &mcp.Tool{Name: "reconcile", Description: "Relocate tasks onto the target branch after merging a source branch."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in reconcileArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--into", in.Into)
			flags = optRepeated(flags, "--from", in.From)
			flags = optBool(flags, "--force", in.Force)
			flags = optBool(flags, "--dry-run", in.DryRun)
			return b.run(ctx, argvFor([]string{"reconcile"}, flags)...)
		})

	addTool(ts, &mcp.Tool{Name: "history", Description: "Show the append-only op history of any entity by id prefix."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in historyArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optBool(flags, "--reverse", in.Reverse)
			flags = optInt(flags, "--limit", in.Limit)
			return b.run(ctx, argvFor([]string{"history"}, flags, in.ID)...)
		})

	addTool(ts, &mcp.Tool{Name: "search", Description: "Ranked search across every note, doc, log, and runbook."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in searchArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optInt(flags, "--limit", in.Limit)
			flags = optStr(flags, "--path", in.Path)
			flags = optStr(flags, "--commit", in.Commit)
			flags = optStr(flags, "--dir", in.Dir)
			flags = optStr(flags, "--branch", in.Branch)
			return b.run(ctx, argvFor([]string{"search"}, flags, in.Query)...)
		})

	idTool(ts, b, "show", "Show any note, doc, log, task, sprint, project, or runbook by id prefix.", "show")

	addTool(ts, &mcp.Tool{Name: "blame", Description: "Find the tasks that produced a commit, via its recorded commit links and task trailers."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in blameArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"blame"}, []string{"--json"}, in.SHA)...)
		})

	addTool(ts, &mcp.Tool{Name: "attachment_path", Description: "Print the local filesystem path of an entity's attachment object."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in attachmentPathArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"attachment", "path"}, nil, in.ID, in.Name)...)
		})

	addTool(ts, &mcp.Tool{Name: "attachment_get", Description: "Write an entity's attachment bytes to a file path (binary is never returned inline)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in attachmentGetArgs) (*mcp.CallToolResult, any, error) {
			flags := optStr(nil, "--output", in.Output)
			res, _, err := b.run(ctx, argvFor([]string{"attachment", "get"}, flags, in.ID, in.Name)...)
			if err != nil {
				return nil, nil, err
			}
			if tc := res.Content[0].(*mcp.TextContent); tc.Text == "" {
				tc.Text = fmt.Sprintf("wrote attachment %q to %s", in.Name, in.Output)
			}
			return res, nil, nil
		})
}
