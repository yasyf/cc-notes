package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	bolt "go.etcd.io/bbolt"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/managed"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourcegraph"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/internal/sourceview"
	"github.com/yasyf/cc-notes/model"
)

// CommitSourceOperation checks stable identity before the source fence under the publication lease.
func (s *Store) CommitSourceOperation(ctx context.Context, request sourceindex.CommitRequest) (operation sourceindex.Operation, err error) {
	ref, err := sourceindex.OperationRef(request.OperationID)
	if err != nil {
		return operation, err
	}
	var preparation *managedPreparation
	var preparationErr error
	if _, found, readErr := s.ReadOperation(ctx, request.OperationID); readErr != nil {
		preparationErr = readErr
	} else if !found {
		preparation, preparationErr = s.prepareManaged(ctx, request.Updates, request.Snapshots)
	}
	err = s.managedWrite(ctx, func(db *bolt.DB) (*managedIntent, error) {
		var current sourceindex.Revision
		var existing sourceindex.Operation
		var found bool
		if err := db.View(func(tx *bolt.Tx) error {
			var err error
			existing, found, err = sourceOperation(tx, request.OperationID)
			if err != nil || found {
				return err
			}
			current, err = sourceRevision(tx, "")
			return err
		}); err != nil {
			return nil, err
		}
		if found {
			if existing.RequestDigest != request.RequestDigest || existing.State == gitobj.SourceOperationForgotten {
				return nil, sourceindex.ErrOperationExists
			}
			operation = existing
			return nil, nil
		}
		var expected sourceindex.Revision
		if err := db.View(func(tx *bolt.Tx) error {
			var err error
			expected, err = sourceRevision(tx, request.Expected)
			return err
		}); err != nil {
			return nil, err
		}
		if current.Token != request.Expected || current.EntityGeneration != expected.EntityGeneration {
			return nil, &sourceindex.StaleRevisionError{Expected: request.Expected, Actual: current.Token}
		}
		if len(request.Updates) == 0 {
			return nil, errors.New("source operation has no entity updates")
		}
		changes := sourceindex.Changes{}
		seen := make(map[string]struct{}, len(request.Updates))
		if err := s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
			index := sourcegraph.New(objects)
			for _, update := range request.Updates {
				if _, err := refs.Parse(update.Ref); err != nil {
					return err
				}
				if _, found := seen[update.Ref]; found {
					return fmt.Errorf("duplicate source entity %s", update.Ref)
				}
				seen[update.Ref] = struct{}{}
				data, found, err := index.Get(current.EntityRoot, update.Ref)
				if err != nil {
					return err
				}
				var old sourceview.Entity
				if found {
					if err := json.Unmarshal(data, &old); err != nil {
						return err
					}
				}
				if old.Tip != update.Old && (old.Tip != "" || !sourceZero(update.Old)) {
					return &sourceindex.StaleRevisionError{Expected: request.Expected, Actual: current.Token}
				}
				if update.New == "" {
					changes.Deletes = append(changes.Deletes, update.Ref)
				} else if update.New != old.Tip {
					changes.Upserts = append(changes.Upserts, gitobj.SourceRefTip{Ref: update.Ref, Tip: update.New})
				}
			}
			return nil
		}); err != nil {
			return nil, err
		}
		slices.SortFunc(changes.Upserts, func(a, b gitobj.SourceRefTip) int { return strings.Compare(a.Ref, b.Ref) })
		slices.Sort(changes.Deletes)
		if preparationErr != nil {
			return nil, preparationErr
		}
		intent, err := s.buildManagedIntent(ctx, db, request.Updates, preparation)
		if err != nil {
			return nil, err
		}
		next := current
		if intent.Source != nil {
			next = *intent.Source.Revision
		} else {
			intent.Source = &sourcePublication{}
		}
		if next.RetentionConflicts != 0 {
			return nil, errors.New("source retained content has invalid LFS declarations or unavailable bytes")
		}
		if err := sourceProjection(next); err != nil {
			return nil, err
		}
		operation = sourceindex.Operation{Token: next.Token, Previous: request.Expected, Result: request.Result, RequestDigest: request.RequestDigest, State: gitobj.SourceOperationApplied, Changes: changes, RetentionRoot: next.RetentionRoot}
		proof, err := s.writeManagedSourceProof(ctx, request.OperationID, operation)
		if err != nil {
			return nil, err
		}
		operation.Proof, operation.Pin = proof, proof
		intent.Source.OperationID, intent.Source.Operation = request.OperationID, &operation
		intent.Updates = append(intent.Updates, gitcmd.RefUpdate{Ref: ref, New: proof}, gitcmd.RefUpdate{Ref: sourceindex.OperationPinRef(request.OperationID), New: proof})
		return intent, nil
	})
	return operation, err
}

// SettleSourceOperation atomically replaces one proof and releases only its exact pin.
func (s *Store) SettleSourceOperation(ctx context.Context, id string, request, receipt [sha256.Size]byte, desired gitobj.SourceOperationState) (operation sourceindex.Operation, err error) {
	if desired != gitobj.SourceOperationAcknowledged && desired != gitobj.SourceOperationForgotten || receipt == ([sha256.Size]byte{}) {
		return operation, sourceindex.ErrOperationState
	}
	ref, err := sourceindex.OperationRef(id)
	if err != nil {
		return operation, err
	}
	err = s.managedWrite(ctx, func(db *bolt.DB) (*managedIntent, error) {
		var found bool
		if err := db.View(func(tx *bolt.Tx) error { var err error; operation, found, err = sourceOperation(tx, id); return err }); err != nil {
			return nil, err
		}
		if !found || operation.RequestDigest != request {
			return nil, sourceindex.ErrOperationState
		}
		if operation.State == desired && operation.ReceiptDigest == receipt {
			return nil, nil
		}
		if desired == gitobj.SourceOperationAcknowledged && operation.State != gitobj.SourceOperationApplied || desired == gitobj.SourceOperationForgotten && operation.State != gitobj.SourceOperationAcknowledged {
			return nil, sourceindex.ErrOperationState
		}
		if operation.State == gitobj.SourceOperationAcknowledged && operation.ReceiptDigest != receipt {
			return nil, sourceindex.ErrOperationState
		}
		oldProof, oldPin := operation.Proof, operation.Pin
		operation.State, operation.ReceiptDigest = desired, receipt
		operation.RetentionRoot = ""
		operation.Pin = ""
		if desired == gitobj.SourceOperationForgotten {
			operation.Token = ""
			operation.Previous = ""
			operation.Result = ""
			operation.Changes = sourceindex.Changes{}
		}
		proof, err := s.writeManagedSourceProof(ctx, id, operation)
		if err != nil {
			return nil, err
		}
		operation.Proof = proof
		updates := []gitcmd.RefUpdate{{Ref: ref, New: proof, Old: oldProof}}
		if desired == gitobj.SourceOperationAcknowledged {
			if oldPin == "" {
				return nil, managed.ErrReindex
			}
			updates = append(updates, gitcmd.RefUpdate{Ref: sourceindex.OperationPinRef(id), Old: oldPin})
		}
		return &managedIntent{Updates: updates, Source: &sourcePublication{OperationID: id, Operation: &operation}}, nil
	})
	return operation, err
}

func (s *Store) writeManagedSourceProof(ctx context.Context, id string, operation sourceindex.Operation) (sha model.SHA, err error) {
	if err := validateManagedSourceOperation(id, operation); err != nil {
		return "", err
	}
	err = s.Repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
		body := struct {
			Format      string
			OperationID string
			Operation   sourceindex.Operation
		}{"cc-notes-source-operation-v2", id, operation}
		body.Operation.Proof = ""
		body.Operation.Pin = ""
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		blob, err := objects.WriteBlob(data)
		if err != nil {
			return err
		}
		tree, err := objects.WriteTree([]object.TreeEntry{{Name: "source-operation-v2", Mode: filemode.Regular, Hash: plumbing.NewHash(string(blob))}})
		if err != nil {
			return err
		}
		var parents []model.SHA
		when := time.Unix(0, 0).UTC()
		if operation.State != gitobj.SourceOperationForgotten {
			parents = append(parents, operation.Token)
		}
		if operation.State == gitobj.SourceOperationApplied {
			if operation.RetentionRoot != "" {
				parents = append(parents, operation.RetentionRoot)
			}
			when = sourcegraph.RetentionTime
		}
		identity := gitobj.Signature{Name: "CC Notes Source Driver", Email: "source-driver@cc-notes.invalid", When: when}
		sha, err = objects.WriteCommit(gitobj.SourceCommit{Tree: tree, Parents: parents, Author: identity, Committer: identity, Message: "cc-notes: source operation v2"})
		return err
	})
	return
}

func sourceZero(sha model.SHA) bool {
	for _, c := range sha {
		if c != '0' {
			return false
		}
	}
	return true
}

func validateManagedSourceOperation(id string, operation sourceindex.Operation) error {
	if _, err := sourceindex.OperationRef(id); err != nil {
		return err
	}
	if strings.ContainsAny(operation.Result, "\x00\n\r") {
		return errors.New("source operation result is invalid")
	}
	zero := [sha256.Size]byte{}
	if operation.RequestDigest == zero {
		return errors.New("source operation request digest is empty")
	}
	if operation.Proof != "" && !plumbing.IsHash(string(operation.Proof)) || operation.Pin != "" && !plumbing.IsHash(string(operation.Pin)) {
		return errors.New("source operation proof identity is invalid")
	}
	switch operation.State {
	case gitobj.SourceOperationApplied:
		if !plumbing.IsHash(string(operation.Previous)) || !plumbing.IsHash(string(operation.Token)) {
			return errors.New("source operation revisions are invalid")
		}
		if operation.ReceiptDigest != zero {
			return errors.New("applied source operation receipt digest must be empty")
		}
		if operation.RetentionRoot != "" && (!plumbing.IsHash(string(operation.RetentionRoot)) || operation.RetentionRoot == operation.Token) {
			return errors.New("source operation retention root is invalid")
		}
	case gitobj.SourceOperationAcknowledged:
		if !plumbing.IsHash(string(operation.Previous)) || !plumbing.IsHash(string(operation.Token)) {
			return errors.New("source operation revisions are invalid")
		}
		if operation.ReceiptDigest == zero || operation.RetentionRoot != "" || operation.Pin != "" {
			return errors.New("acknowledged source operation proof is invalid")
		}
	case gitobj.SourceOperationForgotten:
		if operation.Previous != "" || operation.Token != "" || operation.Result != "" || operation.RetentionRoot != "" || operation.Pin != "" || operation.ReceiptDigest == zero || len(operation.Changes.Upserts) != 0 || len(operation.Changes.Deletes) != 0 {
			return errors.New("forgotten source operation proof is invalid")
		}
	default:
		return errors.New("source operation state is invalid")
	}
	previous := ""
	for _, entry := range operation.Changes.Upserts {
		if _, err := refs.Parse(entry.Ref); err != nil {
			return err
		}
		if !plumbing.IsHash(string(entry.Tip)) || previous != "" && entry.Ref <= previous {
			return errors.New("source operation upserts are not canonical")
		}
		previous = entry.Ref
	}
	previous = ""
	for _, ref := range operation.Changes.Deletes {
		if _, err := refs.Parse(ref); err != nil {
			return err
		}
		if previous != "" && ref <= previous {
			return errors.New("source operation deletes are not canonical")
		}
		previous = ref
		_, found := slices.BinarySearchFunc(operation.Changes.Upserts, ref, func(entry gitobj.SourceRefTip, target string) int { return strings.Compare(entry.Ref, target) })
		if found {
			return errors.New("source operation delta both replaces and deletes one ref")
		}
	}
	return nil
}
