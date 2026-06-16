// Package fold turns an entity's commit DAG into deterministic state. It is
// the CRDT core: Linearize totally orders the commits, and Fold replays
// their operation packs into a snapshot. Scalars resolve last-write-wins
// under the linearization, sets resolve per-element last-op-wins, claim is
// conditional first-wins, and deletion is monotone. Fold never validates
// transitions — every replica must converge on whatever the history says;
// legality is an append-time concern. The package is pure: no git, no I/O.
package fold

import (
	"cmp"
	"errors"
	"fmt"
	"slices"

	"github.com/yasyf/cc-notes/internal/model"
)

var (
	// ErrNoCreate reports a chain whose first operation is not a create.
	ErrNoCreate = errors.New("first op is not a create")
	// ErrDuplicateCreate reports a chain with a second create operation.
	ErrDuplicateCreate = errors.New("duplicate create op")
	// ErrKindMismatch reports an op that does not apply to the chain's
	// entity kind, or a chain folded as the wrong kind.
	ErrKindMismatch = errors.New("entity kind mismatch")
)

// Fold linearizes the chain and replays its operation packs into a snapshot,
// dispatching on the create op: a create_note chain folds to model.Note, a
// create_task chain to model.Task. Set-valued snapshot fields come back as
// non-nil sorted slices; anchors sort by (kind, value).
//
// A chain may contain Checkpoint commits. Fold receives the full chain (the
// create commit is always the root); when the newest checkpoint is seed-safe it
// starts the snapshot from Checkpoint.State and replays only the uncovered
// suffix, otherwise it folds the full history with every checkpoint a no-op.
// Either way the result equals folding the uncompacted history, so a compacted
// fold and a full fold converge on every replica.
func Fold(commits []model.PackCommit) (model.Snapshot, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return nil, err
	}
	switch first := firstOp(ordered).(type) {
	case model.CreateNote:
		return foldNote(ordered)
	case model.CreateTask:
		return foldTask(ordered)
	case nil:
		return nil, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	default:
		return nil, fmt.Errorf("%w: got %s", ErrNoCreate, first.OpKind())
	}
}

// Note linearizes the chain and folds it as a note. It fails with
// ErrKindMismatch when the chain was created as a task.
func Note(commits []model.PackCommit) (model.Note, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Note{}, err
	}
	return foldNote(ordered)
}

// Task linearizes the chain and folds it as a task. It fails with
// ErrKindMismatch when the chain was created as a note.
func Task(commits []model.PackCommit) (model.Task, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Task{}, err
	}
	return foldTask(ordered)
}

func foldNote(ordered []model.PackCommit) (model.Note, error) {
	base, seedSHA, covered, seeded := selectSeed(ordered)
	var note model.Note
	tags := map[string]bool{}
	anchors := map[model.Anchor]bool{}
	superseded := map[model.EntityID]bool{}
	var witness []model.AnchorWitness
	created := false
	if seeded {
		seed, ok := base.State.(model.Note)
		if !ok {
			return model.Note{}, fmt.Errorf("%w: checkpoint over a task folded as a note", ErrKindMismatch)
		}
		note = seed
		for _, t := range seed.Tags {
			tags[t] = true
		}
		for _, a := range seed.Anchors {
			anchors[a] = true
		}
		for _, id := range seed.SupersededBy {
			superseded[id] = true
		}
		witness = seed.Witness
		created = true
	} else {
		note = model.Note{ID: model.EntityID(ordered[0].SHA), CreatedAt: ordered[0].AuthorTime}
	}
	for _, c := range ordered {
		if seeded && (c.SHA == seedSHA || covered[c.SHA]) {
			continue
		}
		for _, op := range c.Pack.Ops {
			if !created {
				switch o := op.(type) {
				case model.CreateNote:
					created = true
					note.Title, note.Body, note.Author = o.Title, o.Body, c.Author
					for _, t := range o.Tags {
						tags[t] = true
					}
					for _, a := range o.Anchors {
						anchors[a] = true
					}
				case model.CreateTask:
					return model.Note{}, fmt.Errorf("%w: create_task chain folded as a note", ErrKindMismatch)
				default:
					return model.Note{}, fmt.Errorf("%w: got %s", ErrNoCreate, op.OpKind())
				}
				continue
			}
			switch o := op.(type) {
			case model.Checkpoint:
				// A non-seed checkpoint is a fold no-op: its covered ops replay
				// through their original commits, which remain in the chain.
			case model.CreateNote, model.CreateTask:
				return model.Note{}, fmt.Errorf("%w: %s", ErrDuplicateCreate, op.OpKind())
			case model.SetTitle:
				note.Title = o.Title
			case model.SetBody:
				note.Body = o.Body
			case model.AddTag:
				tags[o.Tag] = true
			case model.RemoveTag:
				delete(tags, o.Tag)
			case model.AddAnchor:
				anchors[o.Anchor] = true
			case model.RemoveAnchor:
				delete(anchors, o.Anchor)
			case model.DeleteNote:
				note.Deleted = true
			case model.VerifyNote:
				note.VerifiedAt = c.AuthorTime
				note.VerifiedBy = c.Author
				note.VerifiedCommit = o.VerifiedCommit
				witness = o.Witness
			case model.AddSupersededBy:
				superseded[o.ID] = true
			case model.RemoveSupersededBy:
				delete(superseded, o.ID)
			default:
				return model.Note{}, fmt.Errorf("%w: %s on a note", ErrKindMismatch, op.OpKind())
			}
		}
		if hasNonCheckpointOp(c) {
			note.UpdatedAt = c.AuthorTime
		}
	}
	if !created {
		return model.Note{}, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	}
	note.Tags = sortedKeys(tags)
	note.Anchors = sortedAnchors(anchors)
	note.SupersededBy = sortedKeys(superseded)
	note.Witness = witness
	note.Head = ordered[len(ordered)-1].SHA
	return note, nil
}

func foldTask(ordered []model.PackCommit) (model.Task, error) {
	base, seedSHA, covered, seeded := selectSeed(ordered)
	var task model.Task
	labels := map[string]bool{}
	deps := map[model.EntityID]bool{}
	commits := map[model.SHA]bool{}
	created := false
	if seeded {
		seed, ok := base.State.(model.Task)
		if !ok {
			return model.Task{}, fmt.Errorf("%w: checkpoint over a note folded as a task", ErrKindMismatch)
		}
		task = seed
		task.Comments = slices.Clone(seed.Comments)
		for _, l := range seed.Labels {
			labels[l] = true
		}
		for _, id := range seed.BlockedBy {
			deps[id] = true
		}
		for _, sha := range seed.Commits {
			commits[sha] = true
		}
		created = true
	} else {
		task = model.Task{ID: model.EntityID(ordered[0].SHA), CreatedAt: ordered[0].AuthorTime, Comments: []model.Comment{}}
	}
	for _, c := range ordered {
		if seeded && (c.SHA == seedSHA || covered[c.SHA]) {
			continue
		}
		for _, op := range c.Pack.Ops {
			if !created {
				switch o := op.(type) {
				case model.CreateTask:
					created = true
					task.Title, task.Description = o.Title, o.Description
					task.Type, task.Priority = o.Type, o.Priority
					task.Branch, task.Parent = o.Branch, o.Parent
					task.Status = model.StatusOpen
					for _, l := range o.Labels {
						labels[l] = true
					}
				case model.CreateNote:
					return model.Task{}, fmt.Errorf("%w: create_note chain folded as a task", ErrKindMismatch)
				default:
					return model.Task{}, fmt.Errorf("%w: got %s", ErrNoCreate, op.OpKind())
				}
				continue
			}
			switch o := op.(type) {
			case model.Checkpoint:
				// A non-seed checkpoint is a fold no-op: its covered ops replay
				// through their original commits, which remain in the chain.
			case model.CreateNote, model.CreateTask:
				return model.Task{}, fmt.Errorf("%w: %s", ErrDuplicateCreate, op.OpKind())
			case model.SetTitle:
				task.Title = o.Title
			case model.SetDescription:
				task.Description = o.Description
			case model.SetType:
				task.Type = o.Type
			case model.SetPriority:
				task.Priority = o.Priority
			case model.SetStatus:
				applyStatus(&task, o.Status, c.AuthorTime)
			case model.SetAssignee:
				task.Assignee = o.Assignee
			case model.Claim:
				if task.Status == model.StatusOpen && task.Assignee == "" {
					task.Status = model.StatusInProgress
					task.Assignee = o.Assignee
					task.StartedAt = c.AuthorTime
				}
			case model.AddLabel:
				labels[o.Label] = true
			case model.RemoveLabel:
				delete(labels, o.Label)
			case model.AddDep:
				deps[o.ID] = true
			case model.RemoveDep:
				delete(deps, o.ID)
			case model.SetParent:
				task.Parent = o.Parent
			case model.AddComment:
				task.Comments = append(task.Comments, model.Comment{Author: c.Author, TS: c.AuthorTime, Body: o.Body})
			case model.SetBranch:
				task.Branch = o.Branch
			case model.Renew:
				// heartbeat refresh handled uniformly below
			case model.Reclaim:
				if task.Assignee == o.From && task.HeartbeatLamport <= o.AfterLamport {
					task.Assignee = o.Assignee
					task.Status = model.StatusInProgress
					task.HeartbeatAt = c.AuthorTime
					task.HeartbeatLamport = c.Pack.Lamport
				}
			case model.LinkCommit:
				commits[o.SHA] = true
			case model.UnlinkCommit:
				delete(commits, o.SHA)
			default:
				return model.Task{}, fmt.Errorf("%w: %s on a task", ErrKindMismatch, op.OpKind())
			}
		}
		if hasNonCheckpointOp(c) {
			task.UpdatedAt = c.AuthorTime
			if task.Assignee != "" && c.Author == task.Assignee {
				task.HeartbeatAt = c.AuthorTime
				task.HeartbeatLamport = c.Pack.Lamport
			}
		}
	}
	if !created {
		return model.Task{}, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	}
	task.Labels = sortedKeys(labels)
	task.BlockedBy = sortedKeys(deps)
	task.Commits = sortedKeys(commits)
	task.Head = ordered[len(ordered)-1].SHA
	return task, nil
}

func applyStatus(task *model.Task, status model.Status, at int64) {
	if status == model.StatusInProgress && task.Status != model.StatusInProgress {
		task.StartedAt = at
	}
	task.Status = status
	switch status {
	case model.StatusDone, model.StatusCancelled:
		task.ClosedAt = at
	case model.StatusOpen, model.StatusInProgress:
		task.ClosedAt = 0
	}
}

func firstOp(ordered []model.PackCommit) model.Op {
	for _, c := range ordered {
		if len(c.Pack.Ops) > 0 {
			return c.Pack.Ops[0]
		}
	}
	return nil
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
