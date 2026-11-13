package store

import (
	"context"
	"encoding/json"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
	"github.com/yasyf/cc-notes/internal/sourceview"
	"github.com/yasyf/cc-notes/model"
)

func querySourceKey(binding string) []byte {
	return []byte("query-source/" + binding)
}

func (q queryMetadataSource) synchronizeSource(ctx context.Context, tx *bolt.Tx, writable bool) (bool, error) {
	if q.kind != model.KindAnswer {
		return true, nil
	}
	root, err := managedRoot(tx)
	if err != nil {
		return false, err
	}
	key := querySourceKey(q.binding)
	token := model.SHA(root.Get(key))
	if token == "" {
		return false, managed.ErrReindex
	}
	after, err := sourceRevision(tx, "")
	if err != nil {
		return false, err
	}
	if token == after.Token {
		return true, nil
	}
	if !writable {
		return false, nil
	}
	before, err := sourceRevision(tx, token)
	if err != nil {
		return false, err
	}
	batch := make(queryGenerationBatch)
	var updates []gitcmd.RefUpdate
	err = q.store.Repo.SourceObjects(ctx, func(scope *gitobj.SourceObjects) error {
		return sourcegraph.New(scope).Diff(before.EntityRoot, after.EntityRoot, func(change sourcegraph.Change) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			parsed, err := refs.Parse(change.Key)
			if err != nil {
				return err
			}
			if parsed.Kind != q.kind {
				return nil
			}
			if !change.AfterExists {
				return batch.deleteMetadata(tx, q.binding, parsed.ID)
			}
			var entity sourceview.Entity
			if err := json.Unmarshal(change.After, &entity); err != nil {
				return err
			}
			if tips := queryMetadataKindTips(tx, q.binding, q.kind); tips != nil {
				if tip := tips.Get([]byte(parsed.ID)); len(tip) > 0 && model.SHA(tip[1:]) == entity.Tip {
					return nil
				}
			}
			updates = append(updates, gitcmd.RefUpdate{Ref: change.Key, New: entity.Tip})
			return nil
		})
	})
	if err != nil {
		return false, err
	}
	rows, err := q.store.managedRows(ctx, updates, nil, nil)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if err := batch.replaceMetadata(tx, row); err != nil {
			return false, err
		}
	}
	if err := batch.flush(ctx, tx); err != nil {
		return false, err
	}
	return true, root.Put(key, []byte(after.Token))
}
