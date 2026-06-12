// Package sync replicates the refs/cc-notes/ namespace between
// repositories. Install wires a remote's refspecs so plain git fetch and
// push carry entity refs alongside branches; Sync is the explicit engine:
// fetch into a per-remote tracking namespace, converge every ref by create,
// fast-forward, or union merge, consolidate task siblings left behind by a
// promote, then push — looping on contention, never forcing. Promote moves
// a task between branch namespaces by appending the promote op (the
// tombstone) to the old chain and folding the new tip into the destination
// ref. Refs are never deleted: deletions do not propagate through refspecs,
// so a deleted ref would resurrect on the next fetch.
package sync

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"

	"golang.org/x/sync/errgroup"

	"github.com/yasyf/cc-notes/internal/fold"
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
	// refConcurrency bounds the per-ref fan-out of reconcile and consolidate.
	refConcurrency = 8
)

// syntheticHead stands in for an unwritten merge commit when consolidation
// folds the union of diverged sibling tips: fold is pure, so the head needs
// no git object — only a sha no real commit can carry.
const syntheticHead = model.SHA("cc-notes:synthetic-union-head")

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
	// FastForwarded counts local refs fast-forwarded to a remote or
	// consolidated tip.
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
// absorb the reconcile and consolidate fan-outs.
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
// union merge, never a clobber — consolidates task siblings split across
// branch namespaces by a promote, and pushes whatever differs, unforced. A
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
		if err := e.consolidate(ctx); err != nil {
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

// consolidate repairs the wake of a promote: it groups every local task ref
// by entity id, folds the union of each group's tips to find the live
// branch, folds every sibling tip into refs.Task(liveBranch, id) — creating
// it when absent — and then converges every other sibling to the resulting
// union tip, so no op stranded on a dead sibling ref is ever lost and every
// sibling folds to the same branch: exactly one ref satisfies liveness, even
// after racing promotes to different destinations. Dead siblings keep their
// refs: liveness is folded branch == ref branch, never ref deletion.
func (e *engine) consolidate(ctx context.Context) error {
	local, err := e.store.Repo.ListPrefix(ctx, namespace)
	if err != nil {
		return err
	}
	groups := make(map[model.EntityID][]string)
	for name := range local {
		parsed, err := refs.Parse(name)
		if err != nil {
			return fmt.Errorf("local ref: %w", err)
		}
		if parsed.Kind == refs.KindTask {
			groups[parsed.ID] = append(groups[parsed.ID], name)
		}
	}
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(refConcurrency)
	for id, group := range groups {
		g.Go(func() error { return e.consolidateEntity(gctx, id, group) })
	}
	return g.Wait()
}

func (e *engine) consolidateEntity(ctx context.Context, id model.EntityID, group []string) error {
	slices.Sort(group)
	tips := make([]model.SHA, 0, len(group))
	for _, ref := range group {
		tip, err := e.store.Repo.Tip(ctx, ref)
		if err != nil {
			return fmt.Errorf("consolidate task %s: %w", id.Short(), err)
		}
		tips = append(tips, tip)
	}
	frontier, err := e.frontier(ctx, tips)
	if err != nil {
		return fmt.Errorf("consolidate task %s: %w", id.Short(), err)
	}
	task, err := e.foldUnion(ctx, frontier)
	if err != nil {
		return fmt.Errorf("consolidate task %s: %w", id.Short(), err)
	}
	liveRef := refs.Task(task.Branch, id)
	for _, tip := range frontier {
		if err := e.ensure(ctx, liveRef, tip); err != nil {
			return fmt.Errorf("consolidate task %s: %w", id.Short(), err)
		}
	}
	// Converge the siblings to the live ref's union tip — not each to the
	// raw frontier, which would mint mirrored merge commits per sibling and
	// never settle. Every sibling tip is dominated by the union tip, so this
	// is a fast-forward unless a concurrent writer moved the sibling.
	union, err := e.store.Repo.Tip(ctx, liveRef)
	if err != nil {
		return fmt.Errorf("consolidate task %s: %w", id.Short(), err)
	}
	for _, ref := range group {
		if ref == liveRef {
			continue
		}
		if err := e.ensure(ctx, ref, union); err != nil {
			return fmt.Errorf("consolidate task %s: %w", id.Short(), err)
		}
	}
	return nil
}

// frontier drops every tip contained in another, leaving the minimal head
// set whose union covers all siblings, sorted for deterministic folds.
func (e *engine) frontier(ctx context.Context, tips []model.SHA) ([]model.SHA, error) {
	slices.Sort(tips)
	tips = slices.Compact(tips)
	frontier := make([]model.SHA, 0, len(tips))
	for i, tip := range tips {
		dominated := false
		for j, other := range tips {
			if i == j {
				continue
			}
			contained, err := e.store.Repo.IsAncestor(ctx, tip, other)
			if err != nil {
				return nil, err
			}
			if contained {
				dominated = true
				break
			}
		}
		if !dominated {
			frontier = append(frontier, tip)
		}
	}
	return frontier, nil
}

// foldUnion folds the union of the frontier chains as a task. Multiple
// heads fold under a synthetic empty-ops merge head — fold is pure, so the
// union's branch is known before any merge commit is written.
func (e *engine) foldUnion(ctx context.Context, frontier []model.SHA) (model.Task, error) {
	seen := make(map[model.SHA]bool)
	var commits []model.PackCommit
	for _, tip := range frontier {
		chain, err := e.store.Repo.ReadChain(ctx, tip)
		if err != nil {
			return model.Task{}, err
		}
		for _, c := range chain {
			if !seen[c.SHA] {
				seen[c.SHA] = true
				commits = append(commits, c)
			}
		}
	}
	if len(frontier) > 1 {
		commits = append(commits, model.PackCommit{
			SHA:     syntheticHead,
			Parents: frontier,
			Pack:    model.Pack{Lamport: nextLamport(commits)},
		})
	}
	return fold.Task(commits)
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

func nextLamport(commits []model.PackCommit) model.Lamport {
	var top model.Lamport
	for _, c := range commits {
		top = max(top, c.Pack.Lamport)
	}
	return top + 1
}
