package viz

import (
	"sort"

	"github.com/yasyf/cc-notes/model"
)

// filterLanes splits the enumerated branches into the lanes the graph renders
// and the ones it hides, before any per-branch git work runs — a repository with
// hundreds of long-dead branches pays for the handful that are still interesting.
// A branch keeps its lane when its tip is no older than the window cutoff, when
// a live task names it, or when it rejoined the trunk inside the window; every
// other branch is hidden, as is any surplus past the lane cap, dropped
// stalest-tip first with ties broken by name. Task-named lanes are exempt from
// the cap: an agent's branch stays on the board however stale the repository
// around it. The trunk is never a candidate — it always keeps its lane.
//
// Hidden branches still exist, so the callers that reconstruct deleted lanes
// must keep excluding them by name and tip; they are dropped from the rendering,
// not from the repository.
func (b *Builder) filterLanes(trunk *branchState, others []*branchState, tasks []model.Task, r *topoRun) (kept, hidden []*branchState, err error) {
	claimed := liveTaskBranches(tasks)
	var rescued, windowed, stale []*branchState
	for _, s := range others {
		switch {
		case claimed[s.name]:
			rescued = append(rescued, s)
		case s.tipTime >= r.cutoff:
			windowed = append(windowed, s)
		default:
			stale = append(stale, s)
		}
	}

	limit := b.laneCap()
	// A stale branch's tip predates every windowed tip, so it can only survive
	// the cap while the windowed lanes leave room — which is what makes the
	// merged-rescue probe skippable on a repository already over the cap.
	if len(stale) > 0 && len(windowed) < limit {
		rejoined, err := b.rescueMerged(trunk, stale, r)
		if err != nil {
			return nil, nil, err
		}
		for _, s := range stale {
			if rejoined[s.name] {
				windowed = append(windowed, s)
				continue
			}
			hidden = append(hidden, s)
		}
	} else {
		hidden = append(hidden, stale...)
	}

	if len(windowed) > limit {
		sort.Slice(windowed, func(i, j int) bool {
			if windowed[i].tipTime != windowed[j].tipTime {
				return windowed[i].tipTime > windowed[j].tipTime
			}
			return windowed[i].name < windowed[j].name
		})
		hidden = append(hidden, windowed[limit:]...)
		windowed = windowed[:limit]
	}

	kept = append(rescued, windowed...)
	sort.Slice(kept, func(i, j int) bool { return kept[i].name < kept[j].name })
	sort.Slice(hidden, func(i, j int) bool { return hidden[i].name < hidden[j].name })
	return kept, hidden, nil
}

// rescueMerged reports which of the stale branches rejoined the trunk inside the
// window, keyed by short name. A branch reachable from the trunk tip but not
// from the pre-merge trunk the window opened at was absorbed during the window,
// so its lane is recent history even though its own commits are not. With no
// merge in the window there is no boundary to probe against and nothing is
// rescued.
func (b *Builder) rescueMerged(trunk *branchState, stale []*branchState, r *topoRun) (map[string]bool, error) {
	merged, err := r.mergedIntoTrunk(trunk.tip)
	if err != nil {
		return nil, err
	}
	candidates := false
	for _, s := range stale {
		if merged[s.ref] {
			candidates = true
			break
		}
	}
	if !candidates {
		return nil, nil
	}
	before, ok, err := r.mergedBeforeWindow(trunk.tip)
	if err != nil || !ok {
		return nil, err
	}
	rejoined := make(map[string]bool)
	for _, s := range stale {
		if merged[s.ref] && !before[s.ref] {
			rejoined[s.name] = true
		}
	}
	return rejoined, nil
}

// laneCap is the configured lane cap, defaultMaxLanes when unset.
func (b *Builder) laneCap() int {
	if b.MaxLanes > 0 {
		return b.MaxLanes
	}
	return defaultMaxLanes
}

// liveTaskBranches is the set of branch names named by a task that is still
// being worked — neither done nor cancelled.
func liveTaskBranches(tasks []model.Task) map[string]bool {
	live := make(map[string]bool)
	for _, t := range tasks {
		if t.Branch == "" || t.Status == model.StatusDone || t.Status == model.StatusCancelled {
			continue
		}
		live[string(t.Branch)] = true
	}
	return live
}
