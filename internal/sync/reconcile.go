package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
)

// ReconcileReport is the outcome of a Reconcile run: the target branch plus one
// BranchResult per source branch that held open or in-progress work.
type ReconcileReport struct {
	Into     model.Branch
	Branches []BranchResult
}

// BranchResult records what Reconcile found for one source branch: the
// open and in-progress tasks it carried (as folded on the source branch),
// whether it counted as merged into the target, and — when it did not
// promote — why. Reason is empty for a branch that was promoted.
type BranchResult struct {
	Branch model.Branch
	Merged bool
	Reason string
	Tasks  []model.Task
}

// Scanned is the number of source branches that held open or in-progress
// work and were therefore examined.
func (r ReconcileReport) Scanned() int { return len(r.Branches) }

// Merged is the number of scanned branches that counted as merged into the
// target — the branches whose tasks Reconcile promoted (or, under dry-run,
// would promote).
func (r ReconcileReport) Merged() int {
	n := 0
	for _, b := range r.Branches {
		if b.Merged {
			n++
		}
	}
	return n
}

// Promoted is the total number of tasks promoted into the target across
// every merged branch — the plan's task count under dry-run.
func (r ReconcileReport) Promoted() int {
	n := 0
	for _, b := range r.Branches {
		if b.Merged {
			n += len(b.Tasks)
		}
	}
	return n
}

// Reconcile promotes each merged source branch's open and in-progress tasks
// into the target branch. It is a discovery layer over Promote: it resolves
// the target tip, selects source branches (the explicit from list, else the
// branches that hold task refs minus the target), and for each one collects
// its live open and in-progress tasks. A source branch counts as merged when
// force is set or its branch tip is an ancestor of — or equal to — the target
// tip; only merged branches are promoted, and only when dryRun is false.
// Promotion runs through Promote, the single writer, so the run is
// idempotent: a promoted task no longer folds live on its old branch and is
// not re-promoted on a later pass. Source branches are processed in sorted
// order for deterministic output.
func Reconcile(ctx context.Context, s *store.Store, into model.Branch, from []model.Branch, force, dryRun bool) (ReconcileReport, error) {
	targetTip, err := s.Repo.Tip(ctx, "refs/heads/"+string(into))
	if err != nil {
		return ReconcileReport{}, fmt.Errorf("resolve target branch %s: %w", into, err)
	}
	sources, err := candidateBranches(ctx, s, into, from)
	if err != nil {
		return ReconcileReport{}, err
	}
	report := ReconcileReport{Into: into}
	for _, b := range sources {
		tasks, err := s.ListTasks(ctx, b)
		if err != nil {
			return ReconcileReport{}, err
		}
		var open []model.Task
		for _, t := range tasks {
			if t.Status == model.StatusOpen || t.Status == model.StatusInProgress {
				open = append(open, t)
			}
		}
		if len(open) == 0 {
			continue
		}
		result := BranchResult{Branch: b, Tasks: open}
		if force {
			result.Merged = true
		} else {
			bTip, err := s.Repo.Tip(ctx, "refs/heads/"+string(b))
			if errors.Is(err, gitobj.ErrRefNotFound) {
				result.Reason = "branch ref missing"
				report.Branches = append(report.Branches, result)
				continue
			}
			if err != nil {
				return ReconcileReport{}, fmt.Errorf("resolve source branch %s: %w", b, err)
			}
			merged, err := s.Repo.IsAncestor(ctx, bTip, targetTip)
			if err != nil {
				return ReconcileReport{}, err
			}
			result.Merged = merged
			if !merged {
				result.Reason = "not merged"
			}
		}
		report.Branches = append(report.Branches, result)
		if !result.Merged || dryRun {
			continue
		}
		ids := make([]model.EntityID, len(open))
		for i, t := range open {
			ids[i] = t.ID
		}
		if err := Promote(ctx, s, b, into, ids); err != nil {
			return ReconcileReport{}, err
		}
	}
	return report, nil
}

// candidateBranches returns the source branches to examine: the explicit
// from list verbatim, or — when from is empty — every branch that holds task
// refs except the target.
func candidateBranches(ctx context.Context, s *store.Store, into model.Branch, from []model.Branch) ([]model.Branch, error) {
	if len(from) > 0 {
		return from, nil
	}
	branches, err := taskBranches(ctx, s)
	if err != nil {
		return nil, err
	}
	out := make([]model.Branch, 0, len(branches))
	for _, b := range branches {
		if b != into {
			out = append(out, b)
		}
	}
	return out, nil
}

// taskBranches enumerates the distinct branch namespaces that hold task refs
// under refs.TasksRoot, sorted for deterministic iteration.
func taskBranches(ctx context.Context, s *store.Store) ([]model.Branch, error) {
	tips, err := s.Repo.ListPrefix(ctx, refs.TasksRoot)
	if err != nil {
		return nil, err
	}
	seen := make(map[model.Branch]bool, len(tips))
	branches := make([]model.Branch, 0, len(tips))
	for name := range tips {
		parsed, err := refs.Parse(name)
		if err != nil {
			return nil, fmt.Errorf("parse task ref %s: %w", name, err)
		}
		if seen[parsed.Branch] {
			continue
		}
		seen[parsed.Branch] = true
		branches = append(branches, parsed.Branch)
	}
	slices.Sort(branches)
	return branches, nil
}
