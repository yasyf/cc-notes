package gitobj

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/yasyf/cc-notes/internal/gittest"
)

func TestReferenceCounterDecode(t *testing.T) {
	valid := make([]byte, 28)
	binary.LittleEndian.PutUint32(valid[:4], 28)
	binary.LittleEndian.PutUint32(valid[4:8], unix.ATTR_CMN_GEN_COUNT)
	binary.LittleEndian.PutUint32(valid[24:], 7)
	for _, tc := range []struct {
		name               string
		edit               func([]byte) []byte
		counter            uint32
		supported, invalid bool
	}{
		{"supported", func(data []byte) []byte { return data }, 7, true, false},
		{"supported zero", func(data []byte) []byte { clear(data[24:]); return data }, 0, true, false},
		{"unsupported", func(data []byte) []byte { clear(data[4:8]); return data }, 0, false, false},
		{"unsupported omitted", func(data []byte) []byte { clear(data[4:8]); binary.LittleEndian.PutUint32(data[:4], 24); return data }, 0, false, false},
		{"short buffer", func(data []byte) []byte { return data[:27] }, 0, false, true},
		{"short header", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[:4], 23); return data }, 0, false, true},
		{"overflow", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[:4], 29); return data }, 0, false, true},
		{"supported truncated", func(data []byte) []byte { binary.LittleEndian.PutUint32(data[:4], 24); return data }, 0, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			counter, supported, err := decodeReferenceCounter(tc.edit(append([]byte(nil), valid...)))
			if counter != tc.counter || supported != tc.supported || (err != nil) != tc.invalid {
				t.Fatalf("decode = %d %t %v, want %d %t invalid=%t", counter, supported, err, tc.counter, tc.supported, tc.invalid)
			}
		})
	}
}

func TestReferenceCounterRapidNativeUpdates(t *testing.T) {
	dir := gittest.InitRepo(t)
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "first")
	first := gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "second")
	second := gittest.Git(t, dir, "rev-parse", "HEAD")
	gittest.Git(t, dir, "update-ref", "refs/cc-notes/docs/keep", first)
	gittest.Git(t, dir, "update-ref", "refs/cc-notes/docs/mutable", first)
	linked := gittest.AddWorktree(t, dir, "linked")
	gitDir, commonDir := gittest.Dirs(t, linked)
	repo, err := Open(gitDir, commonDir)
	if err != nil {
		t.Fatal(err)
	}
	_, supported, err := nativeReferenceCounter(filepath.Join(commonDir, "refs/cc-notes/docs"))
	if err != nil {
		t.Fatal(err)
	}
	if !supported {
		if os.Getenv("CCN_REQUIRE_NATIVE_REF_COUNTER") == "1" {
			t.Fatal("native reference counter is required on this CI filesystem")
		}
		t.Skip("filesystem does not return COMMON generation counter")
	}
	stamp := func() ReferenceGeneration {
		t.Helper()
		taken := time.Now()
		generation, err := repo.RefGeneration(t.Context(), "refs/cc-notes/docs/")
		if err != nil || !generation.SettledAt(taken) || !generation.loose.nativeKnown {
			t.Fatalf("native generation = %+v %v", generation, err)
		}
		return generation
	}
	before := stamp()
	for i := range 32 {
		next := second
		if i%2 == 1 {
			next = first
		}
		gittest.Git(t, linked, "update-ref", "refs/cc-notes/docs/mutable", next)
		after := stamp()
		if after.Key() == before.Key() || after != stamp() {
			t.Fatalf("cycle %d did not distinguish mutation then stable state", i)
		}
		before = after
	}
	gittest.Git(t, linked, "update-ref", "-d", "refs/cc-notes/docs/mutable")
	afterDelete := stamp()
	if afterDelete.Key() == before.Key() {
		t.Fatal("deletion retained generation")
	}
	gittest.Git(t, linked, "update-ref", "refs/cc-notes/docs/mutable", first)
	if stamp().Key() == afterDelete.Key() {
		t.Fatal("recreation retained generation")
	}
}

func TestReferenceCounterPrivateWorktreeRouting(t *testing.T) {
	dir := gittest.InitRepo(t)
	gittest.Git(t, dir, "commit", "--allow-empty", "-qm", "base")
	linked := gittest.AddWorktree(t, dir, "linked")
	gitDir, commonDir := gittest.Dirs(t, linked)
	repo, err := Open(gitDir, commonDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, namespace := range []string{"bisect", "rewritten", "worktree"} {
		t.Run(namespace, func(t *testing.T) {
			name := "refs/" + namespace
			private := filepath.Join(gitDir, name)
			if err := os.MkdirAll(private, 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(commonDir, name)); !os.IsNotExist(err) {
				t.Fatalf("common private namespace unexpectedly exists: %v", err)
			}
			var previous string
			for _, commonPresent := range []bool{false, true} {
				if commonPresent {
					if err := os.MkdirAll(filepath.Join(commonDir, name), 0o700); err != nil {
						t.Fatal(err)
					}
				}
				generation, err := repo.RefGeneration(t.Context(), name+"/")
				if err != nil {
					t.Fatal(err)
				}
				info, err := os.Stat(private)
				if err != nil {
					t.Fatal(err)
				}
				device, inode := FileID(info)
				if previous != "" && generation.Key() != previous {
					t.Fatal("unrelated common namespace changed private generation")
				}
				previous = generation.Key()
				if generation.directory != name || generation.loose.device != device || generation.loose.inode != inode || generation.loose.nativeKnown {
					t.Fatalf("commonPresent=%t private generation=%+v want %d:%d", commonPresent, generation, device, inode)
				}
			}
		})
	}
}
