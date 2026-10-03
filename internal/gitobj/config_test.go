package gitobj_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
)

func TestConfigValues(t *testing.T) {
	gittest.ScrubEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	included := filepath.Join(dir, "included")
	gittest.Git(t, dir, "config", "--file", included, "cc-notes.storage", "from-include")
	gittest.Git(t, dir, "config", "--file", path, "include.path", included)
	escaped := `{"version":1,"commonDir":"/tmp/a b;#\"\\x","device":16777232,"inode":41}`
	gittest.Git(t, dir, "config", "--file", path, "cc-notes.storage", escaped)
	gittest.Git(t, dir, "config", "--file", path, "cc-notes.sub.storage", "subsection")
	gittest.Git(t, dir, "config", "--file", path, "--add", "cc-notes.storage", "second")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open config: %v", err)
	}
	if _, err := f.WriteString("[CC-Notes]\n\tStorage = cased\n"); err != nil {
		t.Fatalf("append config: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close config: %v", err)
	}

	cases := []struct {
		name         string
		path         string
		section, key string
		want         []string
	}{
		{"values in file order, exact bytes, case-insensitive names", path, "cc-notes", "storage", []string{escaped, "second", "cased"}},
		{"uppercase lookup", path, "CC-NOTES", "STORAGE", []string{escaped, "second", "cased"}},
		{"other key in the section", path, "cc-notes", "other", nil},
		{"other section", path, "core", "storage", nil},
		{"missing file", filepath.Join(dir, "missing"), "cc-notes", "storage", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := gitobj.ConfigValues(tc.path, tc.section, tc.key)
			if err != nil {
				t.Fatalf("ConfigValues: %v", err)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("ConfigValues(%s.%s) = %q, want %q", tc.section, tc.key, got, tc.want)
			}
		})
	}

	// git's own exact-file read is the oracle for the decode; its --includes
	// read shows the included value this decoder must never surface.
	oracle := strings.Split(gittest.Git(t, dir, "config", "--file", path, "--get-all", "cc-notes.storage"), "\n")
	got, err := gitobj.ConfigValues(path, "cc-notes", "storage")
	if err != nil {
		t.Fatalf("ConfigValues: %v", err)
	}
	if !slices.Equal(got, oracle) {
		t.Fatalf("ConfigValues = %q, git config --file reads %q", got, oracle)
	}
	withIncludes := strings.Split(gittest.Git(t, dir, "config", "--file", path, "--includes", "--get-all", "cc-notes.storage"), "\n")
	if !slices.Contains(withIncludes, "from-include") || slices.Contains(got, "from-include") {
		t.Fatalf("include handling: git --includes reads %q, ConfigValues reads %q; the include target must stay unread", withIncludes, got)
	}

	if err := os.WriteFile(path, []byte("[cc-notes\nstorage = broken\n"), 0o600); err != nil {
		t.Fatalf("write broken config: %v", err)
	}
	if _, err := gitobj.ConfigValues(path, "cc-notes", "storage"); err == nil {
		t.Fatal("ConfigValues decoded a malformed config without error")
	}
}

func TestFileID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	if err := os.WriteFile(path, []byte("one"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	idOf := func(p string) [2]uint64 {
		t.Helper()
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %s: %v", p, err)
		}
		device, inode := gitobj.FileID(info)
		return [2]uint64{device, inode}
	}
	first := idOf(path)
	if first[1] == 0 {
		t.Fatalf("FileID inode = 0 for %s", path)
	}
	if again := idOf(path); again != first {
		t.Fatalf("FileID moved between stats of the same file: %v then %v", first, again)
	}
	if err := os.WriteFile(path, []byte("one more byte"), 0o600); err != nil {
		t.Fatalf("rewrite in place: %v", err)
	}
	if rewritten := idOf(path); rewritten != first {
		t.Fatalf("FileID moved on an in-place rewrite: %v then %v", first, rewritten)
	}
	other := filepath.Join(dir, "g")
	if err := os.WriteFile(other, []byte("two"), 0o600); err != nil {
		t.Fatalf("write other: %v", err)
	}
	if otherID := idOf(other); otherID == first {
		t.Fatalf("FileID identical for two files: %v", first)
	} else if otherID[0] != first[0] {
		t.Fatalf("FileID device differs for siblings in one directory: %v vs %v", first, otherID)
	}
	if err := os.Rename(other, path); err != nil {
		t.Fatalf("replace: %v", err)
	}
	if replaced := idOf(path); replaced == first {
		t.Fatalf("FileID unchanged after the file was replaced: %v", replaced)
	}
	if dirID := idOf(dir); dirID[0] != first[0] || dirID[1] == 0 {
		t.Fatalf("FileID of the directory = %v, want the files' device and a nonzero inode", dirID)
	}
}
