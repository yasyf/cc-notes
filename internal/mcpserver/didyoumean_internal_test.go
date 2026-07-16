package mcpserver

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestArgSynonymsResolve guards against a dead did-you-mean entry: every synonym
// candidate must be an accepted property of at least one registered tool.
func TestArgSynonymsResolve(t *testing.T) {
	ts := &toolset{
		srv:   mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil),
		props: map[string]toolProps{},
	}
	registerAll(ts, &bridge{})

	accepted := map[string]bool{}
	for _, p := range ts.props {
		for _, name := range p.accepted {
			accepted[name] = true
		}
	}

	for synonym, candidates := range argSynonyms {
		if len(candidates) == 0 {
			t.Errorf("synonym %q has no candidates", synonym)
		}
		for _, c := range candidates {
			if !accepted[c] {
				t.Errorf("synonym %q -> %q: %q is not an accepted property of any registered tool", synonym, c, c)
			}
		}
	}
}

// TestToolsetRecordsEveryTool asserts set equality between the registered tools
// and ts.props keys: a tool added via raw mcp.AddTool bypasses addTool, so it
// lists but records no props, and the did-you-mean middleware would pass its
// unknown-key calls through to the opaque SDK error.
func TestToolsetRecordsEveryTool(t *testing.T) {
	ts := &toolset{
		srv:   mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil),
		props: map[string]toolProps{},
	}
	registerAll(ts, &bridge{})

	ctx := t.Context()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := ts.srv.Connect(ctx, st, nil)
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

	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(res.Tools) == 0 {
		t.Fatal("no tools registered")
	}

	registered := make(map[string]bool, len(res.Tools))
	for _, tool := range res.Tools {
		registered[tool.Name] = true
		if _, ok := ts.props[tool.Name]; !ok {
			t.Errorf("tool %q is registered but has no props entry (added via raw mcp.AddTool, bypassing addTool?)", tool.Name)
		}
	}
	for name := range ts.props {
		if !registered[name] {
			t.Errorf("props records %q but no such tool is registered", name)
		}
	}
}
