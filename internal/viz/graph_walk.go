package viz

import (
	"context"
	"fmt"

	"golang.org/x/sync/errgroup"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

// windowSet is the set of commit shas reachable from one tip within the history
// window, with min the earliest commit time among them.
type windowSet struct {
	shas map[model.SHA]struct{}
	min  int64
}

// topoRun carries the per-build memos of one topology call: commit times, window
// reachable sets, first-parent merge scans, the batched reachability probes that
// replace per-branch ancestry walks, and the squash-inference trailer reads.
// It is single-goroutine state, so it needs no locking: the prefetch helpers fan
// git execs out under an errgroup but fold the results into the maps on the
// caller's goroutine. Commit times and window sets are keyed by immutable shas
// and never go stale; the ref-keyed sets are ref-dependent, which is why they
// live here for one build rather than on the Builder.
type topoRun struct {
	b   *Builder
	ctx context.Context

	// cutoff is the window floor every per-branch scan is bounded by, fixed
	// before any branch work; since is the attribution window, resolved only
	// after merge classification has rewritten the merged forks.
	cutoff int64
	since  int64

	times     map[model.SHA]int64
	windows   map[model.SHA]windowSet
	merges    map[model.SHA][]gitobj.CodeCommit
	contained map[model.SHA]map[string]bool
	trailers  map[model.SHA]map[model.SHA][]string

	merged       map[string]bool
	mergedBefore map[string]bool
}

// newTopoRun starts a build memo bounded by the window floor cutoff.
func newTopoRun(ctx context.Context, b *Builder, cutoff int64) *topoRun {
	return &topoRun{
		b:         b,
		ctx:       ctx,
		cutoff:    cutoff,
		times:     make(map[model.SHA]int64),
		windows:   make(map[model.SHA]windowSet),
		merges:    make(map[model.SHA][]gitobj.CodeCommit),
		contained: make(map[model.SHA]map[string]bool),
		trailers:  make(map[model.SHA]map[model.SHA][]string),
	}
}

// commitTime returns a commit's committer time, memoized. The walk is unbounded
// so fork and merge points older than the window still resolve.
func (r *topoRun) commitTime(sha model.SHA) (int64, error) {
	if t, ok := r.times[sha]; ok {
		return t, nil
	}
	commits, _, err := r.b.store.Repo.WalkCommits(r.ctx, []model.SHA{sha}, 1, 0)
	if err != nil {
		return 0, fmt.Errorf("commit time %s: %w", sha, err)
	}
	if len(commits) == 0 {
		return 0, fmt.Errorf("commit time %s: no commit", sha)
	}
	r.times[sha] = commits[0].CommitTime
	return commits[0].CommitTime, nil
}

// window returns the window-bounded set of commits reachable from sha, memoized
// by tip.
func (r *topoRun) window(sha model.SHA) (windowSet, error) {
	if w, ok := r.windows[sha]; ok {
		return w, nil
	}
	commits, _, err := r.b.store.Repo.WalkCommits(r.ctx, []model.SHA{sha}, walkLimit, r.since)
	if err != nil {
		return windowSet{}, fmt.Errorf("walk %s: %w", sha, err)
	}
	w := windowSet{shas: make(map[model.SHA]struct{}, len(commits))}
	for i, c := range commits {
		w.shas[c.SHA] = struct{}{}
		if i == 0 || c.CommitTime < w.min {
			w.min = c.CommitTime
		}
	}
	r.windows[sha] = w
	return w, nil
}

// firstParentMerges returns the merge commits on tip's first-parent line back to
// the window cutoff, newest first, memoized by tip. The trunk's line is scanned
// once per build rather than once per branch.
func (r *topoRun) firstParentMerges(tip model.SHA) ([]gitobj.CodeCommit, error) {
	if merges, ok := r.merges[tip]; ok {
		return merges, nil
	}
	merges, err := r.b.store.Repo.FirstParentMerges(r.ctx, tip, walkLimit, r.cutoff)
	if err != nil {
		return nil, fmt.Errorf("first-parent merges %s: %w", tip, err)
	}
	r.merges[tip] = merges
	return merges, nil
}

// mergedIntoTrunk is the set of full branch ref names whose tips are reachable
// from the trunk tip — one for-each-ref replacing an ancestry walk per branch.
func (r *topoRun) mergedIntoTrunk(trunkTip model.SHA) (map[string]bool, error) {
	if r.merged != nil {
		return r.merged, nil
	}
	names, err := r.b.store.Git.MergedRefs(r.ctx, trunkTip, headsPrefix, remotesPrefix)
	if err != nil {
		return nil, err
	}
	r.merged = refSet(names)
	return r.merged, nil
}

// mergedBeforeWindow is the set of branch refs the trunk had already absorbed
// when the window opened, probed at the pre-merge trunk of the oldest in-window
// first-parent merge. A branch reachable from the trunk tip but absent here
// rejoined the trunk inside the window, which is what rescues an otherwise stale
// lane. ok is false when the window holds no merge at all, so no boundary commit
// exists and nothing qualifies.
func (r *topoRun) mergedBeforeWindow(trunkTip model.SHA) (set map[string]bool, ok bool, err error) {
	if r.mergedBefore != nil {
		return r.mergedBefore, true, nil
	}
	merges, err := r.firstParentMerges(trunkTip)
	if err != nil {
		return nil, false, err
	}
	if len(merges) == 0 {
		return nil, false, nil
	}
	names, err := r.b.store.Git.MergedRefs(r.ctx, merges[len(merges)-1].Parents[0], headsPrefix, remotesPrefix)
	if err != nil {
		return nil, false, err
	}
	r.mergedBefore = refSet(names)
	return r.mergedBefore, true, nil
}

// prefetchContained resolves which of refs each merge parent contains, one
// for-each-ref per parent run under bounded concurrency. It is called once per
// build with the full set of refs still needing a merge point, so a memoized
// parent answers every later lookup.
func (r *topoRun) prefetchContained(ctx context.Context, refs []string, parents []model.SHA) error {
	todo := make([]model.SHA, 0, len(parents))
	for _, p := range parents {
		if _, ok := r.contained[p]; ok {
			continue
		}
		r.contained[p] = nil
		todo = append(todo, p)
	}
	found := make([][]string, len(todo))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(gitConcurrency)
	for i, p := range todo {
		g.Go(func() error {
			names, err := r.b.store.Git.MergedRefs(gctx, p, refs...)
			if err != nil {
				return err
			}
			found[i] = names
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	for i, p := range todo {
		r.contained[p] = refSet(found[i])
	}
	return nil
}

// contains reports whether the merge commit landed the branch at ref: whether
// any parent past the first — an octopus merge carries a branch under any of
// them — has the ref's tip in its history.
func (r *topoRun) contains(m gitobj.CodeCommit, ref string) bool {
	for _, p := range m.Parents[1:] {
		if r.contained[p][ref] {
			return true
		}
	}
	return false
}

// prefetchTrailers loads the cc-task trailers on head's first-parent line above
// each fork base, one git log per base under bounded concurrency, memoized by
// base.
func (r *topoRun) prefetchTrailers(ctx context.Context, bases []model.SHA, head model.SHA) error {
	todo := make([]model.SHA, 0, len(bases))
	for _, base := range bases {
		if _, ok := r.trailers[base]; ok {
			continue
		}
		r.trailers[base] = nil
		todo = append(todo, base)
	}
	found := make([]map[model.SHA][]string, len(todo))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(gitConcurrency)
	for i, base := range todo {
		g.Go(func() error {
			trailers, err := r.b.store.Git.TaskTrailersFirstParent(gctx, string(base), string(head))
			if err != nil {
				return fmt.Errorf("task trailers %s..%s: %w", base, head, err)
			}
			found[i] = trailers
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return err
	}
	for i, base := range todo {
		r.trailers[base] = found[i]
	}
	return nil
}

// refSet indexes a for-each-ref result by full ref name.
func refSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[name] = true
	}
	return set
}

// mergeParents lists every parent past the first across the given merges — the
// commits whose containment decides which branch each merge landed.
func mergeParents(merges []gitobj.CodeCommit) []model.SHA {
	var parents []model.SHA
	for _, m := range merges {
		parents = append(parents, m.Parents[1:]...)
	}
	return parents
}

// attribute sets each lane's window commit count and start/end. A branch's
// commits are the walked commits reachable from its tip but not from its fork
// point — its post-fork commits within the window; the trunk keeps every window
// commit reachable from its tip. Start is the fork time (the trunk's earliest
// window commit), and End the merge time, 0 while the lane is open.
func (r *topoRun) attribute(trunk *branchState, others []*branchState) error {
	trunkWin, err := r.window(trunk.tip)
	if err != nil {
		return err
	}
	trunk.commits = len(trunkWin.shas)
	trunk.start = trunkWin.min
	trunk.end = 0
	for _, s := range others {
		tipWin, err := r.window(s.tip)
		if err != nil {
			return err
		}
		if s.hasFork {
			forkWin, err := r.window(s.forkBase)
			if err != nil {
				return err
			}
			n := 0
			for sha := range tipWin.shas {
				if _, ok := forkWin.shas[sha]; !ok {
					n++
				}
			}
			s.commits = n
			s.start = s.forkTime
		} else {
			s.commits = len(tipWin.shas)
			s.start = 0
		}
		if s.merge != nil {
			s.end = s.merge.time
		} else {
			s.end = 0
		}
	}
	return nil
}
