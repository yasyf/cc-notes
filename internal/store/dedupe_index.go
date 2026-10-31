package store

import (
	"cmp"
	"context"
	"slices"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

func scanIndexedDup[S model.Snapshot](ctx context.Context, s *Store, kind model.Kind, candidate []model.PackCommit, foldFn func([]model.PackCommit) (S, error), list func(S) ([]S, error), live func(S) bool, same func(S, S) bool) (model.Snapshot, error) {
	return scanDup(candidate, foldFn, func(candidate S) ([]S, error) {
		values, hit, err := indexedDuplicateList(ctx, s, kind, duplicateFingerprint(candidate), foldFn)
		if err != nil || hit {
			return values, err
		}
		return list(candidate)
	}, live, same)
}

func indexedDuplicateList[S model.Snapshot](ctx context.Context, s *Store, kind model.Kind, fingerprint string, foldFn func([]model.PackCommit) (S, error)) (values []S, hit bool, err error) {
	defer func() {
		if finalErr := s.CheckRecords(); finalErr != nil {
			values, hit, err = nil, false, finalErr
		}
		if finalErr := ctx.Err(); finalErr != nil {
			values, hit, err = nil, false, finalErr
		}
	}()
	source, err := queryMetadataIdentity(ctx, s, kind)
	if err == nil && source.pinned {
		source, err = source.capture(ctx)
	}
	if err != nil {
		return nil, false, err
	}
	entries, hit, err := readQueryMetadataIndexWithoutWait(ctx, source, func(tx *bolt.Tx) ([]tipEntry, error) {
		var entries []tipEntry
		err := walkQueryMetadataPostings(tx, source.binding, queryMetadataDuplicatesBucket, queryMetadataLabelPrefix(kind, fingerprint), false, func(row queryMetadata) bool {
			return row.Kind == kind && row.DuplicateContent == fingerprint
		}, func(row queryMetadata) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := validateQueryMetadata(tx, source, row); err != nil {
				return err
			}
			entries = append(entries, tipEntry{ref: refs.For(kind, row.ID), tip: row.Tip})
			return nil
		})
		return entries, err
	})
	if err != nil || !hit {
		return nil, false, err
	}
	all, err := foldAll(ctx, s, entries, foldFn)
	if err != nil {
		return nil, false, err
	}
	values = make([]S, 0, len(all))
	for _, value := range all {
		if keepInList(value.Meta(), ListOpts{}) {
			values = append(values, value)
		}
	}
	slices.SortFunc(values, func(a, b S) int {
		if order := a.Meta().CreatedAt.Compare(b.Meta().CreatedAt); order != 0 {
			return order
		}
		return cmp.Compare(a.EntityID(), b.EntityID())
	})
	return values, true, nil
}
