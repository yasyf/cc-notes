package viz

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

// errNoTrunk reports that the trunk branch could not be resolved by any of the
// origin/HEAD, HEAD, or main/master probes.
var errNoTrunk = errors.New("cannot determine trunk")

// headsPrefix and remotesPrefix are the ref namespaces branch lanes come from;
// remote-only branches are folded in under their short name, and origin/HEAD is
// skipped.
const (
	headsPrefix   = "refs/heads/"
	remotesPrefix = "refs/remotes/origin/"
	originHead    = "refs/remotes/origin/HEAD"
)

// trunkName resolves the trunk branch: the remote default (origin/HEAD), else
// the branch HEAD points at, else a probe of local main then master. A
// jj-colocated repo runs detached HEAD routinely, so the probe path is normal.
// Every probe exhausted yields errNoTrunk.
func (b *Builder) trunkName(ctx context.Context) (string, error) {
	switch branch, err := b.store.Git.DefaultBranch(ctx); {
	case err == nil:
		return string(branch), nil
	case !errors.Is(err, gitcmd.ErrNoDefaultBranch):
		return "", fmt.Errorf("resolve trunk: %w", err)
	}
	switch branch, err := b.store.Git.HeadBranch(ctx); {
	case err == nil:
		return string(branch), nil
	case !errors.Is(err, gitcmd.ErrDetachedHead):
		return "", fmt.Errorf("resolve trunk: %w", err)
	}
	for _, name := range []string{"main", "master"} {
		switch _, err := b.store.Repo.Tip(ctx, headsPrefix+name); {
		case err == nil:
			return name, nil
		case !errors.Is(err, gitobj.ErrRefNotFound):
			return "", fmt.Errorf("probe trunk %s: %w", name, err)
		}
	}
	return "", errNoTrunk
}

// enumerate lists every branch lane keyed by short name: local heads plus
// remote-only origin branches (origin/HEAD excluded), deduped preferring the
// local ref. The trunk is guaranteed present even when it has only a remote or
// no enumerated ref.
func (b *Builder) enumerate(ctx context.Context, trunkName string) (map[string]*branchState, error) {
	heads, err := b.store.Repo.ListPrefix(ctx, headsPrefix)
	if err != nil {
		return nil, fmt.Errorf("list heads: %w", err)
	}
	remotes, err := b.store.Repo.ListPrefix(ctx, remotesPrefix)
	if err != nil {
		return nil, fmt.Errorf("list remotes: %w", err)
	}
	states := make(map[string]*branchState, len(heads)+len(remotes))
	for full, tip := range heads {
		short := strings.TrimPrefix(full, headsPrefix)
		states[short] = &branchState{name: short, ref: full, tip: tip}
	}
	for full, tip := range remotes {
		if full == originHead {
			continue
		}
		short := strings.TrimPrefix(full, remotesPrefix)
		if _, ok := states[short]; ok {
			continue
		}
		states[short] = &branchState{name: short, ref: full, tip: tip, remote: true}
	}
	if _, ok := states[trunkName]; !ok {
		tip, ref, err := b.resolveTrunkTip(ctx, trunkName)
		if err != nil {
			return nil, err
		}
		states[trunkName] = &branchState{name: trunkName, ref: ref, tip: tip}
	}
	return states, nil
}

// resolveTrunkTip resolves the trunk's tip when it was not among the enumerated
// heads or remotes — a remote default with no local branch — by probing the
// local then the origin ref.
func (b *Builder) resolveTrunkTip(ctx context.Context, trunkName string) (model.SHA, string, error) {
	for _, ref := range []string{headsPrefix + trunkName, remotesPrefix + trunkName} {
		switch tip, err := b.store.Repo.Tip(ctx, ref); {
		case err == nil:
			return tip, ref, nil
		case !errors.Is(err, gitobj.ErrRefNotFound):
			return "", "", fmt.Errorf("resolve trunk tip %s: %w", ref, err)
		}
	}
	return "", "", fmt.Errorf("%w: no ref for %s", errNoTrunk, trunkName)
}
