package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
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

func TestRetainedMetadataPrunesVersionsNoGenerationSees(t *testing.T) {
	s := initStore(t)
	id := create(t, s, docOps("a")).EntityID()
	if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, id); err != nil || !hit {
		t.Fatalf("initial = %v %v", hit, err)
	}
	source, err := queryMetadataIdentity(t.Context(), s, model.KindDoc)
	if err != nil {
		t.Fatal(err)
	}
	count := func() (live, dead, deaths int, keys map[string]bool) {
		t.Helper()
		keys = make(map[string]bool)
		err := s.managedView(t.Context(), func(tx *bolt.Tx) error {
			binding := queryGenerationBinding(tx, source.binding)
			deaths = binding.Bucket(queryDeathsBucket).Stats().KeyN
			return binding.Bucket(queryVersionsBucket).ForEach(func(key, data []byte) error {
				versions, err := decodeQueryVersions(data)
				for _, version := range versions {
					if version.died == 0 {
						live++
					} else {
						dead++
					}
				}
				keys[string(key)] = true
				return err
			})
		})
		if err != nil {
			t.Fatal(err)
		}
		return live, dead, deaths, keys
	}
	beforeLive, _, _, _ := count()
	ref := refs.For(model.KindDoc, id)
	for i := range 12 {
		appendOps(t, s, ref, model.SetTitle{Title: fmt.Sprintf("title %d", i)})
		if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, id); err != nil || !hit {
			t.Fatalf("current = %v %v", hit, err)
		}
	}
	live, dead, deaths, _ := count()
	if live != beforeLive || dead != deaths || dead > 8 {
		t.Fatalf("after 12 writes: %d live versions (was %d), %d dead, %d deaths", live, beforeLive, dead, deaths)
	}
	tag := queryPostingKey(queryMetadataLabelsBucket, queryMetadataLabelPrefix(model.KindDoc, "transient"))
	appendOps(t, s, ref, model.AddTag{Tag: "transient"})
	appendOps(t, s, ref, model.RemoveTag{Tag: "transient"})
	if _, _, hit, err := s.IndexedSupersession(t.Context(), model.KindDoc, id); err != nil || !hit {
		t.Fatalf("after tag = %v %v", hit, err)
	}
	appendOps(t, s, ref, model.SetTitle{Title: "settled"})
	if _, _, _, keys := count(); slices.ContainsFunc(slices.Collect(maps.Keys(keys)), func(key string) bool { return bytes.HasPrefix([]byte(key), []byte(tag)) }) {
		t.Fatal("a removed posting outlived every generation that could see it")
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
			for i := range 3 {
				name := fmt.Sprintf("binding-%d", i)
				binding := queryGenerationBinding(tx, name)
				current, err := readQueryGeneration(binding, queryCurrentRoot)
				if err != nil {
					return err
				}
				state[name] = fmt.Sprint(current.Rows)
				retained := &retainedQuery{versions: binding.Bucket(queryVersionsBucket), generation: current}
				if err := retained.each(func(key, value []byte) error {
					state[name+"/"+string(key)] = string(value)
					return nil
				}); err != nil {
					return err
				}
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

func TestQueryActivationLeavesPruningToWriter(t *testing.T) {
	db := newQueryMetadataDB(t)
	rows := make([]queryMetadata, 256)
	if err := db.Update(func(tx *bolt.Tx) error {
		batch := make(queryGenerationBatch)
		for i := range rows {
			rows[i] = projectQueryMetadata("binding", "before", model.Doc{ID: model.EntityID(fmt.Sprintf("doc-%03d", i)), Title: "before", Tags: []string{"stable"}})
			if err := batch.replaceMetadata(tx, rows[i]); err != nil {
				return err
			}
		}
		return batch.flush(t.Context(), tx)
	}); err != nil {
		t.Fatal(err)
	}
	promote := func() {
		t.Helper()
		if err := db.Update(func(tx *bolt.Tx) error {
			binding := queryGenerationBinding(tx, "binding")
			return binding.Put(queryActiveRoot, bytes.Clone(binding.Get(queryCurrentRoot)))
		}); err != nil {
			t.Fatal(err)
		}
	}
	type state struct {
		current, active queryGeneration
		versionsPage    uint64
		deaths          int
	}
	read := func() state {
		t.Helper()
		var got state
		if err := db.View(func(tx *bolt.Tx) error {
			binding := queryGenerationBinding(tx, "binding")
			var err error
			if got.current, err = readQueryGeneration(binding, queryCurrentRoot); err != nil {
				return err
			}
			if got.active, err = readQueryGeneration(binding, queryActiveRoot); err != nil {
				return err
			}
			got.versionsPage = uint64(binding.Bucket(queryVersionsBucket).Root())
			got.deaths = binding.Bucket(queryDeathsBucket).Stats().KeyN
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if got.versionsPage == 0 {
			t.Fatal("fixture requires a non-inline versions bucket")
		}
		return got
	}
	visible := func(generation queryGeneration, id model.EntityID) queryMetadata {
		t.Helper()
		var row queryMetadata
		if err := db.View(func(tx *bolt.Tx) error {
			retained := &retainedQuery{versions: queryGenerationBinding(tx, "binding").Bucket(queryVersionsBucket), generation: generation}
			data, err := retained.get(queryRowKey(id))
			if err != nil || data == nil {
				return errors.Join(err, fmt.Errorf("no version of %s at %d", id, generation.Number))
			}
			return json.Unmarshal(data, &row)
		}); err != nil {
			t.Fatal(err)
		}
		return row
	}
	promote()
	first := read().current
	rows[0].Tip = "first change"
	if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(t.Context(), tx, rows[0]) }); err != nil {
		t.Fatal(err)
	}
	written := read()
	promote()
	promoted := read()
	if promoted.active != written.current || promoted.versionsPage != written.versionsPage || promoted.deaths != 1 {
		t.Fatalf("promotion = %+v after %+v", promoted, written)
	}
	if row := visible(first, rows[0].ID); row.Tip != "before" {
		t.Fatalf("promotion pruned the version the retired generation saw: %q", row.Tip)
	}
	rows[1].Tip = "second change"
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(ctx, tx, rows[1]) }); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled writer = %v", err)
	}
	if got := read(); got != promoted {
		t.Fatalf("canceled writer changed %+v to %+v", promoted, got)
	}
	if err := db.Update(func(tx *bolt.Tx) error { return replaceQueryMetadata(t.Context(), tx, rows[1]) }); err != nil {
		t.Fatal(err)
	}
	if got := read(); got.deaths != 1 {
		t.Fatalf("writer left %d deaths, want only its own", got.deaths)
	}
	if row := visible(promoted.active, rows[1].ID); row.Tip != "before" {
		t.Fatalf("writer pruned a version the active generation sees: %q", row.Tip)
	}
	if row := visible(promoted.active, rows[0].ID); row.Tip != "first change" {
		t.Fatalf("active generation reads %q", row.Tip)
	}
}
