package fusefs

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/yasyf/fusekit/catalog"
	"github.com/yasyf/fusekit/causal"
	"github.com/yasyf/fusekit/contentstream"
	"github.com/yasyf/fusekit/sourcedriver"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/sourceindex"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

const mutationSourceSettlementTimeout = 5 * time.Second

var errReplayContentUnused = errors.New("cc-notes source: exact mutation replay did not consume content")

// ApplyMutation applies one exact-revision Git transaction and returns its
// durable operation proof.
func (d *GitDriver) ApplyMutation(
	ctx context.Context,
	authority causal.SourceAuthorityID,
	request sourcedriver.MutationRequest,
	content contentstream.Source,
) (_ sourcedriver.MutationReceipt, resultErr error) {
	contentConsumed := false
	if content != nil {
		defer func() {
			settleCause := resultErr
			var suppress error
			if settleCause == nil && !contentConsumed {
				settleCause = errReplayContentUnused
				suppress = settleCause
			}
			settleErr := content.Settle(settleCause)
			waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), mutationSourceSettlementTimeout)
			waitErr := content.Wait(waitCtx)
			cancel()
			if suppress != nil {
				settleErr = withoutError(settleErr, suppress)
				waitErr = withoutError(waitErr, suppress)
			}
			resultErr = errors.Join(resultErr, settleErr, waitErr)
		}()
	}
	if err := sourcedriver.ValidateMutationRequest(request); err != nil {
		return sourcedriver.MutationReceipt{}, err
	}
	if request.HasContent != (content != nil) {
		return sourcedriver.MutationReceipt{}, fmt.Errorf("%w: mutation content ownership differs", sourcedriver.ErrInvalidValue)
	}
	tenant, err := d.targetInSet(authority, request.TargetSet, request.Tenant, request.Generation)
	if err != nil {
		return sourcedriver.MutationReceipt{}, err
	}
	source, index, err := d.open(ctx, authority)
	if err != nil {
		return sourcedriver.MutationReceipt{}, err
	}
	if err := validateDriverMutationContext(tenant, request.Context); err != nil {
		return sourcedriver.MutationReceipt{}, err
	}
	requestDigest, err := sourcedriver.MutationRequestDigest(request)
	if err != nil {
		return sourcedriver.MutationReceipt{}, err
	}
	if operation, found, err := index.InspectOperationState(ctx, request.OperationID.String()); err != nil {
		return sourcedriver.MutationReceipt{}, err
	} else if found {
		if operation.State == gitobj.SourceOperationForgotten {
			return sourcedriver.MutationReceipt{}, sourcedriver.ErrConflict
		}
		return replayMutation(request.OperationID, requestDigest, operation)
	}
	expected, err := sourceSHA(request.Expected)
	if err != nil {
		return sourcedriver.MutationReceipt{}, err
	}
	var body []byte
	if content != nil {
		body, err = readMutationSource(content, request.ContentSize, request.ContentHash)
		if err != nil {
			return sourcedriver.MutationReceipt{}, err
		}
		contentConsumed = true
	}
	head, err := index.Refresh(ctx)
	if err != nil {
		return sourcedriver.MutationReceipt{}, err
	}
	if head != expected {
		return sourcedriver.MutationReceipt{}, &sourcedriver.StaleRevisionError{
			Expected: request.Expected,
			Actual:   sourcedriver.RevisionToken(head),
		}
	}
	manifest, err := index.Snapshot(ctx, head)
	if err != nil {
		return sourcedriver.MutationReceipt{}, err
	}
	prepared, result, err := prepareSourceMutation(ctx, source, manifest, request, body)
	if err != nil {
		return sourcedriver.MutationReceipt{}, err
	}
	updates := make([]gitcmd.RefUpdate, len(prepared))
	for index, value := range prepared {
		updates[index] = value.RefUpdate()
	}
	if _, err := index.CommitOperation(
		ctx, head, request.OperationID.String(), string(result), requestDigest, updates,
	); err != nil {
		if errors.Is(err, sourceindex.ErrOperationExists) {
			operation, found, inspectErr := index.InspectOperationState(ctx, request.OperationID.String())
			if inspectErr != nil {
				return sourcedriver.MutationReceipt{}, inspectErr
			}
			if !found {
				return sourcedriver.MutationReceipt{}, fmt.Errorf("%w: committed operation proof disappeared", sourcedriver.ErrIntegrity)
			}
			if operation.State == gitobj.SourceOperationForgotten {
				return sourcedriver.MutationReceipt{}, sourcedriver.ErrConflict
			}
			return replayMutation(request.OperationID, requestDigest, operation)
		}
		if errors.Is(err, gitcmd.ErrCASMismatch) {
			actual, refreshErr := index.Refresh(ctx)
			if refreshErr != nil {
				return sourcedriver.MutationReceipt{}, errors.Join(err, refreshErr)
			}
			return sourcedriver.MutationReceipt{}, &sourcedriver.StaleRevisionError{
				Expected: request.Expected,
				Actual:   sourcedriver.RevisionToken(actual),
			}
		}
		return sourcedriver.MutationReceipt{}, err
	}
	source.RememberPrepared(prepared...)
	operation, found, err := index.InspectOperation(ctx, request.OperationID.String())
	if err != nil {
		return sourcedriver.MutationReceipt{}, err
	}
	if !found {
		return sourcedriver.MutationReceipt{}, fmt.Errorf("%w: committed operation proof is missing", sourcedriver.ErrIntegrity)
	}
	return replayMutation(request.OperationID, requestDigest, operation)
}

func withoutError(err, target error) error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		remaining := make([]error, 0, len(joined.Unwrap()))
		for _, nested := range joined.Unwrap() {
			if filtered := withoutError(nested, target); filtered != nil {
				remaining = append(remaining, filtered)
			}
		}
		return errors.Join(remaining...)
	}
	if errors.Is(err, target) {
		return nil
	}
	return err
}

// InspectMutation returns the durable source-side state for one operation ID.
func (d *GitDriver) InspectMutation(
	ctx context.Context,
	authority causal.SourceAuthorityID,
	operationID catalog.MutationID,
	requestDigest [sha256.Size]byte,
) (sourcedriver.MutationReceipt, error) {
	if operationID == (catalog.MutationID{}) || requestDigest == ([sha256.Size]byte{}) {
		return sourcedriver.MutationReceipt{}, fmt.Errorf("%w: mutation operation id or request digest is empty", sourcedriver.ErrInvalidValue)
	}
	_, index, err := d.open(ctx, authority)
	if err != nil {
		return sourcedriver.MutationReceipt{}, err
	}
	operation, found, err := index.InspectOperation(ctx, operationID.String())
	if err != nil {
		return sourcedriver.MutationReceipt{}, err
	}
	if !found {
		receipt := sourcedriver.MutationReceipt{OperationID: operationID, State: sourcedriver.MutationNotFound}
		if err := sourcedriver.ValidateMutationReceipt(receipt); err != nil {
			return sourcedriver.MutationReceipt{}, err
		}
		return receipt, nil
	}
	return replayMutation(operationID, requestDigest, operation)
}

// SettleMutation durably acknowledges and later forgets one exact applied
// receipt while retaining its operation-id tombstone.
func (d *GitDriver) SettleMutation(
	ctx context.Context,
	authority causal.SourceAuthorityID,
	settlement sourcedriver.MutationSettlement,
) error {
	if err := sourcedriver.ValidateMutationSettlement(settlement); err != nil {
		return err
	}
	if _, err := d.requestTargets(authority, settlement.TargetSet); err != nil {
		return err
	}
	_, index, err := d.open(ctx, authority)
	if err != nil {
		return err
	}
	operation, found, err := index.InspectOperationState(ctx, settlement.OperationID.String())
	if err != nil {
		return err
	}
	if !found {
		return sourcedriver.ErrConflict
	}
	var desired gitobj.SourceOperationState
	switch settlement.Kind {
	case sourcedriver.MutationSettlementAcknowledge:
		desired = gitobj.SourceOperationAcknowledged
	case sourcedriver.MutationSettlementForget:
		desired = gitobj.SourceOperationForgotten
	case sourcedriver.MutationSettlementAbandon:
		return sourcedriver.ErrConflict
	default:
		return sourcedriver.ErrInvalidValue
	}
	if operation.State == gitobj.SourceOperationForgotten {
		if desired != gitobj.SourceOperationForgotten || operation.RequestDigest != settlement.RequestDigest || operation.ReceiptDigest != settlement.ReceiptDigest {
			return sourcedriver.ErrConflict
		}
		return nil
	}
	receipt, err := replayMutation(settlement.OperationID, settlement.RequestDigest, operation)
	if err != nil {
		return err
	}
	if receipt.Digest != settlement.ReceiptDigest {
		return sourcedriver.ErrConflict
	}
	if _, err := index.SettleOperation(
		ctx, settlement.OperationID.String(), settlement.RequestDigest, settlement.ReceiptDigest, desired,
	); err != nil {
		if errors.Is(err, sourceindex.ErrOperationState) {
			return sourcedriver.ErrConflict
		}
		return err
	}
	return nil
}

func prepareSourceMutation(
	ctx context.Context,
	source *store.Store,
	manifest gitobj.SourceManifest,
	request sourcedriver.MutationRequest,
	body []byte,
) ([]store.PreparedRef, sourcedriver.LogicalID, error) {
	sourceContext := request.Context
	switch sourceContext.Operation.Kind {
	case catalog.MutationCreate:
		kind, err := driverMutationKind(sourceContext)
		if err != nil {
			return nil, "", err
		}
		ops, err := codecOf(kind).New(body)
		if err != nil {
			return nil, "", fmt.Errorf("cc-notes source: parse new %s: %w", kind, err)
		}
		ops[0], err = setCreateNonce(ops[0], request.OperationID.String())
		if err != nil {
			return nil, "", err
		}
		prepared, err := source.PrepareCreateExact(ctx, ops)
		if err != nil {
			return nil, "", err
		}
		key, err := preparedSourceKey(kind, prepared)
		return []store.PreparedRef{prepared}, sourcedriver.LogicalID(key), err
	case catalog.MutationRevise:
		entity, err := sourceEntityAt(ctx, source, manifest, sourceContext.Object)
		if err != nil {
			return nil, "", err
		}
		prepared, err := prepareRevision(ctx, source, entity, body)
		return []store.PreparedRef{prepared}, sourcedriver.LogicalID(entity.key), err
	case catalog.MutationDelete:
		entity, err := sourceEntityAt(ctx, source, manifest, sourceContext.Object)
		if err != nil {
			return nil, "", err
		}
		prepared, err := prepareTombstone(ctx, source, entity)
		return []store.PreparedRef{prepared}, "", err
	case catalog.MutationReplace:
		object, err := sourceEntityAt(ctx, source, manifest, sourceContext.Object)
		if err != nil {
			return nil, "", err
		}
		target, err := sourceEntityAt(ctx, source, manifest, sourceContext.Target)
		if err != nil {
			return nil, "", err
		}
		if object.ref == target.ref {
			return nil, "", fmt.Errorf("%w: replace object and target are identical", sourcedriver.ErrConflict)
		}
		prepared := make([]store.PreparedRef, 0, 2)
		if sourceContext.Operation.HasContent {
			revised, err := prepareRevision(ctx, source, object, body)
			if err != nil {
				return nil, "", err
			}
			prepared = append(prepared, revised)
		}
		tombstone, err := prepareTombstone(ctx, source, target)
		if err != nil {
			return nil, "", err
		}
		prepared = append(prepared, tombstone)
		return prepared, sourcedriver.LogicalID(object.key), nil
	default:
		return nil, "", fmt.Errorf("%w: unsupported mutation kind", sourcedriver.ErrInvalidValue)
	}
}

type sourceEntity struct {
	kind     model.Kind
	key      string
	ref      string
	tip      model.SHA
	snapshot model.Snapshot
}

func sourceEntityAt(
	ctx context.Context,
	source *store.Store,
	manifest gitobj.SourceManifest,
	locator *catalog.SourceLocator,
) (sourceEntity, error) {
	kind, root, key, err := sourceEntityLocator(locator)
	if err != nil {
		return sourceEntity{}, err
	}
	ref := refs.For(kind, model.EntityID(root))
	tip, found := manifest.Tips()[ref]
	if !found {
		return sourceEntity{}, sourcedriver.ErrNotFound
	}
	rooted, err := source.LoadRootedAt(ctx, tip)
	if err != nil {
		return sourceEntity{}, err
	}
	if rooted.Root.SHA != root || rooted.Snapshot.EntityID() != model.EntityID(root) {
		return sourceEntity{}, fmt.Errorf("%w: locator identity differs from immutable entity", sourcedriver.ErrIntegrity)
	}
	if rooted.Snapshot.Meta().Deleted {
		return sourceEntity{}, sourcedriver.ErrNotFound
	}
	return sourceEntity{kind: kind, key: key, ref: ref, tip: tip, snapshot: rooted.Snapshot}, nil
}

func sourceEntityLocator(locator *catalog.SourceLocator) (model.Kind, model.SHA, string, error) {
	if locator == nil {
		return "", "", "", fmt.Errorf("%w: entity locator is missing", sourcedriver.ErrInvalidValue)
	}
	parts := strings.Split(string(locator.SourceKey), ":")
	if len(parts) != 3 || parts[0] != "entity" || !plumbing.IsHash(parts[2]) {
		return "", "", "", fmt.Errorf("%w: entity locator is malformed", sourcedriver.ErrInvalidValue)
	}
	kind := model.Kind(parts[1])
	codec, ok := codecs[kind]
	if !ok || codec.ReadOnly() {
		return "", "", "", fmt.Errorf("%w: entity locator is not writable", sourcedriver.ErrInvalidValue)
	}
	return kind, model.SHA(parts[2]), string(locator.SourceKey), nil
}

func validateDriverMutationContext(tenant Tenant, source catalog.SourceMutationContext) error {
	operation := source.Operation
	if operation.ObjectKind != catalog.KindFile || operation.Mode != 0o644 || operation.LinkTarget != "" {
		return fmt.Errorf("%w: only writable regular entity files are mutable", sourcedriver.ErrInvalidValue)
	}
	if !operation.HasContent && (operation.Kind == catalog.MutationCreate || operation.Kind == catalog.MutationRevise) {
		return fmt.Errorf("%w: create and revise require content", sourcedriver.ErrInvalidValue)
	}
	for _, locator := range []*catalog.SourceLocator{source.Object, source.Parent, source.Target} {
		if locator != nil && locator.SourceAuthority != tenant.Authority {
			return fmt.Errorf("%w: locator authority differs from tenant", sourcedriver.ErrInvalidValue)
		}
	}
	kind, err := driverMutationKind(source)
	if err != nil {
		return err
	}
	if source.Parent == nil || source.Parent.SourceKey != catalog.SourceObjectKey("kind:"+string(kind)) {
		return fmt.Errorf("%w: mutation parent differs from entity kind", sourcedriver.ErrInvalidValue)
	}
	if source.Target != nil {
		targetKind, _, _, err := sourceEntityLocator(source.Target)
		if err != nil {
			return err
		}
		if targetKind != kind {
			return fmt.Errorf("%w: replace target kind differs", sourcedriver.ErrInvalidValue)
		}
	}
	return nil
}

func driverMutationKind(source catalog.SourceMutationContext) (model.Kind, error) {
	if source.Operation.Kind == catalog.MutationCreate {
		if source.Parent == nil {
			return "", fmt.Errorf("%w: create parent is missing", sourcedriver.ErrInvalidValue)
		}
		value, ok := strings.CutPrefix(string(source.Parent.SourceKey), "kind:")
		kind := model.Kind(value)
		layout, registered := layouts[kind]
		codec, hasCodec := codecs[kind]
		if !ok || !registered || !hasCodec || codec.ReadOnly() || !strings.HasSuffix(source.Operation.Name, layout.ext) {
			return "", fmt.Errorf("%w: create parent or filename is not writable", sourcedriver.ErrInvalidValue)
		}
		return kind, nil
	}
	kind, _, _, err := sourceEntityLocator(source.Object)
	return kind, err
}

func prepareRevision(
	ctx context.Context,
	source *store.Store,
	entity sourceEntity,
	body []byte,
) (store.PreparedRef, error) {
	ops, err := codecOf(entity.kind).Diff(entity.snapshot, body)
	if err != nil {
		return store.PreparedRef{}, fmt.Errorf("cc-notes source: revise %s: %w", entity.kind, err)
	}
	if len(ops) == 0 {
		return store.PreparedRef{}, fmt.Errorf("%w: revision produced no durable operation", sourcedriver.ErrConflict)
	}
	return source.PrepareAppendAt(ctx, entity.ref, entity.tip, ops)
}

func prepareTombstone(ctx context.Context, source *store.Store, entity sourceEntity) (store.PreparedRef, error) {
	return source.PrepareAppendAt(ctx, entity.ref, entity.tip, []model.Op{model.DeleteNote{}})
}

func preparedSourceKey(kind model.Kind, prepared store.PreparedRef) (string, error) {
	if prepared.Snapshot == nil || prepared.Snapshot.EntityID() != model.EntityID(prepared.New) {
		return "", fmt.Errorf("%w: prepared create identity differs from root", sourcedriver.ErrIntegrity)
	}
	return entitySourceKey(kind, model.PackCommit{SHA: prepared.New})
}

func readMutationSource(
	content contentstream.Source,
	expectedSize int64,
	expectedHash catalog.ContentHash,
) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(content, expectedSize+1))
	if err != nil {
		return nil, fmt.Errorf("cc-notes source: read mutation content: %w", err)
	}
	if int64(len(body)) != expectedSize {
		return nil, fmt.Errorf("%w: mutation content size differs", sourcedriver.ErrIntegrity)
	}
	digest := sha256.Sum256(body)
	if catalog.ContentHash(digest) != expectedHash {
		return nil, fmt.Errorf("%w: mutation content hash differs", sourcedriver.ErrIntegrity)
	}
	return body, nil
}

func replayMutation(
	operationID catalog.MutationID,
	requestDigest [sha256.Size]byte,
	operation sourceindex.Operation,
) (sourcedriver.MutationReceipt, error) {
	if operation.RequestDigest != requestDigest {
		return sourcedriver.MutationReceipt{}, fmt.Errorf("%w: operation id has a different request", sourcedriver.ErrConflict)
	}
	return appliedMutationReceipt(operationID, operation)
}

func appliedMutationReceipt(
	operationID catalog.MutationID,
	operation sourceindex.Operation,
) (sourcedriver.MutationReceipt, error) {
	receipt := sourcedriver.MutationReceipt{
		OperationID:   operationID,
		State:         sourcedriver.MutationApplied,
		RequestDigest: operation.RequestDigest,
		Expected:      sourcedriver.RevisionToken(operation.Previous),
		Committed:     sourcedriver.RevisionToken(operation.Token),
		Result:        sourcedriver.LogicalID(operation.Result),
	}
	digest, err := sourcedriver.MutationReceiptDigest(receipt)
	if err != nil {
		return sourcedriver.MutationReceipt{}, err
	}
	receipt.Digest = digest
	if err := sourcedriver.ValidateMutationReceipt(receipt); err != nil {
		return sourcedriver.MutationReceipt{}, err
	}
	return receipt, nil
}
