package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/yasyf/cc-notes/internal/gitcmd"
)

const (
	// oldFetchRefspec is the pre-fix same-namespace force-mirror. A plain
	// git fetch --prune uses it to force-clobber a diverged canonical entity
	// ref and to prune a locally-created, not-yet-synced one; Install rewrites
	// it to fetchRefspec so a plain fetch touches only the tracking namespace.
	oldFetchRefspec = "+" + namespace + "*:" + namespace + "*"
	// pushRefspec carries entity refs on plain git push, never forced: a
	// diverged ref must resolve through Sync's union merge, never a clobber.
	pushRefspec = namespace + "*:" + namespace + "*"
	// headRefspec keeps plain git push pushing the current branch: any
	// remote.<r>.push entry overrides push.default, so installing a push
	// refspec into a remote that had none must restore the default first.
	headRefspec = "HEAD"
)

// fetchRefspec is the plain-fetch refspec Install writes for remote: it
// force-mirrors the remote's entity refs into the per-remote tracking
// namespace Sync converges from — refs/cc-notes-sync/<remote>/ — never into
// the canonical refs/cc-notes/ namespace. So fetch.prune can only prune
// tracking copies, which mirror the remote by definition, never a
// locally-created, not-yet-synced canonical ref. It is byte-for-byte the
// refspec Sync fetches with, so a plain fetch pre-populates exactly the
// tracking refs Sync then folds into canonical refs via compare-and-swap.
func fetchRefspec(remote string) string {
	return "+" + namespace + "*:" + syncNamespace + remote + "/*"
}

// InstallReport lists what one Install call changed in .git/config.
type InstallReport struct {
	// Added holds each config line written, as "key=value", in write order;
	// it is empty when the install was an idempotent no-op. A pre-fix fetch
	// refspec rewritten in place — on the wired remote or on any other remote
	// swept for the killer refspec — appears here as its new tracking-namespace
	// value.
	Added []string
	// Removed names each pre-fix fetch refspec cleared, as "key=value". A repo
	// carrying both the old same-namespace refspec and the new tracking one
	// still applies the old on every plain git fetch --prune, so it keeps
	// deleting unsynced canonical refs even with the new line present. Clearing
	// the old line is therefore a data-loss fix, not cosmetic, and autoInstall
	// announces it. An old binary re-adding the killer refspec after a new one
	// leaves this state.
	Removed []string
	// HeadPushAdded reports that the HEAD push refspec was among the added
	// lines: the remote had no push refspec before, so plain git push now
	// takes its behavior from remote.<r>.push instead of push.default.
	HeadPushAdded bool
}

// Install wires remote so plain git fetch mirrors cc-notes entity refs into
// the per-remote tracking namespace Sync converges from and plain git push
// carries them alongside branches, reporting every config line it added. It
// is idempotent — each line is added only when absent, and a rerun reports
// nothing — and it upgrades a repo wired before the prune-safety fix by
// rewriting the old same-namespace fetch refspec in place. The upgrade sweeps
// every configured remote, not just remote: a repo wired pre-fix on a
// non-default remote keeps the killer refspec there, which a plain
// git fetch --all --prune uses to delete unsynced canonical refs, so each
// remote carrying it is rewritten to its own tracking-namespace form. It
// preserves existing push behavior: a remote with no push refspec gets HEAD
// before the cc-notes refspec, while a remote with its own push refspecs keeps
// them untouched. An unconfigured remote fails wrapping ErrRemoteNotFound.
func Install(ctx context.Context, g gitcmd.Git, remote string) (InstallReport, error) {
	report, err := install(ctx, g, remote)
	if err != nil {
		return InstallReport{}, fmt.Errorf("install cc-notes refspecs for %s: %w", remote, err)
	}
	return report, nil
}

func install(ctx context.Context, g gitcmd.Git, remote string) (InstallReport, error) {
	var report InstallReport
	if err := ensureRemote(ctx, g, remote); err != nil {
		return report, err
	}
	if err := upgradeFetch(ctx, g, remote, true, &report); err != nil {
		return report, err
	}
	pushKey := "remote." + remote + ".push"
	push, err := g.ConfigGetAll(ctx, pushKey)
	if err != nil {
		return report, err
	}
	if len(push) == 0 {
		if err := g.ConfigAdd(ctx, pushKey, headRefspec); err != nil {
			return report, err
		}
		report.Added = append(report.Added, pushKey+"="+headRefspec)
		report.HeadPushAdded = true
	}
	if !slices.Contains(push, pushRefspec) {
		if err := g.ConfigAdd(ctx, pushKey, pushRefspec); err != nil {
			return report, err
		}
		report.Added = append(report.Added, pushKey+"="+pushRefspec)
	}
	reflog, err := g.ConfigGetAll(ctx, "core.logAllRefUpdates")
	if err != nil {
		return report, err
	}
	if !slices.Equal(reflog, []string{"always"}) {
		if err := g.ConfigSet(ctx, "core.logAllRefUpdates", "always"); err != nil {
			return report, err
		}
		report.Added = append(report.Added, "core.logAllRefUpdates=always")
	}
	// Sweep every other configured remote for the pre-fix killer fetch refspec
	// and rewrite it in place; a remote carrying neither the old nor the new
	// refspec was never wired for cc-notes and is left untouched.
	remotes, err := g.Remotes(ctx)
	if err != nil {
		return report, err
	}
	for _, other := range remotes {
		if other == remote {
			continue
		}
		if err := upgradeFetch(ctx, g, other, false, &report); err != nil {
			return report, err
		}
	}
	return report, nil
}

// upgradeFetch reconciles remote's fetch refspecs with the prune-safe
// tracking-namespace form. It rewrites the pre-fix same-namespace force-mirror
// in place, drops a redundant old line when the new one is already present, and
// — only when wiring the target remote — adds the tracking refspec to a remote
// that carries neither. It is idempotent: a remote already on the new refspec
// is a no-op.
func upgradeFetch(ctx context.Context, g gitcmd.Git, remote string, target bool, report *InstallReport) error {
	fetchKey := "remote." + remote + ".fetch"
	fetch, err := g.ConfigGetAll(ctx, fetchKey)
	if err != nil {
		return err
	}
	want := fetchRefspec(remote)
	hasOld := slices.Contains(fetch, oldFetchRefspec)
	hasNew := slices.Contains(fetch, want)
	switch {
	case hasOld && hasNew:
		// Both lines live: a plain fetch --prune still deletes unsynced refs
		// through the old one, so drop it. A concurrent autoInstall racing us may
		// have dropped it already (ErrConfigNoMatch) — tolerate that and report
		// nothing, since this call changed nothing.
		switch err := g.ConfigUnsetValue(ctx, fetchKey, oldFetchRefspec); {
		case errors.Is(err, gitcmd.ErrConfigNoMatch):
		case err != nil:
			return err
		default:
			report.Removed = append(report.Removed, fetchKey+"="+oldFetchRefspec)
		}
	case hasOld:
		if err := g.ConfigReplaceValue(ctx, fetchKey, oldFetchRefspec, want); err != nil {
			return err
		}
		report.Added = append(report.Added, fetchKey+"="+want)
	case !hasNew && target:
		if err := g.ConfigAdd(ctx, fetchKey, want); err != nil {
			return err
		}
		report.Added = append(report.Added, fetchKey+"="+want)
	}
	return nil
}
