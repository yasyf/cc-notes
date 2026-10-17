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
	"math"

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
	// ErrExtendCheckpoint reports an Extend by a checkpoint commit, which can
	// reseed the fold in a way only the whole chain decides.
	ErrExtendCheckpoint = errors.New("checkpoint commit cannot extend a fold")
)

var folders = map[model.Kind]kindFold{
	model.KindNote:          kindFoldOf[model.Note](newNoteFolder),
	model.KindDoc:           kindFoldOf[model.Doc](newDocFolder),
	model.KindLog:           kindFoldOf[model.Log](newLogFolder),
	model.KindTask:          kindFoldOf[model.Task](newTaskFolder),
	model.KindSprint:        kindFoldOf[model.Sprint](newSprintFolder),
	model.KindProject:       kindFoldOf[model.Project](newProjectFolder),
	model.KindRunbook:       kindFoldOf[model.Runbook](newRunbookFolder),
	model.KindInvestigation: kindFoldOf[model.Investigation](newInvestigationFolder),
	model.KindPlan:          kindFoldOf[model.Plan](newPlanFolder),
	model.KindAnswer:        kindFoldOf[model.Answer](newAnswerFolder),
	model.KindLedger:        kindFoldOf[model.Ledger](newLedgerFolder),
}

type kindFold struct {
	fold   func([]model.PackCommit, mode) (model.Snapshot, error)
	walk   func([]model.PackCommit, func(Step) error) error
	extend func(model.Snapshot, model.PackCommit) (model.Snapshot, error)
}

func kindFoldOf[T model.Snapshot, F folder[T]](newFolder func() F) kindFold {
	return kindFold{
		fold: func(ordered []model.PackCommit, m mode) (model.Snapshot, error) {
			return run[T](ordered, newFolder(), m)
		},
		walk: func(ordered []model.PackCommit, fn func(Step) error) error {
			return walk(ordered, func() folder[T] { return newFolder() }, fn)
		},
		extend: func(base model.Snapshot, c model.PackCommit) (model.Snapshot, error) {
			return extend[T](base, c, newFolder())
		},
	}
}

// Fold linearizes the chain and replays its operation packs into a snapshot,
// dispatching on the create op: a create_note chain folds to model.Note, a
// create_doc chain to model.Doc, a create_log chain to model.Log, a create_task
// chain to model.Task, a create_sprint chain to model.Sprint, a create_project
// chain to model.Project, a create_runbook chain to model.Runbook, a
// create_investigation chain to model.Investigation, a create_plan chain to
// model.Plan, and a create_answer chain to model.Answer. Set-valued snapshot fields come back as non-nil sorted slices;
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
	kf, err := dispatch(firstOp(ordered))
	if err != nil {
		return nil, err
	}
	return kf.fold(ordered, tolerant)
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
	kf, err := dispatch(firstOp(ordered))
	if err != nil {
		return nil, err
	}
	return kf.fold(ordered, strict)
}

// Extend folds commit, the sole child of base's head, onto base under Strict's
// rules. When base is Strict's fold of a chain and commit's lamport exceeds its
// ExtendFloor, the result equals Strict over the chain plus commit. Refusals:
// ErrMissingParent (not a sole child), ErrExtendCheckpoint, ErrKindMismatch
// (base skipped ops), or the error Strict returns for commit's ops.
func Extend(base model.Snapshot, commit model.PackCommit) (model.Snapshot, error) {
	meta := base.Meta()
	if len(commit.Parents) != 1 || commit.Parents[0] != meta.Head {
		return nil, fmt.Errorf("%w: %s does not extend head %s", ErrMissingParent, commit.SHA, meta.Head)
	}
	if _, ok := checkpointOf(commit); ok {
		return nil, fmt.Errorf("%w: %s", ErrExtendCheckpoint, commit.SHA)
	}
	if meta.SkippedOps > 0 {
		return nil, fmt.Errorf("%w: base %s skipped %d ops", ErrKindMismatch, meta.Head, meta.SkippedOps)
	}
	return folders[meta.Kind].extend(base, commit)
}

// ExtendFloor returns the lamport a sole child of commits' head must exceed for
// Extend over Strict's fold of commits to equal Strict over commits plus that
// child: the greatest CoversLamport of any checkpoint, or MaxUint64 when a
// checkpoint's State is not the kind the chain was created as.
func ExtendFloor(commits []model.PackCommit) model.Lamport {
	var created model.Kind
	for _, c := range commits {
		if len(c.Parents) == 0 && len(c.Pack.Ops) > 0 {
			if co, ok := c.Pack.Ops[0].(model.CreateOp); ok {
				created = co.CreateKind()
			}
		}
	}
	var floor model.Lamport
	for _, c := range commits {
		cp, ok := checkpointOf(c)
		if !ok {
			continue
		}
		if cp.State.Meta().Kind != created {
			return math.MaxUint64
		}
		floor = max(floor, cp.CoversLamport)
	}
	return floor
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

// Answer linearizes the chain and folds it as an answer. It fails with
// ErrKindMismatch when the chain was created as a different kind.
func Answer(commits []model.PackCommit) (model.Answer, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Answer{}, err
	}
	return foldAnswer(ordered, tolerant)
}

func dispatch(first model.Op) (kindFold, error) {
	co, ok := first.(model.CreateOp)
	if !ok {
		if first == nil {
			return kindFold{}, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
		}
		return kindFold{}, fmt.Errorf("%w: got %s", ErrNoCreate, first.OpKind())
	}
	kf, ok := folders[co.CreateKind()]
	if !ok {
		// A codec-decoded pack can't reach here — pack.go's kind gate only
		// admits registered ops — but a hand-built PackCommit can carry a
		// CreateOp whose kind has no folder; treat it as a non-create root.
		return kindFold{}, fmt.Errorf("%w: got %s", ErrNoCreate, first.OpKind())
	}
	return kf, nil
}

func firstOp(ordered []model.PackCommit) model.Op {
	for _, c := range ordered {
		if len(c.Pack.Ops) > 0 {
			return c.Pack.Ops[0]
		}
	}
	return nil
}

// Ledger linearizes the chain and folds it as a ledger. It fails with
// ErrKindMismatch when the chain was created as a different kind.
func Ledger(commits []model.PackCommit) (model.Ledger, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return model.Ledger{}, err
	}
	return foldLedger(ordered, tolerant)
}
