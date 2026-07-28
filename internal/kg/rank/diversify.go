package rank

import (
	"cmp"
	"slices"

	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/model"
)

// diversify reorders the results by maximal marginal relevance: at each step it
// takes the candidate maximizing lambda*score - (1-lambda)*maxSimilarity to
// what is already selected. The returned score is that objective shifted by
// (1-lambda) onto [0,1], which is affine and so preserves the selection order
// under a descending sort.
func diversify(results []eval.Result, tokens map[model.EntityID]map[string]struct{}, lambda float64, k int) []eval.Result {
	pool := slices.Clone(results)
	slices.SortFunc(pool, func(a, b eval.Result) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	out := make([]eval.Result, 0, min(k, len(pool)))
	peak := make([]float64, len(pool))
	for len(out) < cap(out) {
		best := -1
		var bestScore float64
		for i, r := range pool {
			if r.ID == "" {
				continue
			}
			score := lambda*r.Score - (1-lambda)*peak[i]
			if best < 0 || score > bestScore {
				best, bestScore = i, score
			}
		}
		chosen := pool[best]
		chosen.Score = bestScore + (1 - lambda)
		out = append(out, chosen)
		pool[best].ID = ""
		for i, r := range pool {
			if r.ID == "" {
				continue
			}
			peak[i] = max(peak[i], jaccard(tokens[chosen.ID], tokens[r.ID]))
		}
	}
	return out
}

// jaccard is the overlap of two token sets over their union — the corpus's
// cheapest similarity, and the one the lexical lane already implies.
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	shared := 0
	for t := range a {
		if _, ok := b[t]; ok {
			shared++
		}
	}
	return float64(shared) / float64(len(a)+len(b)-shared)
}

// tokenSet is the distinct tokens of a record's text, under the corpus's own
// tokenization.
func tokenSet(text string) map[string]struct{} {
	tokens := eval.Tokenize(text)
	out := make(map[string]struct{}, len(tokens))
	for _, t := range tokens {
		out[t] = struct{}{}
	}
	return out
}
