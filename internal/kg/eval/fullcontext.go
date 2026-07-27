package eval

import (
	"context"

	"github.com/yasyf/cc-notes/model"
)

// FullContextLane is the lane attribution FullContext stamps on every result.
const FullContextLane = "full-context"

// FullContextScore is the score FullContext assigns every entity: retrieving
// everything expresses no preference, so every entity ties.
const FullContextScore = 1.0

// FullContext is the no-retrieval control: it returns the whole corpus, the
// behaviour of pasting every record into the prompt. Because every entity ties,
// its top-k is an arbitrary slice of the corpus whose order the seed picks —
// the spread across seeds is exactly the order-luck a single run would hide.
type FullContext struct {
	ids  []model.EntityID
	seed int64
}

// NewFullContext builds the control over the corpus, ordering ties by seed.
func NewFullContext(corpus []Entity, seed int64) *FullContext {
	ids := make([]model.EntityID, len(corpus))
	for i, e := range corpus {
		ids[i] = e.ID
	}
	return &FullContext{ids: ids, seed: seed}
}

// Retrieve returns the whole corpus, truncated to k.
func (f *FullContext) Retrieve(_ context.Context, _ string, k int) ([]Result, error) {
	out := make([]Result, len(f.ids))
	for i, id := range f.ids {
		out[i] = Result{ID: id, Score: FullContextScore, Lane: FullContextLane}
	}
	return Rank(out, f.seed, k), nil
}
