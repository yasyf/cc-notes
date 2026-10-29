package gitobj

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/idxfile"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"

	"github.com/yasyf/cc-notes/internal/ccnhome"
	"github.com/yasyf/cc-notes/internal/gittest"
)

type indexReadFS struct {
	billy.Filesystem
	reads map[string]int64
}

func (fs indexReadFS) Open(path string) (billy.File, error) {
	file, err := fs.Filesystem.Open(path)
	if err != nil || filepath.Ext(path) != ".idx" {
		return file, err
	}
	return &indexReadFile{File: file, path: path, reads: fs.reads}, nil
}

type indexReadFile struct {
	billy.File
	path  string
	reads map[string]int64
}

func (f *indexReadFile) Read(data []byte) (int, error) {
	n, err := f.File.Read(data)
	f.reads[f.path] += int64(n)
	return n, err
}

func testPackIndex(t *testing.T, commonDir string) (*packIndex, map[string]int64) {
	t.Helper()
	reads := make(map[string]int64)
	fs := indexReadFS{Filesystem: gitDirFS{osfs.New(commonDir)}, reads: reads}
	index, err := newPackIndex(dotgit.New(fs), commonDir)
	if err != nil {
		t.Fatal(err)
	}
	return index, reads
}

func indexedCommitFixture(t *testing.T) (string, string, plumbing.Hash) {
	t.Helper()
	t.Setenv(ccnhome.Env, t.TempDir())
	dir := gittest.InitRepo(t)
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "first")
	hash := plumbing.NewHash(gittest.Git(t, dir, "rev-parse", "HEAD"))
	gittest.Git(t, dir, "repack", "-adq")
	_, commonDir := gittest.Dirs(t, dir)
	return dir, commonDir, hash
}

func assertIndexedCommit(t *testing.T, index *packIndex, hash plumbing.Hash) {
	t.Helper()
	db, err := index.open(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(errors.Join(err, db.Close()))
	}
	err = func() error {
		_, pack, err := locateIndexedPack(t.Context(), tx, hash)
		if err != nil {
			return err
		}
		offset, err := pack.FindOffset(hash)
		if err != nil {
			return err
		}
		got, err := pack.FindHash(offset)
		if err != nil {
			return err
		}
		if got != hash {
			t.Errorf("reverse lookup = %s, want %s", got, hash)
		}
		return nil
	}()
	if err := errors.Join(err, tx.Rollback(), db.Close()); err != nil {
		t.Fatal(err)
	}
}

func TestPackIndexFreshHandleReusesValidatedGeneration(t *testing.T) {
	_, commonDir, hash := indexedCommitFixture(t)
	first, reads := testPackIndex(t, commonDir)
	if err := first.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(reads) != 1 {
		t.Fatalf("initial index reads = %v, want exactly one decoded index", reads)
	}
	second, reads := testPackIndex(t, commonDir)
	if err := second.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertIndexedCommit(t, second, hash)
	if len(reads) != 0 {
		t.Fatalf("fresh handle read index payloads %v, want zero", reads)
	}
}

func TestPackIndexLandingDecodesOnlyTheNewPack(t *testing.T) {
	dir, commonDir, firstHash := indexedCommitFixture(t)
	first, oldReads := testPackIndex(t, commonDir)
	if err := first.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "second")
	secondHash := plumbing.NewHash(gittest.Git(t, dir, "rev-parse", "HEAD"))
	gittest.Git(t, dir, "repack", "-dq")
	second, reads := testPackIndex(t, commonDir)
	if err := second.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(reads) != 1 {
		t.Fatalf("incremental index reads = %v, want exactly one new index", reads)
	}
	for path := range reads {
		if oldReads[path] != 0 {
			t.Errorf("unchanged index %s decoded again", path)
		}
	}
	assertIndexedCommit(t, second, firstHash)
	assertIndexedCommit(t, second, secondHash)
}

func TestPackIndexRejectsCorruptionAndRecoversInPlace(t *testing.T) {
	_, commonDir, hash := indexedCommitFixture(t)
	first, reads := testPackIndex(t, commonDir)
	if err := first.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	var path string
	for name := range reads {
		path = filepath.Join(commonDir, name)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), raw...)
	corrupt[8+256*4] ^= 1
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	second, _ := testPackIndex(t, commonDir)
	if err := second.prepare(t.Context()); !errors.Is(err, idxfile.ErrMalformedIdxFile) {
		t.Fatalf("corrupt index prepare = %v, want checksum failure", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := second.prepare(t.Context()); err != nil {
		t.Fatalf("repaired index prepare: %v", err)
	}
	assertIndexedCommit(t, second, hash)
}
