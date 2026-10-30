package store

import (
	"context"
	"encoding/json"
	"slices"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// IndexedAnswers reads only answer bodies matching every supplied label. At
// least one label is required; a derived-cache miss leaves listing to the caller.
func (s *Store) IndexedAnswers(ctx context.Context, labels []string, opts ListOpts) ([]model.Answer, bool, error) {
	return indexedLabeledList(ctx, s, model.KindAnswer, labels, opts, fold.Answer)
}

// IndexedTasks reads only task bodies matching every supplied label. At least
// one label is required; a derived-cache miss leaves listing to the caller.
func (s *Store) IndexedTasks(ctx context.Context, labels []string) ([]model.Task, bool, error) {
	return indexedLabeledList(ctx, s, model.KindTask, labels, ListOpts{}, fold.Task)
}

// IndexedLogs reads only log bodies matching every supplied label. At least
// one label is required; a derived-cache miss leaves listing to the caller.
func (s *Store) IndexedLogs(ctx context.Context, labels []string, includeDeleted bool) ([]model.Log, bool, error) {
	return indexedLabeledList(ctx, s, model.KindLog, labels, ListOpts{IncludeDeleted: includeDeleted}, fold.Log)
}

// IndexedInvestigations reads only live investigation bodies matching every
// supplied label. At least one label is required; a derived-cache miss leaves
// listing to the caller.
func (s *Store) IndexedInvestigations(ctx context.Context, labels []string) ([]model.Investigation, bool, error) {
	return indexedLabeledList(ctx, s, model.KindInvestigation, labels, ListOpts{}, fold.Investigation)
}

func indexedLabeledList[T model.Snapshot](ctx context.Context, s *Store, kind model.Kind, labels []string, opts ListOpts, foldFn func([]model.PackCommit) (T, error)) (values []T, hit bool, err error) {
	defer func() {
		if finalErr := s.CheckRecords(); finalErr != nil {
			values, hit, err = nil, false, finalErr
		}
		if finalErr := ctx.Err(); finalErr != nil {
			values, hit, err = nil, false, finalErr
		}
	}()
	entries, hit, err := indexedMetadataQuery(ctx, s, kind, func(tx *bolt.Tx, source queryMetadataSource) ([]tipEntry, error) {
		var entries []tipEntry
		err := walkQueryMetadataLabel(tx, source.binding, source.kind, labels[0], opts.IncludeDeleted, func(row queryMetadata) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := validateQueryMetadata(tx, source, row); err != nil {
				return err
			}
			if !opts.IncludeSuperseded && len(row.SupersededBy) > 0 {
				return nil
			}
			for _, label := range labels[1:] {
				if !slices.Contains(row.Labels, label) {
					return nil
				}
			}
			entries = append(entries, tipEntry{ref: refs.For(kind, row.ID), tip: row.Tip})
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
	variant, err := json.Marshal(struct {
		Labels []string
		Opts   ListOpts
	}{Labels: labels, Opts: opts})
	if err != nil {
		return nil, false, err
	}
	values, err = cachedPinnedList(ctx, s.Pinned(pins), kind, variant, func(pinned *Store) ([]T, error) {
		return foldAll(ctx, pinned, entries, foldFn)
	})
	if err != nil {
		return nil, false, err
	}
	return values, true, nil
}
