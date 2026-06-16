// Package sync replicates the refs/cc-notes/ namespace between
// repositories. Install wires a remote's refspecs so plain git fetch and
// push carry entity refs alongside branches; Sync is the explicit engine:
// fetch into a per-remote tracking namespace, converge every ref by create,
// fast-forward, or union merge, then push — looping on contention, never
// forcing. Refs are never deleted: deletions do not propagate through
// refspecs, so a deleted ref would resurrect on the next fetch.
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
	"github.com/yasyf/cc-notes/internal/model"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
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
	// Rounds counts fetch-reconcile-push rounds run; 1 is a clean pass.
	Rounds int
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
// absorb the reconcile fan-out.
type engine struct {
	store  *store.Store
	remote string

	created       atomic.Int64
	fastForwarded atomic.Int64
	merged        atomic.Int64
}

// Sync converges the local refs/cc-notes/ namespace with remote and pushes
// the result. Each round fetches the remote's entity refs into the tracking
// namespace, reconciles every remote-known ref — create, fast-forward, or
// union merge, never a clobber — and pushes whatever differs, unforced. A
// non-fast-forward push means the remote moved mid-round: the next round
// merges the new tips. Exhausting maxRounds fails wrapping ErrSyncContended;
// an unconfigured remote fails wrapping ErrRemoteNotFound.
func Sync(ctx context.Context, dir, remote string) (Report, error) {
	s, err := store.Open(dir)
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
		if err := s.Git.Fetch(ctx, remote, fetchSpec); err != nil {
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		remoteView, err := e.remoteView(ctx, trackingPrefix)
		if err != nil {
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		if err := e.reconcile(ctx, remoteView); err != nil {
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		pending, err := e.pending(ctx, remoteView)
		if err != nil {
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		if pending == 0 {
			return e.report(round, 0), nil
		}
		switch err := s.Git.Push(ctx, remote, pushRefspec); {
		case err == nil:
			return e.report(round, pending), nil
		case errors.Is(err, gitcmd.ErrNonFastForward):
		default:
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
	}
	return e.report(maxRounds, 0), fmt.Errorf("sync %s: %w: %d rounds exhausted", remote, ErrSyncContended, maxRounds)
}

func (e *engine) report(rounds, pushed int) Report {
	return Report{
		Created:       int(e.created.Load()),
		FastForwarded: int(e.fastForwarded.Load()),
		Merged:        int(e.merged.Load()),
		Pushed:        pushed,
		Rounds:        rounds,
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

// reconcile converges every ref the remote knows. Local-only refs need no
// work here: the push publishes them.
func (e *engine) reconcile(ctx context.Context, remoteView map[string]model.SHA) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(refConcurrency)
	for ref, tip := range remoteView {
		g.Go(func() error { return e.ensure(gctx, ref, tip) })
	}
	return g.Wait()
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
