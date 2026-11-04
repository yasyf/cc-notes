package store

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
	"github.com/yasyf/cc-notes/model"
)

type queryMetadataBuild struct {
	entries map[string][]byte
	rows    uint64
}

func buildQueryMetadata(ctx context.Context, tx *bolt.Tx, rows []queryMetadata) error {
	rows = slices.Clone(rows)
	slices.SortFunc(rows, func(a, b queryMetadata) int {
		if order := cmp.Compare(a.Binding, b.Binding); order != 0 {
			return order
		}
		return cmp.Compare(a.ID, b.ID)
	})
	builds := make(map[string]*queryMetadataBuild)
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return err
		}
		binding, err := ensureQueryMetadataBinding(tx, row.Binding)
		if err != nil {
			return err
		}
		data, err := json.Marshal(row)
		if err != nil {
			return err
		}
		if err := putQueryMetadata(binding, row, data); err != nil {
			return err
		}
		build := builds[row.Binding]
		if build == nil {
			build = &queryMetadataBuild{entries: make(map[string][]byte)}
			builds[row.Binding] = build
		}
		build.rows++
		build.entries[queryRowKey(row.ID)] = data
		for _, posting := range queryRowPostings(row) {
			build.entries[posting.Key] = posting.Value
		}
	}
	root, err := tx.CreateBucketIfNotExists(queryGenerationsBucket)
	if err != nil {
		return err
	}
	objects, err := root.CreateBucketIfNotExists(queryObjectsBucket)
	if err != nil {
		return err
	}
	counterBucket, err := root.CreateBucketIfNotExists([]byte(managed.QueryObjectRefs))
	if err != nil {
		return err
	}
	counts := newQueryReferenceCounts(counterBucket)
	var created []model.SHA
	for binding, build := range builds {
		entries := make([]sourcegraph.Entry, 0, len(build.entries))
		for key, value := range build.entries {
			entries = append(entries, sourcegraph.Entry{Key: key, Value: value})
		}
		index := sourcegraph.New(queryGraphObjects{ctx: ctx, bucket: objects, counts: counts, created: &created})
		hash, err := index.Build(entries)
		if err != nil {
			return err
		}
		if err := retainQueryObject(ctx, objects, counts, hash); err != nil {
			return err
		}
		bucket, err := root.CreateBucketIfNotExists(queryMetadataAtom(binding))
		if err != nil {
			return err
		}
		data, err := json.Marshal(queryGeneration{Root: hash, Rows: build.rows})
		if err != nil {
			return err
		}
		if err := bucket.Put(queryCurrentRoot, data); err != nil {
			return err
		}
	}
	if err := pruneQueryObjects(ctx, objects, counts, created); err != nil {
		return err
	}
	return counts.flush(ctx)
}
