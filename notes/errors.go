package notes

import (
	"fmt"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// Sentinel errors re-exported from the internal layer so callers branch on
// them with errors.Is without importing internal packages.
var (
	// ErrNotFound reports a Resolve prefix that matched no entity.
	ErrNotFound = store.ErrNotFound
	// ErrAmbiguous reports a Resolve prefix that matched more than one entity.
	ErrAmbiguous = store.ErrAmbiguous
	// ErrContended reports a write that lost its ref compare-and-swap on every
	// retry.
	ErrContended = store.ErrContended
	// ErrRefNotFound reports a load of an entity whose ref does not exist.
	ErrRefNotFound = gitobj.ErrRefNotFound
	// ErrDetachedHead reports a branch-dependent operation run on a detached
	// HEAD.
	ErrDetachedHead = gitcmd.ErrDetachedHead
)

// ConflictError reports a transition refused because the entity was not in a
// state that allows it: a task already closed, a claim lost to another actor,
// or a sprint or project already past its active phase.
type ConflictError struct {
	ID  model.EntityID
	Msg string
}

// Error describes the refused transition.
func (e *ConflictError) Error() string {
	return fmt.Sprintf("cc-notes: %s %s", e.ID.Short(), e.Msg)
}
