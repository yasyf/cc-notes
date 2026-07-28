package rank

import "github.com/yasyf/cc-notes/model"

// normalize scales the candidates against the best of them, so the best
// available record scores 1. Fused reciprocal-rank scores carry no absolute
// meaning, and every admitted candidate holds some of one — min-max would
// instead zero the last of them and drop a record that is merely last.
func normalize(values map[model.EntityID]float64, candidates []model.EntityID) map[model.EntityID]float64 {
	out := make(map[model.EntityID]float64, len(candidates))
	hi := 0.0
	for _, id := range candidates {
		hi = max(hi, values[id])
	}
	if hi == 0 {
		return out
	}
	for _, id := range candidates {
		out[id] = values[id] / hi
	}
	return out
}
