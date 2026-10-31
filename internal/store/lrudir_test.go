package store

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"slices"
	"sync"
	"testing"
)

func TestLRUDirHitPromotes(t *testing.T) {
	for _, tc := range []struct {
		name string
		hits []string
		want []string
	}{
		{"no hit evicts the oldest write", nil, []string{"b", "c", "d"}},
		{"a hit on the oldest keeps it", []string{"a"}, []string{"a", "c", "d"}},
		{"later hits rank above earlier ones", []string{"b", "a"}, []string{"a", "b", "d"}},
		{"a hit on an unknown name changes nothing", []string{"z"}, []string{"b", "c", "d"}},
		{"unknown hits cannot displace a real hit", []string{"a", "w", "x", "y", "z"}, []string{"a", "c", "d"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &lruDir{capacity: 3, dir: t.TempDir()}
			t.Cleanup(func() { _ = l.close() })
			for _, name := range []string{"a", "b", "c"} {
				l.write(name, []byte(name))
			}
			for _, name := range tc.hits {
				l.touch(name)
			}
			l.write("d", []byte("d"))
			if got := slices.Sorted(slices.Values(l.names())); !slices.Equal(got, tc.want) {
				t.Fatalf("entries = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLRUDirRacingFirstWritesEvictTheOldestPersistedEntries(t *testing.T) {
	dir := t.TempDir()
	seed := &lruDir{capacity: 4, dir: dir}
	for _, name := range []string{"a", "b", "c", "d"} {
		seed.write(name, []byte(name))
	}
	if err := seed.close(); err != nil {
		t.Fatal(err)
	}
	l := &lruDir{capacity: 4, dir: dir}
	t.Cleanup(func() { _ = l.close() })
	var wg sync.WaitGroup
	for _, name := range []string{"w", "x", "y"} {
		wg.Go(func() { l.write(name, []byte(name)) })
	}
	wg.Wait()
	if got, want := l.names(), []string{"d", "w", "x", "y"}; !slices.Equal(got, want) {
		t.Fatalf("entries = %v, want %v", got, want)
	}
}

func cacheEntryPath(t *testing.T, l *lruDir, name string) string {
	return cacheEntryFrame(t, l, name).path
}

type cacheFrameLocation struct {
	path     string
	slotPath string
	offset   int64
	size     int
	inline   bool
}

func cacheEntryFrame(t *testing.T, l *lruDir, name string) cacheFrameLocation {
	t.Helper()
	entry, ok := l.entry(name)
	if !ok {
		t.Fatalf("cache entry %s missing", name)
	}
	f, ok := l.openEntry(entry)
	if !ok {
		t.Fatalf("cache entry %s missing", name)
	}
	location := cacheFrameLocation{path: f.Name(), slotPath: l.slotPath(entry.slot), inline: entry.inline}
	if entry.inline {
		location.offset = int64(entry.slot) * cacheInlineStride
		location.size = cacheTokenSize + 1 + len(name) + sha256.Size + int(entry.size)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return location
}

func (p cacheFrameLocation) read(t *testing.T) []byte {
	t.Helper()
	if !p.inline {
		data, err := os.ReadFile(p.path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	f, err := os.Open(p.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	data := make([]byte, p.size)
	if _, err := f.ReadAt(data, p.offset); err != nil {
		t.Fatal(err)
	}
	return data
}

func (p cacheFrameLocation) write(t *testing.T, data []byte) {
	t.Helper()
	if !p.inline {
		if err := os.WriteFile(p.path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	var record [cacheInlineStride]byte
	if len(data) > len(record) {
		t.Fatalf("inline fixture frame has %d bytes", len(data))
	}
	copy(record[:], data)
	f, err := os.OpenFile(p.path, os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.WriteAt(record[:], p.offset); err != nil {
		t.Fatal(err)
	}
}

func (p cacheFrameLocation) assertErased(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(p.slotPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed external payload remains: %v", err)
	}
	if p.inline {
		p.assertRecordZero(t)
	}
}

func (p cacheFrameLocation) assertRecordZero(t *testing.T) {
	t.Helper()
	f, err := os.Open(p.path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var record [cacheInlineStride]byte
	n, err := f.ReadAt(record[:], p.offset)
	if err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if !bytes.Equal(record[:n], make([]byte, n)) {
		t.Fatal("removed inline payload remains")
	}
}
