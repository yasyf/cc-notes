package gitobj

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func TestNativePackedDuplicateSurvivesOnePackRemoval(t *testing.T) {
	dir, commonDir, hash := indexedCommitFixture(t)
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "second")
	gittest.Git(t, dir, "repack", "-aq")
	packs, err := filepath.Glob(filepath.Join(commonDir, packDir, "pack-*.pack"))
	if err != nil || len(packs) != 2 {
		t.Fatalf("duplicate packs = %v, %v", packs, err)
	}
	assertNativeCommit(t, openPackedRepo(t, dir), hash)
	for _, ext := range []string{"pack", "idx"} {
		path := packs[0][:len(packs[0])-4] + ext
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	assertNativeCommit(t, openPackedRepo(t, dir), hash)
}

func TestNativePackedReadSeesMaintenancePackNames(t *testing.T) {
	dir, commonDir, hash := indexedCommitFixture(t)
	packs, err := filepath.Glob(filepath.Join(commonDir, packDir, "pack-*.pack"))
	if err != nil || len(packs) != 1 {
		t.Fatalf("packs = %v, %v", packs, err)
	}
	stem := filepath.Base(packs[0])[len(packPrefix) : len(filepath.Base(packs[0]))-5]
	for _, ext := range []string{"pack", "idx"} {
		from := filepath.Join(commonDir, packDir, packPrefix+stem+"."+ext)
		to := filepath.Join(commonDir, packDir, loosePackPrefix+stem+"."+ext)
		if err := os.Rename(from, to); err != nil {
			t.Fatal(err)
		}
	}
	assertNativeCommit(t, openPackedRepo(t, dir), hash)
}

func TestNativePackedReadUsesAlternates(t *testing.T) {
	_, alternateDir, hash := indexedCommitFixture(t)
	dir := gittest.InitRepo(t)
	_, commonDir := gittest.Dirs(t, dir)
	info := filepath.Join(commonDir, "objects", "info")
	if err := os.MkdirAll(info, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(info, "alternates"), []byte(filepath.Join(alternateDir, "objects")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertNativeCommit(t, openPackedRepo(t, dir), hash)
}

func TestNativeReadScopeReusesOneIdleBatch(t *testing.T) {
	dir, _, first := indexedCommitFixture(t)
	for range 32 {
		gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "next")
	}
	tip := plumbing.NewHash(gittest.Git(t, dir, "rev-parse", "HEAD"))
	gittest.Git(t, dir, "repack", "-adq")
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
	for _, hash := range []plumbing.Hash{first, tip, first, tip} {
		if _, err := repo.lookupCommit(hash); err != nil {
			t.Fatal(err)
		}
	}
	if len(repo.storage.scope.batches) != 1 || !repo.storage.scope.batches[0].Idle() {
		t.Fatal("read scope did not retain one reusable idle batch")
	}
}

func TestNativeRepackReadDoesNotWaitForExistingReadScope(t *testing.T) {
	dir, _, first := indexedCommitFixture(t)
	reader := openPackedRepo(t, dir)
	reader.mu.Lock()
	defer reader.mu.Unlock()
	reader.beginRead(t.Context())
	defer func() {
		var err error
		reader.endRead(&err)
		if err != nil {
			t.Error(err)
		}
	}()
	if _, err := reader.lookupCommit(first); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "second")
	second := model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD"))
	gittest.Git(t, dir, "repack", "-dq")
	writer := openPackedRepo(t, dir)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if verdict, err := writer.Ancestry(ctx, model.SHA(first.String()), second); err != nil || verdict != Ancestor {
		t.Fatalf("repack beside live scope = %v, %v", verdict, err)
	}
	if _, err := reader.lookupCommit(first); err != nil {
		t.Fatalf("original read after repack: %v", err)
	}
}
