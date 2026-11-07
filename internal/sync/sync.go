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
// push — a remote ref never references content the server lacks). Sync never
// downloads content; a read fetches the object it needs. Sync is the only
// path holding the upload invariant: plain git push publishes entity refs
// through the installed refspec without uploading content.
package sync

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	stdsync "sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/lfs"
	"github.com/yasyf/cc-notes/internal/managed"
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
	// Namespace is the prefix a whole-namespace Prefixed converges.
	Namespace = namespace
	// maxRounds bounds the fetch-reconcile-push loop against a remote that
	// keeps moving under us.
	maxRounds = 5
	// maxRefAttempts bounds per-ref compare-and-swap retries against
	// concurrent local writers.
	maxRefAttempts = 16
	// refConcurrency bounds the per-ref fan-out of reconcile.
	refConcurrency = 8
	// createBatch bounds the refs one managed transaction creates.
	createBatch = 512
	// contendedBackoffBase scales the jitter before a round that follows a
	// push another writer beat.
	contendedBackoffBase = 250 * time.Millisecond
	pushArgBudget        = 256 << 10
	// checkpointPushBatch bounds the checkpoints one leased push carries, each
	// a lease and a refspec of about 300 bytes, inside pushArgBudget.
	checkpointPushBatch = 512
)

var (
	// ErrSyncContended reports a Sync that exhausted its rounds, or a ref
	// that never settled under concurrent writers.
	ErrSyncContended = errors.New("sync contended")
	// ErrRemoteNotFound reports a remote name with no configuration.
	ErrRemoteNotFound = errors.New("remote not configured")
	// ErrRefsRejected reports a Sync whose remote refused some refs; every
	// other ref still pushed.
	ErrRefsRejected = errors.New("refs rejected")
	// ErrObjectsUnavailable reports a Sync that deferred refs whose objects
	// the remote did not send; every other ref still converged and pushed.
	ErrObjectsUnavailable = errors.New("objects unavailable")
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
	// Withheld counts the refs in this run's scope that exist on this clone and
	// that the final round kept off the remote.
	Withheld int
	// Held counts the local refs the final round left unpushed because an
	// attachment they reference failed to upload; every other ref pushed.
	Held int
	// Deferred counts the remote refs the final round left unfolded and
	// unpushed because the remote did not send objects their chains lack.
	Deferred int
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
	full   bool
	prefix string

	// authority serializes the reconcile workers' authority reads and writes,
	// so a worker waits on a sibling for as long as it takes instead of failing
	// with managed.ErrBusy behind its own process.
	authority stdsync.Mutex

	created       atomic.Int64
	fastForwarded atomic.Int64
	merged        atomic.Int64
	reconciled    atomic.Int64

	// endpoint is the remote's LFS endpoint, discovered once per run on the
	// first transfer; endpointSet distinguishes it from the zero value.
	endpoint    lfs.Endpoint
	endpointSet bool
	// clients holds one lazily-built client per LFS operation.
	clients  map[string]*lfs.Client
	uploaded int
	withheld int
	// blocked holds the refs referencing an attachment the current round
	// failed to upload, uploadErr that failure, and held how many pending
	// refs the round's push left out for it.
	blocked   map[string]bool
	uploadErr error
	held      int
	// deferred maps each ref the current round could not hydrate to the
	// fetch failure that left it out.
	deferred map[string]error
	// rejected maps each ref the current round's push left out because the
	// remote refused it alone to that refusal.
	rejected map[string]error
	// outgoing maps each ref the current round's push carries to its local tip
	// and the tip the remote last showed.
	outgoing map[string]gitcmd.RefUpdate
}

// Sync converges the local refs/cc-notes/ namespace with remote and pushes
// the result. Each round captures the tracking view before and after the
// fetch and reconciles only the refs the remote moved — plus any with no
// local copy — folding through the store's cache; create, fast-forward, or
// union merge, never a clobber. A non-fast-forward push means the remote
// moved mid-round: the next round merges the new tips. full forces a
// whole-namespace reconcile each round, the escape hatch when the scoped
// scan is suspect, and re-asks the LFS server about every attachment the
// upload log already confirms. Exhausting maxRounds fails wrapping
// ErrSyncContended; an unconfigured remote fails wrapping ErrRemoteNotFound.
//
// Referenced, locally-present attachment objects upload each round before
// the push; a failed upload holds back only the refs referencing the failed
// objects, every other ref pushes, and the run then fails naming each held
// reference.
//
// A remote ref whose chain needs objects the remote will not send stays
// unfolded and unpushed for the round while every other ref converges; the
// run then fails wrapping ErrObjectsUnavailable, naming each deferred ref,
// and the next sync retries it.
func Sync(ctx context.Context, s *store.Store, remote string, full bool) (Report, error) {
	return Prefixed(ctx, s, remote, full, namespace)
}

// Prefixed is Sync over only the refs whose names start with prefix, such
// as one ledger's ref: it lists, fetches, folds, pushes, and moves the
// attachments of those refs alone, so converging one entity never waits on
// the whole namespace. A prefix outside refs/cc-notes/ is an error. One
// process at a time syncs a prefix: a call while another runs returns an empty
// Report at once, and the running one syncs again before it returns.
func Prefixed(ctx context.Context, s *store.Store, remote string, full bool, prefix string) (Report, error) {
	if !strings.HasPrefix(prefix, namespace) {
		return Report{}, fmt.Errorf("sync %s: prefix %q is outside %s", remote, prefix, namespace)
	}
	if err := s.CheckRecords(); err != nil {
		return Report{}, fmt.Errorf("sync %s: %w", remote, err)
	}
	if err := ensureRemote(ctx, s.RecordsGit, remote); err != nil {
		return Report{}, fmt.Errorf("sync %s: %w", remote, err)
	}
	turn, err := s.TakeSyncTurn(trackingLockKey(remote, prefix))
	if err != nil {
		return Report{}, fmt.Errorf("sync %s: %w", remote, err)
	}
	var total Report
	for turn != nil {
		report, err := converge(ctx, s, remote, full, prefix)
		total = total.plus(report)
		if err != nil {
			return total, errors.Join(err, turn.Close())
		}
		if turn, err = turn.Next(); err != nil {
			return total, fmt.Errorf("sync %s: %w", remote, err)
		}
	}
	return total, nil
}

func (r Report) plus(o Report) Report {
	return Report{
		Created:       r.Created + o.Created,
		FastForwarded: r.FastForwarded + o.FastForwarded,
		Merged:        r.Merged + o.Merged,
		Pushed:        r.Pushed + o.Pushed,
		Reconciled:    r.Reconciled + o.Reconciled,
		Rounds:        r.Rounds + o.Rounds,
		Uploaded:      r.Uploaded + o.Uploaded,
		Withheld:      o.Withheld,
		Held:          o.Held,
		Deferred:      o.Deferred,
	}
}

func converge(ctx context.Context, s *store.Store, remote string, full bool, prefix string) (Report, error) {
	e := &engine{store: s, remote: remote, full: full, prefix: prefix}
	trackingPrefix := syncNamespace + remote + "/"
	trackingScope := trackingPrefix + strings.TrimPrefix(prefix, namespace)
	fetchSpec := "+" + prefix + "*:" + trackingScope + "*"
	for round := 1; round <= maxRounds; round++ {
		before, err := trackingView(ctx, s, trackingScope)
		if err != nil {
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		checkpoints, err := refreshTracking(ctx, s, remote, prefix, trackingPrefix, trackingScope, fetchSpec)
		switch {
		case errors.Is(err, gitcmd.ErrCASMismatch):
			continue
		case err != nil:
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		after, err := trackingView(ctx, s, trackingScope)
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
		if err := e.hydrate(ctx, scope); err != nil {
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		if err := e.reconcile(ctx, scope); err != nil {
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		pending, secluded, err := e.pending(ctx, after)
		if err != nil {
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		if err := e.uploadAttachments(ctx, secluded); err != nil {
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
		unblocked := slices.DeleteFunc(slices.Clone(pending), func(ref string) bool { return e.blocked[ref] })
		e.held = len(pending) - len(unblocked)
		pending = unblocked
		switch err := e.push(ctx, pending); {
		case err == nil:
			if err := e.pushCheckpoints(ctx, checkpoints); err != nil {
				return e.report(round, len(pending)-len(e.rejected)), fmt.Errorf("sync %s: %w", remote, err)
			}
			return e.finish(round, len(pending)-len(e.rejected))
		case errors.Is(err, gitcmd.ErrNonFastForward):
			if err := contendedBackoff(ctx, round); err != nil {
				return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
			}
		default:
			return e.report(round, 0), fmt.Errorf("sync %s: %w", remote, err)
		}
	}
	return e.report(maxRounds, 0), fmt.Errorf("sync %s: %w: %d rounds exhausted", remote, ErrSyncContended, maxRounds)
}

// finish returns the run's final report once the push loop has converged. A
// held upload, a refused ref, or a deferred ref is the run's error while the
// report still carries the pushes that landed: refs published, the rest named,
// exit non-zero.
func (e *engine) finish(round, pushed int) (Report, error) {
	var errs []error
	if e.uploadErr != nil {
		errs = append(errs, fmt.Errorf("held %d refs to the next sync: %w", e.held, e.uploadErr))
	}
	if len(e.rejected) > 0 {
		causes := make([]error, 0, len(e.rejected))
		for _, ref := range slices.Sorted(maps.Keys(e.rejected)) {
			causes = append(causes, fmt.Errorf("%s: %w", ref, e.rejected[ref]))
		}
		errs = append(errs, fmt.Errorf("%w: the remote refused %d refs: %w", ErrRefsRejected, len(e.rejected), errors.Join(causes...)))
	}
	if len(e.deferred) > 0 {
		causes := make([]error, 0, len(e.deferred))
		for _, ref := range slices.Sorted(maps.Keys(e.deferred)) {
			causes = append(causes, fmt.Errorf("%s: %w", ref, e.deferred[ref]))
		}
		errs = append(errs, fmt.Errorf("%w: deferred %d refs to the next sync: %w", ErrObjectsUnavailable, len(e.deferred), errors.Join(causes...)))
	}
	if len(errs) > 0 {
		return e.report(round, pushed), fmt.Errorf("sync %s: %w", e.remote, errors.Join(errs...))
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
		Withheld:      e.withheld,
		Held:          e.held,
		Deferred:      len(e.deferred),
	}
}

// refreshTracking fetches the remote's entity refs under prefix into their
// tracking refs, and its checkpoints of them into theirs, skipping either fetch
// the remote's listing shows would change nothing, then adopts each fetched
// checkpoint that covers at least as much as the local one. It returns the
// checkpoint refs the remote listed, the leases a push of local checkpoints
// holds.
func refreshTracking(ctx context.Context, s *store.Store, remote, prefix, trackingPrefix, trackingScope, fetchSpec string) (_ map[string]model.SHA, err error) {
	defer Phase(ctx, "fetching "+prefix+" from "+remote)()
	checkpointScope := refs.CheckpointOf(prefix)
	listed, err := s.RecordsGit.LsRemote(ctx, remote, prefix, checkpointScope)
	if err != nil {
		return nil, err
	}
	checkpoints := make(map[string]model.SHA)
	for ref, tip := range listed {
		if strings.HasPrefix(ref, refs.CheckpointNamespace) {
			checkpoints[ref] = tip
			delete(listed, ref)
		}
	}
	lock, err := s.TrackingLock(ctx, trackingLockKey(remote, prefix))
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	tracking, err := recordRefs(ctx, s, trackingScope)
	if err != nil {
		return nil, err
	}
	local, err := s.Repo.ListPrefix(ctx, checkpointScope)
	if err != nil {
		return nil, err
	}
	fetched := make(map[string]string)
	for ref, tip := range checkpoints {
		if local[ref] != tip {
			fetched[ref] = refs.CheckpointTracking(remote, ref)
		}
	}
	var specs []string
	if !mirrors(listed, tracking, trackingPrefix) {
		specs = append(specs, fetchSpec)
	}
	if len(fetched) > 0 {
		specs = append(specs, "+"+checkpointScope+"*:"+refs.CheckpointTracking(remote, checkpointScope)+"*")
	}
	if len(specs) == 0 {
		return checkpoints, nil
	}
	if err := s.RecordsGit.Holding(lock).Fetch(ctx, remote, specs...); err != nil {
		return nil, err
	}
	return checkpoints, s.AdoptCheckpoints(ctx, fetched)
}

// pushCheckpoints pushes every local checkpoint ref in scope that differs from
// the remote's listed one, leased on the listed value, so a checkpoint another
// clone pushed first stands.
func (e *engine) pushCheckpoints(ctx context.Context, listed map[string]model.SHA) error {
	local, err := e.store.Repo.ListPrefix(ctx, refs.CheckpointOf(e.prefix))
	if err != nil {
		return err
	}
	var updates []gitcmd.RefUpdate
	for _, ref := range slices.Sorted(maps.Keys(local)) {
		if local[ref] != listed[ref] {
			updates = append(updates, gitcmd.RefUpdate{Ref: ref, New: local[ref], Old: listed[ref]})
		}
	}
	if len(updates) == 0 {
		return nil
	}
	defer Phase(ctx, fmt.Sprintf("pushing %d checkpoints", len(updates)))()
	for batch := range slices.Chunk(updates, checkpointPushBatch) {
		if err := e.store.RecordsGit.PushLeased(ctx, e.remote, batch); err != nil {
			return err
		}
	}
	return nil
}

func trackingLockKey(remote, prefix string) string {
	if prefix == namespace {
		return remote
	}
	return remote + " " + prefix
}

func mirrors(listed, tracking map[string]model.SHA, trackingPrefix string) bool {
	if len(listed) != len(tracking) {
		return false
	}
	for ref, tip := range listed {
		if got, ok := tracking[trackingPrefix+strings.TrimPrefix(ref, namespace)]; !ok || got != tip {
			return false
		}
	}
	return true
}

func recordRefs(ctx context.Context, s *store.Store, prefix string) (map[string]model.SHA, error) {
	if err := s.CheckRecords(); err != nil {
		return nil, err
	}
	tips, err := s.Repo.ListPrefix(ctx, prefix)
	if err != nil {
		return nil, err
	}
	if err := s.CheckRecords(); err != nil {
		return nil, err
	}
	return tips, nil
}

// trackingView maps the tracking refs under trackingPrefix back to their
// canonical refs/cc-notes/ names. A tracking ref that does not parse as a
// cc-notes ref is an error: it was not written by cc-notes. Sync reads the
// per-remote view a round fetched; foldTracking reads a remote's whole view to
// fold a plain fetch's tracking refs locally.
func trackingView(ctx context.Context, s *store.Store, trackingPrefix string) (map[string]model.SHA, error) {
	tracking, err := recordRefs(ctx, s, trackingPrefix)
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

// hydrate fetches the objects the chains in scope lack. When the remote
// refuses a fetch, it bisects the refs and records each single ref whose fetch
// still fails in e.deferred, which reconcile and pending skip for the round,
// so one unavailable object holds back only the ref whose chain needs it.
func (e *engine) hydrate(ctx context.Context, scope map[string]model.SHA) error {
	defer Phase(ctx, "fetching the objects the remote's refs need")()
	e.deferred = make(map[string]error)
	return e.hydrateRefs(ctx, scope, slices.Sorted(maps.Keys(scope)))
}

func (e *engine) hydrateRefs(ctx context.Context, scope map[string]model.SHA, names []string) error {
	tips := make([]model.SHA, len(names))
	for i, ref := range names {
		tips[i] = scope[ref]
	}
	git := e.store.RecordsGit
	missing, err := git.MissingObjects(ctx, tips)
	if err != nil || len(missing) == 0 {
		return err
	}
	err = git.FetchObjects(ctx, e.remote, missing)
	switch {
	case err == nil:
		return nil
	case len(names) == 1:
		e.deferred[names[0]] = err
		return nil
	}
	half := len(names) / 2
	if err := e.hydrateRefs(ctx, scope, names[:half]); err != nil {
		return err
	}
	return e.hydrateRefs(ctx, scope, names[half:])
}

// reconcile converges every ref in scope but the deferred ones. Local-only
// refs need no work here: the push publishes them. Refs this clone lacks are
// created in batches first; the rest, and any ref a failed batch leaves, go
// through ensure. Each ref is tallied so the report surfaces exactly what this
// run folded. Every ref runs to its own verdict and the error joins each
// failure, so a binding moved mid-run names every ref it hid rather than the
// first.
func (e *engine) reconcile(ctx context.Context, scope map[string]model.SHA) error {
	defer Phase(ctx, fmt.Sprintf("folding %d refs", len(scope)))()
	local, err := e.store.Repo.ListPrefix(ctx, e.prefix)
	if err != nil {
		return err
	}
	var ordered, absent []string
	for _, ref := range slices.Sorted(maps.Keys(scope)) {
		if _, deferred := e.deferred[ref]; deferred {
			continue
		}
		e.reconciled.Add(1)
		if _, found := local[ref]; found {
			ordered = append(ordered, ref)
		} else {
			absent = append(absent, ref)
		}
	}
	var failed []error
	for batch := range slices.Chunk(absent, createBatch) {
		left, err := e.create(ctx, scope, batch)
		ordered = append(ordered, left...)
		failed = append(failed, err)
	}
	errs := make([]error, len(ordered))
	var g errgroup.Group
	g.SetLimit(refConcurrency)
	for i, ref := range ordered {
		g.Go(func() error {
			if err := e.ensure(ctx, ref, scope[ref]); err != nil {
				errs[i] = fmt.Errorf("reconcile %s: %w", ref, err)
			}
			return errs[i]
		})
	}
	if err := g.Wait(); err != nil {
		failed = append(failed, errs...)
	}
	return errors.Join(failed...)
}

// create publishes refs this clone lacks at their remote tips in one managed
// transaction, halving a batch that fails before its refs move, and returns
// the refs left for ensure: a lone ref whose batch failed, or every ref once
// ctx is done. A batch that failed after its refs moved stands, and its error
// returns with it.
func (e *engine) create(ctx context.Context, scope map[string]model.SHA, names []string) ([]string, error) {
	if len(names) == 1 || ctx.Err() != nil {
		return names, nil
	}
	updates := make([]gitcmd.RefUpdate, len(names))
	for i, ref := range names {
		updates[i] = gitcmd.RefUpdate{Ref: ref, New: scope[ref]}
	}
	e.authority.Lock()
	err := e.store.PublishFetchedRefs(ctx, updates)
	e.authority.Unlock()
	if err == nil || store.Published(err) {
		e.created.Add(int64(len(names)))
		return nil, err
	}
	half := len(names) / 2
	low, lowErr := e.create(ctx, scope, names[:half])
	high, highErr := e.create(ctx, scope, names[half:])
	return append(low, high...), errors.Join(lowErr, highErr)
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
	local, err := e.store.ManagedTips(ctx, e.prefix)
	if err != nil {
		return nil, err
	}
	raw, err := recordRefs(ctx, e.store, e.prefix)
	if err != nil {
		return nil, err
	}
	scope := make(map[string]model.SHA, len(after))
	for ref, tip := range after {
		if before[ref] != tip || raw[ref] != local[ref] {
			scope[ref] = tip
			continue
		}
		localTip, ok := local[ref]
		if !ok {
			scope[ref] = tip
			continue
		}
		if localTip == tip {
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

// pending returns the local refs the upcoming push would create or update,
// sorted and leaving out every local entity and deferred ref, and the full
// secluded set the push excludes. A pending ref the local policy keeps on this
// clone joins the set before anything is pushed, so an entity written by a
// cc-notes predating the policy never leaves on its first sync.
func (e *engine) pending(ctx context.Context, remoteView map[string]model.SHA) ([]string, map[string]bool, error) {
	local, err := recordRefs(ctx, e.store, e.prefix)
	if err != nil {
		return nil, nil, err
	}
	secluded, err := e.store.Secluded()
	if err != nil {
		return nil, nil, err
	}
	var add, pending []string
	e.outgoing = make(map[string]gitcmd.RefUpdate)
	for ref, tip := range local {
		if remoteView[ref] == tip || secluded[ref] {
			continue
		}
		if _, deferred := e.deferred[ref]; deferred {
			continue
		}
		reason, err := e.store.LocalReason(ctx, ref)
		if err != nil {
			return nil, nil, err
		}
		if _, published := remoteView[ref]; published && store.Defaulted(reason) {
			reason = ""
		}
		if reason != "" {
			add = append(add, ref)
			secluded[ref] = true
			continue
		}
		pending = append(pending, ref)
		e.outgoing[ref] = gitcmd.RefUpdate{Ref: ref, New: tip, Old: remoteView[ref]}
	}
	if len(add) > 0 {
		if err := e.store.Seclude(ctx, add, nil); err != nil {
			return nil, nil, err
		}
	}
	e.withheld = 0
	for ref := range local {
		if secluded[ref] {
			e.withheld++
		}
	}
	slices.Sort(pending)
	return pending, secluded, nil
}

// contendedBackoff waits a jittered span that doubles per round before the
// round after a push another writer beat, so writers racing on one ref, such
// as a shared ledger, spread out instead of colliding again.
func contendedBackoff(ctx context.Context, round int) error {
	//nolint:gosec // G404: retry jitter is timing, not security.
	wait := time.Duration(rand.Int64N(int64(contendedBackoffBase << round)))
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(wait):
		return nil
	}
}

func (e *engine) push(ctx context.Context, pending []string) (err error) {
	defer Phase(ctx, fmt.Sprintf("pushing %d refs", len(pending)))()
	e.rejected = make(map[string]error)
	if len(pending) == 0 {
		return nil
	}
	lock, err := e.store.TrackingLock(ctx, trackingLockKey(e.remote, e.prefix))
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	git := e.store.RecordsGit.Holding(lock)
	for _, batch := range pushBatches(pending) {
		if err := e.pushRefs(ctx, git, batch); err != nil {
			return err
		}
	}
	return nil
}

func (e *engine) pushRefs(ctx context.Context, git gitcmd.Held, refs []string) error {
	updates := make([]gitcmd.RefUpdate, len(refs))
	for i, ref := range refs {
		updates[i] = e.outgoing[ref]
	}
	err := git.PushUpdates(ctx, e.remote, updates)
	switch {
	case !errors.Is(err, gitcmd.ErrRemoteRejected):
		return err
	case len(refs) == 1:
		e.rejected[refs[0]] = err
		return nil
	}
	half := len(refs) / 2
	if err := e.pushRefs(ctx, git, refs[:half]); err != nil {
		return err
	}
	return e.pushRefs(ctx, git, refs[half:])
}

func pushBatches(pending []string) [][]string {
	var batches [][]string
	start, size := 0, 0
	for i, ref := range pending {
		if i > start && size+len(ref) > pushArgBudget {
			batches = append(batches, pending[start:i])
			start, size = i, 0
		}
		size += len(ref)
	}
	if start < len(pending) {
		batches = append(batches, pending[start:])
	}
	return batches
}

// ensure folds tip into ref and tallies what it took.
func (e *engine) ensure(ctx context.Context, ref string, tip model.SHA) error {
	result, err := ensureContains(ctx, e.store, &e.authority, ref, tip)
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
func ensureContains(ctx context.Context, s *store.Store, authority *stdsync.Mutex, ref string, tip model.SHA) (outcome, error) {
	var lastErr error
	for attempt := range maxRefAttempts {
		if attempt > 0 {
			if err := store.Backoff(ctx, attempt); err != nil {
				return refKept, err
			}
		}
		result, err := advance(ctx, s, authority, ref, tip)
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
// descends from, and union-merges a diverged ref, holding authority across
// every authority read and write. A concurrent writer surfaces as
// gitcmd.ErrCASMismatch for the caller to retry.
func advance(ctx context.Context, s *store.Store, authority *stdsync.Mutex, ref string, tip model.SHA) (outcome, error) {
	current, err := s.Repo.Tip(ctx, ref)
	if errors.Is(err, gitobj.ErrRefNotFound) {
		authority.Lock()
		defer authority.Unlock()
		if err := s.PublishFetchedRefs(ctx, []gitcmd.RefUpdate{{Ref: ref, New: tip}}); err != nil {
			return refKept, err
		}
		return refCreated, nil
	}
	if err != nil {
		return refKept, err
	}
	if current == tip {
		authority.Lock()
		defer authority.Unlock()
		managedTips, err := s.ManagedTips(ctx, ref)
		if err != nil {
			return refKept, err
		}
		if authoritative, found := managedTips[ref]; found {
			if authoritative == tip {
				return refKept, nil
			}
			extended, err := s.Repo.IsAncestor(ctx, authoritative, tip)
			if err != nil {
				return refKept, err
			}
			if !extended {
				return refKept, fmt.Errorf("canonical ref differs from managed authority: %s: %w", ref, managed.ErrReindex)
			}
		}
		return refKept, s.ImportFetchedRef(ctx, ref, tip)
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
	authority.Lock()
	defer authority.Unlock()
	if behind {
		if err := s.PublishFetchedRefs(ctx, []gitcmd.RefUpdate{{Ref: ref, New: tip, Old: current}}); err != nil {
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
