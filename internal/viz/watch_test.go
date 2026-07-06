package viz

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// newWatcher opens a store over r and wires a Watcher, its Builder, and a hub
// polling at interval, returning all four so a test can drive scan directly.
func newWatcher(t *testing.T, r *gitRepo, interval time.Duration) (*Watcher, *store.Store, *Builder, *Hub) {
	t.Helper()
	s := r.openStore()
	b := NewBuilder(s)
	hub := NewHub()
	return NewWatcher(s, b, hub, interval), s, b, hub
}

// recvEvent decodes the one buffered payload on ch, failing when none is queued.
func recvEvent(t *testing.T, ch <-chan []byte) refsEvent {
	t.Helper()
	select {
	case p := <-ch:
		var ev refsEvent
		if err := json.Unmarshal(p, &ev); err != nil {
			t.Fatalf("decode %s: %v", p, err)
		}
		return ev
	default:
		t.Fatal("no event published, want one")
		return refsEvent{}
	}
}

// assertSilent fails when any payload is queued on ch.
func assertSilent(t *testing.T, ch <-chan []byte) {
	t.Helper()
	select {
	case p := <-ch:
		t.Fatalf("published %s, want nothing", p)
	default:
	}
}

// hasRef reports whether names contains want.
func hasRef(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// TestWatchFirstScanSilent pins that the baseline scan records state and
// publishes nothing.
func TestWatchFirstScanSilent(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	w, _, _, hub := newWatcher(t, r, time.Hour)
	ch, _ := hub.Subscribe()

	if err := w.scan(t.Context()); err != nil {
		t.Fatalf("scan: %v", err)
	}
	assertSilent(t, ch)
}

// TestWatchBranchTipMove pins that a real commit on main surfaces as a head-ref
// delta with gen=1, drops the Builder's cached graph (the next Graph reflects
// the new tip), and lists no entity ref.
func TestWatchBranchTipMove(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	w, _, b, hub := newWatcher(t, r, time.Hour)
	ch, _ := hub.Subscribe()
	ctx := t.Context()

	if err := w.scan(ctx); err != nil {
		t.Fatalf("baseline scan: %v", err)
	}
	g1, err := b.Graph(ctx, 0)
	if err != nil {
		t.Fatalf("graph before: %v", err)
	}
	before := laneByName(t, g1, "main").Tip.SHA

	c2 := r.commit("c2")
	if err := w.scan(ctx); err != nil {
		t.Fatalf("scan: %v", err)
	}

	ev := recvEvent(t, ch)
	if ev.Gen != 1 {
		t.Errorf("gen = %d, want 1", ev.Gen)
	}
	if !hasRef(ev.Heads, "refs/heads/main") {
		t.Errorf("heads = %v, want to contain refs/heads/main", ev.Heads)
	}
	if len(ev.Entities) != 0 {
		t.Errorf("entities = %v, want none", ev.Entities)
	}
	if ev.Head != "main" {
		t.Errorf("head = %q, want main", ev.Head)
	}

	g2, err := b.Graph(ctx, 0)
	if err != nil {
		t.Fatalf("graph after: %v", err)
	}
	after := laneByName(t, g2, "main").Tip.SHA
	if after == before {
		t.Errorf("trunk tip unchanged after invalidation: %s", after)
	}
	if after != c2.sha {
		t.Errorf("trunk tip = %s, want %s", after, c2.sha)
	}
}

// TestWatchEntityAppend pins that appending to a task moves its cc-notes ref and
// the delta names that ref in entities, with no head ref listed.
func TestWatchEntityAppend(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	w, s, _, hub := newWatcher(t, r, time.Hour)
	ch, _ := hub.Subscribe()
	ctx := t.Context()

	snap, err := s.Create(ctx, []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: "ship", Type: model.TypeTask, Branch: "main"}})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	task := snap.(model.Task)
	if err := w.scan(ctx); err != nil {
		t.Fatalf("baseline scan: %v", err)
	}

	if _, err := s.Append(ctx, refs.Task(task.ID), []model.Op{model.SetStatus{Status: model.StatusDone}}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := w.scan(ctx); err != nil {
		t.Fatalf("scan: %v", err)
	}

	ev := recvEvent(t, ch)
	wantRef := refs.Task(task.ID)
	if !hasRef(ev.Entities, wantRef) {
		t.Errorf("entities = %v, want to contain %s", ev.Entities, wantRef)
	}
	if len(ev.Heads) != 0 {
		t.Errorf("heads = %v, want none", ev.Heads)
	}
	if ev.Head != "main" {
		t.Errorf("head = %q, want main", ev.Head)
	}
}

// TestWatchSymbolicHeadCheckout pins that switching HEAD to a same-tip branch
// publishes the new head with empty heads and entities lists: only the symbolic
// HEAD changed, so no branch or entity ref is named.
func TestWatchSymbolicHeadCheckout(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	r.git("branch", "feature")
	w, _, _, hub := newWatcher(t, r, time.Hour)
	ch, _ := hub.Subscribe()
	ctx := t.Context()

	if err := w.scan(ctx); err != nil {
		t.Fatalf("baseline scan: %v", err)
	}
	r.git("checkout", "-q", "feature")
	if err := w.scan(ctx); err != nil {
		t.Fatalf("scan: %v", err)
	}

	ev := recvEvent(t, ch)
	if ev.Gen != 1 {
		t.Errorf("gen = %d, want 1", ev.Gen)
	}
	if ev.Head != "feature" {
		t.Errorf("head = %q, want feature", ev.Head)
	}
	if len(ev.Heads) != 0 {
		t.Errorf("heads = %v, want none (no tip moved)", ev.Heads)
	}
	if len(ev.Entities) != 0 {
		t.Errorf("entities = %v, want none", ev.Entities)
	}
}

// TestWatchRunPublishesAndStops drives Run on a 10ms interval: after a baseline
// and a real commit, the subscriber receives the delta, and cancelling the
// context makes Run return nil promptly.
func TestWatchRunPublishesAndStops(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	w, _, _, hub := newWatcher(t, r, 10*time.Millisecond)
	ch, _ := hub.Subscribe()

	baseCtx := t.Context()
	if err := w.scan(baseCtx); err != nil {
		t.Fatalf("baseline scan: %v", err)
	}
	r.commit("c2")

	runCtx, cancel := context.WithCancel(baseCtx)
	done := make(chan error, 1)
	go func() { done <- w.Run(runCtx) }()

	select {
	case p := <-ch:
		var ev refsEvent
		if err := json.Unmarshal(p, &ev); err != nil {
			t.Fatalf("decode %s: %v", p, err)
		}
		if !hasRef(ev.Heads, "refs/heads/main") {
			t.Errorf("heads = %v, want to contain refs/heads/main", ev.Heads)
		}
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("Run published no event within 5s")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on cancel", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return within 5s of cancel")
	}
}
