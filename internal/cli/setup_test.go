package cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

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

func TestWorkflowsInstallWritesTree(t *testing.T) {
	dir := initRepo(t)
	out := mustRun(t, dir, "workflows", "install")

	workflow := filepath.Join(dir, ".github", "workflows", "cc-notes.yml")
	got, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatalf("read installed cc-notes.yml: %v", err)
	}
	want, err := plugin.Files.ReadFile("workflows/cc-notes.yml")
	if err != nil {
		t.Fatalf("read embedded cc-notes.yml: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("installed cc-notes.yml does not match embedded source")
	}
	suffix := filepath.Join(".github", "workflows", "cc-notes.yml")
	if !strings.Contains(out, "wrote ") || !strings.Contains(out, suffix) {
		t.Fatalf("install output %q missing a wrote line for %q", out, suffix)
	}
}

// TestWorkflowsInstallEmitsExpectedBehaviors parses the installed workflow as
// YAML and asserts the contract the CI reconcile depends on: it triggers on
// push, restricts to the default branch resolved at runtime, grants
// contents:write, checks out full history, installs the release binary via
// scripts/install.sh (never `go install`), no-ops without a remote, and
// reconciles into the runtime default branch. A byte-match against the embedded
// source cannot catch a template that drifts from these behaviors.
func TestWorkflowsInstallEmitsExpectedBehaviors(t *testing.T) {
	dir := initRepo(t)
	mustRun(t, dir, "workflows", "install")

	raw, err := os.ReadFile(filepath.Join(dir, ".github", "workflows", "cc-notes.yml"))
	if err != nil {
		t.Fatalf("read installed workflow: %v", err)
	}

	var wf struct {
		Name        string         `yaml:"name"`
		On          map[string]any `yaml:"on"`
		Permissions struct {
			Contents string `yaml:"contents"`
		} `yaml:"permissions"`
		Jobs struct {
			Reconcile struct {
				If    string `yaml:"if"`
				Steps []struct {
					Uses string `yaml:"uses"`
					With struct {
						FetchDepth *int `yaml:"fetch-depth"`
					} `yaml:"with"`
					Run string `yaml:"run"`
				} `yaml:"steps"`
			} `yaml:"reconcile"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("installed workflow is not valid YAML: %v", err)
	}

	if wf.Name != "cc-notes" {
		t.Fatalf("name = %q, want cc-notes", wf.Name)
	}
	if _, ok := wf.On["push"]; !ok {
		t.Fatalf("on = %v, want a push trigger", wf.On)
	}
	if wf.Permissions.Contents != "write" {
		t.Fatalf("permissions.contents = %q, want write", wf.Permissions.Contents)
	}
	if want := "github.ref_name == github.event.repository.default_branch"; wf.Jobs.Reconcile.If != want {
		t.Fatalf("reconcile if = %q, want %q", wf.Jobs.Reconcile.If, want)
	}

	steps := wf.Jobs.Reconcile.Steps
	var sawCheckout bool
	var runs strings.Builder
	for _, s := range steps {
		if strings.HasPrefix(s.Uses, "actions/checkout") {
			sawCheckout = true
			switch {
			case s.With.FetchDepth == nil:
				t.Fatalf("checkout step missing fetch-depth, want 0 (full history)")
			case *s.With.FetchDepth != 0:
				t.Fatalf("checkout fetch-depth = %d, want 0 (full history)", *s.With.FetchDepth)
			}
		}
		runs.WriteString(s.Run)
		runs.WriteByte('\n')
	}
	if !sawCheckout {
		t.Fatalf("no actions/checkout step found in %v", steps)
	}

	run := runs.String()
	for _, want := range []string{
		"scripts/install.sh",
		"git remote | grep -q .",
		"exit 0",
		`cc-notes reconcile --into "${{ github.event.repository.default_branch }}"`,
	} {
		if !strings.Contains(run, want) {
			t.Fatalf("reconcile job run blocks missing %q:\n%s", want, run)
		}
	}
	if strings.Contains(run, "go install") {
		t.Fatalf("reconcile job installs via `go install`; want the release binary via scripts/install.sh:\n%s", run)
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
