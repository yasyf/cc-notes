package helperclient

import (
	"os/user"
	"path/filepath"
	"testing"
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
