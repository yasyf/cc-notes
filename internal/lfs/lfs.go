// Package lfs is a hand-rolled git-lfs client: the local content store,
// endpoint discovery, the batch API, and basic transfers. It speaks only the
// basic transfer adapter, stores objects in the standard git-lfs layout so
// the git-lfs CLI stays interoperable, and never execs the git-lfs binary —
// the only exec paths are git credential (via gitcmd) and
// `ssh <host> git-lfs-authenticate`.
package lfs

import "errors"

var (
	// ErrUnsupported reports a remote with no usable LFS endpoint.
	ErrUnsupported = errors.New("remote has no LFS endpoint")
	// ErrObjectMissing reports content absent from the local LFS store.
	ErrObjectMissing = errors.New("attachment content not in local LFS store")
	// ErrCorrupt reports content whose hash or size does not match its oid.
	ErrCorrupt = errors.New("LFS content hash mismatch")
)
