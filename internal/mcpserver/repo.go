package mcpserver

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type repoKey struct{}

// repoMiddleware lifts a tool call's repo property into its context, where the
// bridge turns it into --repo, so no tool's input type has to carry it.
func repoMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			call := req.(*mcp.CallToolRequest)
			var arguments map[string]json.RawMessage
			if err := json.Unmarshal(call.Params.Arguments, &arguments); err != nil {
				return next(ctx, method, req)
			}
			raw, ok := arguments[repoArg]
			if !ok {
				return next(ctx, method, req)
			}
			var repo string
			if err := json.Unmarshal(raw, &repo); err != nil {
				return next(ctx, method, req)
			}
			delete(arguments, repoArg)
			stripped, err := json.Marshal(arguments)
			if err != nil {
				return nil, err
			}
			call.Params.Arguments = stripped
			if repo != "" {
				ctx = context.WithValue(ctx, repoKey{}, repo)
			}
			return next(ctx, method, req)
		}
	}
}

func repoFrom(ctx context.Context) (string, bool) {
	repo, ok := ctx.Value(repoKey{}).(string)
	return repo, ok
}
