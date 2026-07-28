package rank

import (
	"cmp"
	"slices"

	"github.com/yasyf/cc-notes/model"
)

// lane is one retrieval lane's scores over the candidate set.
type lane struct {
	name   string
	weight float64
	score  map[model.EntityID]float64
}

// fuse combines the lanes by weighted reciprocal-rank fusion:
// sum over lanes of weight/(k + rank). Ranks are competition ranks, so every
// candidate a lane did not score shares the tail rank and contributes the same
// constant — which is what lets one lane degrade to nothing without reordering
// the other.
func fuse(lanes []lane, candidates []model.EntityID, k float64) map[model.EntityID]float64 {
	fused := make(map[model.EntityID]float64, len(candidates))
	for _, l := range lanes {
		for id, rank := range ranks(l.score, candidates) {
			fused[id] += l.weight / (k + float64(rank))
		}
	}
	return fused
}

// ranks assigns every candidate its 1-based competition rank under score: ties
// share the best rank of the tie, and an unscored candidate ranks as a zero.
func ranks(score map[model.EntityID]float64, candidates []model.EntityID) map[model.EntityID]int {
	ordered := slices.Clone(candidates)
	slices.SortFunc(ordered, func(a, b model.EntityID) int {
		if c := cmp.Compare(score[b], score[a]); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	out := make(map[model.EntityID]int, len(ordered))
	rank := 0
	var prev float64
	for i, id := range ordered {
		if i == 0 || score[id] != prev {
			rank, prev = i+1, score[id]
		}
		out[id] = rank
	}
	return out
}
