package notes

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

func TestSupersessionUsesIndexedEdgesWithoutCorpusBodies(t *testing.T) {
	for _, kind := range []model.Kind{model.KindNote, model.KindDoc, model.KindAnswer} {
		t.Run(string(kind), func(t *testing.T) {
			c, dir := newWBClient(t)
			a, b := createSupersessionPair(t, c.s, kind)
			assertSupersessionPair(t, c, kind, a, b)
			for _, id := range []model.EntityID{a, b} {
				tip, err := c.s.Repo.Tip(t.Context(), refs.For(kind, id))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(c.s.CommonDir(), "objects", string(tip)[:2], string(tip)[2:])); err != nil {
					t.Fatal(err)
				}
			}
			clearClientCache(t, c, "folds-v5")
			fresh, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = fresh.Close() })
			assertSupersessionPair(t, fresh, kind, a, b)
			switch kind {
			case model.KindDoc:
				if _, err := fresh.s.ListDocs(t.Context(), false, true); err == nil {
					t.Fatal("doc corpus still readable")
				}
			case model.KindAnswer:
				if _, err := fresh.s.ListAnswers(t.Context(), false, true); err == nil {
					t.Fatal("answer corpus still readable")
				}
			case model.KindNote:
				if _, err := fresh.s.ListNotes(t.Context(), false, true); err == nil {
					t.Fatal("note corpus still readable")
				}
			}
		})
	}
}

func TestSupersessionCacheMissRetainsAuthoritativeAndPinnedViews(t *testing.T) {
	for _, kind := range []model.Kind{model.KindNote, model.KindDoc, model.KindAnswer} {
		t.Run(string(kind), func(t *testing.T) {
			c, _ := newWBClient(t)
			a, b := createSupersessionPair(t, c.s, kind)
			pins := map[string]model.SHA{}
			for _, id := range []model.EntityID{a, b} {
				tip, err := c.s.Repo.Tip(t.Context(), refs.For(kind, id))
				if err != nil {
					t.Fatal(err)
				}
				pins[refs.For(kind, id)] = tip
			}
			pinned := FromStore(c.s.Pinned(pins))
			if err := os.WriteFile(filepath.Join(c.s.CommonDir(), "cc-notes-query"), []byte("unavailable cache"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := c.s.Append(t.Context(), refs.For(kind, a), []model.Op{model.DeleteNote{}}); err != nil {
				t.Fatal(err)
			}
			assertSupersessionPair(t, pinned, kind, a, b)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			switch kind {
			case model.KindDoc:
				if _, _, err := c.DocSupersession(ctx, a); !errors.Is(err, context.Canceled) {
					t.Fatalf("doc cancellation = %v", err)
				}
			case model.KindAnswer:
				if _, _, err := c.AnswerSupersession(ctx, a); !errors.Is(err, context.Canceled) {
					t.Fatalf("answer cancellation = %v", err)
				}
			case model.KindNote:
				if _, err := c.NoteSuperseders(ctx, a); !errors.Is(err, context.Canceled) {
					t.Fatalf("note cancellation = %v", err)
				}
			}
		})
	}
}

func createSupersessionPair(t *testing.T, s *store.Store, kind model.Kind) (model.EntityID, model.EntityID) {
	t.Helper()
	ids := make([]model.EntityID, 0, 2)
	for _, title := range []string{"a", "b"} {
		var op model.Op = model.CreateNote{Nonce: model.NewNonce(), Title: title}
		switch kind {
		case model.KindDoc:
			op = model.CreateDoc{Nonce: model.NewNonce(), Title: title}
		case model.KindAnswer:
			op = model.CreateAnswer{Nonce: model.NewNonce(), Title: title}
		}
		snapshot, err := s.Create(t.Context(), []model.Op{op})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, snapshot.EntityID())
	}
	if _, err := s.Append(t.Context(), refs.For(kind, ids[0]), []model.Op{model.AddSupersededBy{ID: ids[1]}}); err != nil {
		t.Fatal(err)
	}
	return ids[0], ids[1]
}

func assertSupersessionPair(t *testing.T, c *Client, kind model.Kind, a, b model.EntityID) {
	t.Helper()
	var incoming, heads []model.EntityID
	var err error
	switch kind {
	case model.KindDoc:
		incoming, err = c.DocSuperseders(t.Context(), b)
		if err != nil {
			t.Fatal(err)
		}
		heads, err = c.DocSupersedeHeads(t.Context(), a)
		if err != nil {
			t.Fatal(err)
		}
		gotIncoming, gotHeads, err := c.DocSupersession(t.Context(), a)
		if err != nil || gotIncoming != nil || !slices.Equal(gotHeads, []model.EntityID{b}) {
			t.Fatalf("combined = %v %v %v", gotIncoming, gotHeads, err)
		}
	case model.KindAnswer:
		incoming, err = c.AnswerSuperseders(t.Context(), b)
		if err != nil {
			t.Fatal(err)
		}
		heads, err = c.AnswerSupersedeHeads(t.Context(), a)
		if err != nil {
			t.Fatal(err)
		}
		gotIncoming, gotHeads, err := c.AnswerSupersession(t.Context(), a)
		if err != nil || gotIncoming != nil || !slices.Equal(gotHeads, []model.EntityID{b}) {
			t.Fatalf("combined = %v %v %v", gotIncoming, gotHeads, err)
		}
	default:
		incoming, err = c.NoteSuperseders(t.Context(), b)
		if err != nil {
			t.Fatal(err)
		}
		heads, err = c.NoteSupersedeHeads(t.Context(), a)
		if err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(incoming, []model.EntityID{a}) || !slices.Equal(heads, []model.EntityID{b}) {
		t.Fatalf("%s supersession = %v %v; want [%s] [%s]", kind, incoming, heads, a, b)
	}
}
