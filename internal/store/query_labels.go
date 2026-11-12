package store

import (
	"context"
	"slices"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// IndexedAnswers reads only answer bodies matching every supplied label. At
// least one label is required; a derived-cache miss leaves listing to the caller.
func (s *Store) IndexedAnswers(ctx context.Context, labels []string, opts ListOpts) ([]model.Answer, bool, error) {
	return indexedFilteredList(ctx, s, model.KindAnswer, labels, nil, opts, fold.Answer)
}

// IndexedDocs reads only doc bodies matching every supplied label. At least
// one label is required; a derived-cache miss leaves listing to the caller.
func (s *Store) IndexedDocs(ctx context.Context, labels []string, opts ListOpts) ([]model.Doc, bool, error) {
	return indexedFilteredList(ctx, s, model.KindDoc, labels, nil, opts, fold.Doc)
}

// IndexedAnchoredDocs reads doc bodies matching every supplied anchor and label.
// At least one anchor is required; a derived-cache miss leaves listing to the caller.
func (s *Store) IndexedAnchoredDocs(ctx context.Context, labels []string, anchors []model.Anchor, opts ListOpts) ([]model.Doc, bool, error) {
	return indexedFilteredList(ctx, s, model.KindDoc, labels, anchors, opts, fold.Doc)
}

// IndexedTasks reads only task bodies matching every supplied label. At least
// one label is required; a derived-cache miss leaves listing to the caller.
func (s *Store) IndexedTasks(ctx context.Context, labels []string) ([]model.Task, bool, error) {
	return indexedFilteredList(ctx, s, model.KindTask, labels, nil, ListOpts{}, fold.Task)
}

// IndexedLogs reads only log bodies matching every supplied label. At least
// one label is required; a derived-cache miss leaves listing to the caller.
func (s *Store) IndexedLogs(ctx context.Context, labels []string, includeDeleted bool) ([]model.Log, bool, error) {
	return indexedFilteredList(ctx, s, model.KindLog, labels, nil, ListOpts{IncludeDeleted: includeDeleted}, fold.Log)
}

// IndexedInvestigations reads only live investigation bodies matching every
// supplied label. At least one label is required; a derived-cache miss leaves
// listing to the caller.
func (s *Store) IndexedInvestigations(ctx context.Context, labels []string) ([]model.Investigation, bool, error) {
	return indexedFilteredList(ctx, s, model.KindInvestigation, labels, nil, ListOpts{}, fold.Investigation)
}

// IndexedLedgers reads only live ledger bodies matching every supplied label.
// At least one label is required; a derived-cache miss leaves listing to the caller.
func (s *Store) IndexedLedgers(ctx context.Context, labels []string) ([]model.Ledger, bool, error) {
	return indexedFilteredList(ctx, s, model.KindLedger, labels, nil, ListOpts{}, fold.Ledger)
}

func indexedFilteredList[T model.Snapshot](ctx context.Context, s *Store, kind model.Kind, labels []string, anchors []model.Anchor, opts ListOpts, foldFn func([]model.PackCommit) (T, error)) (values []T, hit bool, err error) {
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
		remainingLabels, remainingAnchors := labels, anchors
		if len(labels) > 0 {
			remainingLabels = labels[1:]
		} else {
			remainingAnchors = anchors[1:]
		}
		visit := func(row queryMetadata) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := validateQueryMetadata(tx, source, row); err != nil {
				return err
			}
			if !opts.IncludeSuperseded && len(row.SupersededBy) > 0 {
				return nil
			}
			for _, label := range remainingLabels {
				if !slices.Contains(row.Labels, label) {
					return nil
				}
			}
			for _, anchor := range remainingAnchors {
				if !slices.Contains(row.Anchors, anchor) {
					return nil
				}
			}
			entries = append(entries, tipEntry{ref: refs.For(kind, row.ID), tip: row.Tip})
			return nil
		}
		var err error
		if len(labels) > 0 {
			err = source.walk(tx, queryMetadataLabelsBucket, queryMetadataLabelPrefix(source.kind, labels[0]), opts.IncludeDeleted, func(row queryMetadata) bool { return row.Kind == source.kind && slices.Contains(row.Labels, labels[0]) }, visit)
		} else {
			err = source.walk(tx, queryMetadataAnchorsBucket, queryMetadataAnchorPrefix(source.kind, anchors[0]), opts.IncludeDeleted, func(row queryMetadata) bool {
				return row.Kind == source.kind && slices.Contains(row.Anchors, anchors[0])
			}, visit)
		}
		return entries, err
	})
	if err != nil || !hit {
		return nil, false, err
	}
	values, err = foldAll(ctx, s, entries, foldFn)
	if err != nil {
		return nil, false, err
	}
	return values, true, nil
}
