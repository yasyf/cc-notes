package mcpserver

import (
	"context"
	"strconv"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type papercutArgs struct {
	Body  string `json:"body" jsonschema:"one-paragraph friction complaint"`
	Model string `json:"model,omitempty" jsonschema:"model identity to record on the entry (default: CC_NOTES_MODEL)"`
}

type papercutListArgs struct {
	Limit *int `json:"limit,omitempty" jsonschema:"maximum results (0 = all; default 20)"`
}

type papercutShowArgs struct {
	LogID string `json:"log_id" jsonschema:"journal id prefix, from a papercut_list row's log_id"`
	Index int    `json:"index" jsonschema:"the complaint's index within that journal, from a papercut_list row's index"`
}

func registerPapercut(ts *toolset, b *bridge) {
	addTool(ts, &mcp.Tool{Name: "papercut", Description: "File a one-paragraph friction complaint (dead-end tool call, broken link, misleading doc) to the repo-wide papercut journal."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in papercutArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--model", in.Model)
			return b.run(ctx, argvFor([]string{"papercut"}, flags, in.Body)...)
		})

	addTool(ts, &mcp.Tool{Name: "papercut_list", Description: "List papercut complaints newest first, capped at limit (default 20) and with each complaint's text clipped to a preview. Read one back in full with papercut_show, addressed by the row's log_id and index."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in papercutListArgs) (*mcp.CallToolResult, any, error) {
			flags := optInt([]string{"--json"}, "--limit", in.Limit)
			return b.run(ctx, argvFor([]string{"papercut", "list"}, flags)...)
		})

	addTool(ts, &mcp.Tool{Name: "papercut_show", Description: "Show one papercut complaint with its full untruncated text, addressed by the log_id and index a papercut_list row carries. The index is the complaint's position within its own journal, so it holds whatever limit the listing used."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in papercutShowArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"papercut", "show"}, []string{"--json"}, in.LogID, strconv.Itoa(in.Index))...)
		})
}
