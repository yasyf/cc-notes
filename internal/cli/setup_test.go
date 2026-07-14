package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/plugin"
)

// fakeUvx installs a fake `uvx` on PATH that appends each invocation's argv to a
// log and exits with skillsRC for a `skills install` call, packRC for a
// `pack add` call, and 0 otherwise. It returns the log path so a test can assert
// which capt-hook steps ran. Mocking only the external `uvx` subprocess leaves
// the enable-capt-hook flow itself real.
func fakeUvx(t *testing.T, skillsRC, packRC int) string {
	t.Helper()
	dir := t.TempDir()
	log := filepath.Join(dir, "uvx.log")
	script := fmt.Sprintf("#!/bin/sh\necho \"$*\" >> %q\ncase \"$*\" in\n  *\"skills install\"*) exit %d ;;\n  *\"pack add\"*) exit %d ;;\nesac\nexit 0\n", log, skillsRC, packRC)
	//nolint:gosec // G306: the fake `uvx` must be world-executable for exec.LookPath to run it.
	if err := os.WriteFile(filepath.Join(dir, "uvx"), []byte(script), 0o755); err != nil {
		t.Fatalf("write fake uvx: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// TestHooksInstallEnablesPackAndDispatcher proves `cc-notes hooks install` runs
// BOTH capt-hook steps — `skills install` (the dispatcher plugin) and `pack add`
// (the cc-notes pack) — and that each is independently best-effort: a failure of
// one still runs the other and surfaces as an error. init reuses the same
// enableCaptHook helper, so this covers both entry points.
func TestHooksInstallEnablesPackAndDispatcher(t *testing.T) {
	for _, tc := range []struct {
		name             string
		skillsRC, packRC int
		wantErr          bool
	}{
		{"both succeed", 0, 0, false},
		{"skills install fails, pack add still runs", 1, 0, true},
		{"pack add fails, skills install still runs", 0, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := initRepo(t)
			log := fakeUvx(t, tc.skillsRC, tc.packRC)
			_, stderr, err := runCLI(t, dir, "hooks", "install")
			if tc.wantErr != (err != nil) {
				t.Fatalf("hooks install err = %v (stderr %q), wantErr %v", err, stderr, tc.wantErr)
			}
			//nolint:gosec // G304: reads the fake uvx log under the test's own temp dir.
			calls, _ := os.ReadFile(log)
			// --isolated must prefix both invocations so a machine-wide
			// `uv tool install capt-hook` never short-circuits uvx to a stale env.
			if !strings.Contains(string(calls), "--isolated capt-hook skills install") {
				t.Fatalf("hooks install never ran `--isolated capt-hook skills install` (dispatcher left off or not isolated):\n%s", calls)
			}
			if !strings.Contains(string(calls), "--isolated capt-hook pack add") {
				t.Fatalf("hooks install never ran `--isolated capt-hook pack add` (a prior failure short-circuited it or not isolated):\n%s", calls)
			}
		})
	}
}

func TestSkillsInstallRegistersPlugin(t *testing.T) {
	dir := initRepo(t)
	out := mustRun(t, dir, "skills", "install")

	settings := filepath.Join(dir, ".claude", "settings.json")
	assertCCNotesRegistered(t, settings)
	if _, err := os.Stat(filepath.Join(dir, ".claude", "skills")); !os.IsNotExist(err) {
		t.Fatalf("skills install created .claude/skills; it should register the plugin, not vendor the skill")
	}
	// The repo root prints symlink-resolved (macOS /var -> /private/var), so
	// assert the message frame and the repo settings suffix rather than an exact
	// path equal to the unresolved t.TempDir().
	const prefix = "registered: cc-notes plugin in "
	suffix := filepath.Join(".claude", "settings.json") + "\n"
	if !strings.HasPrefix(out, prefix) || !strings.HasSuffix(out, suffix) {
		t.Fatalf("install output = %q, want %q…%q for the repo settings.json", out, prefix, suffix)
	}
}

// TestSkillsInstallGlobalRegistersUserSettings drives `skills install --global`
// through the cobra tree and proves it enables the plugin in the user-global
// ~/.claude/settings.json under the test-isolated HOME — never the repo's
// .claude, and without a `cc-notes init`. initRepo redirects HOME to a temp dir,
// so the real ~/.claude/settings.json is untouched.
func TestSkillsInstallGlobalRegistersUserSettings(t *testing.T) {
	dir := initRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)

	out := mustRun(t, dir, "skills", "install", "--global")

	global := filepath.Join(home, ".claude", "settings.json")
	assertCCNotesRegistered(t, global)
	if _, err := os.Stat(filepath.Join(dir, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatalf("--global wrote the repo settings.json; it must target only the user-global file")
	}
	if want := "registered: cc-notes plugin in " + global + "\n"; out != want {
		t.Fatalf("--global install output = %q, want %q", out, want)
	}
}

func TestWorkflowsInstallWritesTree(t *testing.T) {
	dir := initRepo(t)
	out := mustRun(t, dir, "workflows", "install")

	workflow := filepath.Join(dir, ".github", "workflows", "cc-notes.yml")
	//nolint:gosec // G304: reads a path under the test's own temp repo.
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

// TestWorkflowsInstallDest pins the --dest destination override (repo-root
// relative) and that the pre-rename --dir spelling is gone.
func TestWorkflowsInstallDest(t *testing.T) {
	dir := initRepo(t)
	mustRun(t, dir, "workflows", "install", "--dest", filepath.Join("ci", "flows"))

	installed := filepath.Join(dir, "ci", "flows", "cc-notes.yml")
	if _, err := os.Stat(installed); err != nil {
		t.Fatalf("workflow not written under --dest: %v", err)
	}

	_, _, err := runCLI(t, dir, "workflows", "install", "--dir", "elsewhere")
	if err == nil {
		t.Fatal("--dir succeeded, want an unknown-flag usage error after the --dest rename")
	}
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("--dir exit = %d, want 2 (usage); err = %v", got, err)
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

	//nolint:gosec // G304: reads a path under the test's own temp repo.
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
