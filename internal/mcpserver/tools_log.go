package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type logAddArgs struct {
	Title  string   `json:"title" jsonschema:"short handle for the log"`
	Entry  string   `json:"entry,omitempty" jsonschema:"optional first entry text"`
	Labels []string `json:"labels,omitempty" jsonschema:"labels (echoed as 'tags' in the log DTO)"`
	anchorSetArgs
	Attach []string `json:"attach,omitempty" jsonschema:"file paths to attach via git-lfs"`
}

type logAppendArgs struct {
	ID      string   `json:"id" jsonschema:"log id prefix"`
	Entry   string   `json:"entry,omitempty" jsonschema:"entry text (required unless attach is given)"`
	Attach  []string `json:"attach,omitempty" jsonschema:"file paths to attach via git-lfs"`
	Replace bool     `json:"replace,omitempty" jsonschema:"allow attach to overwrite a live attachment with the same name"`
}

type logEditArgs struct {
	ID        string   `json:"id" jsonschema:"log id prefix"`
	Title     string   `json:"title,omitempty" jsonschema:"new title"`
	AddLabels []string `json:"add_labels,omitempty" jsonschema:"labels to add"`
	RmLabels  []string `json:"rm_labels,omitempty" jsonschema:"labels to remove"`
	anchorEditArgs
	RmAttachments []string `json:"rm_attachments,omitempty" jsonschema:"attachment names to remove"`
}

type logListArgs struct {
	Labels []string `json:"labels,omitempty" jsonschema:"require every label (ANDed; echoed as 'tags' in the DTO)"`
	Path   string   `json:"path,omitempty" jsonschema:"require path anchor"`
	Commit string   `json:"commit,omitempty" jsonschema:"require commit anchor"`
	Dir    string   `json:"dir,omitempty" jsonschema:"require directory anchor"`
	Branch string   `json:"branch,omitempty" jsonschema:"require branch anchor"`
	All    bool     `json:"all,omitempty" jsonschema:"include tombstoned logs"`
}

func registerLog(ts *toolset, b *bridge) {
	addTool(ts, &mcp.Tool{Name: "log_add", Description: "Create an append-only log (incident timeline, rollout log, debugging record)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in logAddArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--entry", in.Entry)
			if err != nil {
				return nil, nil, err
			}
			flags = optRepeated(flags, "--label", in.Labels)
			flags = anchorSetFlags(flags, in.anchorSetArgs)
			flags = optRepeated(flags, "--attach", in.Attach)
			return b.run(ctx, argvFor([]string{"log", "add"}, flags, in.Title)...)
		})

	addTool(ts, &mcp.Tool{Name: "log_append", Description: "Append one entry to a log, and/or attach files. Entries are append-only."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in logAppendArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--entry", in.Entry)
			if err != nil {
				return nil, nil, err
			}
			flags = optRepeated(flags, "--attach", in.Attach)
			flags = optBool(flags, "--replace", in.Replace)
			return b.run(ctx, argvFor([]string{"log", "append"}, flags, in.ID)...)
		})

	addTool(ts, &mcp.Tool{Name: "log_edit", Description: "Edit a log's title, labels, and anchors (entries are append-only)."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in logEditArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--title", in.Title)
			flags = optRepeated(flags, "--add-label", in.AddLabels)
			flags = optRepeated(flags, "--rm-label", in.RmLabels)
			flags = anchorEditFlags(flags, in.anchorEditArgs)
			flags = optRepeated(flags, "--rm-attachment", in.RmAttachments)
			return b.run(ctx, argvFor([]string{"log", "edit"}, flags, in.ID)...)
		})

	idTool(ts, b, "log_rm", "Tombstone a log.", "log", "rm")

	addTool(ts, &mcp.Tool{Name: "log_list", Description: "List logs, optionally filtered by label and anchors."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in logListArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optStr(flags, "--path", in.Path)
			flags = optStr(flags, "--commit", in.Commit)
			flags = optStr(flags, "--dir", in.Dir)
			flags = optStr(flags, "--branch", in.Branch)
			flags = optBool(flags, "--all", in.All)
			return b.run(ctx, argvFor([]string{"log", "list"}, flags)...)
		})

	idTool(ts, b, "log_show", "Show one log with its entries in chronological order.", "log", "show")

	addTool(ts, &mcp.Tool{Name: "log_search", Description: "Ranked search across log titles, labels, and entry text."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in entitySearchArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optInt(flags, "--limit", in.Limit)
			flags = optStr(flags, "--author", in.Author)
			flags = optStr(flags, "--path", in.Path)
			flags = optStr(flags, "--dir", in.Dir)
			flags = optStr(flags, "--branch", in.Branch)
			flags = optStr(flags, "--commit", in.Commit)
			return b.run(ctx, argvFor([]string{"log", "search"}, flags, in.Query)...)
		})
}
