package sync

import (
	"context"
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitcmd"
)

// WiredRemotes returns each remote whose remote.<name>.fetch wires cc-notes
// entity refs — the current per-remote tracking form fetchRefspec writes or the
// pre-fix same-namespace form — in git config order with duplicates removed. A
// remote carrying neither refspec was never wired for cc-notes and is omitted.
func WiredRemotes(ctx context.Context, g gitcmd.Git) ([]string, error) {
	pairs, err := g.ConfigGetRegexp(ctx, `^remote\..*\.fetch$`)
	if err != nil {
		return nil, err
	}
	var wired []string
	for _, pair := range pairs {
		name := strings.TrimSuffix(strings.TrimPrefix(pair[0], "remote."), ".fetch")
		if pair[1] != fetchRefspec(name) && pair[1] != oldFetchRefspec {
			continue
		}
		if !slices.Contains(wired, name) {
			wired = append(wired, name)
		}
	}
	return wired, nil
}
