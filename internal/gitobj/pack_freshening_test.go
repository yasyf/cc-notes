package gitobj

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"

	"github.com/yasyf/cc-notes/internal/gittest"
)

func packedTreeFreshener(t *testing.T, dir, commonDir string, index *packIndex) func() {
	t.Helper()
	packs, err := index.dir.ObjectPacks()
	if err != nil || len(packs) != 1 {
		t.Fatalf("fixture packs = %v, %v; want one pack", packs, err)
	}
	path := filepath.Join(commonDir, packDir, "pack-"+packs[0].String()+".pack")
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
	want := gittest.Git(t, dir, "rev-parse", "HEAD^{tree}")
	return func() {
		t.Helper()
		if got := gittest.Git(t, dir, "hash-object", "-t", "tree", "-w", emptyTree); got != want {
			t.Fatalf("freshened tree = %s, want %s", got, want)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("Git freshening changed pack payload")
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().After(old) {
			t.Fatal("Git did not freshen the pack timestamp")
		}
	}
}

func TestPackFresheningKeepsCapturedIndexGeneration(t *testing.T) {
	dir, commonDir, hash := indexedCommitFixture(t)
	index, reads := testPackIndex(t, commonDir)
	freshen := packedTreeFreshener(t, dir, commonDir, index)
	index.dir = dotgit.New(packIndexOpenFS{Filesystem: index.dir.Fs(), indexOpen: func(string) { freshen() }})
	if err := index.prepare(t.Context()); err != nil {
		t.Fatalf("publish index during Git pack freshening: %v", err)
	}
	assertIndexedCommit(t, index, hash)
	if len(reads) != 1 {
		t.Fatalf("initial index reads = %v, want one", reads)
	}
}

func TestPackFresheningReusesValidatedIndex(t *testing.T) {
	dir, commonDir, hash := indexedCommitFixture(t)
	first, _ := testPackIndex(t, commonDir)
	freshen := packedTreeFreshener(t, dir, commonDir, first)
	if err := first.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	observer := observeIndexedData(t, first)
	version := indexedDataVersion(t, observer)
	freshen()
	second, reads := testPackIndex(t, commonDir)
	if err := second.prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertIndexedCommit(t, second, hash)
	if len(reads) != 0 {
		t.Fatalf("freshened pack index payload reads = %v, want zero", reads)
	}
	if got := indexedDataVersion(t, observer); got != version {
		t.Fatalf("freshened generation data version = %d, want %d", got, version)
	}
}

func TestPackIndexRejectsPackRoutingMutation(t *testing.T) {
	for _, mutation := range []string{"remove", "replace", "truncate"} {
		t.Run(mutation, func(t *testing.T) {
			_, commonDir, _ := indexedCommitFixture(t)
			index, _ := testPackIndex(t, commonDir)
			index.dir = dotgit.New(packIndexOpenFS{Filesystem: index.dir.Fs(), indexOpen: func(path string) {
				path = filepath.Join(commonDir, strings.TrimSuffix(path, ".idx")+".pack")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				switch mutation {
				case "remove":
					err = os.Remove(path)
				case "replace":
					if err := os.WriteFile(path+".replacement", raw, 0o600); err != nil {
						t.Fatal(err)
					}
					err = os.Rename(path+".replacement", path)
				case "truncate":
					if err := os.Chmod(path, 0o600); err != nil {
						t.Fatal(err)
					}
					err = os.Truncate(path, int64(len(raw)-1))
				}
				if err != nil {
					t.Fatal(err)
				}
			}})
			if err := index.prepare(t.Context()); err == nil || index.ready {
				t.Fatalf("mutated pack prepare = %v, ready = %v; want failure and unpublished generation", err, index.ready)
			}
		})
	}
}

func TestPackIndexRecoversUnpublishedWAL(t *testing.T) {
	_, commonDir, hash := indexedCommitFixture(t)
	index, _ := testPackIndex(t, commonDir)
	if err := os.MkdirAll(filepath.Dir(index.path), 0o750); err != nil {
		t.Fatal(err)
	}
	db, err := index.open(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := enterIndexWAL(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	tx, err := beginIndexWrite(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), packIndexSchema); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := index.prepare(t.Context()); err != nil {
		t.Fatalf("prepare rolled-back initial generation: %v", err)
	}
	assertIndexedCommit(t, index, hash)
}
