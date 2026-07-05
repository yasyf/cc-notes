package viz

import (
	"context"
	"fmt"

	"github.com/yasyf/cc-notes/model"
)

// windowSet is the set of commit shas reachable from one tip within the history
// window, with min the earliest commit time among them.
type windowSet struct {
	shas map[model.SHA]struct{}
	min  int64
}

// topoRun carries the per-build memo of commit times and window reachable sets.
// It is single-goroutine state for one topology call, so it needs no locking;
// commit times and window sets are keyed by immutable shas and never go stale.
type topoRun struct {
	b       *Builder
	ctx     context.Context
	since   int64
	times   map[model.SHA]int64
	windows map[model.SHA]windowSet
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
