package cli

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
)

func TestInitProvisionsRepositoryWithoutInstallingService(t *testing.T) {
	repo := gittest.InitRepo(t)
	remote := gittest.InitBare(t)
	gittest.Git(t, repo, "remote", "add", "origin", remote)
	wantRoot, err := filepath.EvalSymlinks(repo)
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	previousProvision, previousInstall, previousUninstall := provisionRepository, installService, uninstallService
	t.Cleanup(func() {
		provisionRepository, installService, uninstallService = previousProvision, previousInstall, previousUninstall
	})
	calls := 0
	provisionRepository = func(_ context.Context, root string) error {
		calls++
		if root != wantRoot {
			t.Fatalf("provision root = %q, want %q", root, wantRoot)
		}
		return nil
	}
	installService = func(context.Context) error {
		t.Fatal("init attempted service installation")
		return nil
	}
	uninstallService = func(context.Context) error {
		t.Fatal("init attempted service deactivation")
		return nil
	}
	root := NewRootCmd()
	root.SetArgs([]string{"init", "--no-ci"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("provision calls = %d, want 1", calls)
	}
}
