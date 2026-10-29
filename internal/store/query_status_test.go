package store

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestStatusMetadataSourcesRetainOneUniverse(t *testing.T) {
	s := initStore(t)
	n := create(t, s, noteOps("note")).(model.Note)
	a := create(t, s, answerOps("answer")).(model.Answer)
	pinned, sources, err := captureStatusMetadataSources(t.Context(), s, model.KindNote, model.KindAnswer)
	if err != nil {
		t.Fatal(err)
	}
	original := maps.Clone(pinned.pins)
	appendOps(t, s, refs.Root(model.KindNote)+string(n.ID), model.AddTag{Tag: "later"})
	create(t, s, answerOps("later answer"))
	if len(sources) != 2 || !maps.Equal(pinned.pins, original) {
		t.Fatalf("capture changed: sources=%d pins=%v", len(sources), pinned.pins)
	}
	for i, id := range []model.EntityID{n.ID, a.ID} {
		source := sources[i]
		if source.store != pinned || len(source.tips) != 1 || source.tips[id] == "" || source.pinned {
			t.Fatalf("kind source %d = %+v", i, source)
		}
		row, err := source.load(t.Context(), id, source.tips[id])
		if err != nil {
			t.Fatal(err)
		}
		if len(row.Labels) != 0 || row.ID != id {
			t.Fatalf("captured row = %#v", row)
		}
	}
}

func TestStatusMetadataReturnsExactTipsAndBodySource(t *testing.T) {
	s := initStore(t)
	n := create(t, s, noteOps("note")).(model.Note)
	a := create(t, s, answerOps("answer")).(model.Answer)
	view, hit, err := s.StatusMetadata(t.Context(), model.KindNote, model.KindAnswer)
	if err != nil {
		t.Fatal(err)
	}
	if !hit || view.Captured == nil || len(view.Records) != 2 {
		t.Fatalf("view=%+v hit=%t", view, hit)
	}
	for i, id := range []model.EntityID{n.ID, a.ID} {
		row := view.Records[i]
		if row.ID != id || row.Tip == "" || row.Version != queryMetadataVersion {
			t.Fatalf("row %d = %#v", i, row)
		}
		snapshot, err := view.Captured.LoadAt(t.Context(), row.Tip)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.EntityID() != id || snapshot.Meta().Kind != row.Kind {
			t.Fatalf("body differs from metadata: %#v %#v", snapshot, row)
		}
	}
	appendOps(t, s, refs.Root(model.KindNote)+string(n.ID), model.AddTag{Tag: "later"})
	old, err := view.Captured.LoadAt(t.Context(), view.Records[0].Tip)
	if err != nil {
		t.Fatal(err)
	}
	if len(old.Meta().Labels) != 0 {
		t.Fatalf("captured body moved to live: %#v", old)
	}
}

func TestStatusMetadataPreservesOldAndEmptyPinsOnMiss(t *testing.T) {
	s := initStore(t)
	n := create(t, s, noteOps("note")).(model.Note)
	old, hit, err := s.StatusMetadata(t.Context(), model.KindNote)
	if err != nil || !hit {
		t.Fatalf("initial hit=%t err=%v", hit, err)
	}
	appendOps(t, s, refs.Root(model.KindNote)+string(n.ID), model.AddTag{Tag: "later"})
	if _, hit, err := s.StatusMetadata(t.Context(), model.KindNote); err != nil || !hit {
		t.Fatalf("live refresh hit=%t err=%v", hit, err)
	}
	view, hit, err := old.Captured.StatusMetadata(t.Context(), model.KindNote)
	if err != nil || hit || len(view.Records) != 0 || view.Captured == nil || !maps.Equal(view.Captured.pins, old.Captured.pins) {
		t.Fatalf("old view=%+v hit=%t err=%v", view, hit, err)
	}
	view, hit, err = s.Pinned(map[string]model.SHA{}).StatusMetadata(t.Context(), model.KindNote)
	if err != nil || hit || len(view.Records) != 0 || view.Captured == nil || view.Captured.pins == nil || len(view.Captured.pins) != 0 {
		t.Fatalf("empty view=%+v hit=%t err=%v", view, hit, err)
	}
}

func TestStatusMetadataCancellationDoesNotReturnView(t *testing.T) {
	s := initStore(t)
	create(t, s, noteOps("note"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	view, hit, err := s.StatusMetadata(ctx, model.KindNote)
	if !errors.Is(err, context.Canceled) || hit || view.Captured != nil || len(view.Records) != 0 {
		t.Fatalf("cancelled view=%+v hit=%t err=%v", view, hit, err)
	}
}
