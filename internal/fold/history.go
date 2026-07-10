package fold

import (
	"fmt"

	"github.com/yasyf/cc-notes/model"
)

// Step is one commit in an entity's linearized history paired with the folded
// snapshot of every op up to and including that commit. Snapshot's concrete
// type is the entity kind; it equals what Fold returns for the chain truncated
// at this commit. Steps come in linearization order (lamport → author time →
// sha), the same total order Fold replays.
type Step struct {
	Commit   model.PackCommit
	Snapshot model.Snapshot
}

// History linearizes the chain once, then folds each successive prefix into a
// snapshot, returning one Step per commit in linearization order: the audit
// trail of an entity. Step k holds the snapshot through the first k+1 commits,
// so a caller diffs Step k-1 against Step k to see exactly what commit k
// changed. Checkpoint commits stay in the trail as state-neutral steps (their
// snapshot equals the prior step's), so a compacted history reports the same
// trail as the uncompacted one.
//
// History linearizes the full chain a single time and folds prefixes of that
// order with the kind's folder directly; it must not re-linearize a prefix,
// because a prefix sitting mid-fork has more than one head and Linearize
// rejects it. A prefix of a topological order is downward-closed, so every
// folder sees a valid sub-DAG rooted at the create commit.
func History(commits []model.PackCommit) ([]Step, error) {
	ordered, err := Linearize(commits)
	if err != nil {
		return nil, err
	}
	foldPrefix, err := prefixFolder(firstOp(ordered))
	if err != nil {
		return nil, err
	}
	steps := make([]Step, len(ordered))
	for k := range ordered {
		snapshot, err := foldPrefix(ordered[:k+1])
		if err != nil {
			return nil, err
		}
		steps[k] = Step{Commit: ordered[k], Snapshot: snapshot}
	}
	return steps, nil
}

// prefixFolder returns the folder for an already-linearized chain whose first
// op is first, dispatching on the create kind exactly as Fold does. The
// returned function boxes the concrete snapshot into model.Snapshot.
func prefixFolder(first model.Op) (func([]model.PackCommit) (model.Snapshot, error), error) {
	switch first.(type) {
	case model.CreateNote:
		return func(o []model.PackCommit) (model.Snapshot, error) { return foldNote(o) }, nil
	case model.CreateDoc:
		return func(o []model.PackCommit) (model.Snapshot, error) { return foldDoc(o) }, nil
	case model.CreateLog:
		return func(o []model.PackCommit) (model.Snapshot, error) { return foldLog(o) }, nil
	case model.CreateTask:
		return func(o []model.PackCommit) (model.Snapshot, error) { return foldTask(o) }, nil
	case model.CreateSprint:
		return func(o []model.PackCommit) (model.Snapshot, error) { return foldSprint(o) }, nil
	case model.CreateProject:
		return func(o []model.PackCommit) (model.Snapshot, error) { return foldProject(o) }, nil
	case model.CreateRunbook:
		return func(o []model.PackCommit) (model.Snapshot, error) { return foldRunbook(o) }, nil
	case nil:
		return nil, fmt.Errorf("%w: chain has no ops", ErrNoCreate)
	default:
		return nil, fmt.Errorf("%w: got %s", ErrNoCreate, first.OpKind())
	}
}
