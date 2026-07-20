// Package sourceindex derives one immutable, causally linked source revision
// from cc-notes' independently versioned entity refs.
package sourceindex

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"runtime"
	"slices"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

const (
	// Ref is the local, derived source-revision head. It deliberately lives
	// outside refs.Namespace so entity enumeration and wildcard sync never
	// parse or publish it as user state.
	Ref = "refs/cc-notes-source/head"

	maxAttempts = 16
)

var (
	// ErrContended reports repeated source-head movement during refresh.
	ErrContended = errors.New("source index contended")
	// ErrOperationExists reports an already committed operation identity.
	ErrOperationExists = errors.New("source operation already committed")
	// ErrNotAncestor reports a delta request with no causal predecessor relation.
	ErrNotAncestor = errors.New("source revision is not an ancestor")
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
	Token         model.SHA
	Previous      model.SHA
	Result        string
	RequestDigest [sha256.Size]byte
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
	proof, err := i.Repo.WriteSourceOperationProof(ctx, gitobj.SourceOperationProof{
		OperationID: operationID, Expected: expected, Committed: next,
		Result: result, RequestDigest: requestDigest,
	})
	if err != nil {
		return "", err
	}
	transaction = append(transaction,
		gitcmd.RefUpdate{Ref: Ref, New: next, Old: expected},
		gitcmd.RefUpdate{Ref: operationRef, New: proof},
	)
	if err := i.Git.UpdateRefs(ctx, transaction); err != nil {
		return "", fmt.Errorf("source index commit: %w", err)
	}
	return i.Refresh(ctx)
}

// InspectOperation resolves one O(1) durable mutation proof. found is false
// only when no transaction for operationID committed.
func (i Index) InspectOperation(ctx context.Context, operationID string) (operation Operation, found bool, err error) {
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
	changes, err := i.ChangesSince(ctx, proof.Expected, proof.Committed)
	if err != nil {
		return Operation{}, false, err
	}
	return Operation{
		Token: proof.Committed, Previous: proof.Expected, Result: proof.Result,
		RequestDigest: proof.RequestDigest, Changes: changes,
	}, true, nil
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
	return "refs/cc-notes-source/operations/" + operationID, nil
}
