// Package sync replicates the refs/cc-notes/ namespace between
// repositories. Install wires a remote's refspecs so plain git fetch and
// push carry entity refs alongside branches; Sync is the explicit engine:
// fetch into a per-remote tracking namespace, converge every ref by create,
// fast-forward, or union merge, then push — looping on contention, never
// forcing. Refs are never deleted: deletions do not propagate through
// refspecs, so a deleted ref would resurrect on the next fetch.
//
// Attachment content rides along over the LFS batch API: every referenced,
// locally-present object uploads before the ref push (a failure blocks the
// push — a remote ref never references content the server lacks), and every
// referenced, locally-missing object downloads after the push loop
// converges (an LFS outage never blocks publishing refs). Sync is the only
// path holding that invariant: plain git push publishes entity refs through
// the installed refspec without uploading content.
package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/lfs"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

const (
	// namespace holds the canonical entity refs; syncNamespace shadows it
	// per remote, outside refs/cc-notes/ so the wildcard push refspec never
	// republishes tracking state.
	namespace     = "refs/cc-notes/"
	syncNamespace = "refs/cc-notes-sync/"
	// maxRounds bounds the fetch-reconcile-push loop against a remote that
	// keeps moving under us.
	maxRounds = 5
	// maxRefAttempts bounds per-ref compare-and-swap retries against
	// concurrent local writers.
	maxRefAttempts = 16
	// refConcurrency bounds the per-ref fan-out of reconcile.
	refConcurrency = 8
)

var (
	// ErrSyncContended reports a Sync that exhausted its rounds, or a ref
	// that never settled under concurrent writers.
	ErrSyncContended = errors.New("sync contended")
	// ErrRemoteNotFound reports a remote name with no configuration.
	ErrRemoteNotFound = errors.New("remote not configured")
)

// Report tallies one Sync run.
type Report struct {
	// Created counts local refs created from remote-only refs.
	Created int
	// FastForwarded counts local refs fast-forwarded to a remote tip.
	FastForwarded int
	// Merged counts union merge commits written for diverged tips.
	Merged int
	// Pushed counts the refs that differed from the remote view when the
	// final push succeeded: the refs that push created or updated.
	Pushed int
	// Reconciled counts the refs handed to ensure across every round: the
	// scope the sync actually folded. An unchanged remote reconciles nothing.
	Reconciled int
	// Rounds counts fetch-reconcile-push rounds run; 1 is a clean pass.
	Rounds int
	// Uploaded counts attachment objects uploaded to the remote LFS
	// endpoint before the ref push.
	Uploaded int
	// Downloaded counts attachment objects fetched from the remote LFS
	// endpoint after the push loop converged.
	Downloaded int
}

// outcome reports what one advance attempt did to a ref.
type outcome int

const (
	refKept outcome = iota
	refCreated
	refFastForwarded
	refMerged
)

// engine carries one Sync run's handles and counters; the atomic counters
// absorb the reconcile fan-out, while the LFS state and transfer tallies are
// touched only by the single-goroutine transfer phases.
type engine struct {
	store  *store.Store
	remote string

	created       atomic.Int64
	fastForwarded atomic.Int64
	merged        atomic.Int64
	reconciled    atomic.Int64

	// endpoint is the remote's LFS endpoint, discovered once per run on the
	// first transfer; endpointSet distinguishes it from the zero value.
	endpoint    lfs.Endpoint
	endpointSet bool
	// clients holds one lazily-built client per LFS operation.
	clients    map[string]*lfs.Client
	uploaded   int
	downloaded int
}

// Sync converges the local refs/cc-notes/ namespace with remote and pushes
// the result. Each round captures the tracking view before and after the
// fetch and reconciles only the refs the remote moved — plus any with no
// local copy — folding through the store's cache; create, fast-forward, or
// union merge, never a clobber. A non-fast-forward push means the remote
// moved mid-round: the next round merges the new tips. full forces a
// whole-namespace reconcile each round, the escape hatch when the scoped
// scan is suspect. Exhausting maxRounds fails wrapping ErrSyncContended; an
// unconfigured remote fails wrapping ErrRemoteNotFound.
//
// Referenced, locally-present attachment objects upload each round before
// the push; a failed upload fails the sync with the refs unpushed.
// Referenced, locally-missing objects download once the push loop
// converges; a failed download fails the sync while the returned Report
// still carries everything the run pushed.
func Sync(ctx context.Context, dir, remote string, full bool) (Report, error) {
	s, err := store.OpenContext(ctx, dir)
	if err != nil {
		return Report{}, fmt.Errorf("sync %s: %w", remote, err)
	}
	if err := ensureRemote(ctx, s.Git, remote); err != nil {
		return Report{}, fmt.Errorf("sync %s: %w", remote, err)
	}
	e := &engine{store: s, remote: remote}
	trackingPrefix := syncNamespace + remote + "/"
	fetchSpec := "+" + namespace + "*:" + trackingPrefix + "*"
	for round := 1; round <= maxRounds; round++ {
		before, err := e.remoteView(ctx, trackingPrefix)
		if err != nil {
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		if err := s.Git.Fetch(ctx, remote, fetchSpec); err != nil {
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		after, err := e.remoteView(ctx, trackingPrefix)
		if err != nil {
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		scope := after
		if !full {
			scope, err = e.changed(ctx, before, after)
			if err != nil {
				return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
			}
		}
		if err := e.reconcile(ctx, scope); err != nil {
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		if err := e.uploadAttachments(ctx); err != nil {
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		pending, err := e.pending(ctx, after)
		if err != nil {
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		if pending == 0 {
			return e.finish(ctx, round, 0)
		}
		switch err := s.Git.Push(ctx, remote, pushRefspec); {
		case err == nil:
			return e.finish(ctx, round, pending)
		case errors.Is(err, gitcmd.ErrNonFastForward):
		default:
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
	}
	return e.report(maxRounds, 0), fmt.Errorf("sync %s: %w: %d rounds exhausted", remote, ErrSyncContended, maxRounds)
}

// finish runs the download phase once the push loop has converged and
// returns the run's final report. A download failure is the run's error
// while the report still carries the pushes that landed: refs published,
// content missing, exit non-zero.
func (e *engine) finish(ctx context.Context, round, pushed int) (Report, error) {
	if err := e.downloadAttachments(ctx); err != nil {
		return e.report(round, pushed), fmt.Errorf("sync %s: %w", e.remote, err)
	}
	return e.report(round, pushed), nil
}

func (e *engine) report(rounds, pushed int) Report {
	return Report{
		Created:       int(e.created.Load()),
		FastForwarded: int(e.fastForwarded.Load()),
		Merged:        int(e.merged.Load()),
		Pushed:        pushed,
		Reconciled:    int(e.reconciled.Load()),
		Rounds:        rounds,
		Uploaded:      e.uploaded,
		Downloaded:    e.downloaded,
	}
}

// remoteView maps the tracking refs fetched this round back to their
// canonical refs/cc-notes/ names. A remote ref that does not parse as a
// cc-notes ref is an error: it was not written by cc-notes.
func (e *engine) remoteView(ctx context.Context, trackingPrefix string) (map[string]model.SHA, error) {
	tracking, err := e.store.Repo.ListPrefix(ctx, trackingPrefix)
	if err != nil {
		return nil, err
	}
	view := make(map[string]model.SHA, len(tracking))
	for name, tip := range tracking {
		_, canonical, err := refs.ParseTracking(name)
		if err != nil {
			return nil, err
		}
		if _, err := refs.Parse(canonical); err != nil {
			return nil, fmt.Errorf("remote ref: %w", err)
		}
		view[canonical] = tip
	}
	return view, nil
}

// reconcile converges every ref in scope. Local-only refs need no work here:
// the push publishes them. Each ref handed to ensure is tallied so the report
// surfaces exactly what this run folded.
func (e *engine) reconcile(ctx context.Context, scope map[string]model.SHA) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(refConcurrency)
	for ref, tip := range scope {
		e.reconciled.Add(1)
		g.Go(func() error { return e.ensure(gctx, ref, tip) })
	}
	return g.Wait()
}

// changed returns the subset of after whose tip differs from before, plus any
// after-ref the local namespace does not already contain. On round one before
// is empty, so every remote ref reconciles. The tracking==before==after case is
// not proof the local ref already folded that tip: a prior sync interrupted
// mid-reconcile (ctx cancel, ErrSyncContended under concurrent writers, any fold
// error) advances tracking for every ref via the fetch but folds none, leaving
// tracking ahead of a behind-or-diverged local ref. Scoping on the tracking
// delta alone would skip such a ref forever — no fold, a stale non-fast-forward
// push, and ErrSyncContended every round with no self-heal. So a ref whose
// remote tip the local chain does not contain is always in scope, however
// quiet the tracking delta: correctness over speed.
func (e *engine) changed(ctx context.Context, before, after map[string]model.SHA) (map[string]model.SHA, error) {
	local, err := e.store.Repo.ListPrefix(ctx, namespace)
	if err != nil {
		return nil, err
	}
	scope := make(map[string]model.SHA, len(after))
	for ref, tip := range after {
		if before[ref] != tip {
			scope[ref] = tip
			continue
		}
		localTip, ok := local[ref]
		if !ok {
			scope[ref] = tip
			continue
		}
		contains, err := e.store.Repo.IsAncestor(ctx, tip, localTip)
		if err != nil {
			return nil, err
		}
		if !contains {
			scope[ref] = tip
		}
	}
	return scope, nil
}

// pending counts local refs that differ from the remote view: the refs the
// upcoming push would create or update.
func (e *engine) pending(ctx context.Context, remoteView map[string]model.SHA) (int, error) {
	local, err := e.store.Repo.ListPrefix(ctx, namespace)
	if err != nil {
		return 0, err
	}
	count := 0
	for ref, tip := range local {
		if remoteView[ref] != tip {
			count++
		}
	}
	return count, nil
}

// ensure folds tip into ref and tallies what it took.
func (e *engine) ensure(ctx context.Context, ref string, tip model.SHA) error {
	result, err := ensureContains(ctx, e.store, ref, tip)
	if err != nil {
		return err
	}
	switch result {
	case refCreated:
		e.created.Add(1)
	case refFastForwarded:
		e.fastForwarded.Add(1)
	case refMerged:
		e.merged.Add(1)
	case refKept:
	}
	return nil
}

// ensureContains retries advance under compare-and-swap contention, with
// jittered backoff between attempts, until ref's chain contains tip, failing
// with ErrSyncContended when the ref never settles.
func ensureContains(ctx context.Context, s *store.Store, ref string, tip model.SHA) (outcome, error) {
	var lastErr error
	for attempt := range maxRefAttempts {
		if attempt > 0 {
			if err := store.Backoff(ctx, attempt); err != nil {
				return refKept, err
			}
		}
		result, err := advance(ctx, s, ref, tip)
		switch {
		case err == nil:
			return result, nil
		case errors.Is(err, gitcmd.ErrCASMismatch):
			lastErr = err
		default:
			return refKept, err
		}
	}
	return refKept, fmt.Errorf("%w: %s never settled: %w", ErrSyncContended, ref, lastErr)
}

// advance makes one attempt at folding tip into ref: it creates a missing
// ref at tip, keeps a ref already containing tip, fast-forwards a ref tip
// descends from, and union-merges a diverged ref. A concurrent writer
// surfaces as gitcmd.ErrCASMismatch for the caller to retry.
func advance(ctx context.Context, s *store.Store, ref string, tip model.SHA) (outcome, error) {
	current, err := s.Repo.Tip(ctx, ref)
	if errors.Is(err, gitobj.ErrRefNotFound) {
		if err := s.Git.UpdateRef(ctx, ref, tip, ""); err != nil {
			return refKept, err
		}
		return refCreated, nil
	}
	if err != nil {
		return refKept, err
	}
	if current == tip {
		return refKept, nil
	}
	contains, err := s.Repo.IsAncestor(ctx, tip, current)
	if err != nil {
		return refKept, err
	}
	if contains {
		return refKept, nil
	}
	behind, err := s.Repo.IsAncestor(ctx, current, tip)
	if err != nil {
		return refKept, err
	}
	if behind {
		if err := s.Git.UpdateRef(ctx, ref, tip, current); err != nil {
			return refKept, err
		}
		return refFastForwarded, nil
	}
	if _, err := s.Merge(ctx, ref, current, tip); err != nil {
		return refKept, err
	}
	return refMerged, nil
}

func ensureRemote(ctx context.Context, g gitcmd.Git, remote string) error {
	remotes, err := g.Remotes(ctx)
	if err != nil {
		return err
	}
	if !slices.Contains(remotes, remote) {
		return fmt.Errorf("%w: %q", ErrRemoteNotFound, remote)
	}
	return nil
}
