// Package sourceindex exposes immutable managed source revisions and receipts.
package sourceindex

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/sourceview"
	"github.com/yasyf/cc-notes/model"
)

const (
	// Ref is the complete managed projection head, outside the entity sync namespace.
	Ref = "refs/cc-notes-source-v2/head"
	// LegacyRef is enumerated only by explicit historical import.
	LegacyRef = "refs/cc-notes-source-v1/head"
	// LiveRef pins the complete current raw entity and LFS view.
	LiveRef = "refs/heads/cc-notes-source-live-v2"
	// CatalogRef preserves imported metadata without historical payload edges.
	CatalogRef = "refs/cc-notes-source-v2/catalog"
	// OperationPrefix preserves consumed operation identities across source formats.
	OperationPrefix = "refs/cc-notes-source-v1/operations/"
)

var (
	// ErrContended reports publication contention.
	ErrContended = errors.New("source index contended")
	// ErrOperationExists preserves a consumed operation identity.
	ErrOperationExists = errors.New("source operation already committed")
	// ErrNotAncestor rejects revisions outside the requested causal lineage.
	ErrNotAncestor = errors.New("source revision is not an ancestor")
	// ErrOperationState rejects an invalid receipt transition or digest.
	ErrOperationState = errors.New("source operation state conflict")
	// ErrProjectionUnavailable preserves released history without inventing content.
	ErrProjectionUnavailable = errors.New("historical source projection unavailable")
)

// Revision describes permanent metadata roots; retention hashes are opaque values.
type Revision struct {
	Token               model.SHA
	Predecessor         model.SHA
	Height              uint64
	AncestorJumps       []model.SHA
	EntityGeneration    uint64
	EntityRoot          model.SHA
	ObjectRoot          model.SHA
	DependencyRoot      model.SHA
	NameRoot            model.SHA
	LFSRoot             model.SHA
	RetentionIndex      model.SHA
	RetentionRoot       model.SHA
	ProjectionConflicts uint64
	RetentionConflicts  uint64
	ProjectionErrors    uint64
	ProjectionAvailable bool
	RenderPolicy        string
	ImportEvidence      string
	ProjectionDigest    [sha256.Size]byte
}

// Changes is the exact logical ref-tip delta between two source revisions.
type Changes struct {
	Upserts gitobj.SourceManifest
	Deletes []string
}

// Operation preserves the exact committed mutation receipt and bounded tip delta.
type Operation struct {
	Proof         model.SHA
	Token         model.SHA
	Previous      model.SHA
	Result        string
	RequestDigest [sha256.Size]byte
	ReceiptDigest [sha256.Size]byte
	State         gitobj.SourceOperationState
	Changes       Changes
	RetentionRoot model.SHA
	Pin           model.SHA
}

// CommitRequest binds prepared entity changes to one captured source generation.
type CommitRequest struct {
	Expected      model.SHA
	OperationID   string
	Result        string
	RequestDigest [sha256.Size]byte
	Updates       []gitcmd.RefUpdate
	Snapshots     map[model.SHA]model.Snapshot
}

// Backend requires complete managed state and never reconstructs it on a read.
type Backend interface {
	SourceHead(context.Context) (Revision, error)
	ResolveRevision(context.Context, model.SHA) (Revision, error)
	ResolveEntityAt(context.Context, model.SHA, string) (gitobj.SourceRefTip, bool, error)
	SourceSnapshot(context.Context, model.SHA) (gitobj.SourceManifest, error)
	SourceChanges(context.Context, model.SHA, model.SHA) (Changes, error)
	ReadOperation(context.Context, string) (Operation, bool, error)
	CommitSourceOperation(context.Context, CommitRequest) (Operation, error)
	SettleSourceOperation(context.Context, string, [sha256.Size]byte, [sha256.Size]byte, gitobj.SourceOperationState) (Operation, error)
	SourceObject(context.Context, model.SHA, string) (sourceview.Object, bool, error)
	SourceObjectRange(context.Context, model.SHA, uint64, int) ([]sourceview.Object, uint64, error)
	SourceObjectRank(context.Context, model.SHA, string) (uint64, bool, error)
	SourceObjectChanges(context.Context, model.SHA, model.SHA) ([]sourceview.Object, []string, error)
	OpenSourceContent(context.Context, model.SHA, string) (sourceview.Object, io.ReadCloser, error)
}

// Index delegates every ordinary source operation to one mandatory managed backend.
type Index struct {
	Backend Backend
}

// Refresh reads the current immutable token without enumerating entities.
func (i Index) Refresh(ctx context.Context) (model.SHA, error) {
	if err := i.validate(); err != nil {
		return "", err
	}
	revision, err := i.Backend.SourceHead(ctx)
	return revision.Token, err
}

// Snapshot materializes only an explicitly requested complete entity-tip manifest.
func (i Index) Snapshot(ctx context.Context, token model.SHA) (gitobj.SourceManifest, error) {
	if err := i.validate(); err != nil {
		return nil, err
	}
	return i.Backend.SourceSnapshot(ctx, token)
}

// ChangesSince compares indexed roots after checking normalized ancestry.
func (i Index) ChangesSince(ctx context.Context, from, to model.SHA) (Changes, error) {
	if err := i.validate(); err != nil {
		return Changes{}, err
	}
	return i.Backend.SourceChanges(ctx, from, to)
}

// CommitOperation fences prepared entity changes with their issued source revision.
func (i Index) CommitOperation(ctx context.Context, expected model.SHA, operationID, result string, digest [sha256.Size]byte, updates []gitcmd.RefUpdate, snapshots map[model.SHA]model.Snapshot) (model.SHA, error) {
	if err := i.validate(); err != nil {
		return "", err
	}
	operation, err := i.Backend.CommitSourceOperation(ctx, CommitRequest{Expected: expected, OperationID: operationID, Result: result, RequestDigest: digest, Updates: updates, Snapshots: snapshots})
	return operation.Token, err
}

// InspectOperation hides forgotten receipt metadata while preserving consumed IDs.
func (i Index) InspectOperation(ctx context.Context, id string) (Operation, bool, error) {
	operation, found, err := i.InspectOperationState(ctx, id)
	if operation.State == gitobj.SourceOperationForgotten {
		return Operation{}, false, err
	}
	return operation, found, err
}

// InspectOperationState includes the forgotten tombstone needed for exact replay.
func (i Index) InspectOperationState(ctx context.Context, id string) (Operation, bool, error) {
	if err := i.validate(); err != nil {
		return Operation{}, false, err
	}
	return i.Backend.ReadOperation(ctx, id)
}

// SettleOperation releases only the selected receipt's content pin.
func (i Index) SettleOperation(ctx context.Context, id string, request, receipt [sha256.Size]byte, state gitobj.SourceOperationState) (Operation, error) {
	if err := i.validate(); err != nil {
		return Operation{}, err
	}
	return i.Backend.SettleSourceOperation(ctx, id, request, receipt, state)
}

func (i Index) validate() error {
	if i.Backend == nil {
		return errors.New("source index: managed backend is required")
	}
	return nil
}

// OperationRef validates the stable operation-ID namespace.
func OperationRef(id string) (string, error) {
	if len(id) != 64 {
		return "", errors.New("source operation id must be 64 lowercase hex characters")
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", errors.New("source operation id must be 64 lowercase hex characters")
		}
	}
	return OperationPrefix + id, nil
}

// OperationPinPrefix holds the existing Git-LFS receipt retention branches.
const OperationPinPrefix = "refs/heads/cc-notes-receipt-pins/"

// OperationPinRef is the existing Git-LFS receipt retention branch.
func OperationPinRef(id string) string { return OperationPinPrefix + id }

// StaleRevisionError carries the actual committed source fence.
type StaleRevisionError struct{ Expected, Actual model.SHA }

func (e *StaleRevisionError) Error() string {
	return fmt.Sprintf("source revision is %s, expected %s", e.Actual, e.Expected)
}
func (e *StaleRevisionError) Unwrap() error { return gitcmd.ErrCASMismatch }
