package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourceview"
	"github.com/yasyf/cc-notes/model"
)

func requireFetchedSource(t *testing.T, s *Store, snapshot model.Snapshot) {
	t.Helper()
	head, err := s.SourceHead(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("entity:%s:%s", snapshot.Meta().Kind, snapshot.EntityID())
	_, stream, err := s.OpenSourceContent(t.Context(), head.Token, key)
	if err != nil {
		t.Fatal(err)
	}
	actual, readErr := io.ReadAll(stream)
	if err := errors.Join(readErr, stream.Close()); err != nil {
		t.Fatal(err)
	}
	want := sourceview.CodecOf(snapshot.Meta().Kind).Render(snapshot)
	if !bytes.Equal(actual, want) {
		t.Fatalf("imported source bytes=%q; want %q", actual, want)
	}
}

func TestImportFetchedRefRequiresExistingAuthority(t *testing.T) {
	s := initStore(t)
	note := create(t, s, noteOps("missing authority import"))
	ref := refs.For(model.KindNote, note.EntityID())
	path := managed.Path(s.recordsCommonDir)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	operations := map[string]func() error{
		"list":    func() error { _, err := s.ListSnapshots(t.Context(), model.KindNote, ListOpts{}); return err },
		"history": func() error { _, err := s.History(t.Context(), ref); return err },
		"import":  func() error { return s.ImportFetchedRef(t.Context(), ref, note.Meta().Head) },
	}
	for name, operation := range operations {
		if err := operation(); !errors.Is(err, managed.ErrReindex) {
			t.Fatalf("%s without authority=%v; want ErrReindex", name, err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing authority was recreated: %v", err)
	}
}

func TestImportFetchedRefPublishesOnlyTheSuppliedEntity(t *testing.T) {
	s := initStore(t)
	prepared, err := s.PrepareCreateExact(t.Context(), noteOps("fetched canonical note"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordsGit.UpdateRefs(t.Context(), []gitcmd.RefUpdate{prepared.RefUpdate()}); err != nil {
		t.Fatal(err)
	}
	poison := poisonManagedUnrelatedRef(t, s, model.KindTask)
	if err := s.ImportFetchedRef(t.Context(), "refs/heads/main", prepared.New); !errors.Is(err, refs.ErrNotCCNotes) {
		t.Fatalf("nonentity import=%v; want ErrNotCCNotes", err)
	}
	if err := s.ImportFetchedRef(t.Context(), prepared.Ref, ""); err == nil {
		t.Fatal("empty-tip import succeeded")
	}
	if err := s.ImportFetchedRef(t.Context(), prepared.Ref, prepared.New); err != nil {
		t.Fatal(err)
	}
	requireManagedProjection(t, s, prepared.Ref, prepared.Snapshot)
	requireFetchedSource(t, s, prepared.Snapshot)
	updated, err := s.PrepareAppendAt(t.Context(), prepared.Ref, prepared.New, []model.Op{model.SetBody{Body: "fetched replacement"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordsGit.UpdateRefs(t.Context(), []gitcmd.RefUpdate{updated.RefUpdate()}); err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRef(t.Context(), prepared.Ref, updated.New, updated.New); !errors.Is(err, managed.ErrReindex) {
		t.Fatalf("ordinary publication adopted external drift: %v", err)
	}
	if err := s.ImportFetchedRef(t.Context(), prepared.Ref, prepared.New); !errors.Is(err, gitcmd.ErrCASMismatch) {
		t.Fatalf("stale import=%v; want ErrCASMismatch", err)
	}
	if got, err := s.Load(t.Context(), prepared.Ref); err != nil || !reflect.DeepEqual(got, prepared.Snapshot) {
		t.Fatalf("rejected import changed authority=%#v, %v; want %#v", got, err, prepared.Snapshot)
	}
	if err := s.ImportFetchedRef(t.Context(), prepared.Ref, updated.New); err != nil {
		t.Fatal(err)
	}
	requireManagedProjection(t, s, prepared.Ref, updated.Snapshot)
	requireFetchedSource(t, s, updated.Snapshot)
	if err := os.Remove(poison); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordsGit.DeleteRef(t.Context(), prepared.Ref, updated.New); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, s.Git.Dir, "reflog", "expire", "--expire=now", "--all")
	gittest.Git(t, s.Git.Dir, "gc", "--prune=now")
	chain, err := s.Repo.ReadChain(t.Context(), updated.New)
	if err != nil || len(chain) != 2 || chain[0].SHA != updated.New {
		t.Fatalf("retained imported chain=%#v, %v; want two commits headed by %s", chain, err, updated.New)
	}
	requireFetchedSource(t, s, updated.Snapshot)
}

func TestImportFetchedRefRecoveryKeepsProjectionWithItsRefTransaction(t *testing.T) {
	for _, applied := range []bool{false, true} {
		t.Run(fmt.Sprintf("applied=%t", applied), func(t *testing.T) {
			s := initStore(t)
			prepared, err := s.PrepareCreateExact(t.Context(), noteOps("recovered fetched note"))
			if err != nil {
				t.Fatal(err)
			}
			if err := s.RecordsGit.UpdateRefs(t.Context(), []gitcmd.RefUpdate{prepared.RefUpdate()}); err != nil {
				t.Fatal(err)
			}
			updates := []gitcmd.RefUpdate{{Ref: prepared.Ref, Old: prepared.New, New: prepared.New}}
			preparation, err := s.prepareManaged(t.Context(), updates, nil)
			if err != nil {
				t.Fatal(err)
			}
			db, err := s.openManaged(false)
			if err != nil {
				t.Fatal(err)
			}
			intent, err := s.buildManagedIntent(t.Context(), db, updates, preparation)
			if err != nil {
				t.Fatal(err)
			}
			intent.Imported = prepared.Ref
			intent.Before = make([]model.SHA, len(intent.Updates))
			for i, update := range intent.Updates {
				tip, err := s.Repo.Tip(t.Context(), update.Ref)
				if err != nil && !errors.Is(err, gitobj.ErrRefNotFound) {
					t.Fatal(err)
				}
				intent.Before[i] = tip
			}
			data, err := json.Marshal(intent)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Update(func(tx *bolt.Tx) error {
				root, err := managedRoot(tx)
				if err != nil {
					return err
				}
				return root.Put(managedPendingKey, data)
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			if applied {
				if err := s.RecordsGit.UpdateRefs(t.Context(), intent.Updates); err != nil {
					t.Fatal(err)
				}
			}
			tips, err := s.ManagedTips(t.Context(), refs.Namespace)
			if err != nil {
				t.Fatal(err)
			}
			if !applied {
				if len(tips) != 0 {
					t.Fatalf("unpublished import became authoritative: %v", tips)
				}
				if err := s.ImportFetchedRef(t.Context(), prepared.Ref, prepared.New); err != nil {
					t.Fatal(err)
				}
			}
			requireManagedProjection(t, s, prepared.Ref, prepared.Snapshot)
			requireFetchedSource(t, s, prepared.Snapshot)
		})
	}
}
