package helperclient

import (
	"context"
	"testing"
)

func TestProvisionRepositoryUsesOnlyCanonicalSignedHelper(t *testing.T) {
	originalPath, originalProvision := serviceExecutablePath, serviceRunProvision
	t.Cleanup(func() {
		serviceExecutablePath, serviceRunProvision = originalPath, originalProvision
	})
	const executable = "/Users/test/Applications/CCNotesHelper.app/Contents/MacOS/CCNotesHelper"
	serviceExecutablePath = func() (string, error) { return executable, nil }
	const root = "/tmp/repository"
	serviceRunProvision = func(_ context.Context, gotExecutable, gotRoot string) error {
		if gotExecutable != executable || gotRoot != root {
			t.Fatalf("provision = (%q, %q)", gotExecutable, gotRoot)
		}
		return nil
	}
	if err := ProvisionRepository(t.Context(), root); err != nil {
		t.Fatal(err)
	}
}
