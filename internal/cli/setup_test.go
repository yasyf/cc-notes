package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/plugin"
)

func TestSkillsInstallWritesTree(t *testing.T) {
	dir := initRepo(t)
	out := mustRun(t, dir, "skills", "install")

	skill := filepath.Join(dir, ".claude", "skills", "using-cc-notes", "SKILL.md")
	got, err := os.ReadFile(skill)
	if err != nil {
		t.Fatalf("read installed SKILL.md: %v", err)
	}
	want, err := plugin.Files.ReadFile("skills/using-cc-notes/SKILL.md")
	if err != nil {
		t.Fatalf("read embedded SKILL.md: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("installed SKILL.md does not match embedded source")
	}
	if a, _ := plugin.Files.ReadFile("skills/using-cc-notes/references/coordination.md"); len(a) > 0 {
		ref := filepath.Join(dir, ".claude", "skills", "using-cc-notes", "references", "coordination.md")
		if _, err := os.Stat(ref); err != nil {
			t.Fatalf("references tree not installed: %v", err)
		}
	}
	suffix := filepath.Join("using-cc-notes", "SKILL.md")
	if !strings.Contains(out, "wrote ") || !strings.Contains(out, suffix) {
		t.Fatalf("install output %q missing a wrote line for %q", out, suffix)
	}
}

func TestHooksInstallWiresSettings(t *testing.T) {
	dir := initRepo(t)
	mustRun(t, dir, "hooks", "install")

	hook := filepath.Join(dir, ".claude", "hooks", "cc_notes.py")
	if _, err := os.Stat(hook); err != nil {
		t.Fatalf("cc_notes.py not installed: %v", err)
	}

	groups := postToolUseGroups(t, dir)
	if n := len(groups); n != 1 {
		t.Fatalf("PostToolUse groups = %d, want 1", n)
	}
	if cmd := firstCommand(t, groups[0]); cmd != "uvx capt-hook run PostToolUse" {
		t.Fatalf("PostToolUse command = %q, want uvx capt-hook run PostToolUse", cmd)
	}
}

func TestHooksInstallIdempotent(t *testing.T) {
	dir := initRepo(t)
	mustRun(t, dir, "hooks", "install")
	first, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	mustRun(t, dir, "hooks", "install")
	second, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("second install changed settings.json:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
	if n := len(postToolUseGroups(t, dir)); n != 1 {
		t.Fatalf("PostToolUse groups after two installs = %d, want 1", n)
	}
}

func TestHooksInstallPreservesExistingHooks(t *testing.T) {
	dir := initRepo(t)
	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	seed := `{"model":"opus","hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"echo existing"}]}]}}`
	if err := os.WriteFile(settingsPath, []byte(seed), 0o644); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	mustRun(t, dir, "hooks", "install")

	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	if parsed["model"] != "opus" {
		t.Fatalf("install clobbered unrelated key model = %v", parsed["model"])
	}
	groups := postToolUseGroups(t, dir)
	if n := len(groups); n != 2 {
		t.Fatalf("PostToolUse groups = %d, want 2 (existing + capt-hook)", n)
	}
	if cmd := firstCommand(t, groups[0]); cmd != "echo existing" {
		t.Fatalf("existing hook not preserved, group 0 command = %q", cmd)
	}
	if cmd := firstCommand(t, groups[1]); cmd != "uvx capt-hook run PostToolUse" {
		t.Fatalf("capt-hook not appended, group 1 command = %q", cmd)
	}
}

func postToolUseGroups(t *testing.T, dir string) []any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("read settings: %v", err)
	}
	var parsed struct {
		Hooks struct {
			PostToolUse []any `json:"PostToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("parse settings: %v", err)
	}
	return parsed.Hooks.PostToolUse
}

func firstCommand(t *testing.T, group any) string {
	t.Helper()
	g, ok := group.(map[string]any)
	if !ok {
		t.Fatalf("group is not an object: %#v", group)
	}
	hooks, ok := g["hooks"].([]any)
	if !ok || len(hooks) == 0 {
		t.Fatalf("group has no hooks: %#v", group)
	}
	h, ok := hooks[0].(map[string]any)
	if !ok {
		t.Fatalf("hook entry is not an object: %#v", hooks[0])
	}
	cmd, _ := h["command"].(string)
	return cmd
}
