package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type docAddArgs struct {
	noteAddArgs
	When string `json:"when,omitempty" jsonschema:"free-text read-this-when trigger"`
}

type docEditArgs struct {
	noteEditArgs
	When string `json:"when,omitempty" jsonschema:"new read-this-when trigger"`
}

func registerDoc(srv *mcp.Server, b *bridge) {
	mcp.AddTool(srv, &mcp.Tool{Name: "doc_add", Description: "Record living guidance as a doc: the FULL markdown in body, a free-text when-trigger, and the note freshness lifecycle."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in docAddArgs) (*mcp.CallToolResult, any, error) {
			flags := []string{"--json"}
			flags = optStr(flags, "--body", in.Body)
			flags = optStr(flags, "--when", in.When)
			flags = optRepeated(flags, "--label", in.Labels)
			flags = optRepeated(flags, "--commit", in.Commits)
			flags = optRepeated(flags, "--path", in.Paths)
			flags = optRepeated(flags, "--dir", in.Dirs)
			flags = optRepeated(flags, "--branch", in.Branches)
			flags = optRepeated(flags, "--attach", in.Attach)
			return b.run(ctx, argvFor([]string{"doc", "add"}, flags, in.Title)...)
		})

	mcp.AddTool(srv, &mcp.Tool{Name: "doc_edit", Description: "Edit a doc: title, body, when-trigger, labels, anchors, and attachments."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in docEditArgs) (*mcp.CallToolResult, any, error) {
			flags := optStr(noteDocEditFlags(in.noteEditArgs), "--when", in.When)
			return b.run(ctx, argvFor([]string{"doc", "edit"}, flags, in.ID)...)
		})

	registerNoteDocShared(srv, b, "doc")
}
