package cli

import (
	"errors"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/store"
	ccsync "github.com/yasyf/cc-notes/internal/sync"
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
	switch {
	case err == nil:
		return 0, ""
	case errors.As(err, &usage):
		return 2, "usage"
	case errors.Is(err, store.ErrAmbiguous):
		return 5, "ambiguous"
	case errors.Is(err, store.ErrNotFound), errors.Is(err, gitobj.ErrRefNotFound):
		return 3, "not-found"
	case errors.As(err, &conflict), errors.Is(err, store.ErrContended), errors.Is(err, ccsync.ErrSyncContended),
		errors.Is(err, mountd.ErrBusy), errors.Is(err, mountd.ErrForeignMount), errors.Is(err, mountd.ErrBaseMismatch):
		// Mount-holder conflicts (a dir busy with another op, a foreign mount in
		// the way, a base mismatch) are transient/holder-state conditions the
		// caller resolves and retries — exit 4, like a write conflict. Every
		// other holder-class error (ErrHolderUnavailable, ErrTCCDenied,
		// ErrUnmountWedged, ErrMountTimeout, ErrMountFailed, ErrUnknownClass) and
		// the fuse sentinels fall through to a plain exit 1.
		return 4, "conflict"
	default:
		return 1, "error"
	}
}
