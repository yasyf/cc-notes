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
func (s *Store) IndexedAnswers(ctx context.Context, labels []string, opts ListOpts) (answers []model.Answer, hit bool, err error) {
	defer func() {
		if finalErr := s.CheckRecords(); finalErr != nil {
			answers, hit, err = nil, false, finalErr
		}
		if finalErr := ctx.Err(); finalErr != nil {
			answers, hit, err = nil, false, finalErr
		}
	}()
	entries, hit, err := indexedMetadataQuery(ctx, s, model.KindAnswer, func(tx *bolt.Tx, source queryMetadataSource) ([]tipEntry, error) {
		var entries []tipEntry
		err := walkQueryMetadataLabel(tx, source.binding, source.kind, labels[0], opts.IncludeDeleted, func(data []byte) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			row, err := decodeQueryMetadata(tx, source, data)
			if err != nil {
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
			entries = append(entries, tipEntry{ref: refs.For(model.KindAnswer, row.ID), tip: row.Tip})
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
	answers, err = cachedPinnedList(ctx, s.Pinned(pins), model.KindAnswer, variant, func(pinned *Store) ([]model.Answer, error) {
		return foldAll(ctx, pinned, entries, fold.Answer)
	})
	if err != nil {
		return nil, false, err
	}
	return answers, true, nil
}
