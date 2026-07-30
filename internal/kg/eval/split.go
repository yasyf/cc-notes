package eval

import "hash/fnv"

// Split partitions questions into the fold a hyperparameter may be chosen on
// and the fold a chosen value may be scored on. A question's fold is a stable
// hash of its id, so it does not move as the set grows and neither fold is a
// slice of file order; both halves keep the input's relative order.
//
// The split exists because a value swept over a question set and then scored on
// that same set is an in-sample number: the sweep re-confirms the choice it
// made. A repository too small to fill both folds yields an empty one, and the
// caller has to refuse the selected-value headline rather than fall back to the
// selection fold.
func Split(questions []Question) (selection, holdout []Question) {
	for _, q := range questions {
		h := fnv.New64a()
		_, _ = h.Write([]byte(q.ID))
		if h.Sum64()%2 == 0 {
			selection = append(selection, q)
			continue
		}
		holdout = append(holdout, q)
	}
	return selection, holdout
}
