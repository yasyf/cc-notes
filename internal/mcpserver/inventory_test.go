package mcpserver_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/mcpserver"
)

// neuteredRoot builds the real CLI root, then stubs every command's RunE to a
// no-op. Cobra still resolves command paths, validates arg counts, and parses
// flags before RunE, so the tree rejects an unknown flag or command exactly as
// in production — the layer this cross-check exercises — without touching the
// store.
func neuteredRoot() *cobra.Command {
	root := cli.NewRootCmd()
	stubRunE(root)
	return root
}

func stubRunE(cmd *cobra.Command) {
	cmd.RunE = func(*cobra.Command, []string) error { return nil }
	cmd.Run = nil
	cmd.PreRunE = nil
	cmd.PreRun = nil
	cmd.PersistentPreRunE = nil
	cmd.PersistentPreRun = nil
	for _, c := range cmd.Commands() {
		stubRunE(c)
	}
}

// connectNeutered wires an mcpserver whose bridge drives the neutered root to an
// SDK client over in-memory transports.
func connectNeutered(t *testing.T) *mcp.ClientSession {
	t.Helper()
	ctx := t.Context()
	srv := mcpserver.New(mcpserver.Config{Version: "test", NewRoot: neuteredRoot, Label: cli.Label, Message: cli.Message})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.Connect(ctx, st, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// synthesizeArgs fills every published property so every optStr/optRepeated/
// optBool/optInt branch in the handler emits its flag literal into the argv.
func synthesizeArgs(t *testing.T, name string, schema any) map[string]any {
	t.Helper()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("tool %q: marshal input schema: %v", name, err)
	}
	var doc struct {
		Properties map[string]struct {
			Type json.RawMessage `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("tool %q: parse input schema: %v", name, err)
	}
	args := make(map[string]any, len(doc.Properties))
	for prop, def := range doc.Properties {
		switch typ := schemaType(t, name, prop, def.Type); typ {
		case "string":
			args[prop] = "x"
		case "boolean":
			args[prop] = true
		case "integer", "number":
			args[prop] = 1
		case "array":
			args[prop] = []any{"x"}
		default:
			t.Fatalf("tool %q property %q: unhandled schema type %q", name, prop, typ)
		}
	}
	return args
}

// schemaType returns the concrete JSON type from a schema "type" keyword, which
// is either a bare string or an array pairing the type with "null" for an
// optional field.
func schemaType(t *testing.T, tool, prop string, raw json.RawMessage) string {
	t.Helper()
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return single
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		t.Fatalf("tool %q property %q: unreadable type %s", tool, prop, raw)
	}
	for _, ty := range many {
		if ty != "null" {
			return ty
		}
	}
	t.Fatalf("tool %q property %q: type has only null: %s", tool, prop, raw)
	return ""
}

// TestMCPVocabularyMatchesCobra is the guard rail for the hand-typed argv
// literals in tools_*.go: every tool, invoked with every schema property filled,
// must produce an argv the cobra tree accepts as vocabulary. Semantic errors
// (mutual exclusion, arg-count, not-found) are fine — RunE is stubbed and this
// only asserts no flag or command was rejected as unknown. A missed rename makes
// exactly the affected tool fail, naming it.
func TestMCPVocabularyMatchesCobra(t *testing.T) {
	initRepo(t)
	cs := connectNeutered(t)

	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(res.Tools) == 0 {
		t.Fatal("no tools registered")
	}

	rejections := []string{"unknown flag", "unknown shorthand", "unknown command"}
	for _, tool := range res.Tools {
		args := synthesizeArgs(t, tool.Name, tool.InputSchema)
		out, callErr := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: tool.Name, Arguments: args})
		if callErr != nil {
			t.Errorf("tool %q: protocol error calling with synthesized args: %v", tool.Name, callErr)
			continue
		}
		text := toolText(out)
		for _, bad := range rejections {
			if strings.Contains(text, bad) {
				t.Errorf("tool %q emits an argv the CLI rejects (%q): %s", tool.Name, bad, text)
			}
		}
	}
}
