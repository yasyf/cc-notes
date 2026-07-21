package fusefs

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yasyf/fusekit/mountproto"
)

func TestRepositoryProvisionIsOpaqueExactAndStable(t *testing.T) {
	presentation := filepath.Join(t.TempDir(), "mount")
	repository := filepath.Join(t.TempDir(), "repository")
	first, err := NewRepositoryProvision(presentation, repository)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewRepositoryProvision(presentation, repository)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("same repository changed identity: %+v != %+v", first, second)
	}
	if first.Tenant.Generation != 1 || first.Definition.Generation != 1 ||
		first.Definition.PresentationRoot != filepath.Join(presentation, first.Tenant.RouteName) ||
		first.Definition.BackingRoot != repository ||
		first.Definition.ContentSourceID != string(first.Tenant.Authority) ||
		first.Definition.AccessMode != mountproto.AccessModeReadWrite ||
		first.Definition.CasePolicy != mountproto.CasePolicySensitive ||
		len(first.Definition.Presentations) != 1 || first.Definition.Presentations[0] != mountproto.PresentationMount {
		t.Fatalf("repository provision = %+v", first)
	}
	if first.Declaration.Authority != first.Tenant.Authority || first.Declaration.DriverID != gitDriverID {
		t.Fatalf("source declaration = %+v", first.Declaration)
	}
	other, err := NewRepositoryProvision(presentation, filepath.Join(t.TempDir(), "repository"))
	if err != nil {
		t.Fatal(err)
	}
	if other.Tenant.ID == first.Tenant.ID || other.Tenant.Authority == first.Tenant.Authority ||
		other.Tenant.RouteName == first.Tenant.RouteName {
		t.Fatalf("distinct repository reused identity: %+v", other)
	}
}

func TestRepositoryProvisionRejectsNoncanonicalRoots(t *testing.T) {
	presentation := filepath.Join(t.TempDir(), "mount")
	for _, test := range []struct {
		presentation string
		repository   string
	}{
		{presentation: "relative", repository: t.TempDir()},
		{presentation: presentation, repository: "relative"},
		{presentation: presentation + "/../mount", repository: t.TempDir()},
		{presentation: presentation, repository: t.TempDir() + "/../repository"},
	} {
		if _, err := NewRepositoryProvision(test.presentation, test.repository); err == nil {
			t.Fatalf("noncanonical provision accepted: %+v", test)
		}
	}
}
