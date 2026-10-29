package store

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// StatusRecord carries the captured, payload-free inputs of the status classifiers.
type StatusRecord queryMetadata

// StatusMetadata retains one ref universe for both metadata and exposed bodies.
type StatusMetadata struct {
	Records  []StatusRecord
	Captured *Store
}

// StatusMetadata reads every requested kind from one captured ref universe.
// A derived-cache miss retains that capture for the authoritative status reader.
func (s *Store) StatusMetadata(ctx context.Context, kinds ...model.Kind) (StatusMetadata, bool, error) {
	pinned, sources, err := captureStatusMetadataSources(ctx, s, kinds...)
	if err != nil {
		return StatusMetadata{}, false, err
	}
	result := StatusMetadata{Captured: pinned}
	for _, source := range sources {
		rows, hit, err := readQueryMetadataIndex(ctx, source, func(tx *bolt.Tx) ([]StatusRecord, error) {
			rows := make([]StatusRecord, 0, len(source.tips))
			for id := range source.tips {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				row, err := decodeQueryMetadata(tx, source, readQueryMetadata(tx, source.binding, id))
				if err != nil {
					return nil, err
				}
				if row.ID != id {
					return nil, fmt.Errorf("status metadata identity differs from %s", id)
				}
				rows = append(rows, StatusRecord(row))
			}
			slices.SortFunc(rows, func(a, b StatusRecord) int {
				if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
					return c
				}
				return cmp.Compare(a.ID, b.ID)
			})
			return rows, nil
		})
		if err != nil {
			return StatusMetadata{}, false, err
		}
		if !hit {
			return StatusMetadata{Captured: pinned}, false, nil
		}
		result.Records = append(result.Records, rows...)
	}
	if err := ctx.Err(); err != nil {
		return StatusMetadata{}, false, err
	}
	if err := pinned.CheckRecords(); err != nil {
		return StatusMetadata{}, false, err
	}
	return result, true, nil
}

func captureStatusMetadataSources(ctx context.Context, s *Store, kinds ...model.Kind) (*Store, []queryMetadataSource, error) {
	sources := make([]queryMetadataSource, len(kinds))
	for i, kind := range kinds {
		source, err := queryMetadataIdentity(ctx, s, kind)
		if err != nil {
			return nil, nil, err
		}
		if !source.pinned {
			source.generation, err = source.currentGeneration(ctx)
			if err != nil {
				return nil, nil, err
			}
		}
		sources[i] = source
	}
	var pinned *Store
	if s.pins != nil {
		pinned = s.Pinned(maps.Clone(s.pins))
	} else {
		var err error
		pinned, err = s.PinnedKinds(ctx, kinds...)
		if err != nil {
			return nil, nil, err
		}
	}
	for i, source := range sources {
		if !source.pinned {
			generation, err := source.currentGeneration(ctx)
			if err != nil {
				return nil, nil, err
			}
			if generation != source.generation {
				source.generation = ""
			}
		}
		source.store = pinned
		source.tips = make(map[model.EntityID]model.SHA)
		root := refs.Root(source.kind)
		for _, entry := range directChildren(root, pinned.pins) {
			if err := ctx.Err(); err != nil {
				return nil, nil, err
			}
			source.tips[model.EntityID(strings.TrimPrefix(entry.ref, root))] = entry.tip
		}
		sources[i] = source
	}
	return pinned, sources, nil
}
