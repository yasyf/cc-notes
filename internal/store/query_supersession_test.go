package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func TestIndexedSupersessionGraphSemantics(t *testing.T) {
	for _, kind := range []model.Kind{model.KindNote, model.KindDoc, model.KindAnswer} {
		t.Run(string(kind), func(t *testing.T) {
			s := initStore(t)
			ids := make(map[string]model.EntityID)
			for _, name := range []string{"a", "b", "c", "d", "e", "f", "deleted", "incoming", "self"} {
				ops := noteOps(name)
				switch kind {
				case model.KindDoc:
					ops = docOps(name)
				case model.KindAnswer:
					ops = []model.Op{model.CreateAnswer{Nonce: model.NewNonce(), Title: name}}
				}
				ids[name] = create(t, s, ops).EntityID()
			}
			missing := model.EntityID("1111111111111111111111111111111111111111")
			for _, edge := range [][2]string{{"a", "b"}, {"a", "c"}, {"b", "d"}, {"c", "d"}, {"c", "e"}, {"e", "f"}, {"f", "e"}, {"deleted", "a"}, {"incoming", "a"}, {"self", "self"}, {"a", "self"}, {"a", "deleted"}} {
				appendOps(t, s, refs.For(kind, ids[edge[0]]), model.AddSupersededBy{ID: ids[edge[1]]})
			}
			appendOps(t, s, refs.For(kind, ids["a"]), model.AddSupersededBy{ID: missing})
			appendOps(t, s, refs.For(kind, ids["deleted"]), model.DeleteNote{})
			cases := []struct {
				id       model.EntityID
				incoming []model.EntityID
				heads    []model.EntityID
			}{
				{ids["a"], []model.EntityID{ids["incoming"]}, []model.EntityID{ids["d"]}},
				{ids["b"], []model.EntityID{ids["a"]}, []model.EntityID{ids["d"]}},
				{ids["d"], []model.EntityID{ids["b"], ids["c"]}, nil},
				{ids["deleted"], []model.EntityID{ids["a"]}, nil},
				{ids["self"], []model.EntityID{ids["a"], ids["self"]}, nil},
				{ids["incoming"], nil, []model.EntityID{ids["d"]}},
				{missing, []model.EntityID{ids["a"]}, nil},
			}
			for _, tc := range cases {
				slices.Sort(tc.incoming)
				incoming, heads, hit, err := s.IndexedSupersession(t.Context(), kind, tc.id)
				if err != nil || !hit || !slices.Equal(incoming, tc.incoming) || !slices.Equal(heads, tc.heads) || (incoming == nil) != (tc.incoming == nil) || (heads == nil) != (tc.heads == nil) {
					t.Fatalf("%s = %v, %v, %v, %v; want %v, %v", tc.id, incoming, heads, hit, err, tc.incoming, tc.heads)
				}
				if got, hit, err := s.IndexedSuperseders(t.Context(), kind, tc.id); err != nil || !hit || !slices.Equal(got, tc.incoming) {
					t.Fatalf("incoming %s = %v %v %v", tc.id, got, hit, err)
				}
				if got, hit, err := s.IndexedSupersedeHeads(t.Context(), kind, tc.id); err != nil || !hit || !slices.Equal(got, tc.heads) {
					t.Fatalf("heads %s = %v %v %v", tc.id, got, hit, err)
				}
			}
		})
	}
}

func TestIndexedSupersessionAvoidsUnchangedCorpusBodies(t *testing.T) {
	for _, count := range []int{8, 80} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s := initStore(t)
			a := create(t, s, docOps("a")).EntityID()
			b := create(t, s, docOps("b")).EntityID()
			appendOps(t, s, refs.For(model.KindDoc, a), model.AddSupersededBy{ID: b})
			var unrelated model.EntityID
			for i := range count {
				unrelated = create(t, s, docOps(fmt.Sprintf("unrelated %d", i))).EntityID()
			}
			if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit {
				t.Fatalf("warm = %v %v", hit, err)
			}
			source, err := captureQueryMetadata(t.Context(), s, model.KindDoc)
			if err != nil {
				t.Fatal(err)
			}
			db, err := bolt.Open(source.indexPath(), 0o600, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.Update(func(tx *bolt.Tx) error {
				return queryMetadataBinding(tx, source.binding).Bucket(queryMetadataRowsBucket).Put([]byte(unrelated), []byte("not JSON"))
			}); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			for _, tip := range source.tips {
				if err := os.Remove(filepath.Join(s.recordsCommonDir, "objects", string(tip)[:2], string(tip)[2:])); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.RemoveAll(s.cache.dir); err != nil {
				t.Fatal(err)
			}
			fresh, err := Open(s.Git.Dir)
			if err != nil {
				t.Fatal(err)
			}
			if incoming, heads, hit, err := fresh.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || incoming != nil || !slices.Equal(heads, []model.EntityID{b}) {
				t.Fatalf("unchanged with unreadable bodies = %v %v %v %v", incoming, heads, hit, err)
			}
			if _, err := fresh.ListDocs(t.Context(), false, true); err == nil {
				t.Fatal("authoritative corpus unexpectedly readable after removing its commits")
			}
			if _, err := fresh.CreateExact(t.Context(), docOps("one new tip")); err != nil {
				t.Fatal(err)
			}
			if incoming, heads, hit, err := fresh.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || incoming != nil || !slices.Equal(heads, []model.EntityID{b}) {
				t.Fatalf("one new tip with unreadable unchanged bodies = %v %v %v %v", incoming, heads, hit, err)
			}
		})
	}
}

func TestIndexedSupersessionPinnedAndCancellation(t *testing.T) {
	s := initStore(t)
	a := create(t, s, docOps("a")).EntityID()
	b := create(t, s, docOps("b")).EntityID()
	source, err := captureQueryMetadata(t.Context(), s, model.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit {
		t.Fatalf("initial = %v %v", hit, err)
	}
	old := s.Pinned(source.store.pins)
	appendOps(t, s, refs.For(model.KindDoc, a), model.AddSupersededBy{ID: b})
	if _, heads, hit, err := old.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || heads != nil {
		t.Fatalf("exact old pin = %v %v %v", heads, hit, err)
	}
	if _, heads, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || !slices.Equal(heads, []model.EntityID{b}) {
		t.Fatalf("live = %v %v %v", heads, hit, err)
	}
	if _, _, hit, err := old.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || hit {
		t.Fatalf("old mismatched pin = %v %v", hit, err)
	}
	if _, _, hit, err := s.Pinned(map[string]model.SHA{}).IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || hit {
		t.Fatalf("empty mismatched pin = %v %v", hit, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, hit, err := s.IndexedSupersession(ctx, model.KindDoc, a); !errors.Is(err, context.Canceled) || hit {
		t.Fatalf("canceled = %v %v", hit, err)
	}
	ctx, cancel = context.WithCancel(t.Context())
	value, hit, err := indexedMetadataQuery(ctx, s, model.KindDoc, func(*bolt.Tx, queryMetadataSource) ([]model.EntityID, error) {
		cancel()
		return []model.EntityID{a}, nil
	})
	if value != nil || hit || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled result = %v %v %v", value, hit, err)
	}
}
