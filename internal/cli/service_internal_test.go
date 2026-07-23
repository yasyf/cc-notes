package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestServiceInstallIsMachineOnlyAndDeterministic(t *testing.T) {
	previous := installService
	previousProvision := provisionRepository
	t.Cleanup(func() {
		installService = previous
		provisionRepository = previousProvision
	})
	t.Chdir(t.TempDir())
	provisionRepository = func(context.Context, string) error {
		t.Fatal("service install attempted repository provisioning")
		return nil
	}
	calls := 0
	installService = func(context.Context) error {
		calls++
		return nil
	}

	root := NewRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{"service", "install"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("install calls = %d, want 1", calls)
	}
	if got, want := stdout.String(), "installed: CCNotesHelper service\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestServiceInstallRejectsRepositoryAndArguments(t *testing.T) {
	previous := installService
	t.Cleanup(func() { installService = previous })
	installService = func(context.Context) error {
		t.Fatal("install invoked for invalid command")
		return nil
	}
	for _, args := range [][]string{
		{"--repo", t.TempDir(), "service", "install"},
		{"-R", t.TempDir(), "service", "install"},
		{"service", "--repo", t.TempDir(), "install"},
		{"service", "-R", t.TempDir(), "install"},
		{"service", "install", "--repo", t.TempDir()},
		{"service", "install", "-R", t.TempDir()},
		{"service", "install", "unexpected"},
		{"--repo", t.TempDir(), "service", "uninstall"},
		{"-R", t.TempDir(), "service", "uninstall"},
		{"service", "--repo", t.TempDir(), "uninstall"},
		{"service", "-R", t.TempDir(), "uninstall"},
		{"service", "uninstall", "--repo", t.TempDir()},
		{"service", "uninstall", "-R", t.TempDir()},
		{"service", "uninstall", "unexpected"},
	} {
		root := NewRootCmd()
		root.SetArgs(args)
		if err := root.ExecuteContext(t.Context()); err == nil {
			t.Fatalf("accepted arguments %q", args)
		} else if ExitCode(err) != 2 {
			t.Fatalf("arguments %q exit = %d, want 2: %v", args, ExitCode(err), err)
		}
	}
}

func TestServiceUninstallIsMachineOnlyAndDeterministic(t *testing.T) {
	previous, previousProvision := uninstallService, provisionRepository
	t.Cleanup(func() {
		uninstallService, provisionRepository = previous, previousProvision
	})
	t.Chdir(t.TempDir())
	provisionRepository = func(context.Context, string) error {
		t.Fatal("service uninstall attempted repository provisioning")
		return nil
	}
	calls := 0
	uninstallService = func(context.Context) error {
		calls++
		return nil
	}
	root := NewRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetArgs([]string{"service", "uninstall"})
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("uninstall calls = %d, want 1", calls)
	}
	if got, want := stdout.String(), "uninstalled: CCNotesHelper service\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
}

func TestServiceUninstallReturnsDeactivationFailure(t *testing.T) {
	previous := uninstallService
	t.Cleanup(func() { uninstallService = previous })
	want := errors.New("deactivation failed")
	uninstallService = func(context.Context) error { return want }
	root := NewRootCmd()
	root.SetArgs([]string{"service", "uninstall"})
	err := root.ExecuteContext(t.Context())
	if !errors.Is(err, want) || strings.Contains(err.Error(), "repository") || ExitCode(err) != 1 {
		t.Fatalf("error = %v, want %v", err, want)
	}
}

func TestServiceInstallReturnsDeploymentFailure(t *testing.T) {
	previous := installService
	t.Cleanup(func() { installService = previous })
	want := errors.New("deployment failed")
	installService = func(context.Context) error { return want }
	root := NewRootCmd()
	root.SetArgs([]string{"service", "install"})
	err := root.ExecuteContext(t.Context())
	if !errors.Is(err, want) || strings.Contains(err.Error(), "repository") || ExitCode(err) != 1 {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
