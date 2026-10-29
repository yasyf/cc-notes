package gitobj

import (
	"testing"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func TestFreshObjectReadsAvoidTheOriginalEagerIndexReload(t *testing.T) {
	dir, commonDir, hash := indexedCommitFixture(t)
	gitDir, _ := gittest.Dirs(t, dir)
	for attempt := range 2 {
		reads := make(map[string]int64)
		fs := indexReadFS{Filesystem: gitDirFS{osfs.New(commonDir)}, reads: reads}
		old := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
		if _, err := object.GetCommit(old, hash); err != nil {
			t.Fatal(err)
		}
		if len(reads) != 1 {
			t.Errorf("original backend fresh handle %d decoded %v, want one index", attempt, reads)
		}
		fresh, err := Open(gitDir, commonDir)
		if err != nil {
			t.Fatal(err)
		}
		index, newReads := testPackIndex(t, commonDir)
		fresh.storage.index = index
		sha := model.SHA(hash.String())
		if verdict, err := fresh.Ancestry(t.Context(), sha, sha); err != nil || verdict != Ancestor {
			t.Fatalf("new backend fresh handle %d = %v, %v", attempt, verdict, err)
		}
		if attempt == 0 && len(newReads) != 1 {
			t.Errorf("new backend initial conversion decoded %v, want one index", newReads)
		}
		if attempt == 1 && len(newReads) != 0 {
			t.Errorf("new backend reused generation decoded %v, want no indexes", newReads)
		}
	}
}
