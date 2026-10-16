package fold

import "github.com/yasyf/cc-notes/model"

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
	kf, err := dispatch(firstOp(ordered))
	if err != nil {
		return nil, err
	}
	steps := make([]Step, len(ordered))
	for k := range ordered {
		snapshot, err := kf.fold(ordered[:k+1], tolerant)
		if err != nil {
			return nil, err
		}
		steps[k] = Step{Commit: ordered[k], Snapshot: snapshot}
	}
	return steps, nil
}

// Walk calls fn with each Step History would return, in the same order,
// folding incrementally: a commit that keeps its prefix's seed checkpoint
// advances the previous step's fold by one commit, and one that changes the
// seed refolds its prefix under the new one. A Snapshot shares state with the
// next step's fold, so it is valid only until fn returns. fn's error stops the
// walk and is returned.
func Walk(commits []model.PackCommit, fn func(Step) error) error {
	ordered, err := Linearize(commits)
	if err != nil {
		return err
	}
	kf, err := dispatch(firstOp(ordered))
	if err != nil {
		return err
	}
	return kf.walk(ordered, fn)
}
