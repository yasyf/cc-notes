package gitobj

import (
	"context"
	"fmt"
	"slices"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/yasyf/cc-notes/model"
)

// reachCap bounds how many descendants keep a frontier; the drift sweep checks
// many ancestors against a handful of heads.
const reachCap = 8

// reach is one descendant's partially expanded reachable set: seen holds every
// commit reached so far, queue the frontier not yet expanded. A drained queue
// means seen is complete, which is what makes a negative verdict cacheable.
type reach struct {
	seen  map[plumbing.Hash]bool
	queue []plumbing.Hash
}

// IsAncestor reports whether a is an ancestor of — or equal to — b. A
// shallow-clone graft bounds the walk at its boundary commits, so the verdict
// matches git merge-base --is-ancestor rather than treating the grafted
// parents as reachable.
func (r *Repo) IsAncestor(ctx context.Context, a, b model.SHA) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.refreshGraft(); err != nil {
		return false, err
	}
	ancestor, err := r.commit(a)
	if err != nil {
		return false, err
	}
	descendant, err := r.commit(b)
	if err != nil {
		return false, err
	}
	ok, err := r.reachable(ctx, ancestor.Hash, descendant.Hash)
	if err != nil {
		return false, fmt.Errorf("walk ancestry of %s: %w", b, err)
	}
	return ok, nil
}

// reachable expands descendant's frontier until it reaches ancestor or runs
// out, resuming a frontier an earlier call left partially expanded.
//
// The queue is peeked and only dequeued once its commit has been read: a read
// that fails — a stale pack index lookupCommit's reindex cannot repair, a
// cancelled context — must leave the frontier exactly where it was, or the
// next call resumes past a commit it never expanded and memoizes a false
// negative.
func (r *Repo) reachable(ctx context.Context, ancestor, descendant plumbing.Hash) (bool, error) {
	w := r.reachOf(descendant)
	for !w.seen[ancestor] {
		if len(w.queue) == 0 {
			return false, nil
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		commit, err := r.lookupCommit(w.queue[0])
		if err != nil {
			return false, err
		}
		w.queue = w.queue[1:]
		if r.shallow[commit.Hash] {
			continue
		}
		for _, parent := range commit.ParentHashes {
			if !w.seen[parent] {
				w.seen[parent] = true
				w.queue = append(w.queue, parent)
			}
		}
	}
	return true, nil
}

func (r *Repo) reachOf(descendant plumbing.Hash) *reach {
	if w, ok := r.reaches[descendant]; ok {
		i := slices.Index(r.reachOrder, descendant)
		r.reachOrder = append(slices.Delete(r.reachOrder, i, i+1), descendant)
		return w
	}
	w := &reach{seen: map[plumbing.Hash]bool{descendant: true}, queue: []plumbing.Hash{descendant}}
	r.reaches[descendant] = w
	r.reachOrder = append(r.reachOrder, descendant)
	for len(r.reachOrder) > reachCap {
		delete(r.reaches, r.reachOrder[0])
		r.reachOrder = r.reachOrder[1:]
	}
	return w
}
