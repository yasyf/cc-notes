package mcpserver

import (
	"context"
	"sort"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type toolset struct {
	srv   *mcp.Server
	props map[string]toolProps
}

type toolProps struct {
	accepted []string
	required map[string]bool
}

func addTool[In, Out any](ts *toolset, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	schema, err := jsonschema.For[In](&jsonschema.ForOptions{})
	if err != nil {
		panic(err)
	}
	required := make(map[string]bool, len(schema.Required))
	accepted := make([]string, 0, len(schema.Properties))
	for _, name := range schema.Required {
		required[name] = true
		accepted = append(accepted, name)
	}
	optional := make([]string, 0, len(schema.Properties)-len(schema.Required))
	for name := range schema.Properties {
		if !required[name] {
			optional = append(optional, name)
		}
	}
	sort.Strings(optional)
	accepted = append(accepted, optional...)
	ts.props[t.Name] = toolProps{accepted: accepted, required: required}
	mcp.AddTool(ts.srv, t, h)
}

// idTool registers a tool that takes only an entity id and runs the sub
// subcommand with --json against it.
func idTool(ts *toolset, b *bridge, name, desc string, sub ...string) {
	addTool(ts, &mcp.Tool{Name: name, Description: desc},
		func(ctx context.Context, _ *mcp.CallToolRequest, in entityIDArgs) (*mcp.CallToolResult, any, error) {
			return b.run(ctx, argvFor(sub, []string{"--json"}, in.ID)...)
		})
}

// commentTool registers "<noun>_comment", appending a comment to an entity.
func commentTool(ts *toolset, b *bridge, noun string) {
	addTool(ts, &mcp.Tool{Name: noun + "_comment", Description: "Add a comment to a " + noun + "."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in commentArgs) (*mcp.CallToolResult, any, error) {
			flags, err := freeTextFlag([]string{"--json"}, "--body", in.Body)
			if err != nil {
				return nil, nil, err
			}
			return b.run(ctx, argvFor([]string{noun, "comment"}, flags, in.ID)...)
		})
}

// statusTools registers a "<noun>_<verb>" transition tool for each verb.
func statusTools(ts *toolset, b *bridge, noun string, verbs ...string) {
	for _, verb := range verbs {
		addTool(ts, &mcp.Tool{Name: noun + "_" + verb, Description: "Transition a " + noun + " to " + verb + "."},
			func(ctx context.Context, _ *mcp.CallToolRequest, in entityIDArgs) (*mcp.CallToolResult, any, error) {
				return b.run(ctx, argvFor([]string{noun, verb}, []string{"--json"}, in.ID)...)
			})
	}
}
