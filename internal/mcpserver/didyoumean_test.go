package mcpserver_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// acceptedProps lifts the property names from a tool's InputSchema, the
// independent oracle for the set the pre-check names on an unknown key.
func acceptedProps(t *testing.T, name string, schema any) []string {
	t.Helper()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("tool %q: marshal input schema: %v", name, err)
	}
	var doc struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("tool %q: parse input schema: %v", name, err)
	}
	props := make([]string, 0, len(doc.Properties))
	for p := range doc.Properties {
		props = append(props, p)
	}
	return props
}

// TestMCPUnknownKeyNamesAccepted drives every tool with a bogus property and
// asserts the pre-check errors, naming the offending key and every accepted
// property of that tool.
func TestMCPUnknownKeyNamesAccepted(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(res.Tools) == 0 {
		t.Fatal("no tools registered")
	}

	const bogus = "bogus_key_zzz"
	for _, tool := range res.Tools {
		out, callErr := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool.Name, Arguments: map[string]any{bogus: "x"}})
		if callErr != nil {
			t.Errorf("tool %q: protocol error calling with a bogus key: %v", tool.Name, callErr)
			continue
		}
		if !out.IsError {
			t.Errorf("tool %q: bogus key did not produce an error result: %s", tool.Name, toolText(out))
			continue
		}
		text := toolText(out)
		if !strings.Contains(text, bogus) {
			t.Errorf("tool %q: error text does not name the unknown key %q: %s", tool.Name, bogus, text)
		}
		for _, prop := range acceptedProps(t, tool.Name, tool.InputSchema) {
			if !strings.Contains(text, prop) {
				t.Errorf("tool %q: error text omits accepted property %q: %s", tool.Name, prop, text)
			}
		}
	}
}

// TestMCPWrongKeyHints pins the exact pre-check message across the comment-tool
// siblings and papercut. note_comment is not registered, so the sibling coverage
// uses sprint_comment and project_comment.
func TestMCPWrongKeyHints(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	tests := []struct {
		name string
		tool string
		args map[string]any
		want string
	}{
		{
			name: "task_comment text->body",
			tool: "task_comment",
			args: map[string]any{"id": "abc", "text": "hi"},
			want: `task_comment: unknown property "text" (did you mean "body"?); accepted: id*, body* (* = required)`,
		},
		{
			name: "papercut complaint->body",
			tool: "papercut",
			args: map[string]any{"complaint": "the tool dead-ended"},
			want: `papercut: unknown property "complaint" (did you mean "body"?); accepted: body*, model (* = required)`,
		},
		{
			name: "sprint_comment text->body",
			tool: "sprint_comment",
			args: map[string]any{"id": "abc", "text": "hi"},
			want: `sprint_comment: unknown property "text" (did you mean "body"?); accepted: id*, body* (* = required)`,
		},
		{
			name: "project_comment text->body",
			tool: "project_comment",
			args: map[string]any{"id": "abc", "text": "hi"},
			want: `project_comment: unknown property "text" (did you mean "body"?); accepted: id*, body* (* = required)`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.tool, Arguments: tc.args})
			if err != nil {
				t.Fatalf("call %s: %v", tc.tool, err)
			}
			if !out.IsError {
				t.Fatalf("tool %s did not return an error result: %s", tc.tool, toolText(out))
			}
			if got := toolText(out); got != tc.want {
				t.Fatalf("error text =\n  %q\nwant\n  %q", got, tc.want)
			}
		})
	}
}

// TestInputSchemasClosed is the SDK-upgrade tripwire: every tool's InputSchema
// must marshal with "additionalProperties":false, keeping the pre-check
// equivalent to the SDK's own validation.
func TestInputSchemasClosed(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(res.Tools) == 0 {
		t.Fatal("no tools registered")
	}
	for _, tool := range res.Tools {
		data, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Errorf("tool %q: marshal input schema: %v", tool.Name, err)
			continue
		}
		if !strings.Contains(string(data), `"additionalProperties":false`) {
			t.Errorf("tool %q: input schema is not closed (no additionalProperties:false): %s", tool.Name, data)
		}
	}
}

// TestMCPMissingRequiredPassesThrough proves the pre-check hints only on unknown
// keys: a call whose keys are all accepted but omits a required property reaches
// the SDK's own validation, which rejects it as an error result.
func TestMCPMissingRequiredPassesThrough(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	out, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "task_comment", Arguments: map[string]any{"id": "abc"}})
	if err != nil {
		t.Fatalf("call task_comment: %v", err)
	}
	if !out.IsError {
		t.Fatalf("task_comment missing the required body did not return an error result: %s", toolText(out))
	}
	if text := toolText(out); strings.Contains(text, "unknown property") {
		t.Fatalf("pre-check middleware intercepted a missing-required call meant for SDK validation: %s", text)
	}
}
