package cli_test

import (
	"slices"
	"strings"
	"testing"

	"errors"
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

func TestTaskPromote(t *testing.T) {
	dir := initRepo(t)
	task := addTask(t, dir, "Ship it")
	closed := addTask(t, dir, "Closed")
	mustRun(t, dir, "task", "done", closed.ID)

	out := mustRun(t, dir, "task", "promote", "--to", "release/1.0")
	if want := task.ID[:7] + "\topen\tP2\t-\tShip it\n"; out != want {
		t.Fatalf("promote output = %q, want the promoted lean line %q (closed tasks stay)", out, want)
	}
	if out := mustRun(t, dir, "task", "list", "--branch", "release/1.0"); !strings.HasPrefix(out, task.ID[:7]+"\t") {
		t.Fatalf("destination list = %q, want %s", out, task.ID[:7])
	}
	if out := mustRun(t, dir, "task", "list"); strings.Contains(out, task.ID[:7]) {
		t.Fatalf("source list = %q, want %s gone", out, task.ID[:7])
	}
	shown := mustJSON[[]taskJSON](t, mustRun(t, dir, "task", "list", "--branch", "release/1.0", "--json"))
	if len(shown) != 1 || shown[0].Branch != "release/1.0" {
		t.Fatalf("promoted branch = %q, want release/1.0", shown[0].Branch)
	}

	_, _, err := runCLI(t, dir, "task", "promote", task.ID)
	if cli.ExitCode(err) != 2 {
		t.Fatalf("promote without --to err = %v, want exit 2", err)
	}
	_, _, err = runCLI(t, dir, "task", "promote", "--to", "release/2.0", task.ID)
	if !errors.Is(err, ccsync.ErrNotLive) || cli.ExitCode(err) != 4 {
		t.Fatalf("promote promoted-away id err = %v (exit %d), want ErrNotLive exit 4", err, cli.ExitCode(err))
	}
	_, _, err = runCLI(t, dir, "task", "promote", "--to", "release/1.0", "feedfacefeedface")
	if !errors.Is(err, store.ErrNotFound) || cli.ExitCode(err) != 3 {
		t.Fatalf("promote unknown id err = %v (exit %d), want not-found exit 3", err, cli.ExitCode(err))
	}
}

func TestTaskPromoteExplicitIDsAndFrom(t *testing.T) {
	dir := initRepo(t)
	one := addTask(t, dir, "One", "--branch", "feature")
	two := addTask(t, dir, "Two", "--branch", "feature")
	out := mustRun(t, dir, "task", "promote", "--from", "feature", "--to", "main", one.ID, two.ID)
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	want := []string{one.ID[:7] + "\topen\tP2\t-\tOne", two.ID[:7] + "\topen\tP2\t-\tTwo"}
	if !slices.Equal(lines, want) {
		t.Fatalf("promote output = %v, want lean lines %v", lines, want)
	}
	if out := mustRun(t, dir, "task", "list"); strings.Count(out, "\n") != 2 {
		t.Fatalf("main list = %q, want both tasks", out)
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
