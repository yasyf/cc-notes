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
	// apply folds one non-create, non-checkpoint op. It fails with
	// ErrKindMismatch when op does not apply to this kind.
	apply(op model.Op, c model.PackCommit) error
	// touch stamps per-commit state after a commit carrying a non-checkpoint op.
	touch(c model.PackCommit)
	// finalize sorts the auxiliary sets into the snapshot, sets its head, and
	// returns it.
	finalize(head model.SHA) T
}

// run replays a linearized chain into a snapshot through f. It selects the seed
// checkpoint, seeds or freshly initializes f, folds every uncovered commit, and
// finalizes. Every kind flows through this one skeleton, so all kinds share the
// create/duplicate-create handling, the checkpoint no-op, and the touch gating —
// the guarantees that make a compacted fold converge with a full fold.
func run[T model.Snapshot](ordered []model.PackCommit, f folder[T]) (T, error) {
	var zero T
	base, seedSHA, covered, seeded := selectSeed(ordered)
	created := false
	if seeded {
		if err := f.seed(base.State); err != nil {
			return zero, err
		}
		created = true
	} else {
		f.fresh(ordered[0].SHA, ordered[0].AuthorTime)
	}
	for _, c := range ordered {
		if seeded && (c.SHA == seedSHA || covered[c.SHA]) {
			continue
		}
		for _, op := range c.Pack.Ops {
			if !created {
				co, ok := op.(model.CreateOp)
				if !ok {
					return zero, fmt.Errorf("%w: got %s", ErrNoCreate, op.OpKind())
				}
				if err := f.create(co, c.Author); err != nil {
					return zero, err
				}
				created = true
				continue
			}
			switch op.(type) {
			case model.Checkpoint:
				// A non-seed checkpoint is a fold no-op: its covered ops replay
				// through their original commits, which remain in the chain.
			default:
				if co, ok := op.(model.CreateOp); ok {
					return zero, fmt.Errorf("%w: %s", ErrDuplicateCreate, co.OpKind())
				}
				if err := f.apply(op, c); err != nil {
					return zero, err
				}
			}
		}
		if hasNonCheckpointOp(c) {
			f.touch(c)
		}
	}
	if !created {
		return zero, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	}
	return f.finalize(ordered[len(ordered)-1].SHA), nil
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
func selectSeed(ordered []model.PackCommit) (base model.Checkpoint, seedSHA model.SHA, covered map[model.SHA]bool, ok bool) {
	found := false
	for _, c := range ordered {
		cp, isCheckpoint := checkpointOf(c)
		if !isCheckpoint {
			continue
		}
		if !found || cp.CoversLamport > base.CoversLamport ||
			(cp.CoversLamport == base.CoversLamport && c.SHA > seedSHA) {
			base, seedSHA, found = cp, c.SHA, true
		}
	}
	if !found {
		return model.Checkpoint{}, "", nil, false
	}
	covered = make(map[model.SHA]bool, len(base.CoversShas))
	for _, s := range base.CoversShas {
		covered[s] = true
	}
	for _, c := range ordered {
		if c.SHA == seedSHA || covered[c.SHA] {
			continue
		}
		if c.Pack.Lamport <= base.CoversLamport {
			return model.Checkpoint{}, "", nil, false
		}
	}
	return base, seedSHA, covered, true
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
