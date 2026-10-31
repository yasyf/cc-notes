package store

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"math"
	"os"
	"strconv"
	"testing"
)

func TestCacheReaderBoundaries(t *testing.T) {
	for _, size := range []int{0, 1, cacheChunkSize - 1, cacheChunkSize, cacheChunkSize + 1, cacheChunkSize*3 + 17} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			l := newProcessCache(t, 1)
			want := bytes.Repeat([]byte("x"), size)
			l.write("entry", want)
			reader, ok := l.open("entry")
			if !ok {
				t.Fatal("verified entry missing")
			}
			defer func() { _ = reader.Close() }()
			if reader.Size() != int64(size) {
				t.Fatalf("logical size = %d, want %d", reader.Size(), size)
			}
			got, err := io.ReadAll(reader)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("stream = %d bytes, %v, want %d verified bytes", len(got), err, size)
			}
			if got, ok := l.read("entry"); !ok || !bytes.Equal(got, want) {
				t.Fatalf("full read = %d bytes, %v", len(got), ok)
			}
		})
	}
}

func TestCacheReaderRejectsTruncatedFrames(t *testing.T) {
	l := newProcessCache(t, 1)
	want := bytes.Repeat([]byte("t"), cacheChunkSize*2+7)
	l.write("entry", want)
	path := cacheEntryPath(t, l, "entry")
	frame, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	prefix := cacheTokenSize + 1 + len("entry")
	for _, size := range []int{0, cacheTokenSize - 1, cacheTokenSize, prefix - 1, prefix, prefix + sha256.Size - 1, prefix + sha256.Size, prefix + sha256.Size + cacheChunkSize - 1, prefix + sha256.Size + cacheChunkSize, len(frame) - 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			if err := os.WriteFile(path, frame[:size], 0o600); err != nil {
				t.Fatal(err)
			}
			if _, ok := l.read("entry"); ok {
				t.Fatalf("accepted truncated frame at %d", size)
			}
			reader, ok := l.open("entry")
			if !ok {
				return
			}
			defer func() { _ = reader.Close() }()
			if _, err := io.ReadAll(reader); err == nil || errors.Is(err, io.EOF) {
				t.Fatalf("truncated non-final block = %v", err)
			}
		})
	}
}

func TestCacheReaderRejectsCorruptionBeforeExposure(t *testing.T) {
	prefix := cacheTokenSize + 1 + len("entry")
	for _, offset := range []int{0, 32, cacheTokenSize, cacheTokenSize + 1, prefix, prefix + sha256.Size, prefix + sha256.Size + cacheChunkSize, prefix + sha256.Size*2 + cacheChunkSize} {
		t.Run(strconv.Itoa(offset), func(t *testing.T) {
			l := newProcessCache(t, 1)
			want := bytes.Repeat([]byte("a"), cacheChunkSize*2)
			l.write("entry", want)
			path := cacheEntryPath(t, l, "entry")
			frame, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			frame[offset] ^= 1
			if err := os.WriteFile(path, frame, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, ok := l.read("entry"); ok {
				t.Fatal("corrupt full frame was accepted")
			}
			reader, ok := l.open("entry")
			if offset < prefix+sha256.Size+cacheChunkSize {
				if ok {
					_ = reader.Close()
					t.Fatal("corrupt first block exposed a reader")
				}
				return
			}
			if !ok {
				t.Fatal("valid first block rejected")
			}
			defer func() { _ = reader.Close() }()
			prefix := make([]byte, cacheChunkSize)
			if _, err := io.ReadFull(reader, prefix); err != nil || !bytes.Equal(prefix, want[:cacheChunkSize]) {
				t.Fatalf("verified prefix changed: %v", err)
			}
			var next [1]byte
			if n, err := reader.Read(next[:]); n != 0 || !errors.Is(err, errCacheIntegrity) {
				t.Fatalf("corrupt later block exposed %d bytes, %v", n, err)
			}
		})
	}
}

func TestCacheReaderRejectsInvalidLogicalSize(t *testing.T) {
	for _, size := range []int64{-1, 0, cacheChunkSize + 1, math.MaxInt64} {
		t.Run(strconv.FormatInt(size, 10), func(t *testing.T) {
			l := newProcessCache(t, 1)
			l.write("entry", bytes.Repeat([]byte("s"), cacheChunkSize*2))
			mutateCacheHint(t, l, "entry", func(hint *cacheHint) { hint.size = size })
			if reader, ok := l.open("entry"); ok {
				_ = reader.Close()
				t.Fatalf("invalid size %d exposed a reader", size)
			}
		})
	}
}

func TestCacheReaderRejectsMixedPublicationBlocks(t *testing.T) {
	l := newProcessCache(t, 1)
	old := bytes.Repeat([]byte("a"), cacheChunkSize*2)
	fresh := bytes.Repeat([]byte("b"), len(old))
	l.write("entry", old)
	path := cacheEntryPath(t, l, "entry")
	prior, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	l.write("entry", fresh)
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	boundary := cacheTokenSize + 1 + len("entry") + sha256.Size + cacheChunkSize
	copy(current[boundary:], prior[boundary:])
	if err := os.WriteFile(path, current, 0o600); err != nil {
		t.Fatal(err)
	}
	reader, ok := l.open("entry")
	if !ok {
		t.Fatal("valid first block rejected")
	}
	defer func() { _ = reader.Close() }()
	prefix := make([]byte, cacheChunkSize)
	if _, err := io.ReadFull(reader, prefix); err != nil || !bytes.Equal(prefix, fresh[:cacheChunkSize]) {
		t.Fatalf("fresh verified prefix changed: %v", err)
	}
	var next [1]byte
	if n, err := reader.Read(next[:]); n != 0 || !errors.Is(err, errCacheIntegrity) {
		t.Fatalf("mixed publication exposed %d bytes, %v", n, err)
	}
}

func TestCacheReaderRejectsReorderedBlocks(t *testing.T) {
	l := newProcessCache(t, 1)
	data := append(bytes.Repeat([]byte("a"), cacheChunkSize), bytes.Repeat([]byte("b"), cacheChunkSize)...)
	l.write("entry", data)
	path := cacheEntryPath(t, l, "entry")
	frame, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	boundary := cacheTokenSize + 1 + len("entry") + sha256.Size + cacheChunkSize
	prefix := cacheTokenSize + 1 + len("entry")
	first := bytes.Clone(frame[prefix:boundary])
	copy(frame[prefix:boundary], frame[boundary:])
	copy(frame[boundary:], first)
	if err := os.WriteFile(path, frame, 0o600); err != nil {
		t.Fatal(err)
	}
	if reader, ok := l.open("entry"); ok {
		_ = reader.Close()
		t.Fatal("reordered block exposed a reader")
	}
}

func TestCacheReaderOwnsMultiBlockStreamAfterReplaceAndClose(t *testing.T) {
	l := newProcessCache(t, 1)
	want := bytes.Repeat([]byte("old stream"), cacheChunkSize)
	l.write("entry", want)
	reader, ok := l.open("entry")
	if !ok {
		t.Fatal("old stream missing")
	}
	defer func() { _ = reader.Close() }()
	position, err := reader.file.Seek(0, io.SeekCurrent)
	if err != nil || position != int64(cacheTokenSize+1+len("entry")+sha256.Size+cacheChunkSize) {
		t.Fatalf("open read more than one bounded block: %d, %v", position, err)
	}
	l.remove("entry")
	l.write("replacement", []byte("new stream"))
	if err := l.close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("independent old stream = %d bytes, %v", len(got), err)
	}
}

func TestCacheReaderRejectsOldWholePublicationAgainstNewHint(t *testing.T) {
	l := newProcessCache(t, 1)
	l.write("entry", []byte("old"))
	path := cacheEntryPath(t, l, "entry")
	old, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	l.write("entry", []byte("new"))
	if err := os.WriteFile(path, old, 0o600); err != nil {
		t.Fatal(err)
	}
	if reader, ok := l.open("entry"); ok {
		_ = reader.Close()
		t.Fatal("old whole publication matched a new nonce hint")
	}
}
