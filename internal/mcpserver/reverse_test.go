package mcpserver_test

import (
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/mcpserver"
)

// excludedCommands are operator-only surfaces intentionally absent from the
// agent-facing MCP tool set, keyed by their top-level command word (the whole
// subtree is skipped).
var excludedCommands = map[string]bool{
	"init":      true, // one-time repo adoption, a human-operator step
	"mcp":       true, // the MCP server's own launch command
	"viz":       true, // launches a local visualization web server
	"gc":        true, // destructive object-store maintenance
	"compact":   true, // op-log checkpoint maintenance, an operator task
	"hooks":     true, // installs Claude Code hooks into the checkout
	"skills":    true, // installs the plugin skill into the checkout
	"workflows": true, // installs workflow templates into the checkout
	"version":   true, // prints the binary version
}

// excludedCommandPaths are noun-scoped subcommands whose capability the MCP
// surface carries through a kind-agnostic tool instead, keyed by full command
// path. The history tool resolves any entity by id prefix, so per-kind history
// wrappers would be redundant tool surface for an agent to search through.
var excludedCommandPaths = map[string]bool{
	"cc-notes note history":          true,
	"cc-notes doc history":           true,
	"cc-notes log history":           true,
	"cc-notes task history":          true,
	"cc-notes sprint history":        true,
	"cc-notes project history":       true,
	"cc-notes runbook history":       true,
	"cc-notes investigation history": true,
}

// excludedFlags are CLI-only flags with no agent-facing MCP surface, keyed by
// flag name.
var excludedFlags = map[string]bool{
	"json":     true, // MCP always requests JSON; not an agent-facing choice
	"checkout": true, // CLI-only editable-buffer mode; MCP writes the body inline
	"apply":    true, // CLI-only editable-buffer mode; MCP writes the body inline
	"abort":    true, // CLI-only editable-buffer mode; MCP writes the body inline
}

// coverageRecorder accumulates the command paths and Changed flag names the
// neutered recording root observes as MCP tool calls drive the real cobra tree,
// plus a per-tool attribution (byTool) keyed by the driving tool the caller names
// via setTool before each tool's calls.
type coverageRecorder struct {
	mu       sync.Mutex
	tool     string
	commands map[string]bool
	flags    map[string]map[string]bool
	byTool   map[string]map[string]bool
}

func newCoverageRecorder() *coverageRecorder {
	return &coverageRecorder{commands: map[string]bool{}, flags: map[string]map[string]bool{}, byTool: map[string]map[string]bool{}}
}

// setTool names the MCP tool whose subsequent calls the recorder attributes. The
// driver sets it before driving each tool; calls are synchronous, so no record
// races the swap.
func (r *coverageRecorder) setTool(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tool = name
}

func (r *coverageRecorder) record(cmd *cobra.Command) {
	r.mu.Lock()
	defer r.mu.Unlock()
	path := cmd.CommandPath()
	r.commands[path] = true
	reached := r.byTool[r.tool]
	if reached == nil {
		reached = map[string]bool{}
		r.byTool[r.tool] = reached
	}
	reached[path] = true
	seen := r.flags[path]
	if seen == nil {
		seen = map[string]bool{}
		r.flags[path] = seen
	}
	cmd.Flags().Visit(func(f *pflag.Flag) { seen[f.Name] = true })
}

// snapshot returns immutable copies of the accumulated coverage — global
// commands, per-command flags, and per-tool reached paths — for the walk to read
// without racing the (now-finished) recording goroutines.
func (r *coverageRecorder) snapshot() (map[string]bool, map[string]map[string]bool, map[string]map[string]bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cmds := make(map[string]bool, len(r.commands))
	for k := range r.commands {
		cmds[k] = true
	}
	return cmds, copyNested(r.flags), copyNested(r.byTool)
}

// copyNested deep-copies a path/tool → name-set map so the snapshot is safe to
// read after the recording goroutines finish.
func copyNested(m map[string]map[string]bool) map[string]map[string]bool {
	out := make(map[string]map[string]bool, len(m))
	for k, v := range m {
		inner := make(map[string]bool, len(v))
		for f := range v {
			inner[f] = true
		}
		out[k] = inner
	}
	return out
}

// recordingRoot builds the real CLI root with every command's RunE stubbed to
// record into rec. Flag parsing, arg-count checks, and flag-group validation all
// run before RunE, so a call reaches the recorder only for an argv the tree
// fully accepts — the exact surface this cross-check measures.
func recordingRoot(rec *coverageRecorder) func() *cobra.Command {
	return func() *cobra.Command {
		root := cli.NewRootCmd()
		var stub func(*cobra.Command)
		stub = func(c *cobra.Command) {
			c.RunE = func(cmd *cobra.Command, _ []string) error {
				rec.record(cmd)
				return nil
			}
			c.Run = nil
			c.PreRunE = nil
			c.PreRun = nil
			c.PersistentPreRunE = nil
			c.PersistentPreRun = nil
			for _, child := range c.Commands() {
				stub(child)
			}
		}
		stub(root)
		return root
	}
}

// connectRecording wires an mcpserver whose bridge drives the recording root to
// an SDK client over in-memory transports.
func connectRecording(t *testing.T, rec *coverageRecorder) *mcp.ClientSession {
	t.Helper()
	ctx := t.Context()
	srv := mcpserver.New(mcpserver.Config{Version: "test", NewRoot: recordingRoot(rec), Label: cli.Label, Message: cli.Message})
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

// requiredProps returns the schema's required property names (the non-omitempty
// fields), which every call must carry so parsing and arg-count validation pass.
func requiredProps(t *testing.T, name string, schema any) []string {
	t.Helper()
	data, err := json.Marshal(schema)
	if err != nil {
		t.Fatalf("tool %q: marshal input schema: %v", name, err)
	}
	var doc struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("tool %q: parse input schema: %v", name, err)
	}
	return doc.Required
}

// driveTool invokes one tool once per optional schema property (each set alone,
// atop the required props) and once with every property set. A lone optional
// flag never trips a mutual-exclusion group, so its per-property call always
// reaches RunE and records it; the all-set call may hit a group error but its
// flags are already covered individually.
func driveTool(t *testing.T, cs *mcp.ClientSession, rec *coverageRecorder, name string, schema any) {
	t.Helper()
	rec.setTool(name)
	full := synthesizeArgs(t, name, schema)
	// "1h" parses as a duration, so a DurationVar flag (--timeout) reaches RunE.
	for k, v := range full {
		if _, ok := v.(string); ok {
			full[k] = "1h"
		}
	}
	base := map[string]any{}
	for _, p := range requiredProps(t, name, schema) {
		base[p] = full[p]
	}
	for prop, val := range full {
		if _, req := base[prop]; req {
			continue
		}
		args := map[string]any{prop: val}
		for k, v := range base {
			args[k] = v
		}
		_, _ = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	}
	_, _ = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: full})
}

// TestCobraReachableFromMCP is the reverse of TestMCPVocabularyMatchesCobra: it
// asserts every runnable leaf command and every local flag the CLI exposes is
// reachable through some MCP tool, except the explicitly excluded operator-only
// commands and CLI-only flags above. It is the drift guard the forward test
// cannot be — a CLI capability with no MCP schema field (the class of bug that
// let runbook anchors ship CLI-only) turns this red, naming the exact gap.
func TestCobraReachableFromMCP(t *testing.T) {
	initRepo(t)
	rec := newCoverageRecorder()
	cs := connectRecording(t, rec)

	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	if len(res.Tools) == 0 {
		t.Fatal("no tools registered")
	}
	for _, tool := range res.Tools {
		driveTool(t, cs, rec, tool.Name, tool.InputSchema)
	}

	coveredCmds, coveredFlags, toolPaths := rec.snapshot()

	var missCmds, missFlags []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if !c.HasSubCommands() {
			checkLeaf(c, coveredCmds, coveredFlags, &missCmds, &missFlags)
		}
		for _, child := range c.Commands() {
			walk(child)
		}
	}
	walk(cli.NewRootCmd())

	// Per-tool attribution: a tool whose handler builds empty argv reaches only
	// the bare root, so its coverage cannot hide behind another tool's leaf.
	var toolsNoLeaf []string
	for _, tool := range res.Tools {
		if !reachedNonRoot(toolPaths[tool.Name]) {
			toolsNoLeaf = append(toolsNoLeaf, tool.Name)
		}
	}

	sort.Strings(missCmds)
	sort.Strings(missFlags)
	sort.Strings(toolsNoLeaf)
	if len(missCmds) == 0 && len(missFlags) == 0 && len(toolsNoLeaf) == 0 {
		return
	}
	if len(toolsNoLeaf) > 0 {
		t.Errorf("%d MCP tool(s) reached no non-root command path (handler builds empty argv?):\n  %s",
			len(toolsNoLeaf), strings.Join(toolsNoLeaf, "\n  "))
	}
	if len(missCmds) > 0 || len(missFlags) > 0 {
		t.Errorf("MCP tools do not reach %d command(s) and %d flag(s):\ncommands:\n  %s\nflags:\n  %s",
			len(missCmds), len(missFlags),
			strings.Join(missCmds, "\n  "), strings.Join(missFlags, "\n  "))
	}
}

// reachedNonRoot reports whether any recorded path for a tool names a subcommand
// — i.e. the tool's handler built a non-empty argv resolving past the bare root.
func reachedNonRoot(paths map[string]bool) bool {
	for path := range paths {
		if len(strings.Fields(path)) >= 2 {
			return true
		}
	}
	return false
}

// checkLeaf records the leaf command (and, if the command is reached, each
// non-excluded local flag) that the MCP sweep never covered. An entirely
// uncovered command is reported once, without enumerating its flags.
func checkLeaf(c *cobra.Command, coveredCmds map[string]bool, coveredFlags map[string]map[string]bool, missCmds, missFlags *[]string) {
	path := c.CommandPath()
	fields := strings.Fields(path)
	if len(fields) < 2 {
		return // the bare root
	}
	if excludedCommands[fields[1]] || excludedCommandPaths[path] {
		return
	}
	if !coveredCmds[path] {
		*missCmds = append(*missCmds, path)
		return
	}
	c.LocalFlags().VisitAll(func(f *pflag.Flag) {
		if excludedFlags[f.Name] {
			return
		}
		if !coveredFlags[path][f.Name] {
			*missFlags = append(*missFlags, path+" --"+f.Name)
		}
	})
}
