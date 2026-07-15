package mcpserver_test

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// cliOnlyMarker is the token following "MCP: " on a command block that carries
// no MCP tool. The em dash (U+2014) is the storage-format sentinel the reference
// uses for "CLI-only"; a named-tool line never starts with it.
const cliOnlyMarker = "—"

// referenceRelPath is the module-root-relative path of the machine-parseable CLI
// reference whose "MCP:" lines this test cross-checks against the registered
// tool set.
var referenceRelPath = filepath.Join("plugin", "skills", "using-cc-notes", "references", "cli-reference.md")

// mcpRefLine is one "MCP: " line lifted from the reference, kept with its source
// line number so a failure names the exact offending line.
type mcpRefLine struct {
	no   int
	text string
}

// moduleRoot walks up from this test's own source directory to the directory
// holding go.mod, so the reference is located relative to the module regardless
// of the working directory (initRepo chdirs into a temp repo).
func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller: no caller information")
	}
	dir := filepath.Dir(file)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found walking up from %s", filepath.Dir(file))
		}
		dir = parent
	}
}

// mcpRefLines returns every line with the exact prefix "MCP: " that sits outside
// a fenced code block, tracking ``` fence state so a fenced example never leaks
// into the cross-check.
func mcpRefLines(t *testing.T, path string) []mcpRefLine {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var lines []mcpRefLine
	inFence := false
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	no := 0
	for sc.Scan() {
		no++
		text := sc.Text()
		if strings.HasPrefix(strings.TrimSpace(text), "```") {
			inFence = !inFence
			continue
		}
		if inFence || !strings.HasPrefix(text, "MCP: ") {
			continue
		}
		lines = append(lines, mcpRefLine{no: no, text: text})
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return lines
}

// refToolName extracts the tool name from a "MCP: " line: the first
// whitespace-delimited token after the prefix, with any trailing "(props)"
// stripped. A CLI-only line yields the em-dash marker.
func refToolName(line string) string {
	rest := strings.TrimPrefix(line, "MCP: ")
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return ""
	}
	name := fields[0]
	if i := strings.IndexByte(name, '('); i >= 0 {
		name = name[:i]
	}
	return name
}

// TestMCPReferenceMatchesRegistry cross-checks the reference's machine-parseable
// "MCP:" lines against the live tool registry in both directions: every named
// tool line resolves to a registered tool, and every registered tool is named by
// at least one line. It is the drift guard that keeps the agent-facing docs and
// the server's tool table from parting ways silently.
func TestMCPReferenceMatchesRegistry(t *testing.T) {
	initRepo(t)
	cs := connect(t)

	res, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	registered := make(map[string]bool, len(res.Tools))
	for _, tool := range res.Tools {
		registered[tool.Name] = true
	}
	if len(registered) == 0 {
		t.Fatal("no tools registered")
	}

	refPath := filepath.Join(moduleRoot(t), referenceRelPath)
	lines := mcpRefLines(t, refPath)
	if len(lines) == 0 {
		t.Fatalf("no \"MCP: \" lines found in %s", refPath)
	}

	documented := map[string]int{}
	for _, ln := range lines {
		name := refToolName(ln.text)
		if name == cliOnlyMarker {
			continue // a "MCP: — (CLI-only: …)" line names no tool
		}
		if name == "" {
			t.Errorf("%s:%d: empty MCP tool name in %q", referenceRelPath, ln.no, ln.text)
			continue
		}
		// Forward: a named tool must be a registered MCP tool.
		if !registered[name] {
			t.Errorf("%s:%d: MCP: line names %q, which is not a registered MCP tool", referenceRelPath, ln.no, name)
		}
		if _, seen := documented[name]; !seen {
			documented[name] = ln.no
		}
	}

	// Reverse: every registered tool must be named by at least one line.
	var missing []string
	for name := range registered {
		if _, ok := documented[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("registered MCP tool %q is named by no \"MCP: \" line in %s", name, referenceRelPath)
	}
}
