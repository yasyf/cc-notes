package rank

import (
	"cmp"
	"slices"

	"github.com/yasyf/cc-notes/model"
)

// lane is one retrieval lane's scores over the candidate set. addressed says
// the query itself reached this lane, so what it scored is evidence about the
// query rather than about where the agent happens to be standing.
type lane struct {
	name      string
	weight    float64
	score     map[model.EntityID]float64
	addressed bool
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

// found is the candidates an addressed lane actually scored — the ranking's
// support, and empty exactly when the query reached nothing.
//
// It is what abstention keys on, because a fused score cannot: fuse hands every
// unscored candidate the same tail rank, so a record no lane ever looked at
// still carries mass, and normalize then scales whatever leads to 1. Lane
// membership is the one signal here with an absolute meaning.
func found(lanes []lane, candidates []model.EntityID) map[model.EntityID]struct{} {
	out := make(map[model.EntityID]struct{}, len(candidates))
	for _, l := range lanes {
		if !l.addressed {
			continue
		}
		for id, score := range l.score {
			if score > 0 {
				out[id] = struct{}{}
			}
		}
	}
	return out
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
