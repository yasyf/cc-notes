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

type sourceAdoption struct {
	base       sourceindex.Revision
	chain      []sourceindex.Revision
	updates    []gitcmd.RefUpdate
	entities   map[string]sourceview.Entity
	rows       []queryMetadata
	operations map[string]sourceindex.Operation
}

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
	adoption, err := s.prepareSourceAdoption(ctx, db, head, stored)
	if err != nil {
		return err
	}
	return db.Update(func(tx *bolt.Tx) error {
		_, err := s.applySourceAdoption(ctx, tx, adoption, stored)
		return err
	})
}

func (s *Store) prepareSourceAdoption(ctx context.Context, db *bolt.DB, head model.SHA, base sourceindex.Revision) (*sourceAdoption, error) {
	adoption := &sourceAdoption{base: base, entities: make(map[string]sourceview.Entity)}
	unrelated := fmt.Errorf("source head %s does not descend from managed head %s: %w", head, base.Token, managed.ErrReindex)
	if err := s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
		for token := head; token != base.Token; {
			commit, err := objects.ReadCommit(token)
			if err != nil {
				return err
			}
			revision, err := readSourceRevisionTree(objects, commit.Tree, token)
			if err != nil {
				return err
			}
			if revision.Height <= base.Height || len(commit.Parents) > 1 || len(commit.Parents) == 0 && revision.Predecessor != "" || len(commit.Parents) == 1 && commit.Parents[0] != revision.Predecessor {
				return unrelated
			}
			adoption.chain = append(adoption.chain, revision)
			token = revision.Predecessor
		}
		return sourcegraph.New(objects).Diff(base.EntityRoot, adoption.chain[0].EntityRoot, func(change sourcegraph.Change) error {
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
			if before.Tip == after.Tip {
				return nil
			}
			adoption.updates = append(adoption.updates, gitcmd.RefUpdate{Ref: change.Key, Old: before.Tip, New: after.Tip})
			if change.AfterExists {
				adoption.entities[change.Key] = after
			}
			return nil
		})
	}); err != nil {
		return nil, err
	}
	var err error
	if adoption.rows, err = s.managedRows(ctx, adoption.updates, nil, nil); err != nil {
		return nil, err
	}
	if adoption.operations, err = s.adoptedSourceOperations(ctx, db); err != nil {
		return nil, err
	}
	return adoption, nil
}

func (s *Store) applySourceAdoption(ctx context.Context, tx *bolt.Tx, adoption *sourceAdoption, built sourceindex.Revision) ([]gitcmd.RefUpdate, error) {
	if err := s.validateManagedQueryGenerations(tx, adoption.updates); err != nil {
		return nil, err
	}
	head := adoption.chain[0]
	publication := &sourcePublication{Revision: &head, Revisions: adoption.chain[1:]}
	var derived []gitcmd.RefUpdate
	if built.Token != adoption.base.Token {
		if err := applySource(tx, &sourcePublication{Revisions: adoption.chain}); err != nil {
			return nil, err
		}
		content := built
		content.EntityGeneration = head.EntityGeneration + built.EntityGeneration - adoption.base.EntityGeneration
		var err error
		if publication, derived, err = s.extendSource(ctx, tx, head, content, changedSourceRefs(tx, adoption.updates), adoption.entities); err != nil {
			return nil, err
		}
	}
	if err := s.applyManaged(ctx, tx, managedIntent{Updates: adoption.updates, Rows: adoption.rows, Source: publication}); err != nil {
		return nil, err
	}
	root, err := managedRoot(tx)
	if err != nil {
		return nil, err
	}
	for id, operation := range adoption.operations {
		data, err := json.Marshal(operation)
		if err != nil {
			return nil, err
		}
		if err := root.Bucket(sourceOperationsBucket).Put([]byte(id), data); err != nil {
			return nil, err
		}
	}
	return derived, nil
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
