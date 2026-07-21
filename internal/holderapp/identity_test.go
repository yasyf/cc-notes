package holderapp

import (
	"path/filepath"
	"testing"
)

func TestApplicationPinsFixedSignedIdentity(t *testing.T) {
	application := Application(InstalledPath)
	if application.AppPath != InstalledPath || application.BundleID != BundleID || application.TeamID != TeamID {
		t.Fatalf("application = %+v", application)
	}
	if application.Broker.ExecutableName != "" || application.Broker.SigningIdentifier != "" {
		t.Fatalf("mount-only application unexpectedly configured a broker: %+v", application.Broker)
	}
	if application.Runtime.ExecutableName != ExecutableName || application.Runtime.SigningIdentifier != BundleID {
		t.Fatalf("runtime identity = %+v", application.Runtime)
	}
}

func TestRuntimeDirectoryUsesOnlyV1DerivedState(t *testing.T) {
	directory, err := RuntimeDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(directory) != "fusekit-v1" || filepath.Base(filepath.Dir(directory)) != ".cc-notes" {
		t.Fatalf("runtime directory = %q", directory)
	}
}
