package store

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestQueryMetadataSourceRetainsCapturedTips(t *testing.T) {
	s := initStore(t)
	a := create(t, s, answerOps("a")).(model.Answer)
	create(t, s, noteOps("other kind"))
	source, err := captureQueryMetadata(t.Context(), s, model.KindAnswer)
	if err != nil {
		t.Fatal(err)
	}
	oldTip := source.tips[a.ID]
	if len(source.tips) != 1 || oldTip == "" {
		t.Fatalf("captured = %v, want only answer %s", source.tips, a.ID)
	}
	appendOps(t, s, answerRef(a.ID), model.AddTag{Tag: "later"})
	b := create(t, s, answerOps("b")).(model.Answer)
	row, err := source.load(t.Context(), a.ID, oldTip)
	if err != nil {
		t.Fatal(err)
	}
	if len(row.Labels) != 0 || row.ID != a.ID || row.Tip != oldTip || row.Binding != source.binding {
		t.Fatalf("old captured row = %+v", row)
	}
	current, err := captureQueryMetadata(t.Context(), s, model.KindAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if len(current.tips) != 2 || current.tips[a.ID] == oldTip || current.tips[b.ID] == "" {
		t.Fatalf("current = %v, old = %v", current.tips, source.tips)
	}
	if current.binding != source.binding {
		t.Fatalf("source advancement changed schema binding: %q != %q", current.binding, source.binding)
	}
	if _, err := source.load(t.Context(), a.ID, current.tips[a.ID]); err == nil {
		t.Fatal("accepted tip outside captured source")
	}
}

func TestQueryMetadataSourcePreservesPinnedUniverse(t *testing.T) {
	s := initStore(t)
	a := create(t, s, answerOps("a")).(model.Answer)
	tip, err := s.Repo.Tip(t.Context(), answerRef(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	pins := map[string]model.SHA{answerRef(a.ID): tip}
	pinned := s.Pinned(pins)
	appendOps(t, s, answerRef(a.ID), model.AddTag{Tag: "later"})
	create(t, s, answerOps("b"))
	source, err := captureQueryMetadata(t.Context(), pinned, model.KindAnswer)
	if err != nil {
		t.Fatal(err)
	}
	clear(pins)
	if !maps.Equal(source.tips, map[model.EntityID]model.SHA{a.ID: tip}) {
		t.Fatalf("pinned source = %v, want original %s at %s", source.tips, a.ID, tip)
	}
	row, err := source.load(t.Context(), a.ID, tip)
	if err != nil {
		t.Fatal(err)
	}
	if len(row.Labels) != 0 {
		t.Fatalf("pinned labels = %v, want old empty labels", row.Labels)
	}
	empty, err := captureQueryMetadata(t.Context(), s.Pinned(map[string]model.SHA{}), model.KindAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.tips) != 0 || empty.store.pins == nil {
		t.Fatalf("empty pinned source = %+v", empty)
	}
}

func TestQueryMetadataSourceUsesRecordsBackend(t *testing.T) {
	f := newSharedFixture(t)
	f.bind(t)
	backend, err := Open(f.source)
	if err != nil {
		t.Fatal(err)
	}
	a := create(t, backend, answerOps("backend")).(model.Answer)
	bound, err := Open(f.worktreeA)
	if err != nil {
		t.Fatal(err)
	}
	source, err := captureQueryMetadata(t.Context(), bound, model.KindAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if len(source.tips) != 1 || source.tips[a.ID] == "" || source.store.Repo == source.store.ContextRepo {
		t.Fatalf("bound source tips = %v, records/context equal = %v", source.tips, source.store.Repo == source.store.ContextRepo)
	}
	row, err := source.load(t.Context(), a.ID, source.tips[a.ID])
	if err != nil {
		t.Fatal(err)
	}
	if row.ID != a.ID {
		t.Fatalf("backend row ID = %s, want %s", row.ID, a.ID)
	}
	local, err := captureQueryMetadata(t.Context(), backend, model.KindAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if source.binding != local.binding {
		t.Fatalf("same backend bindings differ: %q != %q", source.binding, local.binding)
	}
	replaceRepo(t, f.source)
	if _, err := source.load(t.Context(), a.ID, source.tips[a.ID]); err == nil {
		t.Fatal("cached load accepted replaced backend")
	}
	if _, err := captureQueryMetadata(t.Context(), bound, model.KindAnswer); err == nil {
		t.Fatal("capture accepted replaced backend")
	}
}

func TestQueryMetadataSourceCancellationAndUnreadableTip(t *testing.T) {
	s := initStore(t)
	a := create(t, s, answerOps("a")).(model.Answer)
	source, err := captureQueryMetadata(t.Context(), s, model.KindAnswer)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := captureQueryMetadata(ctx, s, model.KindAnswer); !errors.Is(err, context.Canceled) {
		t.Fatalf("capture cancellation = %v", err)
	}
	if _, err := source.load(ctx, a.ID, source.tips[a.ID]); !errors.Is(err, context.Canceled) {
		t.Fatalf("cached load cancellation = %v", err)
	}
	missing := model.SHA("1111111111111111111111111111111111111111")
	bad, err := captureQueryMetadata(t.Context(), s.Pinned(map[string]model.SHA{answerRef(a.ID): missing}), model.KindAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bad.load(t.Context(), a.ID, missing); err == nil {
		t.Fatal("accepted unreadable captured tip")
	}
	wrong, err := captureQueryMetadata(t.Context(), s.Pinned(map[string]model.SHA{refs.For(model.KindNote, a.ID): source.tips[a.ID]}), model.KindNote)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.load(t.Context(), a.ID, source.tips[a.ID]); err == nil {
		t.Fatal("accepted wrong captured kind")
	}
}

func TestQueryMetadataSourcePreservesPartialFolds(t *testing.T) {
	s := initStore(t)
	a := create(t, s, answerOps("a")).(model.Answer)
	tip, err := s.Repo.Tip(t.Context(), answerRef(a.ID))
	if err != nil {
		t.Fatal(err)
	}
	sig, _, err := s.signature(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	pack := model.Pack{Lamport: 2, Session: session(), Ops: []model.Op{model.SetStatus{Status: model.StatusDone}}}
	partial, err := s.Repo.WriteOpsCommit(t.Context(), []model.SHA{tip}, sig, "cc-notes: set_status", pack)
	if err != nil {
		t.Fatal(err)
	}
	source, err := captureQueryMetadata(t.Context(), s.Pinned(map[string]model.SHA{answerRef(a.ID): partial}), model.KindAnswer)
	if err != nil {
		t.Fatal(err)
	}
	var first queryMetadata
	for i := range 2 {
		row, err := source.load(t.Context(), a.ID, partial)
		if err != nil {
			t.Fatal(err)
		}
		if row.SkippedOps != 1 || row.Tip != partial || row.ID != a.ID {
			t.Fatalf("partial row = %+v", row)
		}
		if i == 0 {
			first = row
		} else if !reflect.DeepEqual(row, first) {
			t.Fatalf("partial refold = %+v, want %+v", row, first)
		}
	}
}
