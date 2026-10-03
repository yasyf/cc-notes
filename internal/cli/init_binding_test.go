package cli_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/store"
)

// TestInitRefusesBindingPublishedDuringInstall pins R3: a binding published
// after init opened the unbound checkout and before its refspecs landed is
// the binding error at exit 1, and the bound backend receives no config.
func TestInitRefusesBindingPublishedDuringInstall(t *testing.T) {
	checkout, _ := initRepoWithRemote(t)
	backend := initRepo(t)
	commitFile(t, backend, "README.md", "backend\n")
	addRemote(t, backend, "origin")
	backendBefore := gitConfigFile(t, backend)
	backendCommon := canonicalPath(t, filepath.Join(backend, ".git"))
	info, err := os.Stat(backendCommon)
	if err != nil {
		t.Fatalf("stat %s: %v", backendCommon, err)
	}
	device, inode := gitobj.FileID(info)
	binding := store.Binding{CommonDir: backendCommon, Device: device, Inode: inode}
	gittest.GitShim{
		Pattern:    `*"config --local --add remote.origin.fetch "*`,
		N:          1,
		Interleave: [][]string{{"config", "--file", filepath.Join(checkout, ".git", "config"), "cc-notes.storage", binding.String()}},
	}.Install(t)

	stdout, _, err := runCLI(t, checkout, "init", "--no-ci")
	if !errors.Is(err, store.ErrBindingChanged) || cli.ExitCode(err) != 1 {
		t.Fatalf("init = %v (exit %d), want ErrBindingChanged at exit 1", err, cli.ExitCode(err))
	}
	if stdout != "" {
		t.Fatalf("init reported %q after a refused install", stdout)
	}
	if !strings.Contains(err.Error(), ".git/config") {
		t.Fatalf("error %q does not name the config already written", err)
	}
	if got := gitConfigFile(t, backend); got != backendBefore {
		t.Fatalf("bound backend config changed:\n%s\nwas:\n%s", got, backendBefore)
	}
	if got := gittest.Git(t, checkout, "config", "cc-notes.storage"); got != binding.String() {
		t.Fatalf("checkout binding = %q, want the published %q kept", got, binding.String())
	}
}
