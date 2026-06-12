package sync

import (
	"context"
	"fmt"
	"slices"

	"github.com/yasyf/cc-notes/internal/gitcmd"
)

const (
	// fetchRefspec force-mirrors entity refs on plain git fetch; the reflog
	// (core.logAllRefUpdates=always) is the safety net for the +.
	fetchRefspec = "+" + namespace + "*:" + namespace + "*"
	// pushRefspec carries entity refs on plain git push, never forced: a
	// diverged ref must resolve through Sync's union merge, never a clobber.
	pushRefspec = namespace + "*:" + namespace + "*"
	// headRefspec keeps plain git push pushing the current branch: any
	// remote.<r>.push entry overrides push.default, so installing a push
	// refspec into a remote that had none must restore the default first.
	headRefspec = "HEAD"
)

// Install wires remote so plain git fetch and push carry cc-notes entity
// refs alongside branches. It is idempotent — each config line is added only
// when absent — and it preserves existing push behavior: a remote with no
// push refspec gets HEAD before the cc-notes refspec, while a remote with
// its own push refspecs keeps them untouched. An unconfigured remote fails
// wrapping ErrRemoteNotFound.
func Install(ctx context.Context, g gitcmd.Git, remote string) error {
	if err := install(ctx, g, remote); err != nil {
		return fmt.Errorf("install cc-notes refspecs for %s: %w", remote, err)
	}
	return nil
}

func install(ctx context.Context, g gitcmd.Git, remote string) error {
	if err := ensureRemote(ctx, g, remote); err != nil {
		return err
	}
	fetchKey := "remote." + remote + ".fetch"
	fetch, err := g.ConfigGetAll(ctx, fetchKey)
	if err != nil {
		return err
	}
	if !slices.Contains(fetch, fetchRefspec) {
		if err := g.ConfigAdd(ctx, fetchKey, fetchRefspec); err != nil {
			return err
		}
	}
	pushKey := "remote." + remote + ".push"
	push, err := g.ConfigGetAll(ctx, pushKey)
	if err != nil {
		return err
	}
	if len(push) == 0 {
		if err := g.ConfigAdd(ctx, pushKey, headRefspec); err != nil {
			return err
		}
	}
	if !slices.Contains(push, pushRefspec) {
		if err := g.ConfigAdd(ctx, pushKey, pushRefspec); err != nil {
			return err
		}
	}
	reflog, err := g.ConfigGetAll(ctx, "core.logAllRefUpdates")
	if err != nil {
		return err
	}
	if !slices.Equal(reflog, []string{"always"}) {
		return g.ConfigSet(ctx, "core.logAllRefUpdates", "always")
	}
	return nil
}
