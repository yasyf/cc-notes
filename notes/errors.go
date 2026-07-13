package notes

import (
	"errors"
	"fmt"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// Additional sentinel errors the domain methods branch on with errors.Is.
var (
	// ErrEmptyEdit reports an edit whose field mask sets nothing — no observable
	// change to apply.
	ErrEmptyEdit = errors.New("empty edit")
	// ErrCycle reports a dependency edge that would close a cycle in the task
	// blocked-by graph.
	ErrCycle = errors.New("dependency cycle")
)

// AmbiguousError is the rich candidate set behind ErrAmbiguous, re-exported so
// callers read it — its Kind, Prefix, and Candidates — without importing
// internal/store. It matches ErrAmbiguous under errors.Is.
type AmbiguousError = store.AmbiguousError

// Candidate is one entity matched by an ambiguous Resolve prefix, re-exported
// from the internal layer.
type Candidate = store.Candidate

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

// UnmetCriteriaError reports a task close refused because acceptance criteria
// have not all been met. Unmet carries every criterion still short of met, so
// the caller renders the detail and the force-close remediation itself.
type UnmetCriteriaError struct {
	ID    model.EntityID
	Unmet []model.Criterion
}

// Error names the task and the count of unmet criteria; the criterion detail
// and the --force hint are the caller's to render from Unmet.
func (e *UnmetCriteriaError) Error() string {
	return fmt.Sprintf("cc-notes: %s has %d unmet criterion/criteria", e.ID.Short(), len(e.Unmet))
}

// AttachmentExistsError reports an attach whose name collides with a live
// attachment, which would orphan the old bytes behind the same name.
type AttachmentExistsError struct {
	Name string
}

// Error names the colliding attachment; the --replace remediation is the
// caller's to render.
func (e *AttachmentExistsError) Error() string {
	return fmt.Sprintf("cc-notes: attachment %q already exists", e.Name)
}

// MissingContentError reports an attachment referenced by an entity whose bytes
// are absent from the local LFS store. Attachment carries the name and oid so
// the caller renders the "run cc-notes sync" remediation itself.
type MissingContentError struct {
	Attachment model.Attachment
}

// Error names the absent attachment and its oid; the sync remediation is the
// caller's to render.
func (e *MissingContentError) Error() string {
	return fmt.Sprintf("cc-notes: attachment %q (oid %s) not present locally", e.Attachment.Name, e.Attachment.OID)
}

// KindMatch is one entity a kind-agnostic prefix resolved to: its kind, id, and
// title.
type KindMatch struct {
	Kind  model.Kind
	ID    model.EntityID
	Title string
}

// AmbiguousKindsError reports a prefix that resolved to entities in more than
// one kind. Ids are globally unique, so this means the prefix is too short.
type AmbiguousKindsError struct {
	Prefix  string
	Matches []KindMatch
}

// Error lists every kind, short id, and title the prefix matched.
func (e *AmbiguousKindsError) Error() string {
	parts := make([]string, len(e.Matches))
	for i, m := range e.Matches {
		parts[i] = fmt.Sprintf("%s %s %q", m.Kind, m.ID.Short(), m.Title)
	}
	return fmt.Sprintf("cc-notes: ambiguous prefix %q: %s", e.Prefix, strings.Join(parts, ", "))
}

// Is reports whether target is ErrAmbiguous, so callers branch on a
// cross-kind ambiguity the same way they branch on a within-kind one.
func (e *AmbiguousKindsError) Is(target error) bool { return target == ErrAmbiguous }
