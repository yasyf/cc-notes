package viz

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
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
// local ref. One for-each-ref carries each ref's tip and committer time
// together, so the window filter can judge a branch before any per-branch git
// work runs. The trunk is guaranteed present even when it has only a remote or
// no enumerated ref.
func (b *Builder) enumerate(ctx context.Context, trunkName string) (map[string]*branchState, error) {
	tips, err := b.store.Git.RefTips(ctx, headsPrefix, remotesPrefix)
	if err != nil {
		return nil, fmt.Errorf("list branch refs: %w", err)
	}
	states := make(map[string]*branchState, len(tips))
	for _, rt := range tips {
		if !strings.HasPrefix(rt.Ref, headsPrefix) {
			continue
		}
		short := strings.TrimPrefix(rt.Ref, headsPrefix)
		states[short] = &branchState{name: short, ref: rt.Ref, tip: rt.Tip, tipTime: rt.Time}
	}
	for _, rt := range tips {
		if !strings.HasPrefix(rt.Ref, remotesPrefix) || rt.Ref == originHead {
			continue
		}
		short := strings.TrimPrefix(rt.Ref, remotesPrefix)
		if _, ok := states[short]; ok {
			continue
		}
		states[short] = &branchState{name: short, ref: rt.Ref, tip: rt.Tip, tipTime: rt.Time, remote: true}
	}
	if _, ok := states[trunkName]; !ok {
		trunk, err := b.resolveTrunkTip(ctx, trunkName)
		if err != nil {
			return nil, err
		}
		states[trunkName] = trunk
	}
	return states, nil
}

// resolveTrunkTip resolves the trunk's lane when it was not among the enumerated
// heads or remotes — a remote default whose branch ref is absent — by probing the
// local then the origin ref.
func (b *Builder) resolveTrunkTip(ctx context.Context, trunkName string) (*branchState, error) {
	local, remote := headsPrefix+trunkName, remotesPrefix+trunkName
	tips, err := b.store.Git.RefTips(ctx, local, remote)
	if err != nil {
		return nil, fmt.Errorf("resolve trunk tip %s: %w", trunkName, err)
	}
	for _, want := range []string{local, remote} {
		for _, rt := range tips {
			if rt.Ref == want {
				return &branchState{name: trunkName, ref: rt.Ref, tip: rt.Tip, tipTime: rt.Time, remote: want == remote}, nil
			}
		}
	}
	return nil, fmt.Errorf("%w: no ref for %s", errNoTrunk, trunkName)
}
