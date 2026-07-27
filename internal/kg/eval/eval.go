// Package eval is the retrieval evaluation harness for the cc-notes knowledge
// graph. It loads a question set, runs Retriever implementations over a
// repository's cc-notes corpus across several seeds, and reports NDCG@k,
// recall@k, MRR, superseded-leak rate, and abstention accuracy with mean and
// standard deviation plus per-category breakdowns.
//
// Two baselines ship with the harness and define the bar a graph ranker must
// beat: BM25 over the same corpus, and FullContext, the no-retrieval control
// that returns every entity.
package eval

import (
	"context"

	"github.com/yasyf/cc-notes/model"
)

// Result is one ranked hit: the entity a retriever surfaced, the score it
// assigned, and the lane that produced it — a free-form attribution string
// ("bm25", "graph:neighbour") a multi-lane ranker uses to explain the hit.
type Result struct {
	ID    model.EntityID
	Score float64
	Lane  string
}

// Retriever ranks corpus entities against a query. Retrieve returns at most k
// results ordered by descending Score, and returns none when it abstains.
type Retriever interface {
	Retrieve(ctx context.Context, query string, k int) ([]Result, error)
}
