package viz

import (
	"context"
	"errors"
	"fmt"

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
