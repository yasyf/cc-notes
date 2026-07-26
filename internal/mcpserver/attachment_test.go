package mcpserver_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// attachNote creates a note carrying one attachment and returns its id.
func attachNote(t *testing.T, cs *mcp.ClientSession, name string, content []byte) string {
	t.Helper()
	src := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(src, content, 0o600); err != nil {
		t.Fatalf("write attachment: %v", err)
	}
	return ackID(t, call(t, cs, "note_add", map[string]any{"title": "Holder", "attach": []string{src}}))
}

// TestAttachmentGetBlankOutputRejected asserts a blank output path errors with
// the destination guard instead of falling through to the CLI's stdout form,
// which would return the raw attachment bytes as the tool result text.
func TestAttachmentGetBlankOutputRejected(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	payload := []byte("\x00\x01raw attachment bytes\xff")
	id := attachNote(t, cs, "artifact.bin", payload)

	tests := []struct {
		name   string
		output string
	}{
		{"empty", ""},
		{"spaces", "   "},
		{"tab and newline", "\t\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "attachment_get", Arguments: map[string]any{
				"id":     id,
				"name":   "artifact.bin",
				"output": tc.output,
			}})
			if err != nil {
				t.Fatalf("call attachment_get: %v", err)
			}
			if !res.IsError {
				t.Fatalf("attachment_get with output=%q did not error: %s", tc.output, toolText(res))
			}
			got := toolText(res)
			if !strings.Contains(got, "--output: a destination file path is required") {
				t.Fatalf("attachment_get error = %q, want the destination-required guard", got)
			}
			if strings.Contains(got, string(payload)) {
				t.Fatalf("attachment_get result carries the attachment bytes: %q", got)
			}
		})
	}
}

// TestAttachmentGetBlankOutputSkipsCLI asserts the guard runs before the bridge,
// so no argv reaches the cobra tree at all.
func TestAttachmentGetBlankOutputSkipsCLI(t *testing.T) {
	initRepo(t)
	rec := newCoverageRecorder()
	cs := connectRecording(t, rec)
	rec.setTool("attachment_get")

	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "attachment_get", Arguments: map[string]any{
		"id":     "abc1234",
		"name":   "artifact.bin",
		"output": "",
	}})
	if err != nil {
		t.Fatalf("call attachment_get: %v", err)
	}
	if !res.IsError {
		t.Fatalf("attachment_get with a blank output did not error: %s", toolText(res))
	}
	_, _, byTool := rec.snapshot()
	if reached := byTool["attachment_get"]; len(reached) != 0 {
		t.Fatalf("attachment_get reached %v, want no command run at all", reached)
	}
}

// TestAttachmentGetWritesFile asserts the happy path still lands the bytes on
// disk and acknowledges with the destination instead of the content.
func TestAttachmentGetWritesFile(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	payload := []byte("\x00\x01raw attachment bytes\xff")
	id := attachNote(t, cs, "artifact.bin", payload)

	dest := filepath.Join(t.TempDir(), "copy.bin")
	got := call(t, cs, "attachment_get", map[string]any{"id": id, "name": "artifact.bin", "output": dest})
	if want := fmt.Sprintf("wrote attachment %q to %s", "artifact.bin", dest); got != want {
		t.Fatalf("attachment_get = %q, want %q", got, want)
	}
	written, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read %s: %v", dest, err)
	}
	if !bytes.Equal(written, payload) {
		t.Fatalf("attachment_get wrote %q, want %q", written, payload)
	}
}

// TestAttachmentGetOutputSchemaRequired pins output as a schema-required
// property, so an SDK-side omission is rejected before the handler runs and the
// runtime guard only has to cover a present-but-blank value.
func TestAttachmentGetOutputSchemaRequired(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	listed, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	for _, tool := range listed.Tools {
		if tool.Name != "attachment_get" {
			continue
		}
		required := requiredProps(t, tool.Name, tool.InputSchema)
		sort.Strings(required)
		if got, want := strings.Join(required, ","), "id,name,output"; got != want {
			t.Fatalf("attachment_get required = %q, want %q", got, want)
		}
		return
	}
	t.Fatal("attachment_get is not registered")
}
