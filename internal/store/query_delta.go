package store

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/model"
)

type queryMetadataTip struct {
	ID  model.EntityID
	Tip model.SHA
}

type queryMetadataDiff struct {
	Changed []queryMetadataTip
	Removed []model.EntityID
}

func putQueryMetadataTip(binding *bolt.Bucket, row queryMetadata) error {
	tips, err := binding.CreateBucketIfNotExists(queryMetadataTipsBucket)
	if err != nil {
		return err
	}
	kind, err := tips.CreateBucketIfNotExists(queryMetadataAtom(string(row.Kind)))
	if err != nil {
		return err
	}
	partial := byte(0)
	if row.SkippedOps > 0 {
		partial = 1
	}
	return kind.Put([]byte(row.ID), append([]byte{partial}, string(row.Tip)...))
}

func deleteQueryMetadataTip(binding *bolt.Bucket, row queryMetadata) error {
	return binding.Bucket(queryMetadataTipsBucket).Bucket(queryMetadataAtom(string(row.Kind))).Delete([]byte(row.ID))
}

func queryMetadataKindTips(tx *bolt.Tx, binding string, kind model.Kind) *bolt.Bucket {
	bucket := queryMetadataBinding(tx, binding)
	if bucket == nil {
		return nil
	}
	tips := bucket.Bucket(queryMetadataTipsBucket)
	if tips == nil {
		return nil
	}
	return tips.Bucket(queryMetadataAtom(string(kind)))
}

func diffQueryMetadata(ctx context.Context, tx *bolt.Tx, binding string, kind model.Kind, captured map[model.EntityID]model.SHA) (queryMetadataDiff, error) {
	var diff queryMetadataDiff
	if err := ctx.Err(); err != nil {
		return diff, err
	}
	tips := queryMetadataKindTips(tx, binding, kind)
	for id, tip := range captured {
		if err := ctx.Err(); err != nil {
			return queryMetadataDiff{}, err
		}
		var previous []byte
		if tips != nil {
			previous = tips.Get([]byte(id))
		}
		changed := previous == nil
		if previous != nil {
			if len(previous) == 0 {
				return queryMetadataDiff{}, fmt.Errorf("empty metadata tip summary")
			}
			if previous[0] > 1 {
				return queryMetadataDiff{}, fmt.Errorf("invalid metadata partial marker: %d", previous[0])
			}
			changed = model.SHA(previous[1:]) != tip || previous[0] != 0
		}
		if changed {
			diff.Changed = append(diff.Changed, queryMetadataTip{ID: id, Tip: tip})
		}
	}
	if tips != nil {
		cursor := tips.Cursor()
		for id, _ := cursor.First(); id != nil; id, _ = cursor.Next() {
			if err := ctx.Err(); err != nil {
				return queryMetadataDiff{}, err
			}
			if _, ok := captured[model.EntityID(id)]; !ok {
				diff.Removed = append(diff.Removed, model.EntityID(id))
			}
		}
	}
	slices.SortFunc(diff.Changed, func(a, b queryMetadataTip) int { return cmp.Compare(a.ID, b.ID) })
	slices.Sort(diff.Removed)
	return diff, nil
}

func applyQueryMetadataDelta(ctx context.Context, tx *bolt.Tx, binding string, kind model.Kind, captured map[model.EntityID]model.SHA, fold func(context.Context, model.EntityID, model.SHA) (queryMetadata, error)) error {
	diff, err := diffQueryMetadata(ctx, tx, binding, kind, captured)
	if err != nil {
		return err
	}
	for _, change := range diff.Changed {
		if err := ctx.Err(); err != nil {
			return err
		}
		row, err := fold(ctx, change.ID, change.Tip)
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if row.Version != queryMetadataVersion || row.Binding != binding || row.Kind != kind || row.ID != change.ID || row.Tip != change.Tip {
			return fmt.Errorf("folded metadata identity differs from captured %s %s %s", binding, kind, change.ID)
		}
		if err := replaceQueryMetadata(tx, row); err != nil {
			return err
		}
	}
	for _, id := range diff.Removed {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := deleteQueryMetadata(tx, binding, id); err != nil {
			return err
		}
	}
	return ctx.Err()
}
