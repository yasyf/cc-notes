package store

import (
	"bytes"
	"crypto/sha256"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

func TestLRUDirReopenAndOverlay(t *testing.T) {
	dir := t.TempDir()
	l := &lruDir{capacity: 2, dir: dir}
	l.write("a", []byte("a"))
	l.write("b", []byte("b"))
	if err := l.close(); err != nil {
		t.Fatal(err)
	}
	l = &lruDir{capacity: 2, dir: dir}
	t.Cleanup(func() { _ = l.close() })
	l.touch("a")
	l.write("c", []byte("c"))
	if got, want := l.names(), []string{"b", "c"}; !slices.Equal(got, want) {
		t.Fatalf("pre-write touch entries = %v, want %v", got, want)
	}
	l.touch("b")
	l.write("d", []byte("d"))
	if got, want := l.names(), []string{"b", "d"}; !slices.Equal(got, want) {
		t.Fatalf("promoted entries = %v, want %v", got, want)
	}
}

func TestLRUDirOverlayRetainsNewerVerifiedHit(t *testing.T) {
	l := newProcessCache(t, 2)
	l.write("a", []byte("old a"))
	l.write("b", []byte("b"))
	old, ok := l.entry("a")
	if !ok {
		t.Fatal("old entry missing")
	}
	l.touch("a")
	l.write("a", []byte("new a"))
	fresh, ok := l.entry("a")
	if !ok {
		t.Fatal("fresh entry missing")
	}
	if data, ok := l.readEntry(fresh); !ok || string(data) != "new a" {
		t.Fatal("fresh publication failed validation")
	}
	l.rememberHit(fresh)
	l.rememberHit(old)
	hit := l.hits[fresh.slot]
	if hit.diskRank != fresh.rank || !bytes.Equal(hit.nonce[:], fresh.token[32:]) {
		t.Fatal("old prefetched hit erased a newer verified hit")
	}
	rank, count := l.rank, len(l.hits)
	l.touch("unknown")
	if l.rank != rank || len(l.hits) != count {
		t.Fatal("unknown touch displaced a real hit")
	}
	l.write("c", []byte("c"))
	if got := l.names(); !slices.Equal(got, []string{"a", "c"}) {
		t.Fatalf("newer hit not promoted: %v", got)
	}
}

func TestLRUDirCapacityAndSlotReuse(t *testing.T) {
	for _, capacity := range []int{0, 1, 3} {
		t.Run(strconv.Itoa(capacity), func(t *testing.T) {
			l := &lruDir{capacity: capacity, dir: t.TempDir()}
			t.Cleanup(func() { _ = l.close() })
			for range 20 {
				for _, name := range []string{"a", "b", "c", "d"} {
					l.write(name, []byte(name))
					if got := len(l.names()); got > capacity {
						t.Fatalf("entries = %d, capacity %d", got, capacity)
					}
					l.remove(name)
				}
			}
			files, err := filepath.Glob(filepath.Join(l.dir, "slot-*"))
			if err != nil || len(files) > capacity {
				t.Fatalf("slots = %v, %v, capacity %d", files, err, capacity)
			}
		})
	}
}

func TestLRUDirCorruptMetadataAndTokensMiss(t *testing.T) {
	for _, mode := range []string{"token", "key", "truncated", "metadata", "capacity"} {
		t.Run(mode, func(t *testing.T) {
			l := &lruDir{capacity: 1, dir: t.TempDir()}
			t.Cleanup(func() { _ = l.close() })
			l.write("a", []byte("a"))
			location := cacheEntryFrame(t, l, "a")
			switch mode {
			case "token", "key":
				data := location.read(t)
				at := 32
				if mode == "key" {
					at = 0
				}
				data[at] ^= 1
				location.write(t, data)
			case "truncated":
				if err := os.Truncate(location.path, location.offset+3); err != nil {
					t.Fatal(err)
				}
			case "metadata", "capacity":
				if err := l.close(); err != nil {
					t.Fatal(err)
				}
				capacity := 1
				if mode == "metadata" {
					if err := os.WriteFile(filepath.Join(l.dir, cacheHintFile), []byte("bad"), 0o600); err != nil {
						t.Fatal(err)
					}
				} else {
					capacity = 2
				}
				l = &lruDir{capacity: capacity, dir: l.dir}
			}
			if _, ok := l.read("a"); ok {
				t.Fatal("corrupt cache hit")
			}
		})
	}
}

func TestLRUDirLargeOpenVerifiesOneBoundedBlock(t *testing.T) {
	l := &lruDir{capacity: 1, dir: t.TempDir()}
	t.Cleanup(func() { _ = l.close() })
	data := bytes.Repeat([]byte("large value\n"), 1<<18)
	l.write("large", data)
	f, ok := l.open("large")
	if !ok {
		t.Fatal("large value missing")
	}
	defer func() { _ = f.Close() }()
	offset, err := f.file.Seek(0, io.SeekCurrent)
	if err != nil || offset != int64(cacheTokenSize+1+len("large")+sha256.Size+cacheChunkSize) {
		t.Fatalf("payload offset = %d, %v", offset, err)
	}
	got := make([]byte, 11)
	if _, err := io.ReadFull(f, got); err != nil || !bytes.Equal(got, data[:11]) {
		t.Fatalf("stream prefix = %q, %v", got, err)
	}
}

func TestLRUDirRemoveReclaimsPayloadBeforeSlotReuse(t *testing.T) {
	l := &lruDir{capacity: 1, dir: t.TempDir()}
	t.Cleanup(func() { _ = l.close() })
	l.write("old", []byte("old payload"))
	f, ok := l.open("old")
	if !ok {
		t.Fatal("old payload missing")
	}
	defer func() { _ = f.Close() }()
	location := cacheEntryFrame(t, l, "old")
	l.remove("old")
	location.assertErased(t)
	if _, ok := l.read("old"); ok {
		t.Fatal("removed metadata still readable")
	}
	l.write("new", []byte("new payload"))
	if next := cacheEntryFrame(t, l, "new"); next.path != location.path || next.offset != location.offset {
		t.Fatalf("slot not reused: %+v, want %+v", next, location)
	}
	if data, err := io.ReadAll(f); err != nil || string(data) != "old payload" {
		t.Fatalf("opened removed payload = %q, %v", data, err)
	}
	if data, ok := l.read("new"); !ok || string(data) != "new payload" {
		t.Fatalf("replacement payload = %q, %v", data, ok)
	}
}
