package gitobj

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func TestNativePackFresheningKeepsTargetedReadUsable(t *testing.T) {
	dir, common, hash := indexedCommitFixture(t)
	paths, err := filepath.Glob(filepath.Join(common, packDir, "pack-*.pack"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("packs=%v, %v", paths, err)
	}
	path := paths[0]
	old := time.Unix(1, 0)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	emptyTree := filepath.Join(t.TempDir(), "tree")
	if err := os.WriteFile(emptyTree, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	repo := openPackedRepo(t, dir)
	assertNativeCommit(t, repo, hash)
	want := gittest.Git(t, dir, "rev-parse", "HEAD^{tree}")
	if got := gittest.Git(t, dir, "hash-object", "-t", "tree", "-w", emptyTree); got != want {
		t.Fatalf("freshened tree=%s", got)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("freshening changed pack payload: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.ModTime().After(old) {
		t.Fatalf("pack was not freshened: %v", err)
	}
	assertNativeCommit(t, repo, hash)
	assertNativeCommit(t, openPackedRepo(t, dir), hash)
}

func TestNativePackedReadRejectsBrokenPackWithoutFalseMissing(t *testing.T) {
	for _, mutation := range []string{"truncate", "header"} {
		t.Run(mutation, func(t *testing.T) {
			dir, common, hash := indexedCommitFixture(t)
			paths, err := filepath.Glob(filepath.Join(common, packDir, "pack-*.pack"))
			if err != nil || len(paths) != 1 {
				t.Fatalf("packs=%v, %v", paths, err)
			}
			path := paths[0]
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
			if mutation == "truncate" {
				err = os.Truncate(path, 12)
			} else {
				err = os.WriteFile(path, []byte("invalid pack header"), 0o600)
			}
			if err != nil {
				t.Fatal(err)
			}
			repo := openPackedRepo(t, dir)
			sha := model.SHA(hash.String())
			_, err = repo.Ancestry(t.Context(), sha, sha)
			if err == nil || errors.Is(err, plumbing.ErrObjectNotFound) || errors.Is(err, ErrCommitNotFound) {
				t.Fatalf("broken pack read=%v, want non-miss error", err)
			}
		})
	}
}

func TestNativePackedReadAcceptsIdenticalPackReplacement(t *testing.T) {
	dir, common, hash := indexedCommitFixture(t)
	repo := openPackedRepo(t, dir)
	assertNativeCommit(t, repo, hash)
	paths, err := filepath.Glob(filepath.Join(common, packDir, "pack-*.pack"))
	if err != nil || len(paths) != 1 {
		t.Fatalf("packs=%v, %v", paths, err)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths[0]+".replacement", raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(paths[0]+".replacement", paths[0]); err != nil {
		t.Fatal(err)
	}
	assertNativeCommit(t, repo, hash)
	assertNativeCommit(t, openPackedRepo(t, dir), hash)
}
