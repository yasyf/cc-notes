package cli_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/gittest"
)

var errStdinBoom = errors.New("stdin boom")

// errReader fails every Read, so `--body -` surfaces a stdin error at the exact
// point bodyArg runs.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errStdinBoom }

// assertNoRefspec fails if .git/config carries a cc-notes refspec — the side
// effect autoInstall writes once the store is open against a remote.
func assertNoRefspec(t *testing.T, dir string) {
	t.Helper()
	cfg, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatalf("read .git/config: %v", err)
	}
	if strings.Contains(string(cfg), "cc-notes") {
		t.Fatalf("add errored but autoInstall still wrote a cc-notes refspec to .git/config:\n%s", cfg)
	}
}

// TestAddBodyValidatedBeforeAutoInstall pins the document add order: the body is
// read and validated before the store opens, so a body error never reaches
// openStore's auto-install side effect. It is the regression guard for the
// body-before-openStore normalization in the document add builder and fails if
// bodyArg (or the required-body check beside it) moves after autoInstall. Each
// case wires a bogus origin remote so autoInstall would install a cc-notes
// fetch/push refspec the moment the store opened.
func TestAddBodyValidatedBeforeAutoInstall(t *testing.T) {
	t.Run("note stdin read error", func(t *testing.T) {
		dir := initRepo(t)
		gittest.Git(t, dir, "remote", "add", "origin", "https://example.com/repo.git")
		t.Chdir(dir)
		root := cli.NewRootCmd()
		var out, errbuf bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&errbuf)
		root.SetIn(errReader{})
		root.SetArgs([]string{"note", "add", "Fact", "--body", "-"})
		if err := root.ExecuteContext(t.Context()); err == nil {
			t.Fatalf("note add --body - with a failing stdin: want error, got nil (stderr %q)", errbuf.String())
		}
		assertNoRefspec(t, dir)
	})

	t.Run("doc empty body", func(t *testing.T) {
		dir := initRepo(t)
		gittest.Git(t, dir, "remote", "add", "origin", "https://example.com/repo.git")
		if _, stderr, err := runCLI(t, dir, "doc", "add", "Fact"); err == nil {
			t.Fatalf("doc add with an empty body: want error, got nil (stderr %q)", stderr)
		}
		assertNoRefspec(t, dir)
	})
}

// TestSprintActivateIdempotent pins that activating an already-active sprint
// succeeds: the transition guard admits Planned or Active, so re-activating is
// idempotent, not a conflict. It fails if the guard narrows to planned-only.
func TestSprintActivateIdempotent(t *testing.T) {
	dir := initRepo(t)
	sprint := mustJSON[struct {
		ID string `json:"id"`
	}](t, mustRun(t, dir, "sprint", "add", "Sprint", "--json"))
	mustRun(t, dir, "sprint", "activate", sprint.ID)
	if _, stderr, err := runCLI(t, dir, "sprint", "activate", sprint.ID); err != nil {
		t.Fatalf("re-activate an active sprint: want success (idempotent), got %v (stderr %q)", err, stderr)
	}
}
