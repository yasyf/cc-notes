package gitobj

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestManagedPackNamesReuseTheListUntilTheDirectoryChanges(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, packDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	const real = "ccn0-1111111111111111111111111111111111111111.idx"
	for _, name := range []string{real, "pack-2222222222222222222222222222222222222222.idx"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	storage := &objectStorage{root: root}
	modified := func() os.FileInfo {
		t.Helper()
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		return info
	}
	names, err := storage.managedPackNames(dir, modified().ModTime())
	if err != nil || !reflect.DeepEqual(names, []string{real}) {
		t.Fatalf("listed %q, %v", names, err)
	}
	const remembered = "ccn1-3333333333333333333333333333333333333333.idx"
	key := strconv.FormatInt(modified().ModTime().UnixNano(), 10)
	if err := os.WriteFile(filepath.Join(root, managedPackListFile), []byte(key+"\n"+remembered+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if names, err = storage.managedPackNames(dir, modified().ModTime()); err != nil || !reflect.DeepEqual(names, []string{remembered}) {
		t.Fatalf("an unchanged directory was read again: %q, %v", names, err)
	}
	const added = "ccn0-4444444444444444444444444444444444444444.idx"
	if err := os.WriteFile(filepath.Join(dir, added), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if names, err = storage.managedPackNames(dir, modified().ModTime()); err != nil || !reflect.DeepEqual(names, []string{real, added}) {
		t.Fatalf("a changed directory kept the old list: %q, %v", names, err)
	}
}
