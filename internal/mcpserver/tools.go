package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// idTool registers a tool that takes only an entity id and runs the sub
// subcommand with --json against it.
func idTool(srv *mcp.Server, b *bridge, name, desc string, sub ...string) {
	mcp.AddTool(srv, &mcp.Tool{Name: name, Description: desc},
		func(ctx context.Context, _ *mcp.CallToolRequest, in entityIDArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor(sub, []string{"--json"}, in.ID)...)
		})
}

// commentTool registers "<noun>_comment", appending a comment to an entity.
func commentTool(srv *mcp.Server, b *bridge, noun string) {
	mcp.AddTool(srv, &mcp.Tool{Name: noun + "_comment", Description: "Add a comment to a " + noun + "."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in commentArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor([]string{noun, "comment"}, []string{"--json"}, in.ID, in.Body)...)
		})
}

// statusTools registers a "<noun>_<verb>" transition tool for each verb.
func statusTools(srv *mcp.Server, b *bridge, noun string, verbs ...string) {
	for _, verb := range verbs {
		mcp.AddTool(srv, &mcp.Tool{Name: noun + "_" + verb, Description: "Transition a " + noun + " to " + verb + "."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in entityIDArgs) (*mcp.CallToolResult, any, error) {
				return b.run(ctx, argvFor([]string{noun, verb}, []string{"--json"}, in.ID)...)
			})
	}
}
