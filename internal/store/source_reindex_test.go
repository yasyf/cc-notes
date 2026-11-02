package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/homeguard"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/model"
)

func TestSourceReindexCrashHelper(t *testing.T) {
	dir := os.Getenv("CCN_SOURCE_REINDEX_CRASH_DIR")
	if dir == "" {
		t.Skip("reindex crash subprocess")
	}
	phase := os.Getenv("CCN_SOURCE_REINDEX_CRASH_PHASE")
	output := os.Getenv("CCN_SOURCE_REINDEX_CRASH_RESULT")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	path := managed.Path(s.recordsCommonDir)
	building := path + ".reindex-building"
	if phase == "before-ready" {
		if err := os.WriteFile(building, []byte("interrupted unprepared database"), 0o600); err != nil {
			t.Fatal(err)
		}
		homeguard.ChildExit(93)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(building, data, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := bolt.Open(building, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	var previous, current sourceindex.Revision
	if err := db.View(func(tx *bolt.Tx) error {
		var err error
		previous, err = sourceRevision(tx, "")
		if err != nil {
			return err
		}
		current = previous
		current.Token = ""
		current.Predecessor = previous.Token
		current.Height++
		current.AncestorJumps, err = sourceJumps(tx, previous)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
		var err error
		current.Token, err = writeSourceRevision(objects, current)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	intent := managedIntent{Updates: []gitcmd.RefUpdate{{Ref: sourceindex.Ref, Old: previous.Token, New: current.Token}}, Before: []model.SHA{previous.Token}}
	intentData, err := json.Marshal(intent)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		if err := applySource(tx, &sourcePublication{Revision: &current}); err != nil {
			return err
		}
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		if err := root.Put(managedPendingKey, intentData); err != nil {
			return err
		}
		if err := root.Put(managedReindexIntent, intentData); err != nil {
			return err
		}
		return root.Put(managedReindexReady, []byte("1"))
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syncManagedDirectory(building); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(building, path+".reindex"); err != nil {
		t.Fatal(err)
	}
	if err := syncManagedDirectory(path + ".reindex"); err != nil {
		t.Fatal(err)
	}
	result, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(output, result, 0o600); err != nil {
		t.Fatal(err)
	}
	if phase == "after-cas" || phase == "after-bolt" {
		lock, err := managed.Lock(t.Context(), s.recordsCommonDir, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.RecordsGit.Holding(lock).UpdateRefs(t.Context(), intent.Updates); err != nil {
			t.Fatal(err)
		}
		if phase == "after-bolt" {
			staged, err := bolt.Open(path+".reindex", 0o600, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := s.recoverManaged(t.Context(), staged); err != nil {
				t.Fatal(err)
			}
			if err := staged.Close(); err != nil {
				t.Fatal(err)
			}
		}
	} else if phase != "after-ready" {
		t.Fatalf("unknown crash phase %s", phase)
	}
	homeguard.ChildExit(93)
}

func TestSourceReindexCrashBoundariesPreserveAuthority(t *testing.T) {
	for _, phase := range []string{"before-ready", "after-ready", "after-cas", "after-bolt"} {
		t.Run(phase, func(t *testing.T) {
			s := initStore(t)
			if err := s.Reindex(t.Context()); err != nil {
				t.Fatal(err)
			}
			before, err := s.SourceHead(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			output := t.TempDir() + "/result.json"
			child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestSourceReindexCrashHelper$", "-test.count=1")
			child.Env = append(os.Environ(), "CCN_SOURCE_REINDEX_CRASH_DIR="+s.Git.Dir, "CCN_SOURCE_REINDEX_CRASH_PHASE="+phase, "CCN_SOURCE_REINDEX_CRASH_RESULT="+output)
			data, err := child.CombinedOutput()
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 93 {
				t.Fatalf("crash child=%v output=%s", err, data)
			}
			after, err := s.SourceHead(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if phase == "before-ready" {
				if after.Token != before.Token {
					t.Fatalf("unprepared crash advanced token=%s want %s", after.Token, before.Token)
				}
				if err := s.Reindex(t.Context()); err != nil {
					t.Fatalf("explicit retry after unprepared crash=%v", err)
				}
			} else {
				data, err := os.ReadFile(output)
				if err != nil {
					t.Fatal(err)
				}
				var expected sourceindex.Revision
				if err := json.Unmarshal(data, &expected); err != nil {
					t.Fatal(err)
				}
				if after.Token != expected.Token {
					t.Fatalf("recovered token=%s want %s", after.Token, expected.Token)
				}
				tip, err := s.Repo.Tip(t.Context(), sourceindex.Ref)
				if err != nil || tip != after.Token {
					t.Fatalf("recovered Git=%s err=%v authority=%s", tip, err, after.Token)
				}
			}
			for _, suffix := range []string{".reindex-building", ".reindex"} {
				if _, err := os.Stat(managed.Path(s.recordsCommonDir) + suffix); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("completed recovery retained %s: %v", suffix, err)
				}
			}
		})
	}
}

func TestSourceReindexNeverDiscardsAmbiguousPreparedResidue(t *testing.T) {
	s := initStore(t)
	if err := s.Reindex(t.Context()); err != nil {
		t.Fatal(err)
	}
	path := managed.Path(s.recordsCommonDir) + ".reindex"
	residue := []byte("ambiguous prepared import")
	if err := os.WriteFile(path, residue, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Reindex(t.Context()); err == nil {
		t.Fatal("ambiguous prepared import was overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, residue) {
		t.Fatalf("prepared import residue=%q err=%v", data, err)
	}
}

func TestSourceReindexSharesPartialFoldWithManagedRows(t *testing.T) {
	s := initStore(t)
	signature := gitobj.Signature{Name: testName, Email: testEmail, When: time.Unix(1, 0).UTC()}
	root, err := s.Repo.WriteOpsCommit(t.Context(), nil, signature, "partial import root", model.Pack{Lamport: 1, Ops: []model.Op{model.CreateAnswer{Nonce: model.NewNonce(), Title: "partial import", Body: "known body"}}})
	if err != nil {
		t.Fatal(err)
	}
	tip, err := s.Repo.WriteOpsCommit(t.Context(), []model.SHA{root}, signature, "partial import tail", model.Pack{Lamport: 2, Ops: []model.Op{model.SetStatus{Status: model.StatusDone}}})
	if err != nil {
		t.Fatal(err)
	}
	ref := refs.For(model.KindAnswer, model.EntityID(root))
	if err := s.RecordsGit.UpdateRef(t.Context(), ref, tip, ""); err != nil {
		t.Fatal(err)
	}
	ctx, work := gitobj.WithReadWork(t.Context())
	if err := s.Reindex(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, decodes := work.Snapshot(); decodes > 2 {
		t.Fatalf("partial bootstrap decoded %d packs for one two-commit history", decodes)
	}
	snapshot, err := s.Load(t.Context(), ref)
	if err != nil || snapshot.Meta().SkippedOps != 1 || snapshot.EntityID() != model.EntityID(root) {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
}
