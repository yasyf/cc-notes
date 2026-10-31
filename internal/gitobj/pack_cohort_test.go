package gitobj

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/yasyf/cc-notes/internal/gittest"
)

func TestNativeRepoSeesLatePackInSameReadScope(t *testing.T) {
	dir, _, first := indexedCommitFixture(t)
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "second")
	second := plumbing.NewHash(gittest.Git(t, dir, "rev-parse", "HEAD"))
	repo := openPackedRepo(t, dir)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.beginRead(t.Context())
	defer func() {
		var err error
		repo.endRead(&err)
		if err != nil {
			t.Error(err)
		}
	}()
	if _, err := repo.lookupCommit(first); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "repack", "-adq")
	gittest.Git(t, dir, "prune-packed")
	if commit, err := repo.lookupCommit(second); err != nil || commit.Hash != second {
		t.Fatalf("late pack commit = %v, %v", commit, err)
	}
}

func TestNativeRepoSeesLateAlternatePackInSameReadScope(t *testing.T) {
	alternate, alternateCommon, first := indexedCommitFixture(t)
	gittest.Git(t, alternate, "commit", "--allow-empty", "-qm", "second")
	second := plumbing.NewHash(gittest.Git(t, alternate, "rev-parse", "HEAD"))
	dir := gittest.InitRepo(t)
	_, common := gittest.Dirs(t, dir)
	if err := os.WriteFile(filepath.Join(common, "objects", "info", "alternates"), []byte(filepath.Join(alternateCommon, "objects")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo := openPackedRepo(t, dir)
	repo.mu.Lock()
	defer repo.mu.Unlock()
	repo.beginRead(t.Context())
	defer func() {
		var err error
		repo.endRead(&err)
		if err != nil {
			t.Error(err)
		}
	}()
	if _, err := repo.lookupCommit(first); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, alternate, "repack", "-dq")
	gittest.Git(t, alternate, "prune-packed")
	if commit, err := repo.lookupCommit(second); err != nil || commit.Hash != second {
		t.Fatalf("late alternate commit = %v, %v", commit, err)
	}
}
