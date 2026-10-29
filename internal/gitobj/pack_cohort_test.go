package gitobj

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

type packIndexOpenFS struct {
	billy.Filesystem
	indexOpen func(string)
}

func (fs packIndexOpenFS) Open(path string) (billy.File, error) {
	if filepath.Ext(path) == ".idx" {
		fs.indexOpen(path)
	}
	return fs.Filesystem.Open(path)
}

func TestPackIndexPublishesCapturedCohortDuringPackArrival(t *testing.T) {
	for _, pendingIndex := range []bool{false, true} {
		name := "complete"
		if pendingIndex {
			name = "pending-index"
		}
		t.Run(name, func(t *testing.T) {
			dir, commonDir, first := indexedCommitFixture(t)
			gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "second")
			second := plumbing.NewHash(gittest.Git(t, dir, "rev-parse", "HEAD"))
			index, reads := testPackIndex(t, commonDir)
			captured, err := index.identities(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var hiddenIndex string
			index.dir = dotgit.New(packIndexOpenFS{Filesystem: index.dir.Fs(), indexOpen: func(path string) {
				gittest.Git(t, dir, "repack", "-aq")
				stamp, err := index.identity(captured[0].hash)
				if err != nil || !bytes.Equal(stamp, captured[0].stamp) {
					t.Fatalf("arrival fixture changed captured pack: %v", err)
				}
				if pendingIndex {
					entries, err := os.ReadDir(filepath.Join(commonDir, packDir))
					if err != nil {
						t.Fatal(err)
					}
					for _, entry := range entries {
						candidate := filepath.Join(packDir, entry.Name())
						if filepath.Ext(candidate) == ".idx" && candidate != path {
							hiddenIndex = filepath.Join(commonDir, candidate)
							if err := os.Rename(hiddenIndex, hiddenIndex+".pending"); err != nil {
								t.Fatal(err)
							}
						}
					}
					if hiddenIndex == "" {
						t.Fatal("new pack index was not found")
					}
				}
			}})
			if err := index.prepare(t.Context()); err != nil {
				t.Fatalf("publish captured cohort beside new pack: %v", err)
			}
			if second == plumbing.ZeroHash || len(reads) != 1 {
				t.Fatalf("arrival hash = %s, index reads = %v; want new hash and one captured index", second, reads)
			}
			assertIndexedCommit(t, index, first)
			observer := observeIndexedData(t, index)
			var generation []byte
			if err := observer.QueryRowContext(t.Context(), "SELECT value FROM metadata WHERE key = 'generation'").Scan(&generation); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(generation, packGeneration(captured)) {
				t.Fatal("published generation includes uncaptured packs")
			}
			var newObjects int
			if err := observer.QueryRowContext(t.Context(), "SELECT count(*) FROM objects WHERE hash = ?", second[:]).Scan(&newObjects); err != nil {
				t.Fatal(err)
			}
			if newObjects != 0 {
				t.Fatalf("uncaptured object copies = %d, want zero", newObjects)
			}
			if hiddenIndex != "" {
				if err := os.Rename(hiddenIndex+".pending", hiddenIndex); err != nil {
					t.Fatal(err)
				}
			}
			fresh, newReads := testPackIndex(t, commonDir)
			if err := fresh.prepare(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(newReads) != 1 {
				t.Fatalf("arrival reconciliation reads = %v, want only one new index", newReads)
			}
			for path := range newReads {
				if reads[path] != 0 {
					t.Errorf("captured index %s decoded again", path)
				}
			}
			assertIndexedCommit(t, fresh, second)
		})
	}
}

func TestPackIndexRejectsCapturedPackMutationDuringLaterDecode(t *testing.T) {
	for _, remove := range []bool{false, true} {
		name := "changed"
		if remove {
			name = "removed"
		}
		t.Run(name, func(t *testing.T) {
			dir, commonDir, _ := indexedCommitFixture(t)
			gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "second")
			gittest.Git(t, dir, "repack", "-dq")
			index, _ := testPackIndex(t, commonDir)
			packs, err := index.dir.ObjectPacks()
			if err != nil {
				t.Fatal(err)
			}
			if len(packs) != 2 {
				t.Fatalf("fixture packs = %d, want two", len(packs))
			}
			first, changed := "", false
			index.dir = dotgit.New(packIndexOpenFS{Filesystem: index.dir.Fs(), indexOpen: func(path string) {
				if first == "" {
					first = path
					return
				}
				changed = true
				path = filepath.Join(commonDir, first)
				if remove {
					if err := os.Remove(path); err != nil {
						t.Fatal(err)
					}
				} else {
					info, err := os.Stat(path)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.Chtimes(path, info.ModTime(), info.ModTime().Add(time.Second)); err != nil {
						t.Fatal(err)
					}
				}
			}})
			if err := index.prepare(t.Context()); err == nil {
				t.Fatal("captured pack mutation was published")
			}
			if !changed || index.ready {
				t.Fatalf("mutation reached = %v, ready = %v; want true, false", changed, index.ready)
			}
			observer := observeIndexedData(t, index)
			var tables int
			if err := observer.QueryRowContext(t.Context(), "SELECT count(*) FROM sqlite_schema WHERE name = 'metadata'").Scan(&tables); err != nil {
				t.Fatal(err)
			}
			if tables != 0 {
				t.Fatalf("rolled-back metadata tables = %d, want zero", tables)
			}
		})
	}
}

func TestIndexedRepoRepairsPackArrivalWithinAncestry(t *testing.T) {
	dir, commonDir, first := indexedCommitFixture(t)
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "second")
	second := model.SHA(gittest.Git(t, dir, "rev-parse", "HEAD"))
	gitDir, _ := gittest.Dirs(t, dir)
	repo, err := Open(gitDir, commonDir)
	if err != nil {
		t.Fatal(err)
	}
	index, reads := testPackIndex(t, commonDir)
	arrived := false
	index.dir = dotgit.New(packIndexOpenFS{Filesystem: index.dir.Fs(), indexOpen: func(string) {
		if !arrived {
			arrived = true
			gittest.Git(t, dir, "repack", "-aq")
			gittest.Git(t, dir, "prune-packed")
		}
	}})
	repo.storage.index = index
	if got, err := repo.Ancestry(t.Context(), model.SHA(first.String()), second); err != nil || got != Ancestor {
		t.Fatalf("ancestry across pack arrival = %v, %v; want Ancestor", got, err)
	}
	if !arrived || len(reads) != 2 {
		t.Fatalf("pack arrived = %v, index payload reads = %v; want true and two distinct indexes", arrived, reads)
	}
}

func TestIndexedRepoSeesLateAlternatePackInSameReadScope(t *testing.T) {
	alternate, alternateCommon, first := indexedCommitFixture(t)
	gittest.Git(t, alternate, "commit", "--allow-empty", "-qm", "second")
	second := plumbing.NewHash(gittest.Git(t, alternate, "rev-parse", "HEAD"))
	dir := gittest.InitRepo(t)
	gitDir, commonDir := gittest.Dirs(t, dir)
	if err := os.WriteFile(filepath.Join(commonDir, "objects", "info", "alternates"), []byte(filepath.Join(alternateCommon, "objects")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	repo, err := Open(gitDir, commonDir)
	if err != nil {
		t.Fatal(err)
	}
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
	if got := gittest.Git(t, dir, "cat-file", "-t", second.String()); got != "commit" {
		t.Fatalf("native Git alternate lookup = %q, want commit", got)
	}
	commit, err := repo.lookupCommit(second)
	if err != nil || commit.Hash != second {
		t.Fatalf("same-scope late alternate commit = %v, %v; want %s", commit, err, second)
	}
}
