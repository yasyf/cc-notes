package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var argSynonyms = map[string][]string{
	"text":      {"body"},
	"comment":   {"body"},
	"complaint": {"body"},
	"evidence":  {"note"},
	"tags":      {"labels"},
}

var argAliases = map[string]string{
	"description": "body",
}

func didYouMeanMiddleware(props map[string]toolProps) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			call := req.(*mcp.CallToolRequest)
			tool, ok := props[call.Params.Name]
			if !ok || len(call.Params.Arguments) == 0 {
				return next(ctx, method, req)
			}
			var arguments map[string]json.RawMessage
			if err := json.Unmarshal(call.Params.Arguments, &arguments); err != nil || len(arguments) == 0 {
				return next(ctx, method, req)
			}
			accepted := make(map[string]bool, len(tool.accepted))
			for _, name := range tool.accepted {
				accepted[name] = true
			}
			if rewriteAliases(arguments, accepted) {
				raw, err := json.Marshal(arguments)
				if err != nil {
					return nil, err
				}
				call.Params.Arguments = raw
			}
			var unknown []string
			for name := range arguments {
				if !accepted[name] {
					unknown = append(unknown, name)
				}
			}
			if len(unknown) == 0 {
				return next(ctx, method, req)
			}
			sort.Strings(unknown)
			message := unknownPropertyMessage(call.Params.Name, tool, accepted, unknown)
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: message}},
				IsError: true,
			}, nil
		}
	}
}

func rewriteAliases(arguments map[string]json.RawMessage, accepted map[string]bool) bool {
	rewrote := false
	for alias, canonical := range argAliases {
		value, sent := arguments[alias]
		if _, taken := arguments[canonical]; !sent || taken || accepted[alias] || !accepted[canonical] {
			continue
		}
		arguments[canonical] = value
		delete(arguments, alias)
		rewrote = true
	}
	return rewrote
}

func unknownPropertyMessage(toolName string, tool toolProps, acceptedSet map[string]bool, unknown []string) string {
	parts := make([]string, 0, len(unknown))
	for _, name := range unknown {
		part := fmt.Sprintf("unknown property %q", name)
		for _, candidate := range argSynonyms[name] {
			if acceptedSet[candidate] {
				part += fmt.Sprintf(" (did you mean %q?)", candidate)
				break
			}
		}
		parts = append(parts, part)
	}
	accepted := make([]string, len(tool.accepted))
	hasRequired := false
	for i, name := range tool.accepted {
		accepted[i] = name
		if tool.required[name] {
			accepted[i] += "*"
			hasRequired = true
		}
	}
	if len(accepted) == 0 {
		accepted = append(accepted, "(none)")
	}
	message := toolName + ": " + strings.Join(parts, "; ") + "; accepted: " + strings.Join(accepted, ", ")
	if hasRequired {
		message += " (* = required)"
	}
	return message
}
