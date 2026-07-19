package mcpserver_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// inventoryRelPath is the module-root-relative path of the hook-pack module
// holding the CC_NOTES_TOOLS inventory this test ties to the live tool registry.
var inventoryRelPath = filepath.Join("plugin", "capt-hook", "hooks", "common.py")

// toolLiteral matches a python double-quoted tool-name literal inside the
// CC_NOTES_TOOLS block (comments in that block carry no quotes, so this never
// mistakes prose for a name).
var toolLiteral = regexp.MustCompile(`"([A-Za-z_][A-Za-z0-9_]*)"`)

// inventoryToolSet parses the pack's CC_NOTES_TOOLS frozenset into a set of
// tool names. It windows to the frozenset's brace-balanced set literal so no
// string literal elsewhere in the module (executable names, shell-word sets)
// leaks into the inventory, then lifts every quoted name.
func inventoryToolSet(t *testing.T, path string) map[string]bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	src := string(data)

	start := strings.Index(src, "CC_NOTES_TOOLS = frozenset(")
	if start < 0 {
		t.Fatalf("%s: no CC_NOTES_TOOLS frozenset block", path)
	}
	open := strings.IndexByte(src[start:], '{')
	if open < 0 {
		t.Fatalf("%s: no set literal after CC_NOTES_TOOLS", path)
	}
	open += start

	depth, end := 0, -1
	for i := open; i < len(src) && end < 0; i++ {
		switch src[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i
			}
		}
	}
	if end < 0 {
		t.Fatalf("%s: unbalanced braces in the CC_NOTES_TOOLS set literal", path)
	}

	set := map[string]bool{}
	for _, m := range toolLiteral.FindAllStringSubmatch(src[open:end+1], -1) {
		set[m[1]] = true
	}
	return set
}

// TestPackInventoryMatchesRegistry ties the hook pack's CC_NOTES_TOOLS set to
// the live registry in both directions: every listed name resolves to a
// registered MCP tool, and every registered tool is listed. It is the drift
// guard that keeps the Bash-failure redirect and the compact tracker from
// steering to a tool that does not exist, or silently missing a new one.
func TestPackInventoryMatchesRegistry(t *testing.T) {
	initRepo(t)
	cs := connectNeutered(t)

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

	documented := inventoryToolSet(t, filepath.Join(moduleRoot(t), inventoryRelPath))
	if len(documented) == 0 {
		t.Fatalf("no tool names parsed from %s", inventoryRelPath)
	}

	// Forward: every CC_NOTES_TOOLS entry must be a registered MCP tool.
	var extra []string
	for name := range documented {
		if !registered[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		t.Errorf("%s CC_NOTES_TOOLS names %q, which is not a registered MCP tool", inventoryRelPath, name)
	}

	// Reverse: every registered tool must appear in CC_NOTES_TOOLS.
	var missing []string
	for name := range registered {
		if !documented[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	for _, name := range missing {
		t.Errorf("registered MCP tool %q is absent from %s CC_NOTES_TOOLS", name, inventoryRelPath)
	}
}
