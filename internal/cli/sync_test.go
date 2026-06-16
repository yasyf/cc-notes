package cli_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/internal/version"
)

type syncJSON struct {
	Created       int `json:"created"`
	FastForwarded int `json:"fast_forwarded"`
	Merged        int `json:"merged"`
	Pushed        int `json:"pushed"`
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
	if !strings.Contains(fetch, "+refs/cc-notes/*:refs/cc-notes/*") {
		t.Fatalf("fetch refspecs = %q, want cc-notes refspec", fetch)
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
	if again, _ := os.ReadFile(hook); string(again) != string(body) {
		t.Fatalf("refused install still clobbered hook: %q", again)
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

func TestTaskMove(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "Ship it")

	out := mustRun(t, dir, "task", "move", task.ID, "--to", "release/1.0")
	if want := task.ID[:7] + "\topen\tP2\t-\tShip it\n"; out != want {
		t.Fatalf("move output = %q, want the moved lean line %q", out, want)
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

	if out := mustRun(t, dir, "task", "move", task.ID, "--backlog"); !strings.HasPrefix(out, task.ID[:7]+"\t") {
		t.Fatalf("move --backlog output = %q, want the moved lean line", out)
	}
	if shown := mustJSON[[]taskJSON](t, mustRun(t, dir, "task", "list", "--backlog", "--json")); len(shown) != 1 || shown[0].Branch != "" {
		t.Fatalf("backlog list = %+v, want the single backlog task", shown)
	}

	_, _, err := runCLI(t, dir, "task", "move", task.ID)
	if cli.ExitCode(err) != 2 {
		t.Fatalf("move without --to err = %v, want exit 2", err)
	}
	_, _, err = runCLI(t, dir, "task", "move", "feedfacefeedface", "--to", "release/2.0")
	if !errors.Is(err, store.ErrNotFound) || cli.ExitCode(err) != 3 {
		t.Fatalf("move unknown id err = %v (exit %d), want not-found exit 3", err, cli.ExitCode(err))
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
