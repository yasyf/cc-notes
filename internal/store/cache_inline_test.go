package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestCacheHintInlineSizeEncoding(t *testing.T) {
	for _, hint := range []cacheHint{
		{size: 0},
		{size: math.MaxInt64},
		{size: 0, inline: true},
		{size: cacheInlineStride - cacheTokenSize - 1 - sha256.Size, inline: true},
	} {
		data := encodeCacheHint(hint)
		got, err := decodeCacheHint(data[:])
		if err != nil || got != hint {
			t.Fatalf("hint round trip = %+v, %v, want %+v", got, err, hint)
		}
	}
	for _, size := range []uint64{cacheHintInlineFlag + cacheInlineStride, math.MaxUint64} {
		var data [cacheHintRecordSize]byte
		binary.BigEndian.PutUint64(data[8:16], size)
		if _, err := decodeCacheHint(data[:]); !errors.Is(err, errCacheIntegrity) {
			t.Fatalf("oversized inline size %d = %v", size, err)
		}
	}
}

func TestCacheInlineBoundaryAndOwnedBytes(t *testing.T) {
	const key = "entry"
	limit := cacheInlineStride - cacheTokenSize - 1 - len(key) - sha256.Size
	for _, size := range []int{0, 1, limit, limit + 1, cacheChunkSize * 2} {
		l := newProcessCache(t, 1)
		want := bytes.Repeat([]byte("x"), size)
		l.write(key, want)
		entry, ok := l.entry(key)
		if !ok || entry.inline != (size <= limit) {
			t.Fatalf("size %d entry = %+v, %v", size, entry, ok)
		}
		reader, ok := l.open(key)
		if !ok || reader.Size() != int64(size) {
			t.Fatalf("size %d reader missing", size)
		}
		location := cacheEntryFrame(t, l, key)
		l.remove(key)
		location.assertErased(t)
		l.write("replacement", []byte("replacement"))
		if err := l.close(); err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(reader)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("held size %d = %d bytes, %v", size, len(got), err)
		}
		if err := reader.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCacheInlineTransitionsEraseRetiredPayloads(t *testing.T) {
	l := newProcessCache(t, 1)
	l.write("entry", bytes.Repeat([]byte("x"), 800))
	location := cacheEntryFrame(t, l, "entry")
	l.write("entry", []byte("s"))
	f, err := os.Open(location.path)
	if err != nil {
		t.Fatal(err)
	}
	var record [cacheInlineStride]byte
	_, err = f.ReadAt(record[:], location.offset)
	if closeErr := f.Close(); err != nil || closeErr != nil {
		t.Fatalf("record read/close: %v/%v", err, closeErr)
	}
	end := cacheTokenSize + 1 + len("entry") + sha256.Size + 1
	if !bytes.Equal(record[end:], make([]byte, len(record)-end)) {
		t.Fatal("old inline tail remains")
	}
	large := bytes.Repeat([]byte("large"), cacheChunkSize)
	l.write("entry", large)
	location.assertRecordZero(t)
	reader, ok := l.open("entry")
	if !ok {
		t.Fatal("large transition missing")
	}
	largeLocation := cacheEntryFrame(t, l, "entry")
	l.write("entry", []byte("small"))
	largeLocation.assertErased(t)
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, large) {
		t.Fatalf("held large transition: %d bytes, %v", len(got), err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if got, ok := l.read("entry"); !ok || string(got) != "small" {
		t.Fatalf("small transition = %q, %v", got, ok)
	}
}

func TestCacheInlineGCLeavesNeighborAndRejectsCorruption(t *testing.T) {
	l := newProcessCache(t, 2)
	l.write("a", []byte("a"))
	l.write("b", []byte("b"))
	a, b := cacheEntryFrame(t, l, "a"), cacheEntryFrame(t, l, "b")
	prior := b.read(t)
	l.remove("a")
	a.assertErased(t)
	if !bytes.Equal(b.read(t), prior) {
		t.Fatal("GC changed neighboring frame")
	}
	if got, ok := l.read("b"); !ok || string(got) != "b" {
		t.Fatalf("neighbor = %q, %v", got, ok)
	}
	for _, at := range []int{0, 32, cacheTokenSize, cacheTokenSize + 1, cacheTokenSize + 2, len(prior) - 1} {
		corrupt := bytes.Clone(prior)
		corrupt[at] ^= 1
		b.write(t, corrupt)
		if reader, ok := l.open("b"); ok {
			_ = reader.Close()
			t.Fatalf("inline corruption at %d exposed bytes", at)
		}
	}
}

func TestCacheInlineMissingAndOwnerReplacement(t *testing.T) {
	dir := t.TempDir()
	seed := &lruDir{capacity: 2, dir: dir}
	seed.write("a", []byte("old"))
	if err := seed.close(); err != nil {
		t.Fatal(err)
	}
	reader := &lruDir{capacity: 2, dir: dir}
	t.Cleanup(func() { _ = reader.close() })
	if err := os.Remove(filepath.Join(dir, cacheInlineFile)); err != nil {
		t.Fatal(err)
	}
	if _, ok := reader.read("a"); ok {
		t.Fatal("missing inline data hit")
	}
	writer := &lruDir{capacity: 2, dir: dir}
	t.Cleanup(func() { _ = writer.close() })
	writer.write("a", []byte("new"))
	if _, ok := reader.read("a"); ok {
		t.Fatal("known absent owner unexpectedly observed external population")
	}
	reader.write("b", []byte("b"))
	if got, ok := reader.read("a"); !ok || string(got) != "new" {
		t.Fatalf("local refresh = %q, %v", got, ok)
	}
	data := reader.data
	if err := reader.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := data.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("owner data FD remains open: %v", err)
	}
	if _, ok := reader.read("a"); ok {
		t.Fatal("closed owner read inline data")
	}
}

func TestCacheInlineMixedPublicationFailureCleanup(t *testing.T) {
	for _, oldInline := range []bool{true, false} {
		for _, repair := range []bool{true, false} {
			l := newProcessCache(t, 1)
			old, next := []byte("old"), bytes.Repeat([]byte("new"), cacheChunkSize)
			if !oldInline {
				old, next = next, old
			}
			l.write("entry", old)
			location := cacheEntryFrame(t, l, "entry")
			entry, ok := l.entry("entry")
			if !ok {
				t.Fatal("initial hint missing")
			}
			window, err := (&cacheHintPages{file: l.index, capacity: l.capacity}).window(cacheNameTag("entry"))
			if err != nil {
				t.Fatal(err)
			}
			previous := l.chooseSlot(window, cacheNameTag("entry"))
			l.writer.Lock()
			lock, locked := l.lockWriter()
			if !locked {
				l.writer.Unlock()
				t.Fatal("writer lock unavailable")
			}
			token := bytes.Clone(entry.token)
			token[cacheTokenSize-1] ^= 1
			writer := cachePayloadWriter{cache: l}
			published := writer.publish(previous, "entry", token, next)
			closeErr := lock.Close()
			l.writer.Unlock()
			if !published || closeErr != nil {
				t.Fatalf("payload publication = %v, %v", published, closeErr)
			}
			if _, ok := l.read("entry"); ok {
				t.Fatal("unpublished representation exposed bytes")
			}
			if repair {
				l.write("entry", []byte("repair"))
				if got, ok := l.read("entry"); !ok || string(got) != "repair" {
					t.Fatalf("repair = %q, %v", got, ok)
				}
			}
			l.remove("entry")
			location.inline, location.path, location.offset = true, l.inlinePath(), int64(entry.slot)*cacheInlineStride
			location.assertErased(t)
		}
	}
}

func TestCacheInlineFileReplacementRefreshesOnWrite(t *testing.T) {
	l := newProcessCache(t, 2)
	l.write("a", []byte("old"))
	reader, ok := l.open("a")
	if !ok {
		t.Fatal("initial reader missing")
	}
	old := l.data
	if err := os.Rename(l.inlinePath(), l.inlinePath()+".retired"); err != nil {
		t.Fatal(err)
	}
	l.write("b", []byte("new"))
	if _, err := old.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("replaced data handle remains open: %v", err)
	}
	if _, ok := l.read("a"); ok {
		t.Fatal("stale hint exposed unrelated replacement bytes")
	}
	if got, ok := l.read("b"); !ok || string(got) != "new" {
		t.Fatalf("replacement = %q, %v", got, ok)
	}
	if err := l.close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil || string(got) != "old" {
		t.Fatalf("held replacement reader = %q, %v", got, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCacheInlineRejectsNameSpecificOversize(t *testing.T) {
	l := newProcessCache(t, 1)
	name := string(bytes.Repeat([]byte("k"), cacheKeyMax))
	l.write(name, []byte("valid"))
	entry, ok := l.entry(name)
	if !ok {
		t.Fatal("maximum key missing")
	}
	entry.size = cacheInlineStride - cacheTokenSize - 1 - sha256.Size
	if _, ok := l.openEntry(entry); ok {
		t.Fatal("name-specific oversized frame opened")
	}
	entry.size = -1
	if _, ok := l.openEntry(entry); ok {
		t.Fatal("negative inline frame opened")
	}
}
