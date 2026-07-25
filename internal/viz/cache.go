package viz

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/model"
)

// mergeBase is a memoized merge-base result: Base is the common ancestor, and
// None reports that the two tips share no ancestor (git merge-base found none).
type mergeBase struct {
	Base model.SHA
	None bool
}

// mergeBaseOf returns the merge base of tips a and c, memoized by the ordered
// pair. found is false when the two share no common ancestor. Both tips are
// immutable commit shas, so a cached result never goes stale.
func (b *Builder) mergeBaseOf(ctx context.Context, a, c model.SHA) (base model.SHA, found bool, err error) {
	key := mergeBaseKey(a, c)

	b.mbMu.Lock()
	if mb, ok := b.mbCache[key]; ok {
		b.mbMu.Unlock()
		return mb.Base, !mb.None, nil
	}
	b.mbMu.Unlock()

	got, err := b.store.Git.MergeBase(ctx, string(a), string(c))
	if errors.Is(err, gitcmd.ErrRevNotFound) {
		b.storeMergeBase(key, mergeBase{None: true})
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("merge base %s %s: %w", a, c, err)
	}
	b.storeMergeBase(key, mergeBase{Base: got})
	return got, true, nil
}

// prefetchMergeBases resolves every uncached pair concurrently so a scan that
// needs many merge bases pays one bounded fan-out of git execs instead of a
// serial one. Pairs already memoized are skipped, duplicates collapse through
// mergeBaseKey, and each worker stores its result through mergeBaseOf, so a
// later lookup is a cache hit. Only these execs run in parallel: the go-git
// reads behind them are serialized by the repository mutex and never fan out.
func (b *Builder) prefetchMergeBases(ctx context.Context, pairs [][2]model.SHA) error {
	seen := make(map[string]struct{}, len(pairs))
	todo := make([][2]model.SHA, 0, len(pairs))
	b.mbMu.Lock()
	for _, pair := range pairs {
		key := mergeBaseKey(pair[0], pair[1])
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if _, ok := b.mbCache[key]; !ok {
			todo = append(todo, pair)
		}
	}
	b.mbMu.Unlock()

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(gitConcurrency)
	for _, pair := range todo {
		g.Go(func() error {
			_, _, err := b.mergeBaseOf(gctx, pair[0], pair[1])
			return err
		})
	}
	return g.Wait()
}

func (b *Builder) storeMergeBase(key string, mb mergeBase) {
	b.mbMu.Lock()
	b.mbCache[key] = mb
	b.mbMu.Unlock()
}

// mergeBaseKey orders the pair so merge-base(a,c) and merge-base(c,a) share one
// cache entry; the operation is commutative.
func mergeBaseKey(a, c model.SHA) string {
	if a > c {
		a, c = c, a
	}
	return string(a) + "\x00" + string(c)
}
