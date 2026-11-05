package cli_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
)

func TestTaskAddStartsThreeGitProcesses(t *testing.T) {
	dir := gittest.InitRepo(t)
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	gittest.Git(t, dir, "remote", "add", "origin", "https://example.invalid/none.git")
	if err := os.WriteFile(filepath.Join(dir, ".git", "relative.config"), []byte("[memo]\n\tkey = value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "config", "--add", "include.path", "relative.config")
	mustRun(t, dir, "task", "add", "wires the remote")
	trace := filepath.Join(t.TempDir(), "trace")
	t.Setenv("GIT_TRACE", trace)
	mustRun(t, dir, "task", "add", "writes again")
	t.Setenv("GIT_TRACE", "")
	data, err := os.ReadFile(trace)
	if err != nil {
		t.Fatal(err)
	}
	var commands []string
	for line := range strings.Lines(string(data)) {
		_, command, found := strings.Cut(line, "trace: built-in: git ")
		if !found {
			continue
		}
		commands = append(commands, strings.Fields(command)[0])
	}
	if want := []string{"rev-parse", "config", "update-ref"}; !reflect.DeepEqual(commands, want) {
		t.Fatalf("git processes = %q, want %q", commands, want)
	}
}
