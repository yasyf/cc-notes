package helperclient

import (
	"os/user"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/version"
)

func TestInstalledPathUsesCanonicalAccountHome(t *testing.T) {
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", t.TempDir())
	path, err := InstalledPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(account.HomeDir, "Applications", "CCNotesHelper.app")
	if path != want {
		t.Fatalf("installed path = %q, want %q", path, want)
	}
}

func TestHelperReleaseIdentityIsExact(t *testing.T) {
	identity := CodeIdentity()
	if identity.TeamID != TeamID || identity.SigningIdentifier != BundleID {
		t.Fatalf("identity = %#v", identity)
	}
	original := version.Version
	t.Cleanup(func() { version.Version = original })
	for tag, want := range map[string]string{
		"v1.2.3":      "1.2.3",
		"v1.2.3-rc.4": "1.2.3",
	} {
		version.Version = tag
		got, err := MarketingVersion()
		if err != nil || got != want {
			t.Fatalf("MarketingVersion(%q) = (%q, %v), want %q", tag, got, err, want)
		}
	}
	for _, tag := range []string{"dev", "1.2.3", "v01.2.3", "v1.2", "v1.2.3-rc..1"} {
		version.Version = tag
		if _, err := MarketingVersion(); err == nil {
			t.Fatalf("MarketingVersion accepted %q", tag)
		}
	}
}
