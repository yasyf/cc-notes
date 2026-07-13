package notes

import (
	"context"

	ccsync "github.com/yasyf/cc-notes/internal/sync"
	"github.com/yasyf/cc-notes/model"
)

// SyncOptions selects what a Sync run converges. Remote names a single remote to
// sync; an empty Remote fans out over every cc-notes-wired remote in git-config
// order, or the default remote when none is wired. Full forces a whole-namespace
// reconcile scan each round rather than the scoped tracking-delta scan.
type SyncOptions struct {
	Remote string
	Full   bool
}

// SyncReport tallies a Sync run, mirroring the engine's per-run report. Created,
// FastForwarded, and Merged count the refs converged by create, fast-forward,
// and union merge; Pushed counts the refs the final push created or updated;
// Reconciled counts the refs handed to the fold across every round; Rounds
// counts fetch-reconcile-push rounds run, 1 being a clean pass; Uploaded and
// Downloaded count attachment objects moved to and from the remote LFS endpoint.
// When Sync fans out over several remotes, every field is the sum across them.
type SyncReport struct {
	Created       int
	FastForwarded int
	Merged        int
	Pushed        int
	Reconciled    int
	Rounds        int
	Uploaded      int
	Downloaded    int
}

// Sync converges the repository's refs/cc-notes/* namespace with one or more
// remotes and pushes the result, returning the aggregate tally. With opts.Remote
// set, it syncs just that remote; with it empty, it fans out over every
// cc-notes-wired remote in git-config order (or the default remote when none is
// wired), attempting every remote even after an earlier one fails and folding
// the tallies into one report. The first remote's error propagates alongside the
// report, which still carries everything the run pushed.
func (c *Client) Sync(ctx context.Context, opts SyncOptions) (SyncReport, error) {
	remotes := []string{opts.Remote}
	if opts.Remote == "" {
		targets, err := c.syncTargets(ctx)
		if err != nil {
			return SyncReport{}, err
		}
		remotes = targets
	}
	var report SyncReport
	var syncErr error
	for _, r := range remotes {
		rep, err := ccsync.Sync(ctx, c.s.Git.Dir, r, opts.Full)
		report.Created += rep.Created
		report.FastForwarded += rep.FastForwarded
		report.Merged += rep.Merged
		report.Pushed += rep.Pushed
		report.Reconciled += rep.Reconciled
		report.Rounds += rep.Rounds
		report.Uploaded += rep.Uploaded
		report.Downloaded += rep.Downloaded
		if err != nil && syncErr == nil {
			syncErr = err
		}
	}
	return report, syncErr
}

// syncTargets is the remote set a bare Sync converges: every cc-notes-wired
// remote in git-config order, or the default remote when none is wired.
func (c *Client) syncTargets(ctx context.Context) ([]string, error) {
	wired, err := ccsync.WiredRemotes(ctx, c.s.Git)
	if err != nil {
		return nil, err
	}
	if len(wired) == 0 {
		return []string{defaultRemote}, nil
	}
	return wired, nil
}

// ReconcileOptions selects a Reconcile run's scope. Into is the target branch,
// which the caller must already have resolved and validated. From, when
// non-empty, is the explicit source-branch set; an empty From scans every branch
// the folded tasks claim except the target and the backlog. Force treats every
// source branch as merged, skipping the ancestry check. DryRun plans the moves
// without writing them.
type ReconcileOptions struct {
	Into   model.Branch
	From   []model.Branch
	Force  bool
	DryRun bool
}

// ReconcileReport is the outcome of a Reconcile run: the target branch plus one
// BranchResult per source branch that held open or in-progress work.
type ReconcileReport struct {
	Into     model.Branch
	Branches []BranchResult
}

// BranchResult records what Reconcile found for one source branch: the open and
// in-progress tasks folding to it, whether it counted as merged into the target,
// and — when it did not merge — why. Reason is empty for a merged branch.
type BranchResult struct {
	Branch model.Branch
	Merged bool
	Reason string
	Tasks  []model.Task
}

// Scanned is the number of source branches that held open or in-progress work
// and were therefore examined.
func (r ReconcileReport) Scanned() int { return len(r.Branches) }

// Merged is the number of scanned branches that counted as merged into the
// target — the branches whose tasks Reconcile moved (or, under dry-run, would
// move).
func (r ReconcileReport) Merged() int {
	n := 0
	for _, b := range r.Branches {
		if b.Merged {
			n++
		}
	}
	return n
}

// Carried is the total number of tasks moved into the target across every merged
// branch — the plan's task count under dry-run.
func (r ReconcileReport) Carried() int {
	n := 0
	for _, b := range r.Branches {
		if b.Merged {
			n += len(b.Tasks)
		}
	}
	return n
}

// Reconcile reassigns each merged source branch's open and in-progress tasks to
// the target branch, returning the per-branch outcome. opts.Into must already be
// resolved and validated by the caller. Reconcile selects source branches
// (opts.From verbatim, else every branch the folded tasks claim minus the target
// and the backlog), collects each one's open and in-progress tasks, and moves
// them onto the target when the branch counts as merged — opts.Force, or a
// branch tip that is an ancestor of (or equal to) the target tip — unless
// opts.DryRun, which only plans the moves.
func (c *Client) Reconcile(ctx context.Context, opts ReconcileOptions) (ReconcileReport, error) {
	result, err := ccsync.Reconcile(ctx, c.s, opts.Into, opts.From, opts.Force, opts.DryRun)
	if err != nil {
		return ReconcileReport{}, err
	}
	report := ReconcileReport{Into: result.Into}
	for _, b := range result.Branches {
		report.Branches = append(report.Branches, BranchResult{
			Branch: b.Branch,
			Merged: b.Merged,
			Reason: b.Reason,
			Tasks:  b.Tasks,
		})
	}
	return report, nil
}

// GCOptions selects a GC run's work. GCLocal always runs, tidying local
// fold-cache state. PruneRemote additionally physically deletes tombstoned note,
// doc, and log refs locally and on a remote; Remote names that remote, and an
// empty Remote resolves to the sole cc-notes-wired remote when exactly one is
// wired, else the default remote.
type GCOptions struct {
	PruneRemote bool
	Remote      string
}

// GCReport tallies a GC run: Tidied fold-cache entries removed, and — under
// PruneRemote — the tombstoned refs Pruned and the per-ref deletes that Failed.
// Pruned and Failed stay zero without PruneRemote.
type GCReport struct {
	Tidied int
	Pruned int
	Failed int
}

// GC tidies local state and, with opts.PruneRemote, physically deletes
// tombstoned note, doc, and log refs locally and on a remote. The local tidy
// prunes fold-cache entries whose tip is no longer any entity ref tip and
// touches no remote. PruneRemote resolves the remote (opts.Remote, else the sole
// cc-notes-wired remote, else the default) and deletes each tombstoned ref via
// git push --delete — best-effort and non-convergent, tallying Pruned and
// Failed.
func (c *Client) GC(ctx context.Context, opts GCOptions) (GCReport, error) {
	tidied, err := c.s.GCLocal(ctx)
	if err != nil {
		return GCReport{}, err
	}
	report := GCReport{Tidied: tidied}
	if opts.PruneRemote {
		remote := opts.Remote
		if remote == "" {
			if remote, err = c.deriveRemote(ctx); err != nil {
				return GCReport{}, err
			}
		}
		if report.Pruned, report.Failed, err = c.s.PruneTombstones(ctx, remote); err != nil {
			return GCReport{}, err
		}
	}
	return report, nil
}
