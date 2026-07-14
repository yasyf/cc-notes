package mcpserver_test

import (
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestFreeTextDashRejected asserts a free-text flag value of exactly "-" is
// rejected with the stdin-form reservation message across a --body flag, a
// comment factory, and criterion add.
func TestFreeTextDashRejected(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	rb := decode[runbookOut](t, call(t, cs, "runbook_add", map[string]any{"title": "Deploy"}))
	task := decode[taskOut](t, call(t, cs, "task_add", map[string]any{"title": "Ship it", "no_validation_criteria": true}))

	tests := []struct {
		name  string
		tool  string
		field string
		base  map[string]any
	}{
		{"runbook_edit body", "runbook_edit", "body", map[string]any{"id": rb.ID}},
		{"runbook_comment body", "runbook_comment", "body", map[string]any{"id": rb.ID}},
		{"task_criterion_add text", "task_criterion_add", "text", map[string]any{"task": task.ID}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{tc.field: "-"}
			for k, v := range tc.base {
				args[k] = v
			}
			res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tc.tool, Arguments: args})
			if err != nil {
				t.Fatalf("call %s: %v", tc.tool, err)
			}
			if !res.IsError {
				t.Fatalf("tool %s with %s=%q did not error: %s", tc.tool, tc.field, "-", toolText(res))
			}
			if got := toolText(res); !strings.Contains(got, `reserved for the CLI's stdin form`) {
				t.Fatalf("tool %s error = %q, want the stdin-form reservation message", tc.tool, got)
			}
		})
	}
}

// TestFreeTextNonDashPasses proves only the exact literal "-" is caught: a normal
// value and values that merely contain dashes round-trip into the note body.
func TestFreeTextNonDashPasses(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	for _, body := range []string{"plain", "a-b", "--x", " - "} {
		t.Run(body, func(t *testing.T) {
			added := decode[noteOut](t, call(t, cs, "note_add", map[string]any{"title": "Fact", "body": body}))
			if added.Body != body {
				t.Fatalf("note body = %q, want %q (value must pass through unchanged)", added.Body, body)
			}
		})
	}
}
