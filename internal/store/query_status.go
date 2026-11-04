package store

import (
	"bytes"
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

// StatusMetadata reads the requested metadata and body tips from one managed
// authority view. Historical pins return a miss when current rows differ.
func (s *Store) StatusMetadata(ctx context.Context, kinds ...model.Kind) (StatusMetadata, bool, error) {
	var result StatusMetadata
	var hit bool
	err := s.managedView(ctx, func(tx *bolt.Tx) error {
		pinned, sources, err := captureStatusMetadataSourcesAt(ctx, s, tx, kinds...)
		if err != nil {
			return err
		}
		result.Captured = pinned
		for _, source := range sources {
			if source.pinned {
				diff, err := diffQueryMetadata(ctx, tx, source.binding, source.kind, source.tips)
				if err != nil {
					return err
				}
				if len(diff.Changed) != 0 || len(diff.Removed) != 0 {
					result.Records = nil
					return nil
				}
			}
			rows := make([]StatusRecord, 0, len(source.tips))
			for id := range source.tips {
				if err := ctx.Err(); err != nil {
					return err
				}
				row, err := decodeQueryMetadata(tx, source, readQueryMetadata(tx, source.binding, id))
				if err != nil {
					return err
				}
				if row.ID != id {
					return fmt.Errorf("status metadata identity differs from %s", id)
				}
				rows = append(rows, StatusRecord(row))
			}
			slices.SortFunc(rows, func(a, b StatusRecord) int {
				if c := cmp.Compare(a.CreatedAt, b.CreatedAt); c != 0 {
					return c
				}
				return cmp.Compare(a.ID, b.ID)
			})
			result.Records = append(result.Records, rows...)
		}
		hit = true
		return nil
	})
	if err != nil {
		return StatusMetadata{}, false, err
	}
	return result, hit, nil
}

func captureStatusMetadataSources(ctx context.Context, s *Store, kinds ...model.Kind) (pinned *Store, sources []queryMetadataSource, err error) {
	err = s.managedView(ctx, func(tx *bolt.Tx) error {
		var err error
		pinned, sources, err = captureStatusMetadataSourcesAt(ctx, s, tx, kinds...)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	return pinned, sources, nil
}

func captureStatusMetadataSourcesAt(ctx context.Context, s *Store, tx *bolt.Tx, kinds ...model.Kind) (*Store, []queryMetadataSource, error) {
	pins := maps.Clone(s.pins)
	if pins == nil {
		pins = make(map[string]model.SHA)
	}
	sources := make([]queryMetadataSource, len(kinds))
	for i, kind := range kinds {
		source, err := queryMetadataIdentity(ctx, s, kind)
		if err != nil {
			return nil, nil, err
		}
		if err := validateQueryMetadataBinding(tx, source); err != nil {
			return nil, nil, err
		}
		captured := make(map[model.EntityID]model.SHA)
		root := refs.Root(kind)
		if source.pinned {
			for _, entry := range directChildren(root, pins) {
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
				captured[model.EntityID(strings.TrimPrefix(entry.ref, root))] = entry.tip
			}
		} else {
			authority, err := managedRoot(tx)
			if err != nil {
				return nil, nil, err
			}
			prefix := []byte(root)
			cursor := authority.Bucket([]byte("refs")).Cursor()
			for ref, value := cursor.Seek(prefix); bytes.HasPrefix(ref, prefix); ref, value = cursor.Next() {
				if err := ctx.Err(); err != nil {
					return nil, nil, err
				}
				if !refs.DirectChild(root, string(ref)) {
					continue
				}
				tip := model.SHA(value)
				captured[model.EntityID(strings.TrimPrefix(string(ref), root))] = tip
				pins[string(ref)] = tip
			}
		}
		source.tips = captured
		sources[i] = source
	}
	pinned := s.Pinned(pins)
	for i := range sources {
		sources[i].store = pinned
	}
	return pinned, sources, nil
}
