package store

import (
	"context"
	"slices"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// IndexedTasksBlocking selects live reverse dependencies from derived postings.
// A derived-cache miss leaves authoritative listing to the caller.
func (s *Store) IndexedTasksBlocking(ctx context.Context, id model.EntityID) ([]model.EntityID, bool, error) {
	return indexedMetadataQuery(ctx, s, model.KindTask, func(tx *bolt.Tx, source queryMetadataSource) ([]model.EntityID, error) {
		return queryTaskRelationIDs(ctx, tx, source, id, walkQueryMetadataBlockedBy)
	})
}

// IndexedTasksBlockingFor selects live reverse dependencies for the supplied task IDs.
func (s *Store) IndexedTasksBlockingFor(ctx context.Context, ids []model.EntityID) (map[model.EntityID][]model.EntityID, bool, error) {
	if len(ids) == 0 {
		if err := s.CheckRecords(); err != nil {
			return nil, false, err
		}
		if err := ctx.Err(); err != nil {
			return nil, false, err
		}
		return make(map[model.EntityID][]model.EntityID), true, nil
	}
	return indexedMetadataQuery(ctx, s, model.KindTask, func(tx *bolt.Tx, source queryMetadataSource) (map[model.EntityID][]model.EntityID, error) {
		return queryTasksBlockingFor(ctx, tx, source, ids)
	})
}

func queryTasksBlockingFor(ctx context.Context, tx *bolt.Tx, source queryMetadataSource, ids []model.EntityID) (map[model.EntityID][]model.EntityID, error) {
	blocks := make(map[model.EntityID][]model.EntityID, len(ids))
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if _, found := blocks[id]; found {
			continue
		}
		values, err := queryTaskRelationIDs(ctx, tx, source, id, walkQueryMetadataBlockedBy)
		if err != nil {
			return nil, err
		}
		blocks[id] = values
	}
	return blocks, nil
}

// IndexedTaskChildren selects live children from derived parent postings.
// A derived-cache miss leaves authoritative listing to the caller.
func (s *Store) IndexedTaskChildren(ctx context.Context, id model.EntityID) ([]model.EntityID, bool, error) {
	return indexedMetadataQuery(ctx, s, model.KindTask, func(tx *bolt.Tx, source queryMetadataSource) ([]model.EntityID, error) {
		return queryTaskRelationIDs(ctx, tx, source, id, walkQueryMetadataTaskChildren)
	})
}

func queryTaskRelationIDs(ctx context.Context, tx *bolt.Tx, source queryMetadataSource, id model.EntityID, walk func(*bolt.Tx, string, model.EntityID, func(queryMetadata) error) error) ([]model.EntityID, error) {
	var ids []model.EntityID
	err := walk(tx, source.binding, id, func(row queryMetadata) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := validateQueryMetadata(tx, source, row); err != nil {
			return err
		}
		ids = append(ids, row.ID)
		return nil
	})
	slices.Sort(ids)
	return ids, err
}

// IndexedTaskRunbooks selects citing runbooks and loads their captured bodies.
// A derived-cache miss leaves authoritative listing to the caller.
func (s *Store) IndexedTaskRunbooks(ctx context.Context, id model.EntityID) (runbooks []model.Runbook, hit bool, err error) {
	defer func() {
		if finalErr := s.CheckRecords(); finalErr != nil {
			runbooks, hit, err = nil, false, finalErr
		}
		if finalErr := ctx.Err(); finalErr != nil {
			runbooks, hit, err = nil, false, finalErr
		}
	}()
	entries, hit, err := indexedMetadataQuery(ctx, s, model.KindRunbook, func(tx *bolt.Tx, source queryMetadataSource) ([]tipEntry, error) {
		var entries []tipEntry
		err := walkQueryMetadataRunTasks(tx, source.binding, id, func(row queryMetadata) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := validateQueryMetadata(tx, source, row); err != nil {
				return err
			}
			entries = append(entries, tipEntry{ref: refs.For(model.KindRunbook, row.ID), tip: row.Tip})
			return nil
		})
		return entries, err
	})
	if err != nil || !hit {
		return nil, false, err
	}
	pins := make(map[string]model.SHA, len(entries))
	for _, entry := range entries {
		pins[entry.ref] = entry.tip
	}
	runbooks, err = foldAll(ctx, s.Pinned(pins), entries, fold.Runbook)
	if err != nil {
		return nil, false, err
	}
	return runbooks, true, nil
}
