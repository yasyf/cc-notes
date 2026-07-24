package helperdeployment

import (
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/version"
)

func TestHelperMarketingVersionIsExactReleaseTag(t *testing.T) {
	original := version.Version
	t.Cleanup(func() { version.Version = original })
	for tag, want := range map[string]string{
		"v1.2.3":      "1.2.3",
		"v1.2.3-rc.4": "1.2.3",
	} {
		version.Version = tag
		got, err := helperMarketingVersion()
		if err != nil || got != want {
			t.Fatalf("helperMarketingVersion(%q) = (%q, %v), want %q", tag, got, err, want)
		}
	}
	for _, tag := range []string{"dev", "1.2.3", "v01.2.3", "v1.2", "v1.2.3-rc..1"} {
		version.Version = tag
		if _, err := helperMarketingVersion(); err == nil {
			t.Fatalf("helperMarketingVersion accepted %q", tag)
		}
	}
}

func TestValidDeploymentOperationIDRequiresFullNonzeroSHA256(t *testing.T) {
	exact := strings.Repeat("ab", 32)
	if !validDeploymentOperationID(exact) {
		t.Fatal("valid operation ID was rejected")
	}
	for _, value := range []string{
		"", strings.Repeat("ab", 16), strings.Repeat("AB", 32), strings.Repeat("0", 64),
		strings.Repeat("gg", 32), exact + "00",
	} {
		if validDeploymentOperationID(value) {
			t.Fatalf("invalid operation ID %q was accepted", value)
		}
	}
}
