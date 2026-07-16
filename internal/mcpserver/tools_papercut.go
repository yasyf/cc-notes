package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type papercutArgs struct {
	Body  string `json:"body" jsonschema:"one-paragraph friction complaint"`
	Model string `json:"model,omitempty" jsonschema:"model identity to record on the entry (default: CC_NOTES_MODEL)"`
}

func registerPapercut(ts *toolset, b *bridge) {
	addTool(ts, &mcp.Tool{Name: "papercut", Description: "File a one-paragraph friction complaint (dead-end tool call, broken link, misleading doc) to the repo-wide papercut journal."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in papercutArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--model", in.Model)
			return b.run(ctx, argvFor([]string{"papercut"}, flags, in.Body)...)
		})

	addTool(ts, &mcp.Tool{Name: "papercut_list", Description: "List every papercut complaint in timestamp order."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{"papercut", "list"}, []string{"--json"})...)
		})
}
