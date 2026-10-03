package gitobj

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/yasyf/cc-notes/model"
)

// reachCap bounds how many descendants keep a frontier; the drift sweep checks
// many ancestors against a handful of heads.
const reachCap = 8

// Ancestry is what one graph walk proves about a commit being an ancestor of
// another.
type Ancestry int

const (
	// NotAncestor reports a walk that explored the descendant's whole reachable
	// graph without meeting the commit.
	NotAncestor Ancestry = iota
	// Ancestor reports a commit that is the descendant or reachable from it.
	Ancestor
	// AncestryUnknown reports a walk that stopped at a shallow boundary before
	// meeting the commit: the history that would decide it is not in the object
	// database.
	AncestryUnknown
)

// reach is one descendant's partially expanded reachable set: seen holds every
// commit reached so far, queue the frontier not yet expanded. A drained queue
// means seen is complete, which is what makes a negative verdict cacheable;
// truncated records that the expansion stopped at a shallow boundary commit
// whose parents the repository does not hold, so that negative is unproven.
type reach struct {
	seen      map[plumbing.Hash]bool
	queue     []plumbing.Hash
	truncated bool
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
	verdict, err := r.reachable(ctx, ancestor.Hash, descendant.Hash)
	if err != nil {
		return false, fmt.Errorf("walk ancestry of %s: %w", b, err)
	}
	return verdict == Ancestor, nil
}

// Ancestry reports what the repository's graph proves about a being an
// ancestor of — or equal to — b. It differs from IsAncestor only where the
// graph ends at a shallow boundary: a walk from b that drains without meeting
// a after cutting a boundary commit's parents is AncestryUnknown rather than a
// negative. An a absent from a shallow repository is judged by AbsentAncestry,
// so its verdict never depends on whether its object happens to be present.
// An a absent from a complete repository, and any absent b, wrap
// ErrCommitNotFound.
func (r *Repo) Ancestry(ctx context.Context, a, b model.SHA) (Ancestry, error) {
	if err := ctx.Err(); err != nil {
		return NotAncestor, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.refreshGraft(); err != nil {
		return NotAncestor, err
	}
	ancestor, err := r.commit(a)
	if errors.Is(err, ErrCommitNotFound) && len(r.shallow) > 0 {
		return r.absentAncestry(ctx, b)
	}
	if err != nil {
		return NotAncestor, err
	}
	descendant, err := r.commit(b)
	if err != nil {
		return NotAncestor, err
	}
	verdict, err := r.reachable(ctx, ancestor.Hash, descendant.Hash)
	if err != nil {
		return NotAncestor, fmt.Errorf("walk ancestry of %s: %w", b, err)
	}
	return verdict, nil
}

// AbsentAncestry reports what the graph proves about b reaching a commit the
// object database does not hold, such as a revision no object resolves to. No
// walk from b can meet such a commit, so a walk that drains without cutting a
// shallow boundary proves NotAncestor and one that cut a boundary commit's
// parents is AncestryUnknown. The drained frontier is memoized like Ancestry's,
// and a complete repository answers NotAncestor without walking. An absent b
// wraps ErrCommitNotFound.
func (r *Repo) AbsentAncestry(ctx context.Context, b model.SHA) (Ancestry, error) {
	if err := ctx.Err(); err != nil {
		return NotAncestor, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.refreshGraft(); err != nil {
		return NotAncestor, err
	}
	return r.absentAncestry(ctx, b)
}

func (r *Repo) absentAncestry(ctx context.Context, b model.SHA) (Ancestry, error) {
	descendant, err := r.commit(b)
	if err != nil {
		return NotAncestor, err
	}
	if len(r.shallow) == 0 {
		return NotAncestor, nil
	}
	w := r.reachOf(descendant.Hash)
	for len(w.queue) > 0 {
		if err := r.expand(ctx, w); err != nil {
			return NotAncestor, fmt.Errorf("walk ancestry of %s: %w", b, err)
		}
	}
	return w.drained(), nil
}

// reachable expands descendant's frontier until it reaches ancestor or runs
// out, resuming a frontier an earlier call left partially expanded.
func (r *Repo) reachable(ctx context.Context, ancestor, descendant plumbing.Hash) (Ancestry, error) {
	w := r.reachOf(descendant)
	for !w.seen[ancestor] {
		if len(w.queue) == 0 {
			return w.drained(), nil
		}
		if err := r.expand(ctx, w); err != nil {
			return NotAncestor, err
		}
	}
	return Ancestor, nil
}

// expand reads the frontier's next commit and queues its unseen parents, or
// marks the frontier truncated when the commit is a shallow boundary that has
// parents.
//
// The queue is peeked and only dequeued once its commit has been read: a read
// that fails — a stale pack index lookupCommit's reindex cannot repair, a
// cancelled context — must leave the frontier exactly where it was, or the
// next call resumes past a commit it never expanded and memoizes a false
// negative.
func (r *Repo) expand(ctx context.Context, w *reach) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	commit, err := r.lookupCommit(w.queue[0])
	if err != nil {
		return err
	}
	w.queue = w.queue[1:]
	if r.shallow[commit.Hash] {
		w.truncated = w.truncated || len(commit.ParentHashes) > 0
		return nil
	}
	for _, parent := range commit.ParentHashes {
		if !w.seen[parent] {
			w.seen[parent] = true
			w.queue = append(w.queue, parent)
		}
	}
	return nil
}

// drained is a fully expanded frontier's verdict for a commit it never met:
// NotAncestor, or AncestryUnknown once it cut a boundary commit's parents.
func (w *reach) drained() Ancestry {
	if w.truncated {
		return AncestryUnknown
	}
	return NotAncestor
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
