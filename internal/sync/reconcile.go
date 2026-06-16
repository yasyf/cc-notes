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

// openOnBranch selects the open and in-progress tasks folding to branch,
// preserving the input order.
func openOnBranch(tasks []model.Task, branch model.Branch) []model.Task {
	var open []model.Task
	for _, t := range tasks {
		if t.Branch != branch {
			continue
		}
		if t.Status == model.StatusOpen || t.Status == model.StatusInProgress {
			open = append(open, t)
		}
	}
	return open
}

// ReconcileReport is the outcome of a Reconcile run: the target branch plus one
// BranchResult per source branch that held open or in-progress work.
type ReconcileReport struct {
	Into     model.Branch
	Branches []BranchResult
}

// BranchResult records what Reconcile found for one source branch: the
// open and in-progress tasks folding to it, whether it counted as merged
// into the target, and — when it did not merge — why. Reason is empty for a
// merged branch.
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
// target — the branches whose tasks Reconcile moved (or, under dry-run,
// would move).
func (r ReconcileReport) Merged() int {
	n := 0
	for _, b := range r.Branches {
		if b.Merged {
			n++
		}
	}
	return n
}

// Carried is the total number of tasks moved into the target across every
// merged branch — the plan's task count under dry-run.
func (r ReconcileReport) Carried() int {
	n := 0
	for _, b := range r.Branches {
		if b.Merged {
			n += len(b.Tasks)
		}
	}
	return n
}

// Reconcile reassigns each merged source branch's open and in-progress tasks
// to the target branch. It resolves the target tip, selects source branches
// (the explicit from list, else every branch that folded tasks claim minus
// the target and the backlog), and for each one collects the open and
// in-progress tasks folding to it. A source branch counts as merged when
// force is set or its branch tip is an ancestor of — or equal to — the target
// tip; only merged branches are moved, and only when dryRun is false. Each
// moved task gets a SetBranch{into} op, so the run is idempotent: a moved
// task folds to into and the next pass's source scan no longer finds it.
// Source branches are processed in sorted order for deterministic output.
func Reconcile(ctx context.Context, s *store.Store, into model.Branch, from []model.Branch, force, dryRun bool) (ReconcileReport, error) {
	targetTip, err := s.Repo.Tip(ctx, "refs/heads/"+string(into))
	if err != nil {
		return ReconcileReport{}, fmt.Errorf("resolve target branch %s: %w", into, err)
	}
	all, err := s.ListTasks(ctx)
	if err != nil {
		return ReconcileReport{}, err
	}
	sources := candidateBranches(all, into, from)
	report := ReconcileReport{Into: into}
	for _, b := range sources {
		open := openOnBranch(all, b)
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
		for _, t := range open {
			if _, err := s.Append(ctx, refs.Task(t.ID), []model.Op{model.SetBranch{Branch: into}}); err != nil {
				return ReconcileReport{}, fmt.Errorf("move task %s: %w", t.ID.Short(), err)
			}
		}
	}
	return report, nil
}

// candidateBranches returns the source branches to examine: the explicit
// from list verbatim, or — when from is empty — every branch the folded
// tasks claim, except the target and the backlog (the empty branch is not a
// mergeable source).
func candidateBranches(tasks []model.Task, into model.Branch, from []model.Branch) []model.Branch {
	if len(from) > 0 {
		return from
	}
	seen := make(map[model.Branch]bool)
	out := make([]model.Branch, 0)
	for _, t := range tasks {
		if t.Branch == "" || t.Branch == into || seen[t.Branch] {
			continue
		}
		seen[t.Branch] = true
		out = append(out, t.Branch)
	}
	slices.Sort(out)
	return out
}
