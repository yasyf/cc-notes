package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

type indexedSupersession struct {
	incoming []model.EntityID
	heads    []model.EntityID
}

// IndexedSuperseders uses the derived incoming-edge postings. A cache miss leaves
// the caller's authoritative listing responsible for the result.
func (s *Store) IndexedSuperseders(ctx context.Context, kind model.Kind, id model.EntityID) ([]model.EntityID, bool, error) {
	return indexedSupersessionQuery(ctx, s, kind, func(tx *bolt.Tx, source queryMetadataSource) ([]model.EntityID, error) {
		return querySuperseders(ctx, tx, source, id)
	})
}

// IndexedSupersedeHeads follows only reachable metadata rows. A cache miss leaves
// the caller's authoritative listing responsible for the result.
func (s *Store) IndexedSupersedeHeads(ctx context.Context, kind model.Kind, id model.EntityID) ([]model.EntityID, bool, error) {
	return indexedSupersessionQuery(ctx, s, kind, func(tx *bolt.Tx, source queryMetadataSource) ([]model.EntityID, error) {
		return querySupersedeHeads(ctx, tx, source, id)
	})
}

// IndexedSupersession captures incoming and terminal edges from the same metadata
// view. A cache miss leaves the caller's authoritative listing responsible for both.
func (s *Store) IndexedSupersession(ctx context.Context, kind model.Kind, id model.EntityID) ([]model.EntityID, []model.EntityID, bool, error) {
	result, hit, err := indexedSupersessionQuery(ctx, s, kind, func(tx *bolt.Tx, source queryMetadataSource) (indexedSupersession, error) {
		incoming, err := querySuperseders(ctx, tx, source, id)
		if err != nil {
			return indexedSupersession{}, err
		}
		heads, err := querySupersedeHeads(ctx, tx, source, id)
		return indexedSupersession{incoming: incoming, heads: heads}, err
	})
	return result.incoming, result.heads, hit, err
}

func indexedSupersessionQuery[T any](ctx context.Context, s *Store, kind model.Kind, read func(*bolt.Tx, queryMetadataSource) (T, error)) (value T, hit bool, err error) {
	source, err := captureQueryMetadata(ctx, s, kind)
	if err != nil {
		return value, false, err
	}
	return readQueryMetadataIndex(ctx, source, func(tx *bolt.Tx) (T, error) {
		return read(tx, source)
	})
}

func querySuperseders(ctx context.Context, tx *bolt.Tx, source queryMetadataSource, id model.EntityID) ([]model.EntityID, error) {
	var incoming []model.EntityID
	err := walkQueryMetadataSuperseders(tx, source.binding, source.kind, id, false, func(data []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := decodeSupersessionMetadata(source, data)
		if err != nil {
			return err
		}
		incoming = append(incoming, row.ID)
		return nil
	})
	slices.Sort(incoming)
	return incoming, err
}

func querySupersedeHeads(ctx context.Context, tx *bolt.Tx, source queryMetadataSource, id model.EntityID) ([]model.EntityID, error) {
	row, live, err := supersessionMetadata(ctx, tx, source, id)
	if err != nil || !live {
		return nil, err
	}
	seen := map[model.EntityID]bool{id: true}
	queue := slices.Clone(row.SupersededBy)
	var heads []model.EntityID
	for len(queue) > 0 {
		next := queue[0]
		queue = queue[1:]
		if seen[next] {
			continue
		}
		seen[next] = true
		row, live, err := supersessionMetadata(ctx, tx, source, next)
		if err != nil {
			return nil, err
		}
		switch {
		case !live:
			continue
		case len(row.SupersededBy) == 0:
			heads = append(heads, next)
		default:
			queue = append(queue, row.SupersededBy...)
		}
	}
	slices.Sort(heads)
	return heads, nil
}

func supersessionMetadata(ctx context.Context, tx *bolt.Tx, source queryMetadataSource, id model.EntityID) (queryMetadata, bool, error) {
	if err := ctx.Err(); err != nil {
		return queryMetadata{}, false, err
	}
	if _, exists := source.tips[id]; !exists {
		return queryMetadata{}, false, nil
	}
	data := readQueryMetadata(tx, source.binding, id)
	row, err := decodeSupersessionMetadata(source, data)
	if err != nil {
		return queryMetadata{}, false, err
	}
	if row.ID != id {
		return queryMetadata{}, false, fmt.Errorf("query metadata identity differs from %s", id)
	}
	return row, !row.Deleted, nil
}

func decodeSupersessionMetadata(source queryMetadataSource, data []byte) (queryMetadata, error) {
	var row queryMetadata
	if err := json.Unmarshal(data, &row); err != nil {
		return queryMetadata{}, err
	}
	if row.Version != queryMetadataVersion || row.Binding != source.binding || row.Kind != source.kind || row.Tip == "" || source.tips[row.ID] != row.Tip {
		return queryMetadata{}, fmt.Errorf("query metadata differs from captured %s %s", source.kind, row.ID)
	}
	return row, nil
}
