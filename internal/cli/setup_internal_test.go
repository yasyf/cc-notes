package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestPackAddArgs(t *testing.T) {
	tests := []struct {
		name string
		ver  string
		want []string
	}{
		{
			name: "dev tracks the default branch",
			ver:  "dev",
			want: []string{"capt-hook", "pack", "add", "github:yasyf/cc-notes"},
		},
		{
			name: "empty tracks the default branch",
			ver:  "",
			want: []string{"capt-hook", "pack", "add", "github:yasyf/cc-notes"},
		},
		{
			name: "stable tag pins the ref",
			ver:  "v1.2.3",
			want: []string{"capt-hook", "pack", "add", "github:yasyf/cc-notes@v1.2.3"},
		},
		{
			name: "prerelease tag pins the ref",
			ver:  "v1.2.3-rc1",
			want: []string{"capt-hook", "pack", "add", "github:yasyf/cc-notes@v1.2.3-rc1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := packAddArgs(tt.ver)
			if !slices.Equal(got, tt.want) {
				t.Fatalf("packAddArgs(%q) = %v, want %v", tt.ver, got, tt.want)
			}
		})
	}
}

func TestRegisterPluginPreservesOrderAndMerges(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o750); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	existing := `{
  "effortLevel": "max",
  "permissions": {
    "allow": [
      "Bash(ls:*)",
      "Bash(rg:*)"
    ]
  },
  "extraKnownMarketplaces": {
    "skills": {
      "source": {
        "source": "github",
        "repo": "yasyf/cc-skills"
      }
    }
  },
  "enabledPlugins": {
    "codex@skills": true
  },
  "hooks": {
    "PreToolUse": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "cd sub && uvx capt-hook run PreToolUse > /tmp/out.log"
          }
        ]
      }
    ]
  }
}
`
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	if err := registerPlugin(dir); err != nil {
		t.Fatalf("registerPlugin: %v", err)
	}

	//nolint:gosec // G304: reads the settings.json path under the test's own temp dir.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	text := string(got)

	if !strings.HasSuffix(text, "}\n") {
		t.Fatalf("settings must end with a single trailing newline, got %q", text[len(text)-3:])
	}
	if !strings.Contains(text, "  \"effortLevel\": \"max\"") {
		t.Fatalf("settings are not 2-space indented:\n%s", text)
	}

	for _, pair := range [][2]string{
		{"effortLevel", "permissions"},            // top-level order preserved
		{"permissions", "extraKnownMarketplaces"}, // ...including across the modified sections
		{"enabledPlugins", "\"hooks\""},           // trailing key stays last
		{"\"skills\"", "\"cc-notes\""},            // existing marketplace stays first
		{"codex@skills", "cc-notes@cc-notes"},     // existing plugin stays first
	} {
		if before, after := strings.Index(text, pair[0]), strings.Index(text, pair[1]); before < 0 || after < 0 || before > after {
			t.Fatalf("expected %q before %q (order not preserved):\n%s", pair[0], pair[1], text)
		}
	}
	if !strings.Contains(text, "\"Bash(ls:*)\"") {
		t.Fatalf("untouched nested arrays were not carried through verbatim:\n%s", text)
	}
	// &, <, > in carried-over values stay literal (no HTML escaping), matching
	// capt-hook's output so the shared settings.json does not escape-ping-pong.
	if !strings.Contains(text, "cd sub && uvx capt-hook run PreToolUse > /tmp/out.log") {
		t.Fatalf("nested command was HTML-escaped instead of carried verbatim:\n%s", text)
	}

	var m map[string]any
	if err := json.Unmarshal(got, &m); err != nil {
		t.Fatalf("result is not valid JSON: %v", err)
	}
	ep := m["enabledPlugins"].(map[string]any)
	if ep["cc-notes@cc-notes"] != true || ep["codex@skills"] != true {
		t.Fatalf("enabledPlugins = %v, want both codex@skills and cc-notes@cc-notes", ep)
	}
	mk := m["extraKnownMarketplaces"].(map[string]any)
	if _, ok := mk["skills"]; !ok {
		t.Fatalf("extraKnownMarketplaces dropped the skills entry: %v", mk)
	}
	src := mk["cc-notes"].(map[string]any)["source"].(map[string]any)
	if src["source"] != "github" || src["repo"] != "yasyf/cc-notes" {
		t.Fatalf("cc-notes marketplace source = %v, want github yasyf/cc-notes", src)
	}
}

func TestRegisterPluginCreatesAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o750); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	path := filepath.Join(dir, ".claude", "settings.json")
	if err := registerPlugin(dir); err != nil {
		t.Fatalf("registerPlugin (create): %v", err)
	}
	//nolint:gosec // G304: reads the settings.json path under the test's own temp dir.
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after create: %v", err)
	}
	if err := registerPlugin(dir); err != nil {
		t.Fatalf("registerPlugin (idempotent): %v", err)
	}
	//nolint:gosec // G304: reads the settings.json path under the test's own temp dir.
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after second run: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("registerPlugin is not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}
