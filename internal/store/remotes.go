package store

import (
	"context"
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/refs"
)

// defaultRemote is the remote a repository with no cc-notes-wired remote
// syncs with.
const defaultRemote = "origin"

// WiredRemotes returns each remote whose remote.<name>.fetch wires cc-notes
// entity refs — the current per-remote tracking form refs.FetchRefspec writes
// or the pre-fix same-namespace form — in git config order with duplicates
// removed. A remote carrying neither refspec was never wired for cc-notes and
// is omitted.
func WiredRemotes(ctx context.Context, g gitcmd.Git) ([]string, error) {
	pairs, err := g.ConfigGetRegexp(ctx, `^remote\..*\.fetch$`)
	if err != nil {
		return nil, err
	}
	var wired []string
	for _, pair := range pairs {
		name := strings.TrimSuffix(strings.TrimPrefix(pair[0], "remote."), ".fetch")
		if pair[1] != refs.FetchRefspec(name) && pair[1] != refs.OldFetchRefspec {
			continue
		}
		if !slices.Contains(wired, name) {
			wired = append(wired, name)
		}
	}
	return wired, nil
}

// SyncRemotes returns the remotes an unscoped sync converges with: every
// cc-notes-wired remote, else origin.
func SyncRemotes(ctx context.Context, g gitcmd.Git) ([]string, error) {
	wired, err := WiredRemotes(ctx, g)
	if err != nil || len(wired) > 0 {
		return wired, err
	}
	return []string{defaultRemote}, nil
}
