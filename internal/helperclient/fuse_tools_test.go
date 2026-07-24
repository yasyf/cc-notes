package helperclient

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFUSEToolStateDirectoryIsStableAndPrivate(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	directory, err := fuseToolStateDirectory()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".cc-notes", "helper-tools")
	if directory != want {
		t.Fatalf("state directory = %q, want %q", directory, want)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("state directory mode = %v", info.Mode())
	}
	again, err := fuseToolStateDirectory()
	if err != nil || again != directory {
		t.Fatalf("second state directory = (%q, %v), want (%q, nil)", again, err, directory)
	}
}

func TestFUSEToolStateDirectoryRejectsBroadExistingPermissions(t *testing.T) {
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	directory := filepath.Join(home, ".cc-notes", "helper-tools")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := fuseToolStateDirectory(); err == nil {
		t.Fatal("broad existing FUSE tool state directory was accepted")
	}
}
