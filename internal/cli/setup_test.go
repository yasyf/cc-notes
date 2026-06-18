package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/yasyf/cc-notes/plugin"
)

func TestSkillsInstallRegistersPlugin(t *testing.T) {
	dir := initRepo(t)
	out := mustRun(t, dir, "skills", "install")

	assertCCNotesRegistered(t, filepath.Join(dir, ".claude", "settings.json"))
	if _, err := os.Stat(filepath.Join(dir, ".claude", "skills")); !os.IsNotExist(err) {
		t.Fatalf("skills install created .claude/skills; it should register the plugin, not vendor the skill")
	}
	if !strings.Contains(out, "registered: cc-notes plugin in .claude/settings.json") {
		t.Fatalf("install output %q missing the registration line", out)
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
