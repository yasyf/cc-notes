package cli

import (
	"bytes"
	"context"
	"testing"
)

func TestPackageCommandsAreExplicitMachineOperations(t *testing.T) {
	previousInstall, previousUninstall := installPackage, uninstallPackage
	t.Cleanup(func() { installPackage, uninstallPackage = previousInstall, previousUninstall })
	for _, test := range []struct {
		verb string
		want string
	}{
		{verb: "install", want: "installed: CCNotesHelper package\n"},
		{verb: "uninstall", want: "uninstalled: CCNotesHelper package\n"},
	} {
		t.Run(test.verb, func(t *testing.T) {
			calls := 0
			operation := func(context.Context) error { calls++; return nil }
			installPackage, uninstallPackage = operation, operation
			root := NewRootCmd()
			var stdout bytes.Buffer
			root.SetOut(&stdout)
			root.SetArgs([]string{"package", test.verb})
			if err := root.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || stdout.String() != test.want {
				t.Fatalf("calls = %d, stdout = %q", calls, stdout.String())
			}
		})
	}
}

func TestPackageCommandsRejectRepositoryAndExtraArguments(t *testing.T) {
	previousInstall, previousUninstall := installPackage, uninstallPackage
	t.Cleanup(func() { installPackage, uninstallPackage = previousInstall, previousUninstall })
	operation := func(context.Context) error {
		t.Fatal("invalid package command invoked an operation")
		return nil
	}
	installPackage, uninstallPackage = operation, operation
	for _, arguments := range [][]string{
		{"--repo", t.TempDir(), "package", "install"},
		{"package", "install", "unexpected"},
		{"--repo", t.TempDir(), "package", "uninstall"},
		{"package", "uninstall", "unexpected"},
	} {
		root := NewRootCmd()
		root.SetArgs(arguments)
		if err := root.ExecuteContext(t.Context()); err == nil || ExitCode(err) != 2 {
			t.Fatalf("arguments %q error = %v", arguments, err)
		}
	}
}
