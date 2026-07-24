//go:build darwin

package helperpackage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestInstallDelegatesExactPackagedCandidate(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "libexec", "CCNotesHelper.app")
	target := filepath.Join(root, "Applications", "CCNotesHelper.app")
	var applied string
	ops := operations{
		packagedPath:  func() (string, error) { return source, nil },
		installedPath: func() (string, error) { return target, nil },
		apply: func(_ context.Context, candidate string) error {
			applied = candidate
			return nil
		},
	}
	if err := install(t.Context(), ops); err != nil {
		t.Fatal(err)
	}
	if applied != source {
		t.Fatalf("candidate = %q, want %q", applied, source)
	}
	info, err := os.Lstat(filepath.Dir(target))
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("application directory = (%v, %v)", info, err)
	}
}

func TestInstallRejectsIdenticalPackagedAndInstalledPaths(t *testing.T) {
	app := filepath.Join(t.TempDir(), "CCNotesHelper.app")
	called := false
	ops := operations{
		packagedPath:  func() (string, error) { return app, nil },
		installedPath: func() (string, error) { return app, nil },
		apply: func(context.Context, string) error {
			called = true
			return nil
		},
	}
	if err := install(t.Context(), ops); err == nil {
		t.Fatal("install accepted an identical packaged and installed path")
	}
	if called {
		t.Fatal("daemonkit apply was called after path validation failed")
	}
}

func TestInstallRejectsSymlinkedApplicationDirectory(t *testing.T) {
	root := t.TempDir()
	realParent := filepath.Join(root, "real")
	if err := os.Mkdir(realParent, 0o755); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(root, "Applications")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	called := false
	ops := operations{
		packagedPath:  func() (string, error) { return filepath.Join(root, "libexec", "CCNotesHelper.app"), nil },
		installedPath: func() (string, error) { return filepath.Join(linkedParent, "CCNotesHelper.app"), nil },
		apply: func(context.Context, string) error {
			called = true
			return nil
		},
	}
	if err := install(t.Context(), ops); err == nil {
		t.Fatal("install accepted a symlinked application directory")
	}
	if called {
		t.Fatal("daemonkit apply was called after directory validation failed")
	}
}

func TestUninstallDelegatesToDaemonkit(t *testing.T) {
	want := errors.New("sealed uninstall failed")
	calls := 0
	ops := operations{uninstall: func(context.Context) error {
		calls++
		return want
	}}
	if err := uninstall(t.Context(), ops); !errors.Is(err, want) {
		t.Fatalf("uninstall error = %v, want %v", err, want)
	}
	if calls != 1 {
		t.Fatalf("uninstall calls = %d, want 1", calls)
	}
}
