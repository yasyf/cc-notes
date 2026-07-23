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

func TestServiceInstallThenImmediateInit(t *testing.T) {
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
	ready := false
	events := make([]string, 0, 2)
	installService = func(context.Context) error {
		ready = true
		events = append(events, "service-ready")
		return nil
	}
	provisionRepository = func(_ context.Context, root string) error {
		if !ready {
			t.Fatal("init provisioned before service install reported readiness")
		}
		if root != wantRoot {
			t.Fatalf("provision root = %q, want %q", root, wantRoot)
		}
		events = append(events, "init-provision")
		return nil
	}
	uninstallService = func(context.Context) error {
		t.Fatal("install/init sequence attempted service deactivation")
		return nil
	}

	service := NewRootCmd()
	service.SetArgs([]string{"service", "install"})
	if err := service.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0] != "service-ready" {
		t.Fatalf("events after service install = %q", events)
	}

	init := NewRootCmd()
	init.SetArgs([]string{"init", "--no-ci"})
	if err := init.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0] != "service-ready" || events[1] != "init-provision" {
		t.Fatalf("events after immediate init = %q", events)
	}
}
