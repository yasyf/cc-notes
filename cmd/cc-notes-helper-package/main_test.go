package main

import (
	"path/filepath"
	"testing"
)

func TestRunRequiresExactApplicationAndSigningIdentity(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Applications", "CCNotesHelper.app")
	for _, arguments := range [][]string{
		nil,
		{"-app", app},
		{"-signing-identity", "Developer ID Application: Example"},
		{"-app", app, "-signing-identity", "Developer ID Application: Example", "extra"},
	} {
		if err := run(t.Context(), arguments); err == nil {
			t.Fatalf("run(%q) accepted incomplete invocation", arguments)
		}
	}
}
