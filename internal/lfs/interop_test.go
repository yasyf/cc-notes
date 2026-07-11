package lfs_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/lfs"
)

// TestGitLFSCLIInterop proves the git-lfs CLI reads what our Store writes:
// an object written by Store lands where `git lfs checkout` smudges it from
// and `git lfs fsck` verifies it, and our pointer text matches the CLI's own
// encoder. Skips when git-lfs is not installed.
func TestGitLFSCLIInterop(t *testing.T) {
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs not installed")
	}
	g := initRepo(t)
	t.Setenv("HOME", g.Dir)
	dir := g.Dir

	content := []byte("attachment bytes served to the git-lfs CLI\n")
	src := filepath.Join(t.TempDir(), "blob.bin")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	store := lfs.Store{Dir: filepath.Join(dir, ".git", "lfs")}
	oid, size, err := store.PutFile(src)
	if err != nil {
		t.Fatalf("PutFile: %v", err)
	}

	pointer := fmt.Sprintf("version https://git-lfs.github.com/spec/v1\noid sha256:%s\nsize %d\n", oid, size)
	cliPointer, err := exec.Command("git-lfs", "pointer", "--file", src).Output()
	if err != nil {
		t.Fatalf("git-lfs pointer: %v", err)
	}
	if !strings.Contains(string(cliPointer), pointer) {
		t.Fatalf("git-lfs pointer output does not contain our encoding.\nours:\n%s\ncli:\n%s", pointer, cliPointer)
	}

	// Commit the pointer text as the blob WITHOUT the lfs clean filter
	// rewriting it (the file already is pointer text), then install the lfs
	// filters and let the CLI restore the content from .git/lfs/objects.
	if err := os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("*.bin filter=lfs diff=lfs merge=lfs -text\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.bin"), []byte(pointer), 0o644); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "add", ".gitattributes")
	gittest.Git(t, dir, "-c", "filter.lfs.clean=cat", "-c", "filter.lfs.smudge=cat", "-c", "filter.lfs.process=", "-c", "filter.lfs.required=false", "add", "file.bin")
	gittest.Git(t, dir, "commit", "-q", "-m", "pointer")
	gittest.Git(t, dir, "lfs", "install", "--local")

	if err := os.Remove(filepath.Join(dir, "file.bin")); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "lfs", "checkout", "file.bin")
	if fsck := gittest.Git(t, dir, "lfs", "fsck"); !strings.Contains(fsck, "OK") {
		t.Fatalf("git lfs fsck: %s", fsck)
	}
	got, err := os.ReadFile(filepath.Join(dir, "file.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("git lfs checkout produced %q, want %q", got, content)
	}
}
