package mcpserver

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type answerAddArgs struct {
	Title  string   `json:"title" jsonschema:"the question the user answered, verbatim"`
	Body   string   `json:"body,omitempty" jsonschema:"the chosen answer on the first line, then optional 'Options: a | b' and 'Notes: ...' lines"`
	Labels []string `json:"labels,omitempty" jsonschema:"labels such as scope:durable and header:<chip> (echoed as 'tags' in the answer DTO)"`
	anchorSetArgs
	Attach []string `json:"attach,omitempty" jsonschema:"file paths to attach via git-lfs (uploaded on sync)"`
}

func registerAnswer(ts *toolset, b *bridge) {
	addTool(ts, &mcp.Tool{Name: "answer_add", Description: "Record a user's answer to a question: the question verbatim as title, the chosen answer in body, with the note freshness lifecycle. The ack is a summary carrying the answer body."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in answerAddArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			flags = optRepeated(flags, "--label", in.Labels)
			flags = anchorSetFlags(flags, in.anchorSetArgs)
			flags = optRepeated(flags, "--attach", in.Attach)
			return b.run(ctx, argvFor([]string{"answer", "add"}, flags, in.Title)...)
		})

	addTool(ts, &mcp.Tool{Name: "answer_edit", Description: "Edit an answer: question, answer body, labels, anchors, and attachments. The ack is a summary carrying the answer body."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in noteEditArgs) (*mcp.CallToolResult, any, error) {
			flags, err := noteDocEditFlags(in)
			if err != nil {
				return nil, nil, err
			}
			return b.run(ctx, argvFor([]string{"answer", "edit"}, flags, in.ID)...)
		})

	registerNoteDocShared(ts, b, "answer", "Returns summaries carrying each answer body; answer_show reads one back in full.")
}
