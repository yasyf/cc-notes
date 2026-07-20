// Package sourceindex derives one immutable, causally linked source revision
// from cc-notes' independently versioned entity refs.
package sourceindex

import (
	"cmp"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/lfs"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

const (
	// Ref is the local, derived source-revision head. It deliberately lives
	// outside refs.Namespace so entity enumeration and wildcard sync never
	// parse or publish it as user state.
	Ref = "refs/cc-notes-source-v1/head"

	maxAttempts = 16
)

var (
	// ErrContended reports repeated source-head movement during refresh.
	ErrContended = errors.New("source index contended")
	// ErrOperationExists reports an already committed operation identity.
	ErrOperationExists = errors.New("source operation already committed")
	// ErrNotAncestor reports a delta request with no causal predecessor relation.
	ErrNotAncestor = errors.New("source revision is not an ancestor")
	// ErrOperationState reports a receipt transition that does not match durable state.
	ErrOperationState = errors.New("source operation state conflict")
)

// Index binds immutable object access to real-Git atomic ref transactions.
type Index struct {
	Repo *gitobj.Repo
	Git  gitcmd.Git
}

// Changes is the exact logical ref-tip delta between two source revisions.
type Changes struct {
	Upserts gitobj.SourceManifest
	Deletes []string
}

// Operation is one durably committed mutation proof. Token is the exact
// source revision atomically published with the entity ref changes.
type Operation struct {
	Proof         model.SHA
	Token         model.SHA
	Previous      model.SHA
	Result        string
	RequestDigest [sha256.Size]byte
	ReceiptDigest [sha256.Size]byte
	State         gitobj.SourceOperationState
	Changes       Changes
}

// Refresh seals the current entity refs as a new source revision when their
// exact manifest changed. An unchanged manifest returns the existing token.
func (i Index) Refresh(ctx context.Context) (model.SHA, error) {
	if err := i.validate(); err != nil {
		return "", err
	}
	for range maxAttempts {
		desired, err := i.liveManifest(ctx)
		if err != nil {
			return "", err
		}
		head, err := i.head(ctx)
		if err != nil {
			return "", err
		}
		if head != "" {
			current, _, err := i.Repo.ReadSourceManifestCommit(ctx, head)
			if err != nil {
				return "", err
			}
			if current.Equal(desired) {
				confirmed, err := i.liveManifest(ctx)
				if err != nil {
					return "", err
				}
				if confirmed.Equal(desired) {
					return head, nil
				}
				continue
			}
		}
		next, err := i.Repo.WriteSourceManifestCommit(ctx, head, desired)
		if err != nil {
			return "", err
		}
		if err := i.Git.UpdateRef(ctx, Ref, next, head); err != nil {
			if errors.Is(err, gitcmd.ErrCASMismatch) {
				runtime.Gosched()
				continue
			}
			return "", fmt.Errorf("advance source index: %w", err)
		}
		confirmed, err := i.liveManifest(ctx)
		if err != nil {
			return "", err
		}
		if confirmed.Equal(desired) {
			return next, nil
		}
	}
	return "", ErrContended
}

// Snapshot returns the immutable manifest sealed by token.
func (i Index) Snapshot(ctx context.Context, token model.SHA) (gitobj.SourceManifest, error) {
	if err := i.validate(); err != nil {
		return nil, err
	}
	manifest, _, err := i.Repo.ReadSourceManifestCommit(ctx, token)
	if err != nil {
		return nil, err
	}
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// ChangesSince returns the exact changed and removed entity refs between two
// causally ordered source revisions.
func (i Index) ChangesSince(ctx context.Context, from, to model.SHA) (Changes, error) {
	if err := i.validate(); err != nil {
		return Changes{}, err
	}
	ancestor, err := i.Repo.IsAncestor(ctx, from, to)
	if err != nil {
		return Changes{}, fmt.Errorf("source index ancestry: %w", err)
	}
	if !ancestor {
		return Changes{}, fmt.Errorf("source index revision %s is not an ancestor of %s: %w", from, to, ErrNotAncestor)
	}
	before, err := i.Snapshot(ctx, from)
	if err != nil {
		return Changes{}, err
	}
	after, err := i.Snapshot(ctx, to)
	if err != nil {
		return Changes{}, err
	}
	old := before.Tips()
	changes := Changes{}
	for _, entry := range after {
		if tip, found := old[entry.Ref]; !found || tip != entry.Tip {
			changes.Upserts = append(changes.Upserts, entry)
		}
		delete(old, entry.Ref)
	}
	for ref := range old {
		changes.Deletes = append(changes.Deletes, ref)
	}
	slices.Sort(changes.Deletes)
	return changes, nil
}

// CommitOperation atomically moves entity refs, the derived source head, and
// an operation-proof ref from one exact source revision. Object creation
// happens before this call; a failed comparison leaves every ref untouched.
// The returned token also incorporates unrelated external ref movement
// observed immediately after the commit.
func (i Index) CommitOperation(
	ctx context.Context,
	expected model.SHA,
	operationID, result string,
	requestDigest [sha256.Size]byte,
	updates []gitcmd.RefUpdate,
) (model.SHA, error) {
	if err := i.validate(); err != nil {
		return "", err
	}
	operationRef, err := operationRef(operationID)
	if err != nil {
		return "", err
	}
	if _, err := i.Repo.Tip(ctx, operationRef); err == nil {
		return "", ErrOperationExists
	} else if !errors.Is(err, gitobj.ErrRefNotFound) {
		return "", fmt.Errorf("source index commit: resolve operation: %w", err)
	}
	if expected == "" {
		return "", errors.New("source index commit: expected revision is required")
	}
	if len(updates) == 0 {
		return "", errors.New("source index commit: no entity updates")
	}
	head, err := i.head(ctx)
	if err != nil {
		return "", err
	}
	if head != expected {
		return "", fmt.Errorf("source index commit: %w: head is %s, expected %s", gitcmd.ErrCASMismatch, head, expected)
	}
	manifest, err := i.Snapshot(ctx, expected)
	if err != nil {
		return "", err
	}
	live, err := i.liveManifest(ctx)
	if err != nil {
		return "", err
	}
	if !live.Equal(manifest) {
		return "", fmt.Errorf("source index commit: %w: entity refs advanced before revision", gitcmd.ErrCASMismatch)
	}
	tips := manifest.Tips()
	seen := make(map[string]struct{}, len(updates))
	transaction := make([]gitcmd.RefUpdate, 0, len(updates)+1)
	for _, update := range updates {
		if _, duplicate := seen[update.Ref]; duplicate {
			return "", fmt.Errorf("source index commit: duplicate ref %s", update.Ref)
		}
		seen[update.Ref] = struct{}{}
		if _, err := refs.Parse(update.Ref); err != nil {
			return "", fmt.Errorf("source index commit: %w", err)
		}
		current, found := tips[update.Ref]
		if found {
			if current != update.Old {
				return "", fmt.Errorf("source index commit: %w: %s is %s, expected %s", gitcmd.ErrCASMismatch, update.Ref, current, update.Old)
			}
		} else if !zero(update.Old) {
			return "", fmt.Errorf("source index commit: %w: %s does not exist", gitcmd.ErrCASMismatch, update.Ref)
		}
		tips[update.Ref] = update.New
		transaction = append(transaction, update)
	}
	nextManifest, err := gitobj.NewSourceManifest(tips)
	if err != nil {
		return "", err
	}
	next, err := i.Repo.WriteSourceManifestCommit(ctx, expected, nextManifest)
	if err != nil {
		return "", err
	}
	retainedLFS, err := i.retainedLFS(ctx, nextManifest)
	if err != nil {
		return "", err
	}
	proof, err := i.Repo.WriteSourceOperationProof(ctx, gitobj.SourceOperationProof{
		OperationID: operationID, State: gitobj.SourceOperationApplied, Expected: expected, Committed: next,
		Result: result, RequestDigest: requestDigest, RetainedTips: retainedTips(nextManifest), RetainedLFS: retainedLFS,
	})
	if err != nil {
		return "", err
	}
	transaction = append(transaction,
		gitcmd.RefUpdate{Ref: Ref, New: next, Old: expected},
		gitcmd.RefUpdate{Ref: operationRef, New: proof},
		gitcmd.RefUpdate{Ref: operationPinRef(operationID), New: proof},
	)
	if err := i.Git.UpdateRefs(ctx, transaction); err != nil {
		return "", fmt.Errorf("source index commit: %w", err)
	}
	return i.Refresh(ctx)
}

// InspectOperation resolves one O(1) durable mutation proof. Forgotten
// receipts are hidden while their operation-id tombstones remain durable.
func (i Index) InspectOperation(ctx context.Context, operationID string) (operation Operation, found bool, err error) {
	operation, found, err = i.readOperation(ctx, operationID)
	if err != nil || !found || operation.State != gitobj.SourceOperationForgotten {
		return operation, found, err
	}
	return Operation{}, false, nil
}

// InspectOperationState resolves the durable proof including a forgotten
// operation-id tombstone for exact settlement replay.
func (i Index) InspectOperationState(ctx context.Context, operationID string) (Operation, bool, error) {
	return i.readOperation(ctx, operationID)
}

// SettleOperation advances one exact applied receipt through acknowledged and
// forgotten states. A forgotten proof remains as the operation-id tombstone.
func (i Index) SettleOperation(
	ctx context.Context,
	operationID string,
	requestDigest [sha256.Size]byte,
	receiptDigest [sha256.Size]byte,
	desired gitobj.SourceOperationState,
) (Operation, error) {
	if desired != gitobj.SourceOperationAcknowledged && desired != gitobj.SourceOperationForgotten || receiptDigest == ([sha256.Size]byte{}) {
		return Operation{}, ErrOperationState
	}
	for range maxAttempts {
		operation, found, err := i.readOperation(ctx, operationID)
		if err != nil {
			return Operation{}, err
		}
		if !found || operation.RequestDigest != requestDigest {
			return Operation{}, ErrOperationState
		}
		if operation.State == desired && operation.ReceiptDigest == receiptDigest {
			return operation, nil
		}
		if desired == gitobj.SourceOperationAcknowledged && operation.State != gitobj.SourceOperationApplied ||
			desired == gitobj.SourceOperationForgotten && operation.State != gitobj.SourceOperationAcknowledged {
			return Operation{}, ErrOperationState
		}
		if operation.State == gitobj.SourceOperationAcknowledged && operation.ReceiptDigest != receiptDigest {
			return Operation{}, ErrOperationState
		}
		next := gitobj.SourceOperationProof{
			OperationID: operationID, State: desired,
			Expected: operation.Previous, Committed: operation.Token,
			Result: operation.Result, RequestDigest: operation.RequestDigest, ReceiptDigest: receiptDigest,
		}
		if desired == gitobj.SourceOperationForgotten {
			next.Expected = ""
			next.Committed = ""
			next.Result = ""
		}
		proof, err := i.Repo.WriteSourceOperationProof(ctx, next)
		if err != nil {
			return Operation{}, err
		}
		ref, _ := operationRef(operationID)
		updates := []gitcmd.RefUpdate{{Ref: ref, New: proof, Old: operation.Proof}}
		if desired == gitobj.SourceOperationAcknowledged {
			pin, pinErr := i.Repo.Tip(ctx, operationPinRef(operationID))
			if pinErr != nil || pin != operation.Proof {
				return Operation{}, fmt.Errorf("settle source operation: applied content pin differs: %w", errors.Join(pinErr, ErrOperationState))
			}
			updates = append(updates, gitcmd.RefUpdate{Ref: operationPinRef(operationID), Old: operation.Proof})
		}
		if err := i.Git.UpdateRefs(ctx, updates); err != nil {
			if errors.Is(err, gitcmd.ErrCASMismatch) {
				runtime.Gosched()
				continue
			}
			return Operation{}, fmt.Errorf("settle source operation: %w", err)
		}
		operation.Proof = proof
		operation.State = desired
		operation.ReceiptDigest = receiptDigest
		if desired == gitobj.SourceOperationForgotten {
			operation.Token = ""
			operation.Previous = ""
			operation.Result = ""
			operation.Changes = Changes{}
		}
		return operation, nil
	}
	return Operation{}, ErrContended
}

func (i Index) readOperation(ctx context.Context, operationID string) (operation Operation, found bool, err error) {
	if err := i.validate(); err != nil {
		return Operation{}, false, err
	}
	ref, err := operationRef(operationID)
	if err != nil {
		return Operation{}, false, err
	}
	proofToken, err := i.Repo.Tip(ctx, ref)
	if errors.Is(err, gitobj.ErrRefNotFound) {
		return Operation{}, false, nil
	}
	if err != nil {
		return Operation{}, false, fmt.Errorf("inspect source operation: %w", err)
	}
	proof, err := i.Repo.ReadSourceOperationProof(ctx, proofToken)
	if err != nil {
		return Operation{}, false, err
	}
	if proof.OperationID != operationID {
		return Operation{}, false, errors.New("inspect source operation: proof identity differs from ref")
	}
	pin, pinErr := i.Repo.Tip(ctx, operationPinRef(operationID))
	if proof.State == gitobj.SourceOperationApplied {
		if pinErr != nil || pin != proofToken {
			return Operation{}, false, fmt.Errorf("inspect source operation: applied content pin differs: %w", errors.Join(pinErr, ErrOperationState))
		}
	} else if !errors.Is(pinErr, gitobj.ErrRefNotFound) {
		if pinErr != nil {
			return Operation{}, false, fmt.Errorf("inspect source operation: resolve content pin: %w", pinErr)
		}
		return Operation{}, false, errors.New("inspect source operation: settled content pin remains")
	}
	if proof.State == gitobj.SourceOperationForgotten {
		return Operation{
			Proof: proofToken, RequestDigest: proof.RequestDigest, ReceiptDigest: proof.ReceiptDigest, State: proof.State,
		}, true, nil
	}
	changes, err := i.ChangesSince(ctx, proof.Expected, proof.Committed)
	if err != nil {
		return Operation{}, false, err
	}
	return Operation{
		Proof: proofToken, Token: proof.Committed, Previous: proof.Expected, Result: proof.Result,
		RequestDigest: proof.RequestDigest, ReceiptDigest: proof.ReceiptDigest, State: proof.State, Changes: changes,
	}, true, nil
}

func retainedTips(manifest gitobj.SourceManifest) []model.SHA {
	unique := make(map[model.SHA]struct{}, len(manifest))
	for _, entry := range manifest {
		unique[entry.Tip] = struct{}{}
	}
	tips := make([]model.SHA, 0, len(unique))
	for tip := range unique {
		tips = append(tips, tip)
	}
	slices.Sort(tips)
	return tips
}

func (i Index) retainedLFS(ctx context.Context, manifest gitobj.SourceManifest) ([]gitobj.SourceOperationLFS, error) {
	byOID := make(map[string]int64)
	for _, entry := range manifest {
		chain, err := i.Repo.ReadChain(ctx, entry.Tip)
		if err != nil {
			return nil, fmt.Errorf("source index retained content: %w", err)
		}
		snapshot, err := fold.Fold(chain)
		if err != nil {
			return nil, fmt.Errorf("source index retained content: fold %s: %w", entry.Ref, err)
		}
		if snapshot.Meta().Deleted {
			continue
		}
		for _, attachment := range snapshot.Meta().Attachments {
			if size, found := byOID[attachment.OID]; found && size != attachment.Size {
				return nil, errors.New("source index retained content: LFS size differs for one object")
			}
			byOID[attachment.OID] = attachment.Size
		}
	}
	retained := make([]gitobj.SourceOperationLFS, 0, len(byOID))
	for oid, size := range byOID {
		retained = append(retained, gitobj.SourceOperationLFS{OID: oid, Size: size})
	}
	slices.SortFunc(retained, func(left, right gitobj.SourceOperationLFS) int {
		return cmp.Compare(left.OID, right.OID)
	})
	if len(retained) == 0 {
		return nil, nil
	}
	common, err := i.Git.CommonDir(ctx)
	if err != nil {
		return nil, fmt.Errorf("source index retained content: %w", err)
	}
	content := lfs.Store{Dir: filepath.Join(common, "lfs")}
	for _, object := range retained {
		info, err := os.Stat(content.Path(object.OID))
		if err != nil {
			return nil, fmt.Errorf("source index retained content: LFS object %s: %w", object.OID, err)
		}
		if info.Size() != object.Size {
			return nil, fmt.Errorf("source index retained content: LFS object %s size is %d, want %d", object.OID, info.Size(), object.Size)
		}
	}
	return retained, nil
}

func (i Index) liveManifest(ctx context.Context) (gitobj.SourceManifest, error) {
	tips, err := i.Repo.ListPrefix(ctx, refs.Namespace)
	if err != nil {
		return nil, fmt.Errorf("list source refs: %w", err)
	}
	manifest, err := gitobj.NewSourceManifest(tips)
	if err != nil {
		return nil, err
	}
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

func (i Index) head(ctx context.Context) (model.SHA, error) {
	head, err := i.Repo.Tip(ctx, Ref)
	if errors.Is(err, gitobj.ErrRefNotFound) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve source index: %w", err)
	}
	return head, nil
}

func (i Index) validate() error {
	if i.Repo == nil {
		return errors.New("source index: git object repository is required")
	}
	if i.Git.Dir == "" {
		return errors.New("source index: git command directory is required")
	}
	return nil
}

func validateManifest(manifest gitobj.SourceManifest) error {
	for _, entry := range manifest {
		if _, err := refs.Parse(entry.Ref); err != nil {
			return fmt.Errorf("source index: %w", err)
		}
	}
	return nil
}

func zero(sha model.SHA) bool {
	if sha == "" {
		return true
	}
	for _, character := range sha {
		if character != '0' {
			return false
		}
	}
	return true
}

func operationRef(operationID string) (string, error) {
	if len(operationID) != 64 {
		return "", errors.New("source index operation id must be 64 lowercase hex characters")
	}
	for _, character := range operationID {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return "", errors.New("source index operation id must be 64 lowercase hex characters")
		}
	}
	return "refs/cc-notes-source-v1/operations/" + operationID, nil
}

func operationPinRef(operationID string) string {
	return "refs/heads/cc-notes-receipt-pins/" + operationID
}
