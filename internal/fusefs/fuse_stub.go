//go:build !fuse

package fusefs

import (
	"context"
	"fmt"
)

// Mount always fails in this build variant: the binary was compiled
// without the fuse tag. See mount.go for the contract the fuse build
// implements.
func Mount(_ context.Context, _, _ string) error {
	return fmt.Errorf("%w: this binary was built without FUSE support; rebuild with -tags fuse, or download the _fuse release binary (macOS: brew install fuse-t; Linux: apt install fuse3)", ErrFuseUnavailable)
}
