//go:build darwin

package helperpackage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestInstallStagesVerifiesSwapsAndActivates(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "delivered", "CCNotesHelper.app")
	target := filepath.Join(root, "Applications", "CCNotesHelper.app")
	writeMarker(t, source, "new")
	writeMarker(t, target, "old")
	var calls []string
	ops := testOperations(source, target)
	ops.copyApp = func(_ context.Context, from, to string) error {
		calls = append(calls, "copy")
		writeMarker(t, to, readMarker(t, from))
		return nil
	}
	ops.verifyCopy = func(_ context.Context, from, to string) error {
		calls = append(calls, "verify")
		if readMarker(t, from) != readMarker(t, to) {
			return errors.New("copy differs")
		}
		return nil
	}
	ops.deactivate = func(context.Context) error {
		calls = append(calls, "deactivate")
		if readMarker(t, target) != "old" {
			t.Fatal("old helper was not canonical during deactivation")
		}
		return nil
	}
	ops.activate = func(context.Context) error {
		calls = append(calls, "activate")
		if readMarker(t, target) != "new" {
			t.Fatal("new helper was not canonical during activation")
		}
		return nil
	}
	if err := install(t.Context(), ops); err != nil {
		t.Fatal(err)
	}
	if got := readMarker(t, target); got != "new" {
		t.Fatalf("installed marker = %q", got)
	}
	if want := []string{"copy", "verify", "deactivate", "activate"}; !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %q, want %q", calls, want)
	}
}

func TestInstallActivationFailureRestoresAndReactivatesPriorGeneration(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "delivered", "CCNotesHelper.app")
	target := filepath.Join(root, "Applications", "CCNotesHelper.app")
	writeMarker(t, source, "new")
	writeMarker(t, target, "old")
	ops := testOperations(source, target)
	ops.copyApp = func(_ context.Context, from, to string) error {
		writeMarker(t, to, readMarker(t, from))
		return nil
	}
	ops.verifyCopy = func(context.Context, string, string) error { return nil }
	var deactivations, activations int
	activationFailure := errors.New("new generation failed")
	ops.deactivate = func(context.Context) error {
		deactivations++
		return nil
	}
	ops.activate = func(context.Context) error {
		activations++
		if readMarker(t, target) == "new" {
			return activationFailure
		}
		return nil
	}
	err := install(t.Context(), ops)
	if !errors.Is(err, activationFailure) {
		t.Fatalf("install error = %v", err)
	}
	if got := readMarker(t, target); got != "old" {
		t.Fatalf("restored marker = %q", got)
	}
	if deactivations != 2 || activations != 2 {
		t.Fatalf("deactivations = %d, activations = %d", deactivations, activations)
	}
}

func TestUninstallDeactivatesBeforeRemovingCanonicalApp(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "Applications", "CCNotesHelper.app")
	writeMarker(t, target, "installed")
	ops := testOperations("", target)
	deactivated := false
	ops.deactivate = func(context.Context) error {
		deactivated = true
		if readMarker(t, target) != "installed" {
			t.Fatal("helper was removed before deactivation")
		}
		return nil
	}
	if err := uninstall(t.Context(), ops); err != nil {
		t.Fatal(err)
	}
	if !deactivated {
		t.Fatal("deactivation was not called")
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installed helper still exists: %v", err)
	}
}

func testOperations(source, target string) operations {
	return operations{
		packagedPath:  func() (string, error) { return source, nil },
		installedPath: func() (string, error) { return target, nil },
		rename:        os.Rename,
		removeAll:     os.RemoveAll,
	}
}

func writeMarker(t *testing.T, app, value string) {
	t.Helper()
	if err := os.MkdirAll(app, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app, "marker"), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readMarker(t *testing.T, app string) string {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(app, "marker"))
	if err != nil {
		t.Fatal(err)
	}
	return string(payload)
}
