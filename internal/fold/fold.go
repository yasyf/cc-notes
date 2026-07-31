// Package fold turns an entity's commit DAG into deterministic state. It is
// the CRDT core: Linearize totally orders the commits, and Fold replays
// their operation packs into a snapshot. Scalars resolve last-write-wins
// under the linearization, sets resolve per-element last-op-wins, claim is
// conditional first-wins, and deletion is monotone. Fold never validates
// transitions — every replica must converge on whatever the history says;
// legality is an append-time concern. The package is pure: no git, no I/O.
//
// One generic engine (run in engine.go) drives every kind through a per-kind
// folder, so all kinds share the seed selection, create/duplicate-create
// handling, checkpoint no-op, and touch gating; a folder contributes only its
// kind-specific op cases and finalize.
package fold

import (
	"errors"
	"fmt"

	"github.com/yasyf/cc-notes/model"
)

var (
	// ErrNoCreate reports a chain whose first operation is not a create.
	ErrNoCreate = errors.New("first op is not a create")
	// ErrDuplicateCreate reports a chain with a second create operation.
	ErrDuplicateCreate = errors.New("duplicate create op")
	// ErrKindMismatch reports a chain folded as the wrong kind — a create op or
	// checkpoint state of a foreign kind — or, under Strict only, an op that
	// does not apply to the chain's entity kind.
	ErrKindMismatch = errors.New("entity kind mismatch")
)

// folders maps each entity kind to the boxing fold that turns a linearized
// chain into its snapshot. Fold, Strict, and History dispatch through it on the
// create op's kind. Its keys are exactly model.Kinds().
var folders = map[model.Kind]func([]model.PackCommit, mode) (model.Snapshot, error){
	model.KindNote:          func(o []model.PackCommit, m mode) (model.Snapshot, error) { return foldNote(o, m) },
	model.KindDoc:           func(o []model.PackCommit, m mode) (model.Snapshot, error) { return foldDoc(o, m) },
	model.KindLog:           func(o []model.PackCommit, m mode) (model.Snapshot, error) { return foldLog(o, m) },
	model.KindTask:          func(o []model.PackCommit, m mode) (model.Snapshot, error) { return foldTask(o, m) },
	model.KindSprint:        func(o []model.PackCommit, m mode) (model.Snapshot, error) { return foldSprint(o, m) },
	model.KindProject:       func(o []model.PackCommit, m mode) (model.Snapshot, error) { return foldProject(o, m) },
	model.KindRunbook:       func(o []model.PackCommit, m mode) (model.Snapshot, error) { return foldRunbook(o, m) },
	model.KindInvestigation: func(o []model.PackCommit, m mode) (model.Snapshot, error) { return foldInvestigation(o, m) },
	model.KindPlan:          func(o []model.PackCommit, m mode) (model.Snapshot, error) { return foldPlan(o, m) },
}

// Fold linearizes the chain and replays its operation packs into a snapshot,
// dispatching on the create op: a create_note chain folds to model.Note, a
// create_doc chain to model.Doc, a create_log chain to model.Log, a create_task
// chain to model.Task, a create_sprint chain to model.Sprint, a create_project
// chain to model.Project, a create_runbook chain to model.Runbook, a
// create_investigation chain to model.Investigation, and a create_plan chain to
// model.Plan. Set-valued snapshot fields come back as non-nil sorted slices;
// anchors sort by (kind, value). The one exception is Attachments (LWW by
// name), which sorts by name and comes back nil when empty — the field marshals
// omitempty, so attachment-less snapshots keep their pre-attachment bytes.
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
	fold, err := dispatch(firstOp(ordered))
	if err != nil {
		return nil, err
	}
	return fold(ordered, tolerant)
}

// Strict folds like Fold but refuses history it cannot fully apply: an op that
// does not apply to the chain's entity kind fails with ErrKindMismatch instead
// of being skipped and counted. The write path validates candidate chains
// through it, so an illegal op combination is rejected before its ref moves.
func Strict(commits []model.PackCommit) (model.Snapshot, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return nil, err
	}
	fold, err := dispatch(firstOp(ordered))
	if err != nil {
		return nil, err
	}
	return fold(ordered, strict)
}

// Note linearizes the chain and folds it as a note. It fails with
// ErrKindMismatch when the chain was created as a task.
func Note(commits []model.PackCommit) (model.Note, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Note{}, err
	}
	return foldNote(ordered, tolerant)
}

// Doc linearizes the chain and folds it as a doc. It fails with
// ErrKindMismatch when the chain was created as a different kind.
func Doc(commits []model.PackCommit) (model.Doc, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Doc{}, err
	}
	return foldDoc(ordered, tolerant)
}

// Log linearizes the chain and folds it as a log. It fails with
// ErrKindMismatch when the chain was created as a different kind.
func Log(commits []model.PackCommit) (model.Log, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Log{}, err
	}
	return foldLog(ordered, tolerant)
}

// Task linearizes the chain and folds it as a task. It fails with
// ErrKindMismatch when the chain was created as a note.
func Task(commits []model.PackCommit) (model.Task, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Task{}, err
	}
	return foldTask(ordered, tolerant)
}

// Sprint linearizes the chain and folds it as a sprint. It fails with
// ErrKindMismatch when the chain was created as a different kind.
func Sprint(commits []model.PackCommit) (model.Sprint, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Sprint{}, err
	}
	return foldSprint(ordered, tolerant)
}

// Project linearizes the chain and folds it as a project. It fails with
// ErrKindMismatch when the chain was created as a different kind.
func Project(commits []model.PackCommit) (model.Project, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Project{}, err
	}
	return foldProject(ordered, tolerant)
}

// Runbook linearizes the chain and folds it as a runbook. It fails with
// ErrKindMismatch when the chain was created as a different kind.
func Runbook(commits []model.PackCommit) (model.Runbook, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Runbook{}, err
	}
	return foldRunbook(ordered, tolerant)
}

// Investigation linearizes the chain and folds it as an investigation. It fails
// with ErrKindMismatch when the chain was created as a different kind.
func Investigation(commits []model.PackCommit) (model.Investigation, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Investigation{}, err
	}
	return foldInvestigation(ordered, tolerant)
}

// Plan linearizes the chain and folds it as a plan. It fails with
// ErrKindMismatch when the chain was created as a different kind.
func Plan(commits []model.PackCommit) (model.Plan, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Plan{}, err
	}
	return foldPlan(ordered, tolerant)
}

// dispatch returns the boxing fold for an already-linearized chain whose first
// op is first, keyed by the create op's kind exactly as Fold and History need.
// It fails with ErrNoCreate when first is nil or not a create op.
func dispatch(first model.Op) (func([]model.PackCommit, mode) (model.Snapshot, error), error) {
	co, ok := first.(model.CreateOp)
	if !ok {
		if first == nil {
			return nil, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
		}
		return nil, fmt.Errorf("%w: got %s", ErrNoCreate, first.OpKind())
	}
	fold, ok := folders[co.CreateKind()]
	if !ok {
		// A codec-decoded pack can't reach here — pack.go's kind gate only
		// admits registered ops — but a hand-built PackCommit can carry a
		// CreateOp whose kind has no folder; treat it as a non-create root.
		return nil, fmt.Errorf("%w: got %s", ErrNoCreate, first.OpKind())
	}
	return fold, nil
}

func firstOp(ordered []model.PackCommit) model.Op {
	for _, c := range ordered {
		if len(c.Pack.Ops) > 0 {
			return c.Pack.Ops[0]
		}
	}
	return nil
}
