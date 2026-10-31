package gitobj

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/yasyf/cc-notes/internal/ccnhome"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
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

type indexInput struct {
	io.Reader
	size int64
}

type indexInputInfo struct {
	os.FileInfo
	size int64
}

func (info indexInputInfo) Size() int64 { return info.size }

func (input indexInput) Stat() (os.FileInfo, error) {
	return indexInputInfo{size: input.size}, nil
}

func newIndexInput(file io.ReadSeeker) (indexInput, error) {
	position, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return indexInput{}, err
	}
	size, seekErr := file.Seek(0, io.SeekEnd)
	_, restoreErr := file.Seek(position, io.SeekStart)
	if err := errors.Join(seekErr, restoreErr); err != nil {
		return indexInput{}, err
	}
	return indexInput{Reader: file, size: size}, nil
}

func openPackedRepo(t *testing.T, dir string) *Repo {
	t.Helper()
	gitDir, commonDir := gittest.Dirs(t, dir)
	repo, err := Open(gitDir, commonDir)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func assertNativeCommit(t *testing.T, repo *Repo, hash plumbing.Hash) {
	t.Helper()
	sha := model.SHA(hash.String())
	if verdict, err := repo.Ancestry(t.Context(), sha, sha); err != nil || verdict != Ancestor {
		t.Fatalf("Ancestry = %v, %v; want Ancestor", verdict, err)
	}
}

func TestNativePackedReadLeavesLegacyIndexUntouched(t *testing.T) {
	dir, commonDir, hash := indexedCommitFixture(t)
	home, err := ccnhome.ForRepo(commonDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(home.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home.Dir, "objects-v2.sqlite")
	want := []byte("unavailable obsolete index")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		assertNativeCommit(t, openPackedRepo(t, dir), hash)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(want) {
		t.Fatalf("legacy index = %q, %v", got, err)
	}
}

func TestNativePackedIndexMutationMatchesTargetedGitLookup(t *testing.T) {
	dir, common, hash := indexedCommitFixture(t)
	paths, err := filepath.Glob(filepath.Join(common, packDir, "pack-*.idx"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("indexes=%v, %v", paths, err)
	}
	path := paths[0]
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
	command := exec.CommandContext(t.Context(), "git", "--no-replace-objects", "--git-dir="+common, "cat-file", "--batch")
	command.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
	command.Stdin = strings.NewReader(hash.String() + "\n")
	var output, diagnostic bytes.Buffer
	command.Stdout, command.Stderr = &output, &diagnostic
	nativeErr := command.Run()
	sha := model.SHA(hash.String())
	verdict, readErr := openPackedRepo(t, dir).Ancestry(t.Context(), sha, sha)
	switch {
	case nativeErr != nil || diagnostic.Len() != 0:
		if readErr == nil || errors.Is(readErr, ErrCommitNotFound) || errors.Is(readErr, plumbing.ErrObjectNotFound) {
			t.Fatalf("diagnosed native corruption became miss: native=%v %q read=%v", nativeErr, diagnostic.String(), readErr)
		}
	case output.String() == hash.String()+" missing\n":
		if !errors.Is(readErr, ErrCommitNotFound) {
			t.Fatalf("targeted native miss=%v, want commit-not-found", readErr)
		}
	default:
		if readErr != nil || verdict != Ancestor {
			t.Fatalf("native target remained readable; read=%v, %v", verdict, readErr)
		}
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	assertNativeCommit(t, openPackedRepo(t, dir), hash)
}
