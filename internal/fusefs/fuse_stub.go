//go:build !fuse

package fusefs

import (
	"context"
	"fmt"

	"github.com/yasyf/fusekit/mountd"
)

// Hostable reports whether this binary can host fuse mounts in-process. It is
// false in the pure build, so automatic mounts (init's auto-mount, the
// session-start ensure-mount) silently skip rather than spawning a holder that
// can only fail — and, critically, never contact a running holder. An explicit
// `cc-notes mount` still attempts and surfaces ErrCannotHost.
const Hostable = false

// Mount always fails in this build variant: the binary was compiled without
// the fuse tag, so it has no in-process fuse host. It wraps mountd.ErrCannotHost
// — the pure-build refusal, distinct from a fuse-built binary whose library
// failed to load (ErrFuseUnavailable) — exactly the sentinel the detached path's
// Spawn raises when a pure binary is asked to host. See mount.go for the
// contract the fuse build implements.
func Mount(_ context.Context, _, _ string) error {
	return fmt.Errorf("%w: this binary was built without FUSE support; rebuild with -tags fuse, or download the _fuse release binary (macOS: brew install fuse-t; Linux: apt install fuse3)", mountd.ErrCannotHost)
}

// ServeContent always fails in this build variant: the content server renders
// the store through the same fuse-tagged renderer the mount uses, so a pure
// binary has nothing to serve. It wraps mountd.ErrCannotHost — the pure-build
// refusal — so the contentd subcommand exits cleanly on a non-fuse binary. See
// contentd.go for the fuse build's implementation.
func ServeContent(_ context.Context, _ string) error {
	return fmt.Errorf("%w: this binary was built without FUSE support; the content server needs the fuse renderer (rebuild with -tags fuse, or download the _fuse release binary)", mountd.ErrCannotHost)
}
