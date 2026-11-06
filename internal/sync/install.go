package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/refs"
)

const (
	pushRefspec = refs.PushRefspec
	// headRefspec keeps plain git push pushing the current branch: any
	// remote.<r>.push entry overrides push.default, so installing a push
	// refspec into a remote that had none must restore the default first.
	headRefspec = "HEAD"
)

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
	pairs, err := g.ConfigLocal(ctx)
	if err != nil {
		return report, err
	}
	local := localConfig(pairs)
	if !local.configures(remote) {
		if err := ensureRemote(ctx, g, remote); err != nil {
			return report, err
		}
	}
	if err := upgradeFetch(ctx, g, remote, local.values("remote."+remote+".fetch"), true, &report); err != nil {
		return report, err
	}
	pushKey := "remote." + remote + ".push"
	push := local.values(pushKey)
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
	if !slices.Equal(local.values("core.logallrefupdates"), []string{"always"}) {
		if err := g.ConfigSet(ctx, "core.logAllRefUpdates", "always"); err != nil {
			return report, err
		}
		report.Added = append(report.Added, "core.logAllRefUpdates=always")
	}
	// Only a remote with local fetch lines can carry the pre-fix killer refspec.
	for _, other := range local.fetchRemotes() {
		if other == remote {
			continue
		}
		if err := upgradeFetch(ctx, g, other, local.values("remote."+other+".fetch"), false, &report); err != nil {
			return report, err
		}
	}
	return report, nil
}

type localConfig [][2]string

func (c localConfig) values(key string) []string {
	var values []string
	for _, pair := range c {
		if pair[0] == key {
			values = append(values, pair[1])
		}
	}
	return values
}

func (c localConfig) configures(remote string) bool {
	prefix := "remote." + remote + "."
	return slices.ContainsFunc(c, func(pair [2]string) bool {
		rest, ok := strings.CutPrefix(pair[0], prefix)
		return ok && !strings.Contains(rest, ".")
	})
}

func (c localConfig) fetchRemotes() []string {
	var remotes []string
	for _, pair := range c {
		rest, ok := strings.CutPrefix(pair[0], "remote.")
		if !ok {
			continue
		}
		if name, ok := strings.CutSuffix(rest, ".fetch"); ok && name != "" && !slices.Contains(remotes, name) {
			remotes = append(remotes, name)
		}
	}
	slices.Sort(remotes)
	return remotes
}

// upgradeFetch reconciles remote's fetch refspecs with the prune-safe
// tracking-namespace form. It rewrites the pre-fix same-namespace force-mirror
// in place, drops a redundant old line when the new one is already present, and
// — only when wiring the target remote — adds the tracking refspec to a remote
// that carries neither. It is idempotent: a remote already on the new refspec
// is a no-op.
func upgradeFetch(ctx context.Context, g gitcmd.Git, remote string, fetch []string, target bool, report *InstallReport) error {
	fetchKey := "remote." + remote + ".fetch"
	want := refs.FetchRefspec(remote)
	hasOld := slices.Contains(fetch, refs.OldFetchRefspec)
	hasNew := slices.Contains(fetch, want)
	switch {
	case hasOld && hasNew:
		// Both lines live: a plain fetch --prune still deletes unsynced refs
		// through the old one, so drop it. A concurrent autoInstall racing us may
		// have dropped it already (ErrConfigNoMatch) — tolerate that and report
		// nothing, since this call changed nothing.
		switch err := g.ConfigUnsetValue(ctx, fetchKey, refs.OldFetchRefspec); {
		case errors.Is(err, gitcmd.ErrConfigNoMatch):
		case err != nil:
			return err
		default:
			report.Removed = append(report.Removed, fetchKey+"="+refs.OldFetchRefspec)
		}
	case hasOld:
		if err := g.ConfigReplaceValue(ctx, fetchKey, refs.OldFetchRefspec, want); err != nil {
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
