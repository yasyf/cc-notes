package fold

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/yasyf/cc-notes/model"
)

// folder is the per-kind hook set the generic engine drives to fold one chain.
// A fresh folder is constructed per fold call, so it may hold the in-progress
// snapshot and its auxiliary sets as mutable state; the engine owns the loop and
// never shares a folder across calls.
type folder[T model.Snapshot] interface {
	// fresh initializes an un-seeded fold: the snapshot takes its id and
	// created-at stamp from the create commit's sha and author time.
	fresh(sha model.SHA, createdAt int64)
	// seed initializes from a checkpoint's decoded State, rebuilding the
	// auxiliary sets from it. It fails with ErrKindMismatch when State is a
	// snapshot of a different kind.
	seed(state model.Snapshot) error
	// create applies the chain's create op with the create commit's author. The
	// engine guarantees op is a create op; a create op of a foreign kind fails
	// with ErrKindMismatch.
	create(op model.CreateOp, author model.Actor) error
	// apply folds one non-create, non-checkpoint op, reporting whether it
	// applies to this kind. The engine resolves one that does not — see mode.
	apply(op model.Op, c model.PackCommit) bool
	// touch stamps per-commit state after a commit carrying a non-checkpoint op.
	touch(c model.PackCommit)
	// finalize sorts the auxiliary sets into the snapshot, sets its head and
	// skipped-op count, and returns it.
	finalize(head model.SHA, skipped int) T
}

// mode selects what a fold does with an op that does not apply to the chain's
// kind: tolerant skips and counts it (SkippedOps), so a binary can read chains
// a newer one wrote; strict fails with ErrKindMismatch, which the write path
// needs — an illegal combination must be refused, not written and skipped.
type mode int

const (
	tolerant mode = iota
	strict
)

// run replays a linearized chain into a snapshot through f. It selects the seed
// checkpoint, seeds or freshly initializes f, folds every uncovered commit, and
// finalizes. Every kind flows through this one skeleton, so all kinds share the
// create/duplicate-create handling, the checkpoint no-op, and the touch gating —
// the guarantees that make a compacted fold converge with a full fold.
//
// A skipped op still touches: the commit is real history, and a binary that
// does apply the op stamps the same UpdatedAt, so the two converge.
func run[T model.Snapshot](ordered []model.PackCommit, f folder[T], m mode) (T, error) {
	var zero T
	b, seeded := selectSeed(ordered)
	r, err := startReplay(f, m, ordered[0], b, seeded)
	if err != nil {
		return zero, err
	}
	for _, c := range ordered {
		if err := r.step(c); err != nil {
			return zero, err
		}
	}
	return r.snapshot(ordered[len(ordered)-1].SHA)
}

func extend[T model.Snapshot](base model.Snapshot, c model.PackCommit, f folder[T]) (T, error) {
	var zero T
	if err := f.seed(base); err != nil {
		return zero, err
	}
	r := &replay[T]{f: f, m: strict, created: true}
	if err := r.step(c); err != nil {
		return zero, err
	}
	return r.snapshot(c.SHA)
}

func walk[T model.Snapshot](ordered []model.PackCommit, newFolder func() folder[T], fn func(Step) error) error {
	var seeds seedTracker
	var r *replay[T]
	for k, c := range ordered {
		b, seeded, changed := seeds.extend(ordered[:k+1])
		pending := ordered[k : k+1]
		if r == nil || changed {
			var err error
			if r, err = startReplay(newFolder(), tolerant, ordered[0], b, seeded); err != nil {
				return err
			}
			pending = ordered[:k+1]
		}
		for _, p := range pending {
			if err := r.step(p); err != nil {
				return err
			}
		}
		snapshot, err := r.snapshot(c.SHA)
		if err != nil {
			return err
		}
		if err := fn(Step{Commit: c, Snapshot: snapshot}); err != nil {
			return err
		}
	}
	return nil
}

type replay[T model.Snapshot] struct {
	f       folder[T]
	m       mode
	base    base
	seeded  bool
	created bool
	skipped int
}

func startReplay[T model.Snapshot](f folder[T], m mode, root model.PackCommit, b base, seeded bool) (*replay[T], error) {
	r := &replay[T]{f: f, m: m, base: b, seeded: seeded}
	if !seeded {
		f.fresh(root.SHA, root.AuthorTime)
		return r, nil
	}
	if err := f.seed(b.checkpoint.State); err != nil {
		return nil, err
	}
	r.created = true
	return r, nil
}

func (r *replay[T]) step(c model.PackCommit) error {
	if r.seeded && r.base.covers(c) {
		return nil
	}
	for _, op := range c.Pack.Ops {
		if !r.created {
			co, ok := op.(model.CreateOp)
			if !ok {
				return fmt.Errorf("%w: got %s", ErrNoCreate, op.OpKind())
			}
			if err := r.f.create(co, c.Author); err != nil {
				return err
			}
			r.created = true
			continue
		}
		switch op.(type) {
		case model.Checkpoint:
		default:
			if co, ok := op.(model.CreateOp); ok {
				return fmt.Errorf("%w: %s", ErrDuplicateCreate, co.OpKind())
			}
			if !r.f.apply(op, c) {
				if r.m == strict {
					var zero T
					return fmt.Errorf("%w: %s does not apply to a %s", ErrKindMismatch, op.OpKind(), zero.Meta().Kind)
				}
				r.skipped++
			}
		}
	}
	if hasNonCheckpointOp(c) {
		r.f.touch(c)
	}
	return nil
}

func (r *replay[T]) snapshot(head model.SHA) (T, error) {
	if !r.created {
		var zero T
		return zero, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	}
	return r.f.finalize(head, r.skipped), nil
}

type base struct {
	checkpoint model.Checkpoint
	sha        model.SHA
	covered    map[model.SHA]bool
}

func newBase(cp model.Checkpoint, sha model.SHA) base {
	covered := make(map[model.SHA]bool, len(cp.CoversShas))
	for _, s := range cp.CoversShas {
		covered[s] = true
	}
	return base{checkpoint: cp, sha: sha, covered: covered}
}

func (b base) covers(c model.PackCommit) bool {
	return c.SHA == b.sha || b.covered[c.SHA]
}

func (b base) admits(c model.PackCommit) bool {
	return b.covers(c) || c.Pack.Lamport > b.checkpoint.CoversLamport
}

func (b base) outranks(cp model.Checkpoint, sha model.SHA) bool {
	return cp.CoversLamport > b.checkpoint.CoversLamport ||
		(cp.CoversLamport == b.checkpoint.CoversLamport && sha > b.sha)
}

// selectSeed chooses the checkpoint a fold may start its snapshot from, plus
// the set of commits its State already covers. It returns the checkpoint with
// the greatest CoversLamport (sha tiebreak) only when that checkpoint is
// seed-safe: every commit in ordered that is neither the checkpoint commit nor
// in its CoversShas has Pack.Lamport > CoversLamport. That gate is the
// load-bearing convergence guarantee — because Linearize orders by lamport
// first and every covered op has lamport <= CoversLamport, a strictly-greater
// uncovered lamport means every uncovered op sorts after every covered op, so
// seeding from State and replaying only the uncovered suffix yields exactly the
// full-history fold. When the gate fails (concurrent checkpoints over different
// frontiers), it returns ok=false and the caller folds the full chain, where
// every checkpoint is a no-op and the covered ops replay through their original
// commits. The fallback is always available: compaction never deletes objects,
// so covered commits remain in the chain.
func selectSeed(ordered []model.PackCommit) (b base, ok bool) {
	var best base
	found := false
	for _, c := range ordered {
		cp, isCheckpoint := checkpointOf(c)
		if !isCheckpoint {
			continue
		}
		if !found || best.outranks(cp, c.SHA) {
			best, found = base{checkpoint: cp, sha: c.SHA}, true
		}
	}
	if !found {
		return base{}, false
	}
	b = newBase(best.checkpoint, best.sha)
	return b, seedSafe(b, ordered)
}

func seedSafe(b base, ordered []model.PackCommit) bool {
	for _, c := range ordered {
		if !b.admits(c) {
			return false
		}
	}
	return true
}

type seedTracker struct {
	best  base
	found bool
	safe  bool
}

func (t *seedTracker) extend(prefix []model.PackCommit) (b base, seeded, changed bool) {
	wasSeeded, wasSHA := t.found && t.safe, t.best.sha
	c := prefix[len(prefix)-1]
	if cp, ok := checkpointOf(c); ok && (!t.found || t.best.outranks(cp, c.SHA)) {
		t.best, t.found = newBase(cp, c.SHA), true
		t.safe = seedSafe(t.best, prefix)
	} else if t.found && t.safe {
		t.safe = t.best.admits(c)
	}
	seeded = t.found && t.safe
	changed = seeded != wasSeeded || (seeded && t.best.sha != wasSHA)
	if !seeded {
		return base{}, false, changed
	}
	return t.best, true, changed
}

// checkpointOf returns the Checkpoint a checkpoint commit carries. A checkpoint
// commit holds exactly one op, the Checkpoint.
func checkpointOf(c model.PackCommit) (model.Checkpoint, bool) {
	if len(c.Pack.Ops) == 1 {
		if cp, ok := c.Pack.Ops[0].(model.Checkpoint); ok {
			return cp, true
		}
	}
	return model.Checkpoint{}, false
}

// hasNonCheckpointOp reports whether c carries any op that is not a Checkpoint.
// A checkpoint commit's compaction time and author are not the entity's, so it
// must not stamp UpdatedAt or the lease heartbeat — only a commit with a real
// op does.
func hasNonCheckpointOp(c model.PackCommit) bool {
	for _, op := range c.Pack.Ops {
		if _, ok := op.(model.Checkpoint); !ok {
			return true
		}
	}
	return false
}

func sortedKeys[K cmp.Ordered](set map[K]bool) []K {
	keys := make([]K, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// sortedAttachments returns the folded attachment set ordered by Name, or nil
// when empty: Attachments marshals omitempty, so a nil result keeps
// attachment-less snapshot bytes identical to their pre-attachment form and
// keeps a fresh fold DeepEqual to its own cache and checkpoint round-trips.
func sortedAttachments(set map[string]model.Attachment) []model.Attachment {
	if len(set) == 0 {
		return nil
	}
	attachments := make([]model.Attachment, 0, len(set))
	for _, a := range set {
		attachments = append(attachments, a)
	}
	slices.SortFunc(attachments, func(a, b model.Attachment) int {
		return cmp.Compare(a.Name, b.Name)
	})
	return attachments
}

// sortedAnchorsNil returns the folded anchor set ordered by (kind, value), or
// nil when empty: Runbook.Anchors marshals omitempty, so a nil result keeps
// anchor-less runbook snapshot bytes identical to their pre-anchor form and
// keeps a fresh fold DeepEqual to its own cache and checkpoint round-trips.
func sortedAnchorsNil(set map[model.Anchor]bool) []model.Anchor {
	if len(set) == 0 {
		return nil
	}
	return sortedAnchors(set)
}

func sortedAnchors(set map[model.Anchor]bool) []model.Anchor {
	anchors := make([]model.Anchor, 0, len(set))
	for a := range set {
		anchors = append(anchors, a)
	}
	slices.SortFunc(anchors, func(a, b model.Anchor) int {
		if c := cmp.Compare(a.Kind, b.Kind); c != 0 {
			return c
		}
		return cmp.Compare(a.Value, b.Value)
	})
	return anchors
}
