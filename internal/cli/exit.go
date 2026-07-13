package cli

import (
	"errors"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/notes"
	"github.com/yasyf/fusekit/mountd"
)

// UsageError reports a malformed invocation: an unknown command or flag,
// wrong argument arity, a missing required flag, or mutually exclusive
// flags. It maps to exit code 2.
type UsageError struct{ Err error }

func (e *UsageError) Error() string { return e.Err.Error() }

func (e *UsageError) Unwrap() error { return e.Err }

// ConflictError reports a write rejected by the entity's current state: a
// lost claim race or an illegal lifecycle transition. It maps to exit code 4.
type ConflictError struct{ Msg string }

func (e *ConflictError) Error() string { return e.Msg }

// ExitCode maps err to the cc-notes exit code contract: 0 ok, 1 error, 2
// usage, 3 not-found, 4 conflict, 5 ambiguous.
func ExitCode(err error) int {
	code, _ := classify(err)
	return code
}

// Label returns the greppable stderr prefix matching ExitCode, or "" for a
// nil error.
func Label(err error) string {
	_, label := classify(err)
	return label
}

func classify(err error) (int, string) {
	var usage *UsageError
	var conflict *ConflictError
	var notesConflict *notes.ConflictError
	var unmet *notes.UnmetCriteriaError
	var attachExists *notes.AttachmentExistsError
	var ambiguousKinds *notes.AmbiguousKindsError
	switch {
	case err == nil:
		return 0, ""
	// An *UnmetCriteriaError from the notes layer is a done-gate refusal the CLI
	// maps to a usage error (exit 2), the same code the CLI's own gate returns.
	// ErrEmptyEdit (a no-op edit mask) and an attachment-name collision are
	// likewise malformed invocations the caller fixes and retries — usage, like
	// the CLI's own arity and mutual-exclusion guards.
	case errors.As(err, &usage), errors.As(err, &unmet), errors.Is(err, notes.ErrEmptyEdit), errors.As(err, &attachExists):
		return 2, "usage"
	// A cross-kind prefix collision (*AmbiguousKindsError) already satisfies
	// Is(ErrAmbiguous); the explicit type match holds the mapping under a future
	// change to that Is method.
	case errors.Is(err, store.ErrAmbiguous), errors.As(err, &ambiguousKinds):
		return 5, "ambiguous"
	case errors.Is(err, store.ErrNotFound), errors.Is(err, gitobj.ErrRefNotFound):
		return 3, "not-found"
	case errors.As(err, &conflict), errors.As(err, &notesConflict), errors.Is(err, store.ErrContended), errors.Is(err, ccsync.ErrSyncContended),
		errors.Is(err, mountd.ErrBusy), errors.Is(err, mountd.ErrForeignMount), errors.Is(err, mountd.ErrBaseMismatch):
		// Mount-holder conflicts (a dir busy with another op, a foreign mount in
		// the way, a base mismatch) are transient/holder-state conditions the
		// caller resolves and retries — exit 4, like a write conflict. Every
		// other holder-class error (ErrHolderUnavailable, ErrTCCDenied,
		// ErrUnmountWedged, ErrMountTimeout, ErrMountFailed, ErrUnknownClass) and
		// the fuse sentinels fall through to a plain exit 1.
		return 4, "conflict"
	default:
		// A *notes.MissingContentError (attachment bytes absent locally) lands
		// here as a plain exit 1; Hint carries its `cc-notes sync` remediation.
		return 1, "error"
	}
}

// Message returns the stderr body for err with the notes-layer "cc-notes: "
// program prefix trimmed, so a raw notes error renders under a classify label
// (`conflict: <msg>`) exactly as one funnelled through taskErr does. An error
// without that prefix renders verbatim.
func Message(err error) string {
	return strings.TrimPrefix(err.Error(), "cc-notes: ")
}

// Hint returns the remediation line an error carries beyond its message, or ""
// when it carries none. A *notes.MissingContentError points at `cc-notes sync`
// to fetch the referenced-but-absent attachment bytes named in the message; a
// *notes.AttachmentExistsError points at --replace to overwrite the name-colliding
// attachment. Both split the pre-migration one-line remediation onto its own line
// under the classify label.
func Hint(err error) string {
	var missing *notes.MissingContentError
	if errors.As(err, &missing) {
		return "run `cc-notes sync` to download it"
	}
	var exists *notes.AttachmentExistsError
	if errors.As(err, &exists) {
		return "pass --replace to overwrite it"
	}
	return ""
}
