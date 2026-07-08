// Package viz assembles a repository's branch topology and cc-notes entity
// history into one JSON-serializable Graph: the swimlanes (branch lifelines
// with fork and merge points), the classified entity lifecycle events, and a
// lean per-entity summary for the legend. It is a pure read layer over
// internal/store, internal/gitobj, and internal/gitcmd — no HTTP, no state of
// its own beyond tip-keyed accelerator caches. The phase-3 server serializes
// these types directly, so their JSON tags are the wire format: pinned field
// order, lowercase snake_case names.
package viz

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/internal/trail"
	"github.com/yasyf/cc-notes/model"
)

const (
	// walkLimit caps the commit walk that feeds lane attribution and the
	// merge-detection first-parent scan; a truncated walk sets
	// RepoInfo.Truncated.
	walkLimit = 1000
	// maxParentageBranches caps nested-parentage inference: above it, every
	// branch's parent is the trunk (flat), because the pairwise merge-base scan
	// is quadratic in the branch count.
	maxParentageBranches = 64
	// defaultWindow is the default history window when Graph is called with
	// since == 0: ninety days back from now, floored no earlier than the
	// oldest live lane's fork.
	defaultWindow = 90 * 24 * time.Hour
)

// Builder assembles the visualization graph for one repository. It is safe for
// concurrent use: each cache carries its own mutex (see cache.go) and the build
// itself reads immutable git objects.
type Builder struct {
	store *store.Store

	// graphMu guards graphCache. It is held only around the get and the put,
	// never across a build, so two concurrent Graph calls may both compute the
	// same digest; the builds are pure reads, so the last put simply wins.
	graphMu    sync.Mutex
	graphCache map[string]*Graph

	// trailMu guards trailCache and refTips together. A trail is keyed by its
	// entity ref tip, which is immutable, so a cached entry is always valid;
	// refTips records each ref's last-seen tip so InvalidateRefs can drop the
	// entry a moved ref left behind.
	trailMu    sync.Mutex
	trailCache map[model.SHA][]trail.Entry
	refTips    map[string]model.SHA

	// mbMu guards mbCache. A merge base is keyed by the ordered tip pair, both
	// immutable, so entries never go stale and survive invalidation.
	mbMu    sync.Mutex
	mbCache map[string]mergeBase

	// attMu guards attCache. The referenced-attachment index is keyed by a
	// digest over every entity ref tip, so a cached index matches the live
	// entity state until an entity ref moves; InvalidateRefs also clears it.
	attMu    sync.Mutex
	attCache *attIndex
}

// NewBuilder returns a Builder that reads the given store.
func NewBuilder(s *store.Store) *Builder {
	return &Builder{
		store:      s,
		graphCache: make(map[string]*Graph),
		trailCache: make(map[model.SHA][]trail.Entry),
		refTips:    make(map[string]model.SHA),
		mbCache:    make(map[string]mergeBase),
	}
}

// Graph assembles the whole graph for the repository over the history window
// beginning at since (unix seconds); since == 0 selects the default window
// (defaultWindow back from now, floored at the oldest live lane's fork). The
// result is cached by a digest of every branch and entity ref tip plus since,
// so repeated calls over an unchanged repository return the same value until a
// ref moves or InvalidateRefs drops the cache.
func (b *Builder) Graph(ctx context.Context, since int64) (*Graph, error) {
	digest, err := b.digest(ctx, since)
	if err != nil {
		return nil, err
	}
	if g, ok := b.cachedGraph(digest); ok {
		return g, nil
	}

	topo, err := b.topology(ctx, since)
	if err != nil {
		return nil, err
	}
	events, entities, err := b.eventsAndEntities(ctx, topo)
	if err != nil {
		return nil, err
	}

	head, err := b.head(ctx)
	if err != nil {
		return nil, err
	}
	root, err := b.store.Git.Root(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve repo root: %w", err)
	}

	g := &Graph{
		Repo: RepoInfo{
			Root:        root,
			Trunk:       topo.trunk.name,
			Head:        head,
			GeneratedAt: time.Now().UTC().Format(time.RFC3339),
			Truncated:   topo.truncated,
		},
		Lanes:    topo.lanes(),
		Events:   events,
		Entities: entities,
	}
	b.putGraph(digest, g)
	return g, nil
}

// InvalidateRefs drops the whole-graph cache and the trail entries for the
// named refs. The phase-3 watcher calls it when a fetch or a local write moves
// a ref: the graph cache is keyed by every ref tip, so any move invalidates it,
// and the trail entries a moved ref left behind are freed by tip.
func (b *Builder) InvalidateRefs(refNames []string) {
	b.graphMu.Lock()
	b.graphCache = make(map[string]*Graph)
	b.graphMu.Unlock()

	b.trailMu.Lock()
	for _, ref := range refNames {
		if tip, ok := b.refTips[ref]; ok {
			delete(b.trailCache, tip)
			delete(b.refTips, ref)
		}
	}
	b.trailMu.Unlock()

	b.attMu.Lock()
	b.attCache = nil
	b.attMu.Unlock()
}

// digest hashes the sorted (ref, tip) pairs of every branch and entity ref, the
// current symbolic HEAD branch, and since into the whole-graph cache key: any
// ref move, appearance, or removal changes it, and so does a checkout that moves
// HEAD to another branch without moving a tip, or a different window.
func (b *Builder) digest(ctx context.Context, since int64) (string, error) {
	prefixes := []string{"refs/heads/", "refs/remotes/origin/", refs.Namespace}
	var lines []string
	for _, prefix := range prefixes {
		tips, err := b.store.Repo.ListPrefix(ctx, prefix)
		if err != nil {
			return "", fmt.Errorf("list %s: %w", prefix, err)
		}
		for ref, tip := range tips {
			lines = append(lines, ref+"\x00"+string(tip))
		}
	}
	sort.Strings(lines)
	head, err := b.head(ctx)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	for _, line := range lines {
		h.Write([]byte(line))
		h.Write([]byte{'\n'})
	}
	_, _ = fmt.Fprintf(h, "since=%d\nhead=%s", since, head)
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (b *Builder) cachedGraph(digest string) (*Graph, bool) {
	b.graphMu.Lock()
	defer b.graphMu.Unlock()
	g, ok := b.graphCache[digest]
	return g, ok
}

func (b *Builder) putGraph(digest string, g *Graph) {
	b.graphMu.Lock()
	defer b.graphMu.Unlock()
	b.graphCache[digest] = g
}

// head returns the branch HEAD points at, or "" when HEAD is detached.
func (b *Builder) head(ctx context.Context) (string, error) {
	branch, err := b.store.Git.HeadBranch(ctx)
	if err != nil {
		if errors.Is(err, gitcmd.ErrDetachedHead) {
			return "", nil
		}
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}
	return string(branch), nil
}

// entityRefs lists every cc-notes entity ref, sorted, paired with its tip.
func (b *Builder) entityRefs(ctx context.Context) ([]refTip, error) {
	tips, err := b.store.Repo.ListPrefix(ctx, refs.Namespace)
	if err != nil {
		return nil, fmt.Errorf("list entity refs: %w", err)
	}
	out := make([]refTip, 0, len(tips))
	for ref, tip := range tips {
		if _, err := refs.Parse(ref); err != nil {
			continue
		}
		out = append(out, refTip{ref: ref, tip: tip})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ref < out[j].ref })
	return out, nil
}

// refTip pairs an entity ref name with the commit it points at.
type refTip struct {
	ref string
	tip model.SHA
}

// trailOf returns the change trail of the entity at ref/tip, caching it by the
// immutable tip and recording the ref→tip binding for InvalidateRefs.
func (b *Builder) trailOf(ctx context.Context, ref string, tip model.SHA) ([]trail.Entry, error) {
	b.trailMu.Lock()
	if entries, ok := b.trailCache[tip]; ok {
		b.refTips[ref] = tip
		b.trailMu.Unlock()
		return entries, nil
	}
	b.trailMu.Unlock()

	steps, err := b.store.History(ctx, ref)
	if err != nil {
		return nil, fmt.Errorf("history %s: %w", ref, err)
	}
	entries, err := trail.Entries(steps)
	if err != nil {
		return nil, fmt.Errorf("trail %s: %w", ref, err)
	}

	b.trailMu.Lock()
	b.trailCache[tip] = entries
	b.refTips[ref] = tip
	b.trailMu.Unlock()
	return entries, nil
}
