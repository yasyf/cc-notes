package lfs_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/yasyf/cc-notes/internal/lfs"
)

func TestPutFileGolden(t *testing.T) {
	store := lfs.Store{Dir: filepath.Join(t.TempDir(), "lfs")}
	src := filepath.Join(t.TempDir(), "probe.bin")
	content := []byte("cc-notes attachment probe\n")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	// Golden oid computed with `shasum -a 256` as the independent oracle.
	const wantOID = "e00f953346181c1f17b75a8a3f924a41516f46fcb6a10ac012237c92f35b9bb6"
	oid, size, err := store.PutFile(src)
	if err != nil {
		t.Fatalf("PutFile: %v", err)
	}
	if oid != wantOID || size != int64(len(content)) {
		t.Fatalf("PutFile = %s/%d, want %s/%d", oid, size, wantOID, len(content))
	}
	if want := filepath.Join(store.Dir, "objects", wantOID[0:2], wantOID[2:4], wantOID); store.Path(oid) != want {
		t.Fatalf("Path = %s, want git-lfs layout %s", store.Path(oid), want)
	}
	if !store.Has(oid) {
		t.Fatal("Has = false after PutFile")
	}

	f, err := store.Open(oid)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer f.Close()
	got, err := os.ReadFile(store.Path(oid))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatalf("stored bytes = %q, want %q", got, content)
	}
}

func TestOpenMissing(t *testing.T) {
	store := lfs.Store{Dir: filepath.Join(t.TempDir(), "lfs")}
	oid := strings.Repeat("ab", 32)
	if _, err := store.Open(oid); !errors.Is(err, lfs.ErrObjectMissing) {
		t.Fatalf("Open missing = %v, want ErrObjectMissing", err)
	}
	if store.Has(oid) {
		t.Fatal("Has = true for missing object")
	}
}

func TestPutVerifiedCorrupt(t *testing.T) {
	store := lfs.Store{Dir: filepath.Join(t.TempDir(), "lfs")}
	wantOID := strings.Repeat("cd", 32)

	for _, tc := range []struct {
		name string
		body string
		size int64
	}{
		{name: "wrong bytes", body: "not the content that hashes to oid", size: 34},
		{name: "wrong size", body: "cc-notes attachment probe\n", size: 999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := store.PutVerified(strings.NewReader(tc.body), wantOID, tc.size)
			if !errors.Is(err, lfs.ErrCorrupt) {
				t.Fatalf("PutVerified = %v, want ErrCorrupt", err)
			}
			if store.Has(wantOID) {
				t.Fatal("corrupt object landed in store")
			}
			entries, err := os.ReadDir(filepath.Join(store.Dir, "tmp"))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("tmp not cleaned: %d entries", len(entries))
			}
		})
	}
}

func TestConcurrentSameOIDPut(t *testing.T) {
	store := lfs.Store{Dir: filepath.Join(t.TempDir(), "lfs")}
	content := bytes.Repeat([]byte("x"), 1<<16)
	src := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}

	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	oids := make([]string, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			oids[i], _, errs[i] = store.PutFile(src)
		}()
	}
	wg.Wait()

	for i := range writers {
		if errs[i] != nil {
			t.Fatalf("writer %d: %v", i, errs[i])
		}
		if oids[i] != oids[0] {
			t.Fatalf("writer %d oid %s, want %s", i, oids[i], oids[0])
		}
	}
	got, err := os.ReadFile(store.Path(oids[0]))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("object corrupted by concurrent writers")
	}
}
