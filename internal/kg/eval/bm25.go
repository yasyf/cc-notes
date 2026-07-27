package eval

import (
	"context"
	"math"

	"github.com/yasyf/cc-notes/model"
)

// BM25 term-saturation and length-normalization parameters.
const (
	BM25K1 = 1.5
	BM25B  = 0.75
)

// BM25Lane is the lane attribution BM25 stamps on every result.
const BM25Lane = "bm25"

// BM25 is the lexical retrieval baseline: Okapi BM25 over the corpus text with
// k1=1.5 and b=0.75. It is the bar a graph ranker must beat.
type BM25 struct {
	ids     []model.EntityID
	tf      []map[string]int
	lengths []float64
	idf     map[string]float64
	avgLen  float64
	seed    int64
}

// NewBM25 indexes the corpus. Ties in the resulting ranking are broken by seed.
func NewBM25(corpus []Entity, seed int64) *BM25 {
	b := &BM25{
		ids:     make([]model.EntityID, len(corpus)),
		tf:      make([]map[string]int, len(corpus)),
		lengths: make([]float64, len(corpus)),
		idf:     map[string]float64{},
		seed:    seed,
	}
	df := map[string]int{}
	total := 0.0
	for i, e := range corpus {
		tokens := tokenize(e.Text())
		counts := make(map[string]int, len(tokens))
		for _, t := range tokens {
			counts[t]++
		}
		b.ids[i] = e.ID
		b.tf[i] = counts
		b.lengths[i] = float64(len(tokens))
		total += float64(len(tokens))
		for t := range counts {
			df[t]++
		}
	}
	if len(corpus) > 0 {
		b.avgLen = total / float64(len(corpus))
	}
	n := float64(len(corpus))
	for t, d := range df {
		b.idf[t] = math.Log(1 + (n-float64(d)+0.5)/(float64(d)+0.5))
	}
	return b
}

// Retrieve scores every entity that shares a term with the query and returns
// the top k. Entities sharing no term score zero and are dropped, so a query
// with no lexical overlap retrieves nothing.
func (b *BM25) Retrieve(_ context.Context, query string, k int) ([]Result, error) {
	terms := tokenize(query)
	scores := make([]float64, len(b.ids))
	for _, t := range terms {
		idf, ok := b.idf[t]
		if !ok {
			continue
		}
		for i, counts := range b.tf {
			f := float64(counts[t])
			if f == 0 {
				continue
			}
			norm := BM25K1 * (1 - BM25B + BM25B*b.lengths[i]/b.avgLen)
			scores[i] += idf * f * (BM25K1 + 1) / (f + norm)
		}
	}
	var out []Result
	for i, s := range scores {
		if s > 0 {
			out = append(out, Result{ID: b.ids[i], Score: s, Lane: BM25Lane})
		}
	}
	return rank(out, b.seed, k), nil
}
