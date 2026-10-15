package gitobj

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing/format/idxfile"

	"github.com/yasyf/cc-notes/internal/gittest"
)

const (
	fixtureBlobs  = 400
	fanoutEntries = 256
)

func TestUnderRefs(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"refs", true},
		{"refs/cc-notes/notes", true},
		{"refs/remotes/prhead", true},
		{"./refs/heads", true},
		{"objects/pack", false},
		{"logs/refs/heads", false},
		{"refsx", false},
		{".", false},
	}
	for _, tc := range cases {
		if got := underRefs(tc.path); got != tc.want {
			t.Errorf("underRefs(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestPackIndexPath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"objects/pack/pack-abc.idx", true},
		{"./objects/pack/pack-abc.idx", true},
		{"objects/pack/loose-abc.idx", true},
		{"objects/pack/pack-abc.pack", false},
		{"objects/pack/pack-abc.rev", false},
		{"objects/info/packs", false},
		{"objects/pack.idx", false},
		{"refs/heads/topic.idx", false},
	}
	for _, tc := range cases {
		if got := packIndexPath(tc.path); got != tc.want {
			t.Errorf("packIndexPath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestPackIndexDecodesThroughOneBufferedRead(t *testing.T) {
	gitDir, index := packedIndex(t)
	var direct, buffered int

	want := decodeIndex(t, countingFS{Filesystem: osfs.New(gitDir), reads: &direct}, index)
	got := decodeIndex(t, gitDirFS{countingFS{Filesystem: osfs.New(gitDir), reads: &buffered}}, index)

	if got != want || got != fixtureBlobs+2 || direct <= fanoutEntries || buffered != 1 {
		t.Fatalf("decoded %d objects buffered and %d direct (want %d); reads: %d buffered, %d direct, want 1 and more than %d",
			got, want, fixtureBlobs+2, buffered, direct, fanoutEntries)
	}
}

func TestPackIndexSeekRereadsFromTheSoughtOffset(t *testing.T) {
	raw, file := openedIndex(t)

	head := readExactly(t, file, 8)
	ahead, err := file.Seek(4, io.SeekCurrent)
	if err != nil {
		t.Fatalf("Seek(4, current): %v", err)
	}
	skipped := readExactly(t, file, 4)
	start, err := file.Seek(0, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek(0, start): %v", err)
	}
	again := readExactly(t, file, 8)

	if ahead != 12 || start != 0 || !bytes.Equal(head, raw[:8]) || !bytes.Equal(skipped, raw[12:16]) || !bytes.Equal(again, raw[:8]) {
		t.Fatalf("seek offsets %d and %d, want 12 and 0; read %x, %x, %x, want %x, %x, %x",
			ahead, start, head, skipped, again, raw[:8], raw[12:16], raw[:8])
	}
}

func TestPackIndexFailedSeekKeepsTheReadPosition(t *testing.T) {
	raw, file := openedIndex(t)

	head := readExactly(t, file, 8)
	if _, err := file.Seek(-1, io.SeekStart); err == nil {
		t.Fatal("Seek(-1, start) succeeded, want an error")
	}
	next := readExactly(t, file, 4)

	if !bytes.Equal(head, raw[:8]) || !bytes.Equal(next, raw[8:12]) {
		t.Fatalf("read %x then %x across a failed seek, want %x then %x", head, next, raw[:8], raw[8:12])
	}
}

func TestPackIndexReadAtLeavesTheReadPosition(t *testing.T) {
	raw, file := openedIndex(t)

	head := readExactly(t, file, 8)
	at := make([]byte, 4)
	if _, err := file.ReadAt(at, 100); err != nil {
		t.Fatalf("ReadAt(100): %v", err)
	}
	next := readExactly(t, file, 4)

	if !bytes.Equal(head, raw[:8]) || !bytes.Equal(at, raw[100:104]) || !bytes.Equal(next, raw[8:12]) {
		t.Fatalf("read %x, ReadAt(100) %x, then read %x; want %x, %x, %x", head, at, next, raw[:8], raw[100:104], raw[8:12])
	}
}

func TestPackDataIsNotBuffered(t *testing.T) {
	gitDir, index := packedIndex(t)
	pack := index[:len(index)-len(packIndexExt)] + ".pack"

	file, err := gitDirFS{osfs.New(gitDir)}.Open(pack)
	if err != nil {
		t.Fatalf("Open(%s): %v", pack, err)
	}
	t.Cleanup(func() { _ = file.Close() })

	if _, buffered := file.(*packIndexFile); buffered {
		t.Fatalf("Open(%s) buffered pack data, which go-git reads by offset", pack)
	}
}

type countingFS struct {
	billy.Filesystem
	reads *int
}

func (fs countingFS) Open(path string) (billy.File, error) {
	file, err := fs.Filesystem.Open(path)
	if err != nil {
		return nil, err
	}
	return countingFile{File: file, reads: fs.reads}, nil
}

type countingFile struct {
	billy.File
	reads *int
}

func (f countingFile) Read(p []byte) (int, error) {
	*f.reads++
	return f.File.Read(p)
}

func packedIndex(t *testing.T) (gitDir, index string) {
	t.Helper()
	dir := gittest.InitRepo(t)
	for i := range fixtureBlobs {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d", i)), fmt.Appendf(nil, "blob %d\n", i), 0o600); err != nil {
			t.Fatalf("write fixture file %d: %v", i, err)
		}
	}
	gittest.Git(t, dir, "add", "-A")
	gittest.Git(t, dir, "commit", "-q", "-m", "fixture")
	gittest.Git(t, dir, "repack", "-a", "-d", "-q")
	_, commonDir := gittest.Dirs(t, dir)
	indexes, err := filepath.Glob(filepath.Join(commonDir, packDir, packPrefix+"*"+packIndexExt))
	if err != nil || len(indexes) != 1 {
		t.Fatalf("pack indexes = %v (%v), want exactly one", indexes, err)
	}
	return commonDir, filepath.Join(packDir, filepath.Base(indexes[0]))
}

func openedIndex(t *testing.T) ([]byte, billy.File) {
	t.Helper()
	gitDir, index := packedIndex(t)
	raw, err := os.ReadFile(filepath.Join(gitDir, index))
	if err != nil {
		t.Fatalf("read %s: %v", index, err)
	}
	file, err := gitDirFS{osfs.New(gitDir)}.Open(index)
	if err != nil {
		t.Fatalf("Open(%s): %v", index, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	return raw, file
}

func decodeIndex(t *testing.T, fs billy.Filesystem, index string) int64 {
	t.Helper()
	file, err := fs.Open(index)
	if err != nil {
		t.Fatalf("Open(%s): %v", index, err)
	}
	t.Cleanup(func() { _ = file.Close() })
	decoded := idxfile.NewMemoryIndex()
	if err := idxfile.NewDecoder(file).Decode(decoded); err != nil {
		t.Fatalf("decode %s: %v", index, err)
	}
	count, err := decoded.Count()
	if err != nil {
		t.Fatalf("count %s: %v", index, err)
	}
	return count
}

func readExactly(t *testing.T, r io.Reader, n int) []byte {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return buf
}
