package gitobj

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/yasyf/cc-notes/internal/ccnhome"
	"github.com/yasyf/cc-notes/internal/gittest"
)

func TestNativePackedObjectCorpusCost(t *testing.T) {
	for _, count := range []int{32, 10000} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			t.Setenv(ccnhome.Env, t.TempDir())
			dir := gittest.InitRepo(t)
			repo := openPackedRepo(t, dir)
			entries := make([]object.TreeEntry, 0, count)
			for i := range count {
				hash, err := repo.writeBlob(fmt.Appendf(nil, "fixture object %d\n", i))
				if err != nil {
					t.Fatal(err)
				}
				entries = append(entries, object.TreeEntry{Name: fmt.Sprintf("f%08d", i), Mode: filemode.Regular, Hash: hash})
			}
			tree, err := repo.writeObject(&object.Tree{Entries: entries})
			if err != nil {
				t.Fatal(err)
			}
			sig := object.Signature{Name: "Fixture", Email: "fixture@example.com", When: time.Unix(1700000000, 0)}
			hash, err := repo.writeObject(&object.Commit{Author: sig, Committer: sig, TreeHash: tree, Message: "fixture"})
			if err != nil {
				t.Fatal(err)
			}
			gittest.Git(t, dir, "update-ref", "refs/heads/main", hash.String())
			gittest.Git(t, dir, "repack", "-adq")
			gittest.Git(t, dir, "multi-pack-index", "write")
			_, common := gittest.Dirs(t, dir)
			home, err := ccnhome.ForRepo(common)
			if err != nil {
				t.Fatal(err)
			}
			for attempt := range 2 {
				started := time.Now()
				assertNativeCommit(t, openPackedRepo(t, dir), hash)
				t.Logf("unrelated=%d fresh_attempt=%d elapsed=%v native_midx=true", count, attempt, time.Since(started))
				if _, err := os.Stat(filepath.Join(home.Dir, "objects-v2.sqlite")); !os.IsNotExist(err) {
					t.Fatalf("normal native read touched object SQL index: %v", err)
				}
			}
		})
	}
}
