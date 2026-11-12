package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func requireManagedProjection(t *testing.T, s *Store, ref string, snapshot model.Snapshot) {
	t.Helper()
	db, err := s.openManaged(true)
	if err != nil {
		t.Fatal(err)
	}
	err = db.View(func(tx *bolt.Tx) error {
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		if root.Get(managedPendingKey) != nil {
			t.Error("completed publication retained a pending intent")
		}
		return nil
	})
	if err := errors.Join(err, db.Close()); err != nil {
		t.Fatal(err)
	}
	tip, err := s.managedTip(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Repo.Tip(t.Context(), ref); err != nil || got != tip {
		t.Fatalf("Git tip=%s, %v; managed=%s", got, err, tip)
	}
	loaded, err := s.Load(t.Context(), ref)
	if err != nil || !reflect.DeepEqual(loaded, snapshot) {
		t.Fatalf("managed snapshot=%#v, %v; want %#v", loaded, err, snapshot)
	}
	source, err := queryMetadataIdentity(t.Context(), s, snapshot.Meta().Kind)
	if err != nil {
		t.Fatal(err)
	}
	want := projectQueryMetadata(source.binding, tip, snapshot)
	if err := s.managedView(t.Context(), func(tx *bolt.Tx) error {
		row, err := decodeQueryMetadata(tx, source, readQueryMetadata(tx, source.binding, snapshot.EntityID()))
		if err != nil {
			return err
		}
		actual, err := json.Marshal(row)
		if err != nil {
			return err
		}
		expected, err := json.Marshal(want)
		if err != nil {
			return err
		}
		if !bytes.Equal(actual, expected) {
			t.Errorf("managed row=%s; want %s", actual, expected)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func requireManagedAnswerLabel(t *testing.T, s *Store, label string, want ...model.EntityID) {
	t.Helper()
	answers, hit, err := s.IndexedAnswers(t.Context(), []string{label}, ListOpts{})
	if err != nil || !hit {
		t.Fatalf("indexed %q hit=%t, %v", label, hit, err)
	}
	ids := make([]model.EntityID, len(answers))
	for i := range answers {
		ids[i] = answers[i].ID
	}
	slices.Sort(ids)
	slices.Sort(want)
	if !slices.Equal(ids, want) {
		t.Fatalf("indexed %q=%v; want %v", label, ids, want)
	}
}

func poisonManagedUnrelatedRef(t *testing.T, s *Store, kind model.Kind) string {
	t.Helper()
	path := filepath.Join(s.recordsCommonDir, filepath.FromSlash(refs.Root(kind)), strings.Repeat("f", 40))
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Repo.ListPrefix(t.Context(), refs.Root(kind)); err == nil {
		t.Fatal("poison fixture did not reject a full native ref listing")
	}
	return path
}

func TestManagedAuthorityMissingAndCorruptRequireReindex(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt"} {
		t.Run(mode, func(t *testing.T) {
			s := initStore(t)
			note := create(t, s, noteOps("existing authoritative note")).(model.Note)
			ref := refs.For(model.KindNote, note.ID)
			path := managed.Path(s.recordsCommonDir)
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			broken := []byte("invalid managed authority")
			if mode == "corrupt" {
				if err := os.WriteFile(path, broken, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			poison := poisonManagedUnrelatedRef(t, s, model.KindNote)
			operations := map[string]func() error{
				"load":      func() error { _, err := s.Load(t.Context(), ref); return err },
				"list":      func() error { _, err := s.ListNotes(t.Context(), true, true); return err },
				"existence": func() error { _, err := s.HasNotes(t.Context()); return err },
				"query":     func() error { _, _, err := s.IndexedDocs(t.Context(), []string{"selected"}, ListOpts{}); return err },
				"create":    func() error { _, err := s.CreateExact(t.Context(), noteOps("must not publish")); return err },
			}
			for name, operation := range operations {
				if err := operation(); !errors.Is(err, managed.ErrReindex) {
					t.Fatalf("%s error=%v; want explicit reindex", name, err)
				}
			}
			data, err := os.ReadFile(path)
			if mode == "missing" && !errors.Is(err, os.ErrNotExist) || mode == "corrupt" && (err != nil || !bytes.Equal(data, broken)) {
				t.Fatalf("ordinary operations rebuilt authority: data=%q, %v", data, err)
			}
			if err := os.Remove(poison); err != nil {
				t.Fatal(err)
			}
			if err := s.Reindex(t.Context()); err != nil {
				t.Fatal(err)
			}
			requireManagedProjection(t, s, ref, note)
			tips, err := s.ManagedTips(t.Context(), refs.Namespace)
			if err != nil || !maps.Equal(tips, map[string]model.SHA{ref: note.Head}) {
				t.Fatalf("explicit reindex refs=%v, %v", tips, err)
			}
		})
	}
}

func TestManagedAuthorityBootstrapPreservesEveryKind(t *testing.T) {
	s := initStore(t)
	want := make(map[string]model.Snapshot)
	for _, kind := range model.Kinds() {
		ops := duplicateKindOperations()[kind]("bootstrap " + string(kind))
		switch kind {
		case model.KindTask, model.KindSprint, model.KindProject, model.KindRunbook, model.KindLedger, model.KindPlan:
			ops = append(ops, model.AddLabel{Label: "imported"})
		default:
			ops = append(ops, model.AddTag{Tag: "imported"})
		}
		snapshot := create(t, s, ops)
		want[refs.For(kind, snapshot.EntityID())] = snapshot
	}
	if err := os.Remove(managed.Path(s.recordsCommonDir)); err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(s.Git.Dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.Close() })
	if err := fresh.Reindex(t.Context()); err != nil {
		t.Fatal(err)
	}
	tips, err := fresh.ManagedTips(t.Context(), refs.Namespace)
	if err != nil || len(tips) != len(want) {
		t.Fatalf("bootstrap refs=%d, %v; want %d", len(tips), err, len(want))
	}
	for ref, snapshot := range want {
		requireManagedProjection(t, fresh, ref, snapshot)
	}
	status, hit, err := fresh.StatusMetadata(t.Context(), model.Kinds()...)
	if err != nil || !hit || len(status.Records) != len(want) {
		t.Fatalf("bootstrap status=%+v, hit=%t, %v", status, hit, err)
	}
	for _, row := range status.Records {
		ref := refs.For(row.Kind, row.ID)
		snapshot, err := status.Captured.Load(t.Context(), ref)
		if err != nil || !reflect.DeepEqual(snapshot, want[ref]) {
			t.Fatalf("bootstrap captured %s=%#v, %v; want %#v", ref, snapshot, err, want[ref])
		}
	}
}

func TestAlignRefsImportsExtensionsAndRestoresRewindsAndDeletions(t *testing.T) {
	s := initStore(t)
	extended := create(t, s, noteOps("extended outside managed publication"))
	extendedRef := refs.For(model.KindNote, extended.EntityID())
	raw, err := s.PrepareAppendAt(t.Context(), extendedRef, extended.Meta().Head, []model.Op{model.SetBody{Body: "raw extension"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordsGit.UpdateRefs(t.Context(), []gitcmd.RefUpdate{raw.RefUpdate()}); err != nil {
		t.Fatal(err)
	}
	created, err := s.PrepareCreateExact(t.Context(), noteOps("created outside managed publication"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordsGit.UpdateRefs(t.Context(), []gitcmd.RefUpdate{created.RefUpdate()}); err != nil {
		t.Fatal(err)
	}
	rewound := create(t, s, noteOps("rewound outside managed publication"))
	rewoundRef := refs.For(model.KindNote, rewound.EntityID())
	kept, err := s.Append(t.Context(), rewoundRef, []model.Op{model.SetBody{Body: "managed append"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordsGit.UpdateRefs(t.Context(), []gitcmd.RefUpdate{{Ref: rewoundRef, Old: kept.Meta().Head, New: rewound.Meta().Head}}); err != nil {
		t.Fatal(err)
	}
	deleted := create(t, s, noteOps("deleted by a pruning fetch"))
	deletedRef := refs.For(model.KindNote, deleted.EntityID())
	if err := s.RecordsGit.DeleteRef(t.Context(), deletedRef, deleted.Meta().Head); err != nil {
		t.Fatal(err)
	}

	adopted, restored, err := s.AlignRefs(t.Context())
	if err != nil || adopted != 2 || restored != 2 {
		t.Fatalf("AlignRefs=%d adopted, %d restored, %v; want the extension and the create adopted, the rewind and the deletion restored", adopted, restored, err)
	}
	requireManagedProjection(t, s, extendedRef, raw.Snapshot)
	requireManagedProjection(t, s, created.Ref, created.Snapshot)
	requireManagedProjection(t, s, rewoundRef, kept)
	requireManagedProjection(t, s, deletedRef, deleted)
	if _, err := s.Append(t.Context(), rewoundRef, []model.Op{model.SetBody{Body: "after the restore"}}); err != nil {
		t.Fatalf("append after a restore: %v", err)
	}
	if adopted, restored, err := s.AlignRefs(t.Context()); err != nil || adopted != 0 || restored != 0 {
		t.Fatalf("second AlignRefs=%d adopted, %d restored, %v; want none", adopted, restored, err)
	}
}

func TestAWriteRestoresARefGitDeletedOrRewoundAndRefusesADivergence(t *testing.T) {
	s := initStore(t)
	note := create(t, s, noteOps("managed tip stays"))
	ref := refs.For(model.KindNote, note.EntityID())
	kept, err := s.Append(t.Context(), ref, []model.Op{model.SetBody{Body: "managed append"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordsGit.UpdateRefs(t.Context(), []gitcmd.RefUpdate{{Ref: ref, Old: kept.Meta().Head, New: note.Meta().Head}}); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportFetchedRef(t.Context(), ref, note.Meta().Head); !errors.Is(err, gitcmd.ErrCASMismatch) {
		t.Fatalf("import of a rewound tip=%v; want ErrCASMismatch once the ref is restored", err)
	}
	requireManagedProjection(t, s, ref, kept)

	if err := s.RecordsGit.DeleteRef(t.Context(), ref, kept.Meta().Head); err != nil {
		t.Fatal(err)
	}
	appended, err := s.Append(t.Context(), ref, []model.Op{model.SetBody{Body: "after a pruning fetch"}})
	if err != nil {
		t.Fatalf("append over a deleted ref: %v", err)
	}
	if parent, err := s.Repo.IsAncestor(t.Context(), kept.Meta().Head, appended.Meta().Head); err != nil || !parent {
		t.Fatalf("append over a deleted ref built on %v, %v; want the managed tip %s", appended.Meta().Head, err, kept.Meta().Head)
	}
	requireManagedProjection(t, s, ref, appended)

	sibling, err := s.PrepareAppendAt(t.Context(), ref, kept.Meta().Head, []model.Op{model.SetBody{Body: "raw sibling"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordsGit.UpdateRefs(t.Context(), []gitcmd.RefUpdate{{Ref: ref, Old: appended.Meta().Head, New: sibling.New}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(t.Context(), ref, []model.Op{model.SetBody{Body: "over a divergence"}}); !errors.Is(err, ErrDiverged) || errors.Is(err, managed.ErrReindex) {
		t.Fatalf("append over a diverged ref=%v; want ErrDiverged, never ErrReindex", err)
	}
	if loaded, err := s.Load(t.Context(), ref); err != nil || !reflect.DeepEqual(loaded, appended) {
		t.Fatalf("refused append changed authority=%#v, %v; want %#v", loaded, err, appended)
	}
}

func TestManagedAuthorityAdoptsExtensionsAndReindexesTheRest(t *testing.T) {
	s := initStore(t)
	a := create(t, s, answerOps("managed before raw writer")).(model.Answer)
	ref := refs.For(model.KindAnswer, a.ID)
	prepared, err := s.PrepareAppendAt(t.Context(), ref, a.Head, []model.Op{model.AddTag{Tag: "raw"}, model.SetBody{Body: "raw replacement"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordsGit.UpdateRefs(t.Context(), []gitcmd.RefUpdate{prepared.RefUpdate()}); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(t.Context(), ref)
	if err != nil || !reflect.DeepEqual(loaded, a) {
		t.Fatalf("raw writer changed managed read=%#v, %v; want %#v", loaded, err, a)
	}
	requireManagedAnswerLabel(t, s, "raw")
	appended, err := s.Append(t.Context(), ref, []model.Op{model.AddTag{Tag: "after raw"}})
	if err != nil {
		t.Fatalf("append over a raw extension: %v", err)
	}
	if got := appended.(model.Answer); got.Body != "raw replacement" {
		t.Fatalf("append dropped the raw extension: %#v", got)
	}
	requireManagedProjection(t, s, ref, appended)
	requireManagedAnswerLabel(t, s, "raw", a.ID)
	requireManagedAnswerLabel(t, s, "after raw", a.ID)
	created, err := s.PrepareCreateExact(t.Context(), append(answerOps("raw create"), model.AddTag{Tag: "raw"}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RecordsGit.UpdateRefs(t.Context(), []gitcmd.RefUpdate{created.RefUpdate()}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(t.Context(), created.Ref); !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Fatalf("raw create visible before reindex: %v", err)
	}
	requireManagedAnswerLabel(t, s, "raw", a.ID)
	if err := s.Reindex(t.Context()); err != nil {
		t.Fatal(err)
	}
	requireManagedProjection(t, s, created.Ref, created.Snapshot)
	requireManagedAnswerLabel(t, s, "raw", a.ID, created.Snapshot.EntityID())
	if err := s.RecordsGit.DeleteRef(t.Context(), ref, appended.Meta().Head); err != nil {
		t.Fatal(err)
	}
	if loaded, err := s.Load(t.Context(), ref); err != nil || !reflect.DeepEqual(loaded, appended) {
		t.Fatalf("raw deletion changed captured authority=%#v, %v", loaded, err)
	}
	if err := s.Reindex(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(t.Context(), ref); !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Fatalf("reindexed raw deletion remains visible: %v", err)
	}
	requireManagedAnswerLabel(t, s, "raw", created.Snapshot.EntityID())
}

func TestManagedAuthorityMutationLifecycleAndCAS(t *testing.T) {
	s := initStore(t)
	a := create(t, s, answerOps("managed lifecycle")).(model.Answer)
	ref := refs.For(model.KindAnswer, a.ID)
	requireManagedProjection(t, s, ref, a)
	appended, err := s.Append(t.Context(), ref, []model.Op{model.AddTag{Tag: "selected"}, model.SetBody{Body: "managed changed body"}})
	if err != nil {
		t.Fatal(err)
	}
	requireManagedProjection(t, s, ref, appended)
	requireManagedAnswerLabel(t, s, "selected", a.ID)
	stale, err := s.PrepareAppendAt(t.Context(), ref, a.Head, []model.Op{model.AddTag{Tag: "loser"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRefs(t.Context(), []gitcmd.RefUpdate{stale.RefUpdate()}); !errors.Is(err, gitcmd.ErrCASMismatch) {
		t.Fatalf("stale CAS=%v; want mismatch", err)
	}
	requireManagedProjection(t, s, ref, appended)
	requireManagedAnswerLabel(t, s, "loser")
	sig, _, err := s.signature(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := s.Repo.WriteOpsCommit(t.Context(), []model.SHA{a.Head}, sig, "remote tag", model.Pack{Lamport: 2, Ops: []model.Op{model.AddTag{Tag: "remote"}}})
	if err != nil {
		t.Fatal(err)
	}
	ours, err := s.managedTip(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := s.Merge(t.Context(), ref, ours, theirs)
	if err != nil {
		t.Fatal(err)
	}
	mergedSnapshot, err := s.LoadAt(t.Context(), merged)
	if err != nil {
		t.Fatal(err)
	}
	requireManagedProjection(t, s, ref, mergedSnapshot)
	requireManagedAnswerLabel(t, s, "remote", a.ID)
	requireManagedAnswerLabel(t, s, "selected", a.ID)
	compacted, err := s.Compact(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	requireManagedProjection(t, s, ref, compacted)
	requireManagedAnswerLabel(t, s, "remote", a.ID)
	tip, err := s.managedTip(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRef(t.Context(), ref, "", tip); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(t.Context(), ref); !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Fatalf("managed deletion remains visible: %v", err)
	}
	requireManagedAnswerLabel(t, s, "remote")
	requireManagedAnswerLabel(t, s, "selected")
	if present, err := s.HasNotes(t.Context()); err != nil || present {
		t.Fatalf("managed deletion existence=%t, %v", present, err)
	}
}

func TestManagedAuthorityPreservesPartialImportedOutput(t *testing.T) {
	s := initStore(t)
	a := create(t, s, append(answerOps("partial"), model.AddTag{Tag: "selected"})).(model.Answer)
	ref := refs.For(model.KindAnswer, a.ID)
	sig, _, err := s.signature(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	partial, err := s.Repo.WriteOpsCommit(t.Context(), []model.SHA{a.Head}, sig, "future kind operation", model.Pack{Lamport: 2, Ops: []model.Op{model.SetStatus{Status: model.StatusDone}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PublishRef(t.Context(), ref, partial, a.Head); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load(t.Context(), ref)
	if err != nil || loaded.Meta().SkippedOps != 1 || loaded.(model.Answer).Title != a.Title || loaded.(model.Answer).Body != a.Body {
		t.Fatalf("partial snapshot=%#v, %v", loaded, err)
	}
	requireManagedProjection(t, s, ref, loaded)
	requireManagedAnswerLabel(t, s, "selected", a.ID)
	if _, cached := s.cache.get(partial); cached {
		t.Fatal("partial imported fold entered snapshot cache")
	}
	if _, err := s.Append(t.Context(), ref, []model.Op{model.SetBody{Body: "must reject"}}); !errors.Is(err, fold.ErrKindMismatch) {
		t.Fatalf("append to partial chain=%v; want kind mismatch", err)
	}
	requireManagedProjection(t, s, ref, loaded)
}

func TestManagedAuthorityCancelledReadDoesNotExposeView(t *testing.T) {
	s := initStore(t)
	create(t, s, noteOps("cancel"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if tips, err := s.ManagedTips(ctx, refs.Namespace); !errors.Is(err, context.Canceled) || len(tips) != 0 {
		t.Fatalf("cancelled managed read=%v, %v", tips, err)
	}
}

func TestManagedAuthorityBatchCASAndAuxiliaryRef(t *testing.T) {
	s := initStore(t)
	a := create(t, s, answerOps("batch first")).(model.Answer)
	b := create(t, s, answerOps("batch second")).(model.Answer)
	first, err := s.PrepareAppendAt(t.Context(), refs.For(model.KindAnswer, a.ID), a.Head, []model.Op{model.AddTag{Tag: "batch"}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.PrepareAppendAt(t.Context(), refs.For(model.KindAnswer, b.ID), b.Head, []model.Op{model.AddTag{Tag: "batch"}})
	if err != nil {
		t.Fatal(err)
	}
	wrong := second.RefUpdate()
	wrong.Old = a.Head
	if err := s.PublishRefs(t.Context(), []gitcmd.RefUpdate{first.RefUpdate(), wrong}); !errors.Is(err, gitcmd.ErrCASMismatch) {
		t.Fatalf("batch stale CAS=%v; want mismatch", err)
	}
	requireManagedProjection(t, s, first.Ref, a)
	requireManagedProjection(t, s, second.Ref, b)
	requireManagedAnswerLabel(t, s, "batch")
	auxiliary := "refs/heads/managed-batch-proof"
	updates := []gitcmd.RefUpdate{first.RefUpdate(), second.RefUpdate(), {Ref: auxiliary, New: first.New}}
	if err := s.PublishRefs(t.Context(), updates); err != nil {
		t.Fatal(err)
	}
	requireManagedProjection(t, s, first.Ref, first.Snapshot)
	requireManagedProjection(t, s, second.Ref, second.Snapshot)
	requireManagedAnswerLabel(t, s, "batch", a.ID, b.ID)
	if tip, err := s.Repo.Tip(t.Context(), auxiliary); err != nil || tip != first.New {
		t.Fatalf("batch auxiliary ref=%s, %v; want %s", tip, err, first.New)
	}
	tips, err := s.ManagedTips(t.Context(), refs.Namespace)
	if err != nil || len(tips) != 2 {
		t.Fatalf("batch entity membership=%v, %v; want exactly two entities", tips, err)
	}
}
