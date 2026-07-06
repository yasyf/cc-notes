package viz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// watchPrefixes are the ref namespaces the Watcher snapshots each tick: local
// and remote branches, and the cc-notes entity namespace. A move under any of
// them changes the graph, so the digest the Builder caches on moves with it.
var watchPrefixes = []string{"refs/heads/", "refs/remotes/", refs.Namespace}

// headKey is the synthetic snapshot key for the commit HEAD resolves to. It
// catches a checkout that repoints HEAD without moving any branch tip; it is not
// a real ref, so it is filtered out of both the heads and entities payload
// lists.
const headKey = "HEAD"

// refsEvent is the JSON payload published on every detected change: a monotonic
// generation counter (so a client that dropped an event notices the gap), the
// changed branch and entity refs, and the branch HEAD points at (empty when
// detached).
type refsEvent struct {
	Gen      uint64   `json:"gen"`
	Heads    []string `json:"heads"`
	Entities []string `json:"entities"`
	Head     string   `json:"head"`
}

// Watcher polls one repository's refs on a fixed interval and publishes a
// refsEvent to the hub whenever a branch tip, entity ref, or the symbolic HEAD
// moves, first invalidating the Builder's cache for the changed refs. It owns no
// goroutine beyond Run's own loop and holds all scan state single-threaded, so
// it carries no synchronization.
type Watcher struct {
	repo     *gitobj.Repo
	git      gitcmd.Git
	builder  *Builder
	hub      *Hub
	interval time.Duration

	prev     map[string]model.SHA
	prevHead string
	gen      uint64
}

// NewWatcher returns a Watcher over the store's refs that publishes to hub every
// interval.
func NewWatcher(s *store.Store, b *Builder, hub *Hub, interval time.Duration) *Watcher {
	return &Watcher{repo: s.Repo, git: s.Git, builder: b, hub: hub, interval: interval}
}

// Run scans on every interval tick until ctx is cancelled, at which point it
// returns nil. The first scan establishes the baseline and publishes nothing; a
// scan that fails for a reason other than cancellation aborts the loop with the
// error.
func (w *Watcher) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := w.scan(ctx); err != nil {
				// A tick can win the select race with a just-cancelled ctx; the
				// scan then fails on ctx.Err(), which is a clean shutdown, not a
				// watch failure.
				if ctx.Err() != nil {
					return nil
				}
				return fmt.Errorf("viz watch: %w", err)
			}
		}
	}
}

// scan snapshots the watched refs and the symbolic HEAD, diffs them against the
// previous snapshot, and — when anything moved — invalidates the changed refs in
// the Builder and publishes a refsEvent with the next generation. The first scan
// records the baseline and publishes nothing.
func (w *Watcher) scan(ctx context.Context) error {
	tips, head, err := w.snapshot(ctx)
	if err != nil {
		return err
	}
	prev, prevHead, first := w.prev, w.prevHead, w.prev == nil
	w.prev, w.prevHead = tips, head
	if first {
		return nil
	}
	changed := diffTips(prev, tips)
	if len(changed) == 0 && head == prevHead {
		return nil
	}
	w.builder.InvalidateRefs(changed)
	w.gen++
	payload, err := json.Marshal(refsEvent{
		Gen:      w.gen,
		Heads:    withPrefix(changed, "refs/heads/", "refs/remotes/"),
		Entities: withPrefix(changed, refs.Namespace),
		Head:     head,
	})
	if err != nil {
		return fmt.Errorf("marshal refs event: %w", err)
	}
	w.hub.Publish(payload)
	return nil
}

// snapshot builds the current ref→tip map over watchPrefixes plus the headKey
// tip, and resolves the branch HEAD points at (empty when detached).
func (w *Watcher) snapshot(ctx context.Context) (map[string]model.SHA, string, error) {
	tips := make(map[string]model.SHA)
	for _, prefix := range watchPrefixes {
		refTips, err := w.repo.ListPrefix(ctx, prefix)
		if err != nil {
			return nil, "", fmt.Errorf("list %s: %w", prefix, err)
		}
		for name, tip := range refTips {
			tips[name] = tip
		}
	}
	tip, err := w.repo.Tip(ctx, headKey)
	switch {
	case errors.Is(err, gitobj.ErrRefNotFound):
	case err != nil:
		return nil, "", fmt.Errorf("resolve HEAD: %w", err)
	default:
		tips[headKey] = tip
	}
	head, err := w.headBranch(ctx)
	if err != nil {
		return nil, "", err
	}
	return tips, head, nil
}

// headBranch returns the branch HEAD points at, mapping a detached HEAD to the
// empty string — the same source and convention as the Builder's head().
func (w *Watcher) headBranch(ctx context.Context) (string, error) {
	branch, err := w.git.HeadBranch(ctx)
	if errors.Is(err, gitcmd.ErrDetachedHead) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("resolve HEAD branch: %w", err)
	}
	return string(branch), nil
}

// diffTips returns every ref name whose tip appeared, moved, or disappeared
// between prev and cur, sorted.
func diffTips(prev, cur map[string]model.SHA) []string {
	var changed []string
	for name, tip := range cur {
		if prev[name] != tip {
			changed = append(changed, name)
		}
	}
	for name := range prev {
		if _, ok := cur[name]; !ok {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	return changed
}

// withPrefix returns the members of names starting with any of prefixes, order
// preserved. It is never nil, so the payload lists serialize as [] not null.
func withPrefix(names []string, prefixes ...string) []string {
	out := []string{}
	for _, name := range names {
		for _, prefix := range prefixes {
			if strings.HasPrefix(name, prefix) {
				out = append(out, name)
				break
			}
		}
	}
	return out
}
