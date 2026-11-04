package store

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
	"github.com/yasyf/cc-notes/model"
)

func TestRetainedQueryPostingsPreserveCapturedGeneration(t *testing.T) {
	s := initStore(t)
	a := create(t, s, docOps("a")).EntityID()
	b := create(t, s, docOps("b")).EntityID()
	appendOps(t, s, refs.For(model.KindDoc, a), model.AddTag{Tag: "before"}, model.AddSupersededBy{ID: b})
	old, err := s.PinnedKinds(t.Context(), model.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, hit, err := old.IndexedSupersession(t.Context(), model.KindDoc, b); err != nil || !hit {
		t.Fatalf("initial pinned = %v %v", hit, err)
	}
	if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, b); err != nil || !hit {
		t.Fatalf("settle active = %v %v", hit, err)
	}
	appendOps(t, s, refs.For(model.KindDoc, a), model.DeleteNote{})
	incoming, _, hit, err := old.IndexedSupersession(t.Context(), model.KindDoc, b)
	if err != nil || !hit || !slices.Equal(incoming, []model.EntityID{a}) {
		t.Fatalf("retained incoming = %v %v %v", incoming, hit, err)
	}
	ids, hit, err := indexedMetadataQuery(t.Context(), old, model.KindDoc, func(tx *bolt.Tx, source queryMetadataSource) ([]model.EntityID, error) {
		var ids []model.EntityID
		err := source.walk(tx, queryMetadataLabelsBucket, queryMetadataLabelPrefix(model.KindDoc, "before"), false, func(row queryMetadata) bool { return slices.Contains(row.Labels, "before") }, func(row queryMetadata) error { ids = append(ids, row.ID); return nil })
		return ids, err
	})
	if err != nil || !hit || !slices.Equal(ids, []model.EntityID{a}) {
		t.Fatalf("retained labels = %v %v %v", ids, hit, err)
	}
	incoming, _, hit, err = s.IndexedSupersession(t.Context(), model.KindDoc, b)
	if err != nil || !hit || len(incoming) != 0 {
		t.Fatalf("current incoming = %v %v %v", incoming, hit, err)
	}
	if _, _, hit, err := old.IndexedSupersession(t.Context(), model.KindDoc, b); err != nil || hit {
		t.Fatalf("retired pin = %v %v", hit, err)
	}
}

func TestRetainedQueryActivationRollsBackAndIsolatesKinds(t *testing.T) {
	s := initStore(t)
	a := create(t, s, docOps("a")).EntityID()
	b := create(t, s, docOps("b")).EntityID()
	old, err := s.PinnedKinds(t.Context(), model.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit {
		t.Fatalf("initial = %v %v", hit, err)
	}
	appendOps(t, s, refs.For(model.KindDoc, a), model.AddSupersededBy{ID: b})
	failed := errors.New("read failed")
	if _, hit, err := indexedMetadataQuery(t.Context(), s, model.KindDoc, func(*bolt.Tx, queryMetadataSource) (bool, error) { return true, failed }); !errors.Is(err, failed) || hit {
		t.Fatalf("failure = %v %v", hit, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	if _, hit, err := indexedMetadataQuery(ctx, s, model.KindDoc, func(*bolt.Tx, queryMetadataSource) (bool, error) { cancel(); return true, nil }); !errors.Is(err, context.Canceled) || hit {
		t.Fatalf("cancellation = %v %v", hit, err)
	}
	create(t, s, answerOps("unrelated kind"))
	if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindAnswer, model.EntityID("missing")); err != nil || !hit {
		t.Fatalf("other kind = %v %v", hit, err)
	}
	if _, heads, hit, err := old.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || heads != nil {
		t.Fatalf("preserved = %v %v %v", heads, hit, err)
	}
}

func TestRetainedQueryGraphUsesCanonicalSourceMap(t *testing.T) {
	s := initStore(t)
	create(t, s, docOps("a"))
	source, err := queryMetadataIdentity(t.Context(), s, model.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	var generation queryGeneration
	var mutations []sourcegraph.Mutation
	err = s.managedView(t.Context(), func(tx *bolt.Tx) error {
		var err error
		generation, err = readQueryGeneration(queryGenerationBinding(tx, source.binding), queryCurrentRoot)
		if err != nil {
			return err
		}
		rows := queryMetadataBinding(tx, source.binding).Bucket(queryMetadataRowsBucket)
		return rows.ForEach(func(_, data []byte) error {
			row, err := decodeQueryMetadata(tx, source, data)
			if err != nil {
				return err
			}
			mutations = append(mutations, sourcegraph.Mutation{Key: queryRowKey(row.ID), Value: slices.Clone(data)})
			mutations = append(mutations, queryRowPostings(row)...)
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Repo.SourceObjects(t.Context(), func(objects *gitobj.SourceObjects) error {
		root, err := sourcegraph.New(objects).Apply("", mutations)
		if err != nil {
			return err
		}
		if root != generation.Root {
			t.Fatalf("Bolt root %s differs from Git root %s", generation.Root, root)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRetainedMetadataMissingStorageRejectsBeforePublication(t *testing.T) {
	s := initStore(t)
	id := create(t, s, docOps("a")).EntityID()
	ref := refs.For(model.KindDoc, id)
	before, err := s.Repo.Tip(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	db, err := s.openManaged(false)
	if err != nil {
		t.Fatal(err)
	}
	err = db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket(queryGenerationsBucket) })
	err = errors.Join(err, db.Close())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(t.Context(), ref, []model.Op{model.AddTag{Tag: "rejected"}}); !errors.Is(err, managed.ErrReindex) {
		t.Fatalf("append = %v", err)
	}
	after, err := s.Repo.Tip(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("ref moved from %s to %s", before, after)
	}
	if _, err := s.CreateExact(t.Context(), docOps("rejected")); !errors.Is(err, managed.ErrReindex) {
		t.Fatalf("create = %v", err)
	}
}

func TestRetainedMetadataSurvivesExplicitReindexUntilLiveQuery(t *testing.T) {
	s := initStore(t)
	a := create(t, s, docOps("a")).EntityID()
	b := create(t, s, docOps("b")).EntityID()
	old, err := s.PinnedKinds(t.Context(), model.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit {
		t.Fatalf("initial = %v %v", hit, err)
	}
	appendOps(t, s, refs.For(model.KindDoc, a), model.AddSupersededBy{ID: b})
	if err := s.Reindex(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, heads, hit, err := old.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || heads != nil {
		t.Fatalf("after reindex = %v %v %v", heads, hit, err)
	}
	if _, heads, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || !hit || !slices.Equal(heads, []model.EntityID{b}) {
		t.Fatalf("current = %v %v %v", heads, hit, err)
	}
	if _, _, hit, err := old.IndexedSupersession(t.Context(), model.KindDoc, a); err != nil || hit {
		t.Fatalf("retired = %v %v", hit, err)
	}
}

func TestRetainedMetadataReclaimsRetiredAndTransientObjects(t *testing.T) {
	s := initStore(t)
	id := create(t, s, docOps("a")).EntityID()
	if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, id); err != nil || !hit {
		t.Fatalf("initial = %v %v", hit, err)
	}
	count := func() int {
		t.Helper()
		total := 0
		err := s.managedView(t.Context(), func(tx *bolt.Tx) error {
			root := tx.Bucket(queryGenerationsBucket)
			objects := queryObjectStoreAt(root)
			if objects.objects.Stats().KeyN != objects.ids.Stats().KeyN || objects.ids.Stats().KeyN != root.Bucket([]byte(managed.QueryObjectRefs)).Stats().KeyN {
				t.Fatal("object, identity, and reference indexes differ in cardinality")
			}
			return objects.ids.ForEach(func(hash, id []byte) error {
				if objects.objects.Get(id) == nil {
					t.Fatalf("missing object data %s", hash)
				}
				total++
				references, err := queryObjectRefCount(root, model.SHA(hex.EncodeToString(hash)))
				if err != nil {
					return err
				}
				if references == 0 {
					t.Fatalf("unreachable immutable object %s", hash)
				}
				return nil
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		return total
	}
	before := count()
	for i := range 12 {
		appendOps(t, s, refs.For(model.KindDoc, id), model.SetTitle{Title: fmt.Sprintf("title %d", i)})
		if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, id); err != nil || !hit {
			t.Fatalf("current = %v %v", hit, err)
		}
	}
	if after := count(); after > before+32 {
		t.Fatalf("retired graphs grew object count from %d to %d", before, after)
	}
}

func TestQueryGenerationBatchMatchesPerRowReplacement(t *testing.T) {
	rows := make([]queryMetadata, 0, 512)
	for i := range 256 {
		id := model.EntityID(fmt.Sprintf("doc-%03d", i))
		binding := fmt.Sprintf("binding-%d", i%3)
		rows = append(rows, projectQueryMetadata(binding, "first", model.Doc{ID: id, Tags: []string{"shared", string(id)}}))
	}
	for i := range 128 {
		id := model.EntityID(fmt.Sprintf("doc-%03d", i))
		binding := fmt.Sprintf("binding-%d", i%3)
		rows = append(rows, projectQueryMetadata(binding, "second", model.Doc{ID: id, Tags: []string{"changed"}}))
	}
	removed := model.EntityID("doc-200")
	dump := func(db *bolt.DB) map[string]string {
		t.Helper()
		state := make(map[string]string)
		if err := db.View(func(tx *bolt.Tx) error {
			root := tx.Bucket(queryGenerationsBucket)
			objects := queryObjectStoreAt(root)
			if err := objects.ids.ForEach(func(key, id []byte) error {
				hash := model.SHA(hex.EncodeToString(key))
				state[managed.QueryObjects+"/"+string(hash)] = string(objects.objects.Get(id))
				count, err := queryObjectRefCount(root, hash)
				if err != nil {
					return err
				}
				state[managed.QueryObjectRefs+"/"+string(hash)] = string(binary.BigEndian.AppendUint64(nil, count))
				return nil
			}); err != nil {
				return err
			}
			for i := range 3 {
				binding := fmt.Sprintf("binding-%d", i)
				state[binding] = string(root.Bucket(queryMetadataAtom(binding)).Get(queryCurrentRoot))
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		return state
	}
	single := newQueryMetadataDB(t)
	for _, row := range rows {
		if err := single.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(t.Context(), tx, row) }); err != nil {
			t.Fatal(err)
		}
	}
	if err := single.Update(func(tx *bolt.Tx) error { return deleteQueryMetadata(t.Context(), tx, "binding-2", removed) }); err != nil {
		t.Fatal(err)
	}
	batched := newQueryMetadataDB(t)
	if err := batched.Update(func(tx *bolt.Tx) error {
		batch := make(queryGenerationBatch)
		for _, row := range rows {
			if err := batch.replaceMetadata(tx, row); err != nil {
				return err
			}
		}
		if err := batch.deleteMetadata(tx, "binding-2", removed); err != nil {
			return err
		}
		return batch.flush(t.Context(), tx)
	}); err != nil {
		t.Fatal(err)
	}
	if want, got := dump(single), dump(batched); !maps.Equal(want, got) {
		t.Fatalf("batched generation state differs: %d keys per row, %d keys batched", len(want), len(got))
	}
}
