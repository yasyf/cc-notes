package gitobj_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func gitErr(dir string, args ...string) error {
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	return cmd.Run()
}

func writeBlobs(t *testing.T, objects *gitobj.SourceObjects, prefix string, count int) []model.SHA {
	t.Helper()
	shas := make([]model.SHA, count)
	for i := range shas {
		sha, err := objects.WriteBlob(fmt.Appendf(nil, "%s %d\n", prefix, i))
		if err != nil {
			t.Fatal(err)
		}
		shas[i] = sha
	}
	return shas
}

func scopePacks(t *testing.T, dir, pattern string) []string {
	t.Helper()
	packs, err := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", pattern))
	if err != nil {
		t.Fatal(err)
	}
	return packs
}

func looseCount(t *testing.T, dir string) string {
	t.Helper()
	for line := range strings.SplitSeq(git(t, dir, "count-objects", "-v"), "\n") {
		if count, ok := strings.CutPrefix(line, "count: "); ok {
			return count
		}
	}
	t.Fatal("count-objects reported no loose count")
	return ""
}

func TestSourceObjectsWriteOnePackAndReadThroughTheBuffer(t *testing.T) {
	dir := gittest.InitRepo(t)
	repo := open(t, dir)
	large := make([]byte, 100<<10)
	if _, err := rand.Read(large); err != nil {
		t.Fatal(err)
	}
	var blobs []model.SHA
	var tree, commit, largeSHA model.SHA
	err := repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
		blobs = writeBlobs(t, objects, "packed", 6)
		var err error
		if largeSHA, err = objects.WriteBlob(large); err != nil {
			return err
		}
		read, err := objects.ReadBlob(blobs[0])
		if err != nil {
			return err
		}
		if string(read) != "packed 0\n" {
			t.Errorf("buffered ReadBlob = %q", read)
		}
		entries := []object.TreeEntry{{Name: "first", Mode: filemode.Regular, Hash: plumbing.NewHash(string(blobs[0]))}}
		if tree, err = objects.WriteTree(entries); err != nil {
			return err
		}
		if kind, err := objects.ObjectType(tree); err != nil || kind != plumbing.TreeObject {
			t.Errorf("buffered ObjectType = %v, %v", kind, err)
		}
		read2, err := objects.ReadTree(tree)
		if err != nil || len(read2) != 1 || read2[0].Name != "first" {
			t.Errorf("buffered ReadTree = %v, %v", read2, err)
		}
		sig := gitobj.Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1700000000, 0)}
		if commit, err = objects.WriteCommit(gitobj.SourceCommit{Tree: tree, Author: sig, Committer: sig, Message: "fixture"}); err != nil {
			return err
		}
		headers, err := objects.ReadCommit(commit)
		if err != nil || headers.Tree != tree {
			t.Errorf("buffered ReadCommit = %+v, %v", headers, err)
		}
		if packs := scopePacks(t, dir, "*.idx"); len(packs) != 0 {
			t.Errorf("packs before the scope succeeded: %v", packs)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := looseCount(t, dir); got != "0" {
		t.Errorf("loose objects = %s, want 0", got)
	}
	indexes := scopePacks(t, dir, "ccn0-*.idx")
	if len(indexes) != 1 {
		t.Fatalf("indexes = %v, want one tier 0 index", indexes)
	}
	git(t, dir, "verify-pack", indexes[0])
	git(t, dir, "fsck", "--full", "--strict", "--no-dangling")
	if got := git(t, dir, "cat-file", "-p", string(blobs[5])); got != "packed 5" {
		t.Errorf("cat-file blob = %q", got)
	}
	if got := git(t, dir, "rev-parse", string(commit)+"^{tree}"); got != string(tree) {
		t.Errorf("commit tree = %q, want %s", got, tree)
	}
	err = repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
		read, err := objects.ReadBlob(largeSHA)
		if err != nil {
			return err
		}
		if !bytes.Equal(read, large) {
			t.Error("packed large blob differs")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSourceObjectsFailureWritesNothing(t *testing.T) {
	dir := gittest.InitRepo(t)
	repo := open(t, dir)
	failure := errors.New("scope failed")
	var blobs []model.SHA
	err := repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
		blobs = writeBlobs(t, objects, "dropped", 6)
		return failure
	})
	if !errors.Is(err, failure) {
		t.Fatalf("err = %v, want %v", err, failure)
	}
	if packs := scopePacks(t, dir, "*"); len(packs) != 0 {
		t.Errorf("packs after a failed scope: %v", packs)
	}
	if err := gitErr(dir, "cat-file", "-e", string(blobs[0])); err == nil {
		t.Errorf("failed scope wrote %s", blobs[0])
	}
}

func TestKeepOnlyWritesWhatTheRootsReach(t *testing.T) {
	dir := gittest.InitRepo(t)
	repo := open(t, dir)
	var kept, dropped []model.SHA
	var root model.SHA
	err := repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
		kept = writeBlobs(t, objects, "kept", 6)
		dropped = writeBlobs(t, objects, "dropped", 6)
		entries := make([]object.TreeEntry, len(kept))
		for i, sha := range kept {
			entries[i] = object.TreeEntry{Name: fmt.Sprintf("kept-%d", i), Mode: filemode.Regular, Hash: plumbing.NewHash(string(sha))}
		}
		tree, err := objects.WriteTree(entries)
		if err != nil {
			return err
		}
		sig := gitobj.Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1700000000, 0)}
		if root, err = objects.WriteCommit(gitobj.SourceCommit{Tree: tree, Author: sig, Committer: sig, Message: "fixture"}); err != nil {
			return err
		}
		return objects.KeepOnly(root)
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, sha := range append(kept, root) {
		if err := gitErr(dir, "cat-file", "-e", string(sha)); err != nil {
			t.Errorf("reachable %s missing: %v", sha, err)
		}
	}
	for _, sha := range dropped {
		if err := gitErr(dir, "cat-file", "-e", string(sha)); err == nil {
			t.Errorf("unreachable %s was written", sha)
		}
	}
	git(t, dir, "fsck", "--full", "--strict", "--no-dangling")
}

func TestRollupMergesAFullTierWithoutLosingObjects(t *testing.T) {
	dir := gittest.InitRepo(t)
	repo := open(t, dir)
	var all []model.SHA
	for scope := range 8 {
		err := repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
			all = append(all, writeBlobs(t, objects, fmt.Sprintf("scope %d", scope), 5)...)
			all = append(all, writeBlobs(t, objects, "shared", 1)...)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.RollupPacks(); err != nil {
		t.Fatal(err)
	}
	if packs := scopePacks(t, dir, "ccn0-*"); len(packs) != 0 {
		t.Errorf("tier 0 left after rollup: %v", packs)
	}
	merged := scopePacks(t, dir, "ccn1-*.idx")
	if len(merged) != 1 {
		t.Fatalf("tier 1 indexes = %v, want one", merged)
	}
	git(t, dir, "verify-pack", merged[0])
	git(t, dir, "fsck", "--full", "--strict", "--no-dangling")
	for _, sha := range all {
		if err := gitErr(dir, "cat-file", "-e", string(sha)); err != nil {
			t.Errorf("%s missing after rollup: %v", sha, err)
		}
	}
	if got, want := strings.Count(git(t, dir, "verify-pack", "-v", merged[0]), " blob "), 41; got != want {
		t.Errorf("merged pack blobs = %d, want %d after dedupe", got, want)
	}
}

func TestRollupLeavesPacksAMultiPackIndexLists(t *testing.T) {
	dir := gittest.InitRepo(t)
	repo := open(t, dir)
	for scope := range 8 {
		err := repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
			writeBlobs(t, objects, fmt.Sprintf("listed %d", scope), 5)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	listed := scopePacks(t, dir, "ccn0-*.idx")
	git(t, dir, "multi-pack-index", "write")
	if err := repo.RollupPacks(); err != nil {
		t.Fatal(err)
	}
	for _, index := range listed {
		if _, err := os.Stat(index); err != nil {
			t.Errorf("rollup removed listed pack: %v", err)
		}
	}
	git(t, dir, "multi-pack-index", "verify")
}

func TestHeldPackSurvivesRepackUntilReleased(t *testing.T) {
	dir := gittest.InitRepo(t)
	repo := open(t, dir)
	repo.HoldNewPacks()
	var blobs []model.SHA
	err := repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
		blobs = writeBlobs(t, objects, "unpublished", 6)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "repack", "-a", "-d")
	if err := gitErr(dir, "cat-file", "-e", string(blobs[0])); err != nil {
		t.Fatalf("repack deleted a pack no ref reaches yet: %v", err)
	}
	if err := repo.ReleaseHeldPacks(); err != nil {
		t.Fatal(err)
	}
	if keeps := scopePacks(t, dir, "*.keep"); len(keeps) != 0 {
		t.Fatalf("keep files after release: %v", keeps)
	}
	git(t, dir, "reflog", "expire", "--expire=now", "--all")
	git(t, dir, "gc", "--prune=now")
	if err := gitErr(dir, "cat-file", "-e", string(blobs[0])); err == nil {
		t.Fatal("gc kept an unreachable object after release")
	}
}

func TestRollupWaitsForAFreshKeepFileAndRetiresAStaleOne(t *testing.T) {
	dir := gittest.InitRepo(t)
	repo := open(t, dir)
	repo.HoldNewPacks()
	for scope := range 8 {
		err := repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
			writeBlobs(t, objects, fmt.Sprintf("held %d", scope), 5)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.RollupPacks(); err != nil {
		t.Fatal(err)
	}
	keeps := scopePacks(t, dir, "ccn0-*.keep")
	if got := len(scopePacks(t, dir, "ccn0-*.idx")); got != 8 || len(keeps) != 8 {
		t.Fatalf("tier 0 indexes = %d, keep files = %v; want 8 of each", got, keeps)
	}
	old := time.Now().Add(-2 * time.Hour)
	for _, keep := range keeps {
		if err := os.Chtimes(keep, old, old); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.RollupPacks(); err != nil {
		t.Fatal(err)
	}
	if merged := scopePacks(t, dir, "ccn1-*.idx"); len(merged) != 1 {
		t.Fatalf("tier 1 indexes = %v, want one", merged)
	}
	if remaining := scopePacks(t, dir, "*.keep"); len(remaining) != 0 {
		t.Fatalf("stale keep files remain: %v", remaining)
	}
}

func TestManagedPacksServeReadsWithoutAGitChild(t *testing.T) {
	dir := gittest.InitRepo(t)
	repo := open(t, dir)
	var blobs []model.SHA
	err := repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
		blobs = writeBlobs(t, objects, "native", 6)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	err = repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
		for i, sha := range blobs {
			read, err := objects.ReadBlob(sha)
			if err != nil {
				return err
			}
			if want := fmt.Sprintf("native %d\n", i); string(read) != want {
				t.Errorf("ReadBlob(%s) = %q, want %q", sha, read, want)
			}
			if kind, err := objects.ObjectType(sha); err != nil || kind != plumbing.BlobObject {
				t.Errorf("ObjectType(%s) = %v, %v", sha, kind, err)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestManagedPacksFallThroughForAnObjectGitPacked(t *testing.T) {
	dir := gittest.InitRepo(t)
	repo := open(t, dir)
	blob := git(t, dir, "hash-object", "-w", "--stdin")
	git(t, dir, "repack", "-a", "-d")
	err := repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
		writeBlobs(t, objects, "other", 6)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	err = repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
		read, err := objects.ReadBlob(model.SHA(blob))
		if err != nil {
			return err
		}
		if len(read) != 0 {
			t.Errorf("empty blob read as %q", read)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
