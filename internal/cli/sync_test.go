package cli_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/internal/version"
	"github.com/yasyf/cc-notes/plugin"
)

type syncJSON struct {
	Created       int `json:"created"`
	FastForwarded int `json:"fast_forwarded"`
	Merged        int `json:"merged"`
	Pushed        int `json:"pushed"`
	Uploaded      int `json:"uploaded"`
	Downloaded    int `json:"downloaded"`
	Rounds        int `json:"rounds"`
}

// initRepoWithRemote wires a bare remote named origin into a fresh repo.
func initRepoWithRemote(t *testing.T) (dir, bare string) {
	t.Helper()
	dir = initRepo(t)
	bare = t.TempDir()
	mustGit(t, bare, "init", "-q", "--bare")
	mustGit(t, dir, "remote", "add", "origin", bare)
	return dir, bare
}

func TestInitInstallsRefspecs(t *testing.T) {
	dir, _ := initRepoWithRemote(t)
	out := mustRun(t, dir, "init")
	if want := "initialized: refs/cc-notes/* refspecs installed for origin\n"; out != want {
		t.Fatalf("init output = %q, want %q", out, want)
	}
	fetch := mustGit(t, dir, "config", "--get-all", "remote.origin.fetch")
	if !strings.Contains(fetch, "+refs/cc-notes/*:refs/cc-notes-sync/origin/*") {
		t.Fatalf("fetch refspecs = %q, want cc-notes tracking refspec", fetch)
	}
}

// TestInitAutoMountEnabledByDefault proves `init` records the auto-mount
// preference (cc-notes.autoMount=true) and attempts the mount best-effort: a pure
// test binary cannot host fuse, so the mount is skipped with a stderr warning,
// but init still succeeds and the preference persists so a fuse-capable
// session-start ensure-mount brings it up later.
func TestInitAutoMountEnabledByDefault(t *testing.T) {
	dir, _ := initRepoWithRemote(t)
	_, stderr, err := runCLI(t, dir, "init")
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if got := strings.TrimSpace(mustGit(t, dir, "config", "--get", "cc-notes.autoMount")); got != "true" {
		t.Errorf("cc-notes.autoMount = %q, want \"true\"", got)
	}
	// This pure test binary cannot host fuse, so auto-mount silently skips — no
	// holder is spawned and nothing is printed; only the preference persists, for
	// a fuse-capable session-start ensure-mount to honor later.
	if strings.Contains(stderr, "auto-mount") {
		t.Errorf("stderr = %q, want no auto-mount attempt from a non-hosting build", stderr)
	}
}

// TestInitNoMountDisablesAutoMount proves `init --no-mount` records the opt-out
// (cc-notes.autoMount=false) and never attempts a mount, so re-running it can
// disable a previously enabled auto-mount.
func TestInitNoMountDisablesAutoMount(t *testing.T) {
	dir, _ := initRepoWithRemote(t)
	_, stderr, err := runCLI(t, dir, "init", "--no-mount")
	if err != nil {
		t.Fatalf("init --no-mount: %v", err)
	}
	if got := strings.TrimSpace(mustGit(t, dir, "config", "--get", "cc-notes.autoMount")); got != "false" {
		t.Errorf("cc-notes.autoMount = %q, want \"false\"", got)
	}
	if strings.Contains(stderr, "auto-mount") {
		t.Errorf("stderr = %q, want no auto-mount attempt under --no-mount", stderr)
	}
}

// TestSelfInitOnFirstWriteNoRemote proves a fresh `git init` repo needs no
// `cc-notes init` before its first write: `note add` and `task add` each create
// their refs/cc-notes/* ref directly. initRepo never runs `cc-notes init`, so a
// passing assertion documents that self-init on first mutation already works.
func TestSelfInitOnFirstWriteNoRemote(t *testing.T) {
	dir := initRepo(t)
	if refs := mustGit(t, dir, "for-each-ref", "refs/cc-notes/"); refs != "" {
		t.Fatalf("fresh repo already has cc-notes refs %q; want none before any write", refs)
	}

	note := mustJSON[noteJSON](t, mustRun(t, dir, "note", "add", "First", "--json"))
	noteRef := "refs/cc-notes/notes/" + note.ID
	if got := mustGit(t, dir, "rev-parse", "--verify", noteRef); got == "" {
		t.Fatalf("note add did not create %s", noteRef)
	}

	task := addTask(t, dir, "Ship it")
	taskRef := "refs/cc-notes/tasks/" + task.ID
	if got := mustGit(t, dir, "rev-parse", "--verify", taskRef); got == "" {
		t.Fatalf("task add did not create %s", taskRef)
	}
}

// TestSelfInitWiresRefspecsOnFirstWrite proves the first mutating command in a
// repo that has a remote but was never `cc-notes init`-ed installs the
// refs/cc-notes/* refspecs itself via autoInstall — fetch and push both — so the
// note it just created can sync. No `cc-notes init` runs first.
func TestSelfInitWiresRefspecsOnFirstWrite(t *testing.T) {
	dir, bare := initRepoWithRemote(t)
	if fetch := mustGit(t, dir, "config", "--get-all", "remote.origin.fetch"); strings.Contains(fetch, "refs/cc-notes/*") {
		t.Fatalf("fresh repo already has cc-notes fetch refspec %q; want none before any write", fetch)
	}

	mustRun(t, dir, "note", "add", "First")

	fetch := mustGit(t, dir, "config", "--get-all", "remote.origin.fetch")
	if !strings.Contains(fetch, "+refs/cc-notes/*:refs/cc-notes-sync/origin/*") {
		t.Fatalf("first write did not auto-install the fetch refspec: %q", fetch)
	}
	push := mustGit(t, dir, "config", "--get-all", "remote.origin.push")
	if !strings.Contains(push, "refs/cc-notes/*:refs/cc-notes/*") {
		t.Fatalf("first write did not auto-install the push refspec: %q", push)
	}

	mustRun(t, dir, "sync")
	if remote := mustGit(t, bare, "for-each-ref", "refs/cc-notes/"); !strings.Contains(remote, "refs/cc-notes/notes/") {
		t.Fatalf("synced remote refs = %q, want the note ref pushed without a prior `cc-notes init`", remote)
	}
}

func TestInitNoRemote(t *testing.T) {
	dir := initRepo(t)
	_, _, err := runCLI(t, dir, "init")
	if !errors.Is(err, ccsync.ErrRemoteNotFound) || cli.ExitCode(err) != 1 {
		t.Fatalf("init err = %v (exit %d), want ErrRemoteNotFound exit 1", err, cli.ExitCode(err))
	}
}

func TestInitHookInstallsPostMerge(t *testing.T) {
	dir, _ := initRepoWithRemote(t)
	out := mustRun(t, dir, "init", "--hook")
	hook := filepath.Join(dir, ".git", "hooks", "post-merge")
	if !strings.Contains(out, "installed: post-merge hook at "+hook) {
		t.Fatalf("init --hook output = %q, want a post-merge install line for %q", out, hook)
	}
	info, err := os.Stat(hook)
	if err != nil {
		t.Fatalf("stat post-merge hook: %v", err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("post-merge hook mode = %v, want executable", info.Mode().Perm())
	}
	//nolint:gosec // G304: reads the post-merge hook path under the test's own temp repo.
	body, err := os.ReadFile(hook)
	if err != nil {
		t.Fatalf("read post-merge hook: %v", err)
	}
	if want := "#!/bin/sh\nexec cc-notes reconcile\n"; string(body) != want {
		t.Fatalf("post-merge hook body = %q, want %q", body, want)
	}

	_, _, err = runCLI(t, dir, "init", "--hook")
	if cli.ExitCode(err) != 2 {
		t.Fatalf("init --hook over existing hook err = %v (exit %d), want UsageError exit 2", err, cli.ExitCode(err))
	}
	//nolint:gosec // G304: reads the post-merge hook path under the test's own temp repo.
	if again, _ := os.ReadFile(hook); string(again) != string(body) {
		t.Fatalf("refused install still clobbered hook: %q", again)
	}
}

func TestInitCIInstallsWorkflow(t *testing.T) {
	dir, _ := initRepoWithRemote(t)
	out := mustRun(t, dir, "init", "--ci")
	workflow := filepath.Join(dir, ".github", "workflows", "cc-notes.yml")
	suffix := filepath.Join(".github", "workflows", "cc-notes.yml")
	if !strings.Contains(out, "wrote ") || !strings.Contains(out, suffix) {
		t.Fatalf("init --ci output = %q, want a wrote line for %q", out, suffix)
	}
	//nolint:gosec // G304: reads the installed workflow path under the test's own temp repo.
	got, err := os.ReadFile(workflow)
	if err != nil {
		t.Fatalf("read installed workflow: %v", err)
	}
	want, err := plugin.Files.ReadFile("workflows/cc-notes.yml")
	if err != nil {
		t.Fatalf("read embedded workflow: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("installed workflow does not match embedded source")
	}
}

func TestInitWithoutCIWritesNoWorkflow(t *testing.T) {
	dir, _ := initRepoWithRemote(t)
	mustRun(t, dir, "init")
	if _, err := os.Stat(filepath.Join(dir, ".github")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("plain init touched .github (stat err = %v); --ci must be opt-in", err)
	}
}

func TestSyncLeanAndJSON(t *testing.T) {
	dir, bare := initRepoWithRemote(t)
	mustRun(t, dir, "note", "add", "Synced")
	push := mustGit(t, dir, "config", "--get-all", "remote.origin.push")
	if !strings.Contains(push, "refs/cc-notes/*:refs/cc-notes/*") {
		t.Fatalf("mutation did not auto-install push refspec: %q", push)
	}
	out := mustRun(t, dir, "sync")
	if want := "pushed: 1\nrounds: 1\n"; out != want {
		t.Fatalf("sync output = %q, want %q", out, want)
	}
	if refs := mustGit(t, bare, "for-each-ref", "refs/cc-notes/"); strings.Count(refs, "\n")+1 != 1 {
		t.Fatalf("remote refs = %q, want exactly one", refs)
	}
	report := mustJSON[syncJSON](t, mustRun(t, dir, "sync", "--json"))
	if (report != syncJSON{Rounds: 1}) {
		t.Fatalf("quiescent sync --json = %+v, want only rounds: 1", report)
	}
	if out := mustRun(t, dir, "sync"); out != "rounds: 1\n" {
		t.Fatalf("quiescent sync = %q, want rounds only", out)
	}
}

func TestTaskEditBranch(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "Ship it")

	out := mustRun(t, dir, "task", "edit", task.ID, "--branch", "release/1.0")
	if want := task.ID[:7] + "\topen\tP2\t-\tShip it\n"; out != want {
		t.Fatalf("edit --branch output = %q, want the moved lean line %q", out, want)
	}
	if out := mustRun(t, dir, "task", "list", "--branch", "release/1.0"); !strings.HasPrefix(out, task.ID[:7]+"\t") {
		t.Fatalf("destination list = %q, want %s", out, task.ID[:7])
	}
	if out := mustRun(t, dir, "task", "list"); strings.Contains(out, task.ID[:7]) {
		t.Fatalf("source list = %q, want %s gone", out, task.ID[:7])
	}
	shown := mustJSON[[]taskJSON](t, mustRun(t, dir, "task", "list", "--branch", "release/1.0", "--json"))
	if len(shown) != 1 || shown[0].Branch != "release/1.0" {
		t.Fatalf("moved branch = %q, want release/1.0", shown[0].Branch)
	}

	if out := mustRun(t, dir, "task", "edit", task.ID, "--backlog"); !strings.HasPrefix(out, task.ID[:7]+"\t") {
		t.Fatalf("edit --backlog output = %q, want the moved lean line", out)
	}
	if shown := mustJSON[[]taskJSON](t, mustRun(t, dir, "task", "list", "--backlog", "--json")); len(shown) != 1 || shown[0].Branch != "" {
		t.Fatalf("backlog list = %+v, want the single backlog task", shown)
	}

	if _, _, err := runCLI(t, dir, "task", "edit", task.ID, "--branch", "x", "--backlog"); cli.ExitCode(err) != 2 {
		t.Fatalf("edit --branch --backlog err = %v, want exit 2 (mutually exclusive)", err)
	}
	// An explicit empty --branch still conflicts with --backlog: validate() keys
	// off Changed("branch"), not branch != "", so this is the mutual-exclusion
	// usage error, not a later invalid-empty-branch error.
	if _, _, err := runCLI(t, dir, "task", "edit", task.ID, "--branch", "", "--backlog"); err == nil || cli.ExitCode(err) != 2 || !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf(`edit --branch "" --backlog err = %v, want exit 2 with "mutually exclusive"`, err)
	}
	if _, _, err := runCLI(t, dir, "task", "edit", "feedfacefeedface", "--branch", "release/2.0"); !errors.Is(err, store.ErrNotFound) || cli.ExitCode(err) != 3 {
		t.Fatalf("edit unknown id err = %v (exit %d), want not-found exit 3", err, cli.ExitCode(err))
	}
}

// TestTaskMoveRemoved keeps one negative for the deleted verb: "task move" exits
// 2 with a hint pointing at the replacement "task edit --branch".
func TestTaskMoveRemoved(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "Ship it")

	_, _, err := runCLI(t, dir, "task", "move", task.ID)
	if cli.ExitCode(err) != 2 {
		t.Fatalf("task move err = %v, want exit 2 (removed verb)", err)
	}
	if !strings.Contains(err.Error(), "task edit --branch") {
		t.Fatalf("task move error = %q, want the 'task edit --branch' hint", err.Error())
	}
}

func TestVersionCommand(t *testing.T) {
	dir := initRepo(t)
	want := version.String() + "\n"
	if out := mustRun(t, dir, "version"); out != want {
		t.Fatalf("version = %q, want %q", out, want)
	}
	if out := mustRun(t, dir, "--version"); out != want {
		t.Fatalf("--version = %q, want %q", out, want)
	}
}

func TestInitInstallsCIWhenGithubExists(t *testing.T) {
	dir, _ := initRepoWithRemote(t)
	if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o750); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	mustRun(t, dir, "init")
	if _, err := os.Stat(filepath.Join(dir, ".github", "workflows", "cc-notes.yml")); err != nil {
		t.Fatalf("init did not install the reconcile workflow with .github present: %v", err)
	}
}

func TestInitNoCISuppressesWorkflow(t *testing.T) {
	dir, _ := initRepoWithRemote(t)
	if err := os.MkdirAll(filepath.Join(dir, ".github"), 0o750); err != nil {
		t.Fatalf("mkdir .github: %v", err)
	}
	mustRun(t, dir, "init", "--no-ci")
	if _, err := os.Stat(filepath.Join(dir, ".github", "workflows", "cc-notes.yml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("init --no-ci installed the workflow anyway (stat err = %v)", err)
	}
}

func TestInitCIAndNoCIConflict(t *testing.T) {
	dir, _ := initRepoWithRemote(t)
	_, _, err := runCLI(t, dir, "init", "--ci", "--no-ci")
	if cli.ExitCode(err) != 2 {
		t.Fatalf("init --ci --no-ci err = %v (exit %d), want a UsageError exit 2", err, cli.ExitCode(err))
	}
}

func TestInitRegistersPluginWhenClaudeExists(t *testing.T) {
	dir, _ := initRepoWithRemote(t)
	if err := os.MkdirAll(filepath.Join(dir, ".claude"), 0o750); err != nil {
		t.Fatalf("mkdir .claude: %v", err)
	}
	stubUvx(t)
	out := mustRun(t, dir, "init")
	assertCCNotesRegistered(t, filepath.Join(dir, ".claude", "settings.json"))
	if !strings.Contains(out, "registered: cc-notes plugin in .claude/settings.json") {
		t.Fatalf("init output %q missing the registration line", out)
	}
}

func TestInitSkipsClaudeWiringWithoutClaude(t *testing.T) {
	dir, _ := initRepoWithRemote(t)
	out := mustRun(t, dir, "init")
	if _, err := os.Stat(filepath.Join(dir, ".claude")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("init created .claude without one present (stat err = %v); it must never scaffold .claude", err)
	}
	if strings.Contains(out, "registered:") {
		t.Fatalf("init registered the plugin without a .claude/ directory: %q", out)
	}
}

// stubUvx puts a no-op uvx on PATH so init's `capt-hook pack add` step runs
// without the real uvx subprocess or network.
func stubUvx(t *testing.T) {
	t.Helper()
	binDir := t.TempDir()
	//nolint:gosec // G306: uvx stub must be executable (0o755) for the test's PATH lookup to run it.
	if err := os.WriteFile(filepath.Join(binDir, "uvx"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("write uvx stub: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// assertCCNotesRegistered checks that settings.json at path enables the cc-notes
// plugin and registers its marketplace.
func assertCCNotesRegistered(t *testing.T, path string) {
	t.Helper()
	//nolint:gosec // G304: reads a settings.json path under the test's own temp repo.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	ep, _ := m["enabledPlugins"].(map[string]any)
	if ep["cc-notes@cc-notes"] != true {
		t.Fatalf("enabledPlugins missing cc-notes@cc-notes: %v", m["enabledPlugins"])
	}
	mk, _ := m["extraKnownMarketplaces"].(map[string]any)
	cc, _ := mk["cc-notes"].(map[string]any)
	src, _ := cc["source"].(map[string]any)
	if src["source"] != "github" || src["repo"] != "yasyf/cc-notes" {
		t.Fatalf("extraKnownMarketplaces[cc-notes] = %v, want source github yasyf/cc-notes", mk["cc-notes"])
	}
}
