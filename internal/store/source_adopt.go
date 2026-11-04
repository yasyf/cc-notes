package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/internal/sourceview"
	"github.com/yasyf/cc-notes/model"
)

func (s *Store) adoptSourceHead(ctx context.Context, db *bolt.DB) error {
	head, err := s.sourceRefTip(ctx, sourceindex.Ref)
	if err != nil || head == "" {
		return err
	}
	var stored sourceindex.Revision
	if err := db.View(func(tx *bolt.Tx) error {
		root, err := managedRoot(tx)
		if err != nil || model.SHA(root.Get(sourceHeadKey)) == head {
			return err
		}
		stored, err = sourceRevision(tx, "")
		return err
	}); err != nil || stored.Token == "" {
		return err
	}
	unrelated := fmt.Errorf("source head %s does not descend from managed head %s: %w", head, stored.Token, managed.ErrReindex)
	var chain []sourceindex.Revision
	var updates []gitcmd.RefUpdate
	if err := s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
		for token := head; token != stored.Token; {
			commit, err := objects.ReadCommit(token)
			if err != nil {
				return err
			}
			revision, err := readSourceRevisionTree(objects, commit.Tree, token)
			if err != nil {
				return err
			}
			if revision.Height <= stored.Height || len(commit.Parents) != 1 || commit.Parents[0] != revision.Predecessor {
				return unrelated
			}
			chain = append(chain, revision)
			token = revision.Predecessor
		}
		return sourcegraph.New(objects).Diff(stored.EntityRoot, chain[0].EntityRoot, func(change sourcegraph.Change) error {
			var before, after sourceview.Entity
			if change.BeforeExists {
				if err := json.Unmarshal(change.Before, &before); err != nil {
					return err
				}
			}
			if change.AfterExists {
				if err := json.Unmarshal(change.After, &after); err != nil {
					return err
				}
			}
			if before.Tip != after.Tip {
				updates = append(updates, gitcmd.RefUpdate{Ref: change.Key, Old: before.Tip, New: after.Tip})
			}
			return nil
		})
	}); err != nil {
		return err
	}
	rows, err := s.managedRows(ctx, updates, nil, nil)
	if err != nil {
		return err
	}
	operations, err := s.adoptedSourceOperations(ctx, db)
	if err != nil {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		if err := s.validateManagedQueryGenerations(tx, updates); err != nil {
			return err
		}
		if err := s.applyManaged(ctx, tx, managedIntent{Updates: updates, Rows: rows, Source: &sourcePublication{Revision: &chain[0], Revisions: chain[1:]}}); err != nil {
			return err
		}
		root, err := managedRoot(tx)
		if err != nil {
			return err
		}
		for id, operation := range operations {
			data, err := json.Marshal(operation)
			if err != nil {
				return err
			}
			if err := root.Bucket(sourceOperationsBucket).Put([]byte(id), data); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) adoptedSourceOperations(ctx context.Context, db *bolt.DB) (map[string]sourceindex.Operation, error) {
	proofs, err := s.Repo.ListPrefix(ctx, sourceindex.OperationPrefix)
	if err != nil {
		return nil, err
	}
	stale := make(map[string]model.SHA)
	if err := db.View(func(tx *bolt.Tx) error {
		for ref, proof := range proofs {
			id := strings.TrimPrefix(ref, sourceindex.OperationPrefix)
			known, _, err := sourceOperation(tx, id)
			if err != nil {
				return err
			}
			if known.Proof != proof {
				stale[id] = proof
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	operations := make(map[string]sourceindex.Operation, len(stale))
	for id, proof := range stale {
		operation, modern, err := s.readManagedSourceProof(ctx, proof, id)
		if err != nil {
			return nil, fmt.Errorf("adopt receipt %s: %w", id, err)
		}
		if !modern {
			return nil, fmt.Errorf("adopt receipt %s: legacy proof: %w", id, managed.ErrReindex)
		}
		if operation.State == gitobj.SourceOperationApplied {
			pin, err := s.sourceRefTip(ctx, sourceindex.OperationPinRef(id))
			if err != nil {
				return nil, err
			}
			if pin != proof {
				return nil, fmt.Errorf("adopt Applied receipt %s: content pin differs: %w", id, managed.ErrReindex)
			}
			operation.Pin = pin
		}
		operations[id] = operation
	}
	return operations, nil
}
