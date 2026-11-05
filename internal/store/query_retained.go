package store

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
	"github.com/yasyf/cc-notes/model"
)

var (
	queryGenerationsBucket = []byte(managed.QueryGenerations)
	queryVersionsBucket    = []byte("versions")
	queryDeathsBucket      = []byte("deaths")
	queryCurrentRoot       = []byte("current")
	queryActiveRoot        = []byte("active")
)

type queryGeneration struct {
	Number uint64
	Rows   uint64
}

type retainedQuery struct {
	versions   *bolt.Bucket
	generation queryGeneration
}

func queryGenerationBinding(tx *bolt.Tx, binding string) *bolt.Bucket {
	root := tx.Bucket(queryGenerationsBucket)
	if root == nil {
		return nil
	}
	return root.Bucket(queryMetadataAtom(binding))
}

func readQueryGeneration(bucket *bolt.Bucket, key []byte) (queryGeneration, error) {
	var generation queryGeneration
	if bucket == nil {
		return generation, managed.ErrReindex
	}
	data := bucket.Get(key)
	if data == nil {
		return generation, nil
	}
	return generation, json.Unmarshal(data, &generation)
}

func queryRowKey(id model.EntityID) string     { return "rows/" + string(id) }
func queryPostingKey(index, key []byte) string { return string(index) + "/" + string(key) }
func queryRowPostings(row queryMetadata) []sourcegraph.Mutation {
	var entries []sourcegraph.Mutation
	add := func(index, prefix []byte) {
		entries = append(entries, sourcegraph.Mutation{Key: queryPostingKey(index, queryMetadataPostingKey(prefix, row)), Value: []byte(row.ID)})
	}
	for _, label := range row.Labels {
		add(queryMetadataLabelsBucket, queryMetadataLabelPrefix(row.Kind, label))
	}
	for _, anchor := range row.Anchors {
		add(queryMetadataAnchorsBucket, queryMetadataAnchorPrefix(row.Kind, anchor))
	}
	for _, target := range row.SupersededBy {
		add(queryMetadataSupersedersBucket, queryMetadataSupersedersPrefix(row.Kind, target))
	}
	if row.Kind == model.KindTask {
		for _, target := range row.BlockedBy {
			add(queryMetadataBlockedByBucket, queryMetadataSupersedersPrefix(row.Kind, target))
		}
		add(queryMetadataParentsBucket, queryMetadataSupersedersPrefix(row.Kind, row.Parent))
	}
	for _, target := range row.RunTasks {
		add(queryMetadataRunTasksBucket, queryMetadataSupersedersPrefix(row.Kind, target))
	}
	add(queryMetadataDuplicatesBucket, queryMetadataLabelPrefix(row.Kind, row.DuplicateContent))
	return entries
}

func queryPostingChanges(previous, next []sourcegraph.Mutation) []sourcegraph.Mutation {
	before := make(map[string]sourcegraph.Mutation, len(previous))
	after := make(map[string]sourcegraph.Mutation, len(next))
	for _, entry := range previous {
		before[entry.Key] = entry
	}
	for _, entry := range next {
		after[entry.Key] = entry
	}
	var changes []sourcegraph.Mutation
	for key := range before {
		if _, found := after[key]; !found {
			changes = append(changes, sourcegraph.Mutation{Key: key, Delete: true})
		}
	}
	for key, entry := range after {
		if previous, found := before[key]; !found || !bytes.Equal(previous.Value, entry.Value) {
			changes = append(changes, entry)
		}
	}
	slices.SortFunc(changes, func(a, b sourcegraph.Mutation) int { return cmp.Compare(a.Key, b.Key) })
	return changes
}

type queryGenerationDelta struct {
	mutations map[string]sourcegraph.Mutation
	added     uint64
	removed   uint64
}

type queryGenerationBatch map[string]*queryGenerationDelta

func (b queryGenerationBatch) replace(binding string, id model.EntityID, previous, next []byte) error {
	delta := b[binding]
	if delta == nil {
		delta = &queryGenerationDelta{mutations: make(map[string]sourcegraph.Mutation)}
		b[binding] = delta
	}
	var before, after []sourcegraph.Mutation
	if previous != nil {
		var row queryMetadata
		if err := json.Unmarshal(previous, &row); err != nil {
			return err
		}
		before = queryRowPostings(row)
	}
	if next == nil {
		delta.mutations[queryRowKey(id)] = sourcegraph.Mutation{Key: queryRowKey(id), Delete: true}
	} else {
		var row queryMetadata
		if err := json.Unmarshal(next, &row); err != nil {
			return err
		}
		after = queryRowPostings(row)
		delta.mutations[queryRowKey(id)] = sourcegraph.Mutation{Key: queryRowKey(id), Value: next}
	}
	for _, entry := range queryPostingChanges(before, after) {
		delta.mutations[entry.Key] = entry
	}
	if previous == nil && next != nil {
		delta.added++
	}
	if previous != nil && next == nil {
		delta.removed++
	}
	return nil
}

func (b queryGenerationBatch) flush(ctx context.Context, tx *bolt.Tx) error {
	if len(b) == 0 {
		return ctx.Err()
	}
	root, err := tx.CreateBucketIfNotExists(queryGenerationsBucket)
	if err != nil {
		return err
	}
	for _, binding := range slices.Sorted(maps.Keys(b)) {
		bucket, err := root.CreateBucketIfNotExists(queryMetadataAtom(binding))
		if err != nil {
			return err
		}
		delta := b[binding]
		if err := advanceQueryGeneration(ctx, bucket, slices.Collect(maps.Values(delta.mutations)), delta.added, delta.removed); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (q queryMetadataSource) retainedAt(ctx context.Context, tx *bolt.Tx, generation queryGeneration) (queryMetadataSource, bool, error) {
	q.retained = &retainedQuery{versions: queryGenerationBinding(tx, q.binding).Bucket(queryVersionsBucket), generation: generation}
	if uint64(len(q.tips)) != generation.Rows {
		return q, false, nil
	}
	for id, tip := range q.tips {
		if err := ctx.Err(); err != nil {
			return q, false, err
		}
		data, err := q.retained.get(queryRowKey(id))
		if err != nil {
			return q, false, err
		}
		if data == nil {
			return q, false, nil
		}
		var row queryMetadata
		if err := json.Unmarshal(data, &row); err != nil {
			return q, false, err
		}
		if row.Tip != tip || row.SkippedOps != 0 {
			return q, false, nil
		}
	}
	return q, true, ctx.Err()
}

func (q queryMetadataSource) row(tx *bolt.Tx, id model.EntityID) ([]byte, error) {
	if q.retained == nil {
		return readQueryMetadata(tx, q.binding, id), nil
	}
	return q.retained.get(queryRowKey(id))
}

func (q queryMetadataSource) walk(tx *bolt.Tx, index, prefix []byte, includeDeleted bool, matches func(queryMetadata) bool, visit func(queryMetadata) error) error {
	if q.retained == nil {
		return walkQueryMetadataPostings(tx, q.binding, index, prefix, includeDeleted, matches, visit)
	}
	if !includeDeleted {
		prefix = append(bytes.Clone(prefix), 0)
	}
	return q.retained.prefix(queryPostingKey(index, prefix), func(id []byte) error {
		data, err := q.row(tx, model.EntityID(id))
		if err != nil {
			return err
		}
		var row queryMetadata
		if err := json.Unmarshal(data, &row); err != nil {
			return err
		}
		if !matches(row) {
			return nil
		}
		return visit(row)
	})
}

func readIndexedQuery[T any](ctx context.Context, q queryMetadataSource, read func(*bolt.Tx, queryMetadataSource) (T, error)) (value T, hit bool, err error) {
	activate := false
	run := func(tx *bolt.Tx, writable bool) error {
		if err := validateQueryMetadataBinding(tx, q); err != nil {
			return err
		}
		bucket := queryGenerationBinding(tx, q.binding)
		if bucket == nil {
			if queryMetadataBinding(tx, q.binding) != nil {
				return managed.ErrReindex
			}
			if q.pinned && len(q.tips) != 0 {
				return nil
			}
			var readErr error
			value, readErr = read(tx, q)
			hit = readErr == nil
			return readErr
		}
		current, err := readQueryGeneration(bucket, queryCurrentRoot)
		if err != nil {
			return err
		}
		active, err := readQueryGeneration(bucket, queryActiveRoot)
		if err != nil {
			return err
		}
		selected := q
		if q.pinned {
			captured := active
			if bucket.Get(queryActiveRoot) == nil {
				captured = current
			}
			var matches bool
			selected, matches, err = q.retainedAt(ctx, tx, captured)
			if err == nil && !matches && captured != current {
				selected, matches, err = q.retainedAt(ctx, tx, current)
			}
			if err != nil || !matches {
				return err
			}
		}
		advance := (!q.pinned && active != current) || bucket.Get(queryActiveRoot) == nil
		if advance && !writable {
			activate = true
			return nil
		}
		value, err = read(tx, selected)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if advance {
			if err := bucket.Put(queryActiveRoot, bucket.Get(queryCurrentRoot)); err != nil {
				return err
			}
		}
		hit = true
		return nil
	}
	err = q.store.managedView(ctx, func(tx *bolt.Tx) error { return run(tx, false) })
	if err == nil && activate {
		lock, lockErr := managed.Lock(ctx, q.store.recordsCommonDir, true)
		if lockErr != nil {
			return value, false, lockErr
		}
		db, openErr := q.store.openManaged(false)
		if openErr != nil {
			return value, false, errors.Join(openErr, lock.Close())
		}
		err = q.store.recoverManaged(ctx, db)
		if err == nil {
			err = db.Update(func(tx *bolt.Tx) error { return run(tx, true) })
		}
		err = errors.Join(err, db.Close(), lock.Close(), q.store.CheckRecords(), ctx.Err())
	}
	if err != nil {
		var empty T
		return empty, false, err
	}
	return value, hit, nil
}

func (s *Store) validateManagedQueryGenerations(tx *bolt.Tx, updates []gitcmd.RefUpdate) error {
	seen := make(map[model.Kind]bool)
	for _, update := range updates {
		parsed, err := refs.Parse(update.Ref)
		if errors.Is(err, refs.ErrNotCCNotes) {
			continue
		}
		if err != nil {
			return err
		}
		if seen[parsed.Kind] {
			continue
		}
		seen[parsed.Kind] = true
		binding := s.listCacheName(parsed.Kind, fmt.Appendf(nil, "metadata %d", queryMetadataVersion), foldCacheGeneration, "")
		if queryMetadataBinding(tx, binding) == nil {
			continue
		}
		generationBucket := queryGenerationBinding(tx, binding)
		if generationBucket == nil || generationBucket.Get(queryCurrentRoot) == nil {
			return managed.ErrReindex
		}
		generation, err := readQueryGeneration(generationBucket, queryCurrentRoot)
		if err != nil {
			return err
		}
		tips := queryMetadataKindTips(tx, binding, parsed.Kind)
		var count uint64
		if tips != nil {
			count = tips.Sequence()
		}
		if generation.Rows != count {
			return managed.ErrReindex
		}
	}
	return nil
}
