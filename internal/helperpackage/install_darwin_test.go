//go:build darwin

package helperpackage

import (
	"context"
	"errors"
	"testing"
)

func TestInstallDelegatesExactPackagedCandidate(t *testing.T) {
	var got string
	ops := operations{
		packagedPath: func() (string, error) { return "/prefix/libexec/CCNotesHelper.app", nil },
		install: func(_ context.Context, source string) error {
			got = source
			return nil
		},
	}
	if err := install(t.Context(), ops); err != nil {
		t.Fatal(err)
	}
	if got != "/prefix/libexec/CCNotesHelper.app" {
		t.Fatalf("candidate = %q", got)
	}
}

func TestInstallPropagatesCandidateFailure(t *testing.T) {
	want := errors.New("candidate rejected")
	ops := operations{
		packagedPath: func() (string, error) { return "/prefix/libexec/CCNotesHelper.app", nil },
		install:      func(context.Context, string) error { return want },
	}
	if err := install(t.Context(), ops); !errors.Is(err, want) {
		t.Fatalf("install error = %v", err)
	}
}

func TestUninstallDelegatesSealedRemoval(t *testing.T) {
	called := false
	ops := operations{uninstall: func(context.Context) error {
		called = true
		return nil
	}}
	if err := uninstall(t.Context(), ops); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("sealed uninstall was not delegated")
	}
}
