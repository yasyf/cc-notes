package viz

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// countEvents counts the graph events belonging to the entity id.
func countEvents(g *Graph, id model.EntityID) int {
	n := 0
	for _, e := range g.Events {
		if e.Entity.ID == id {
			n++
		}
	}
	return n
}

// trailCached reports whether the builder holds a cached trail at tip.
func trailCached(b *Builder, tip model.SHA) bool {
	b.trailMu.Lock()
	defer b.trailMu.Unlock()
	_, ok := b.trailCache[tip]
	return ok
}

// cachedRefTip returns the builder's last-seen tip binding for ref.
func cachedRefTip(b *Builder, ref string) (model.SHA, bool) {
	b.trailMu.Lock()
	defer b.trailMu.Unlock()
	tip, ok := b.refTips[ref]
	return tip, ok
}

// TestGraphCachesUnchangedRefs covers (a): two Graph calls over a repository
// whose refs have not moved return the identical cached pointer.
func TestGraphCachesUnchangedRefs(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	b := NewBuilder(r.openStore())

	g1, err := b.Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	g2, err := b.Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph second call: %v", err)
	}
	if g1 != g2 {
		t.Errorf("unchanged refs returned a fresh graph, want the cached pointer")
	}
}

// TestGraphRebuildsOnMovedTip covers (b): a branch tip that moves without any
// InvalidateRefs call still forces a rebuild, because the digest keys on every
// ref tip. The rebuilt graph reflects the new trunk tip.
func TestGraphRebuildsOnMovedTip(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	b := NewBuilder(r.openStore())

	g1, err := b.Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	c2 := r.commit("c2")

	g2, err := b.Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph after tip move: %v", err)
	}
	if g2 == g1 {
		t.Fatalf("moved tip returned the stale cached graph, want a rebuild")
	}
	trunk := laneByName(t, g2, "main")
	if trunk.Tip == nil || trunk.Tip.SHA != c2.sha {
		t.Errorf("rebuilt trunk tip = %+v, want moved tip %s", trunk.Tip, c2.sha)
	}
}

// TestGraphRebuildsOnHeadCheckout covers a symbolic-HEAD move that touches no
// tip: checking out another branch sitting at the same commit still forces a
// rebuild, because the digest keys on the current HEAD branch. The rebuilt graph
// reflects the new head without any InvalidateRefs call.
func TestGraphRebuildsOnHeadCheckout(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	r.git("checkout", "-q", "-b", "side")
	r.git("checkout", "-q", "main")
	b := NewBuilder(r.openStore())

	g1, err := b.Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if g1.Repo.Head != "main" {
		t.Fatalf("head before checkout = %q, want main", g1.Repo.Head)
	}

	r.git("checkout", "-q", "side")

	g2, err := b.Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph after checkout: %v", err)
	}
	if g2 == g1 {
		t.Fatalf("head checkout returned the stale cached graph, want a rebuild")
	}
	if g2.Repo.Head != "side" {
		t.Errorf("rebuilt head = %q, want side", g2.Repo.Head)
	}
}

// TestInvalidateRefsDropsTrailCache covers (c): InvalidateRefs on an entity ref
// drops the trail entry the moved ref left behind (keyed by its old, immutable
// tip) and its ref binding, and the next Graph reflects the appended op's event.
func TestInvalidateRefsDropsTrailCache(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	s := r.openStore()
	id := createTask(t, s, "cache task", model.Branch("main"))
	ref := refs.Task(id)
	b := NewBuilder(s)

	g1, err := b.Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if got := countEvents(g1, id); got != 1 {
		t.Fatalf("task events before append = %d, want 1 (created)", got)
	}
	oldTip, ok := cachedRefTip(b, ref)
	if !ok {
		t.Fatalf("no refTips binding for %s after Graph", ref)
	}
	if !trailCached(b, oldTip) {
		t.Fatalf("no cached trail at old tip %s after Graph", oldTip)
	}

	appendOps(t, s, ref, model.SetStatus{Status: model.StatusDone})

	b.InvalidateRefs([]string{ref})
	if trailCached(b, oldTip) {
		t.Errorf("InvalidateRefs left the trail cached at the moved ref's old tip %s", oldTip)
	}
	if _, ok := cachedRefTip(b, ref); ok {
		t.Errorf("InvalidateRefs left the refTips binding for %s", ref)
	}

	g2, err := b.Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph after invalidate: %v", err)
	}
	if got := countEvents(g2, id); got != 2 {
		t.Errorf("task events after append = %d, want 2 (created, closed)", got)
	}
}

// TestInvalidateRefsDropsGraphCache covers (d): InvalidateRefs always drops the
// whole-graph cache, even when handed a ref that names nothing in the repository,
// so the next Graph rebuilds.
func TestInvalidateRefsDropsGraphCache(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	b := NewBuilder(r.openStore())

	g1, err := b.Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	g2, err := b.Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph second call: %v", err)
	}
	if g1 != g2 {
		t.Fatalf("unchanged refs returned a fresh graph, want the cached pointer")
	}

	b.InvalidateRefs([]string{"refs/heads/does-not-exist"})

	g3, err := b.Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph after invalidate: %v", err)
	}
	if g3 == g1 {
		t.Errorf("InvalidateRefs did not drop the whole-graph cache")
	}
}
