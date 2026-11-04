package store

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"sort"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
	"github.com/yasyf/cc-notes/model"
)

var (
	queryGenerationsBucket = []byte(managed.QueryGenerations)
	queryObjectsBucket     = []byte(managed.QueryObjects)
	queryCurrentRoot       = []byte("current")
	queryActiveRoot        = []byte("active")
	queryRetiredRoot       = []byte("retired")
)

type queryGeneration struct {
	Root model.SHA
	Rows uint64
}
type queryGraphObjects struct {
	ctx    context.Context
	store  queryObjectStore
	writes *queryObjectWrites
}
type retainedQuery struct {
	index      *sourcegraph.Map
	generation queryGeneration
}

func (o queryGraphObjects) Context() context.Context { return o.ctx }
func (o queryGraphObjects) Err() error               { return o.ctx.Err() }

func queryObjectKind(data byte) (plumbing.ObjectType, error) {
	switch data {
	case byte(plumbing.BlobObject):
		return plumbing.BlobObject, nil
	case byte(plumbing.TreeObject):
		return plumbing.TreeObject, nil
	default:
		return plumbing.InvalidObject, fmt.Errorf("invalid immutable query object type %d: %w", data, managed.ErrReindex)
	}
}

func (o queryGraphObjects) read(hash model.SHA, kind plumbing.ObjectType) (*plumbing.MemoryObject, error) {
	if err := o.Err(); err != nil {
		return nil, err
	}
	data, err := o.store.get(hash)
	if err != nil {
		return nil, err
	}
	if data == nil && o.writes != nil {
		data = o.writes.objects[hash]
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("missing immutable query object %s: %w", hash, managed.ErrReindex)
	}
	actual, err := queryObjectKind(data[0])
	if err != nil {
		return nil, err
	}
	if actual != kind {
		return nil, fmt.Errorf("missing immutable query object %s: %w", hash, managed.ErrReindex)
	}
	if plumbing.ComputeHash(kind, data[1:]).String() != string(hash) {
		return nil, fmt.Errorf("corrupt immutable query object %s", hash)
	}
	value := &plumbing.MemoryObject{}
	value.SetType(kind)
	writer, err := value.Writer()
	if err != nil {
		return nil, err
	}
	_, err = writer.Write(data[1:])
	err = errors.Join(err, writer.Close())
	return value, err
}

func (o queryGraphObjects) put(value *plumbing.MemoryObject, children []object.TreeEntry) (model.SHA, error) {
	if err := o.Err(); err != nil {
		return "", err
	}
	reader, err := value.Reader()
	if err != nil {
		return "", err
	}
	data, err := io.ReadAll(reader)
	err = errors.Join(err, reader.Close())
	if err != nil {
		return "", err
	}
	hash := model.SHA(value.Hash().String())
	var kind byte
	switch value.Type() {
	case plumbing.BlobObject:
		kind = byte(plumbing.BlobObject)
	case plumbing.TreeObject:
		kind = byte(plumbing.TreeObject)
	default:
		return "", fmt.Errorf("invalid immutable query object type %s", value.Type())
	}
	encoded := append([]byte{kind}, data...)
	existing, err := o.store.get(hash)
	if err != nil {
		return "", err
	}
	if existing == nil {
		existing = o.writes.objects[hash]
	}
	if existing != nil {
		if !bytes.Equal(existing, encoded) {
			return "", fmt.Errorf("immutable query object collision %s", hash)
		}
		return hash, nil
	}
	o.writes.objects[hash] = encoded
	o.writes.order = append(o.writes.order, hash)
	for _, entry := range children {
		o.writes.refs[model.SHA(entry.Hash.String())]++
	}
	return hash, nil
}

func (o queryGraphObjects) ReadBlob(hash model.SHA) ([]byte, error) {
	value, err := o.read(hash, plumbing.BlobObject)
	if err != nil {
		return nil, err
	}
	reader, err := value.Reader()
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(reader)
	return data, errors.Join(err, reader.Close())
}

func (o queryGraphObjects) WriteBlob(data []byte) (model.SHA, error) {
	value := &plumbing.MemoryObject{}
	value.SetType(plumbing.BlobObject)
	writer, err := value.Writer()
	if err != nil {
		return "", err
	}
	_, err = writer.Write(data)
	err = errors.Join(err, writer.Close())
	if err != nil {
		return "", err
	}
	return o.put(value, nil)
}

func (o queryGraphObjects) ReadTree(hash model.SHA) ([]object.TreeEntry, error) {
	value, err := o.read(hash, plumbing.TreeObject)
	if err != nil {
		return nil, err
	}
	tree := &object.Tree{}
	if err := tree.Decode(value); err != nil {
		return nil, err
	}
	return tree.Entries, nil
}

func (o queryGraphObjects) WriteTree(entries []object.TreeEntry) (model.SHA, error) {
	entries = slices.Clone(entries)
	sort.Sort(object.TreeEntrySorter(entries))
	value := &plumbing.MemoryObject{}
	if err := (&object.Tree{Entries: entries}).Encode(value); err != nil {
		return "", err
	}
	return o.put(value, entries)
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
	if err := json.Unmarshal(data, &generation); err != nil {
		return generation, err
	}
	if (generation.Root == "") != (generation.Rows == 0) {
		return generation, managed.ErrReindex
	}
	return generation, nil
}

func popRetiredQueryGeneration(bucket *bolt.Bucket) (model.SHA, error) {
	retired, err := readQueryGeneration(bucket, queryRetiredRoot)
	if err != nil {
		return "", err
	}
	if err := bucket.Delete(queryRetiredRoot); err != nil {
		return "", err
	}
	return retired.Root, nil
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
	objects, err := createQueryObjectStore(root)
	if err != nil {
		return err
	}
	_, err = root.CreateBucketIfNotExists([]byte(managed.QueryObjectRefs))
	if err != nil {
		return err
	}
	counts := newQueryReferenceCounts(root)
	writes := &queryObjectWrites{objects: make(map[model.SHA][]byte), refs: make(map[model.SHA]uint64)}
	index := sourcegraph.New(queryGraphObjects{ctx: ctx, store: objects, writes: writes})
	var released []model.SHA
	for _, binding := range slices.Sorted(maps.Keys(b)) {
		delta := b[binding]
		bucket, err := root.CreateBucketIfNotExists(queryMetadataAtom(binding))
		if err != nil {
			return err
		}
		generation, err := readQueryGeneration(bucket, queryCurrentRoot)
		if err != nil {
			return err
		}
		retired, err := popRetiredQueryGeneration(bucket)
		if err != nil {
			return err
		}
		if retired != "" {
			released = append(released, retired)
		}
		previousRoot := generation.Root
		generation.Root, err = index.Apply(previousRoot, slices.Collect(maps.Values(delta.mutations)))
		if err != nil {
			return err
		}
		generation.Rows = generation.Rows + delta.added - delta.removed
		if generation.Root != previousRoot {
			if generation.Root != "" {
				writes.refs[generation.Root]++
			}
			if previousRoot != "" {
				released = append(released, previousRoot)
			}
		}
		data, err := json.Marshal(generation)
		if err != nil {
			return err
		}
		if err := bucket.Put(queryCurrentRoot, data); err != nil {
			return err
		}
	}
	created, err := writes.flush(ctx, objects, counts)
	if err != nil {
		return err
	}
	for _, hash := range released {
		if err := releaseQueryObject(ctx, objects, counts, hash); err != nil {
			return err
		}
	}
	if err := pruneQueryObjects(ctx, objects, counts, created); err != nil {
		return err
	}
	return counts.flush(ctx)
}

func (q queryMetadataSource) retainedAt(ctx context.Context, tx *bolt.Tx, generation queryGeneration) (queryMetadataSource, bool, error) {
	root := tx.Bucket(queryGenerationsBucket)
	if root == nil {
		return q, false, managed.ErrReindex
	}
	objects := queryObjectStoreAt(root)
	if objects.objects == nil || objects.ids == nil {
		return q, false, managed.ErrReindex
	}
	q.retained = &retainedQuery{index: sourcegraph.New(queryGraphObjects{ctx: ctx, store: objects}), generation: generation}
	if uint64(len(q.tips)) != generation.Rows {
		return q, false, nil
	}
	for id, tip := range q.tips {
		data, found, err := q.retained.index.Get(generation.Root, queryRowKey(id))
		if err != nil {
			return q, false, err
		}
		if !found {
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
	data, _, err := q.retained.index.Get(q.retained.generation.Root, queryRowKey(id))
	return data, err
}

func (q queryMetadataSource) walk(tx *bolt.Tx, index, prefix []byte, includeDeleted bool, matches func(queryMetadata) bool, visit func(queryMetadata) error) error {
	if q.retained == nil {
		return walkQueryMetadataPostings(tx, q.binding, index, prefix, includeDeleted, matches, visit)
	}
	if !includeDeleted {
		prefix = append(bytes.Clone(prefix), 0)
	}
	return q.retained.index.Prefix(q.retained.generation.Root, queryPostingKey(index, prefix), func(entry sourcegraph.Entry) error {
		data, err := q.row(tx, model.EntityID(entry.Value))
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
			if err := activateQueryGeneration(ctx, tx, bucket, current, active); err != nil {
				return err
			}
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
		if generation.Root != "" {
			root := tx.Bucket(queryGenerationsBucket)
			data, err := queryObjectStoreAt(root).get(generation.Root)
			if err != nil {
				return err
			}
			if data == nil {
				return managed.ErrReindex
			}
			count, err := queryObjectRefCount(root, generation.Root)
			if err != nil {
				return err
			}
			if count == 0 {
				return managed.ErrReindex
			}
		}
	}
	return nil
}
