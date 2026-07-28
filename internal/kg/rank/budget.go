package rank

import (
	"sort"

	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/model"
)

// bytesPerToken is the estimator the budget fill counts records in. It is the
// usual English-prose ratio, and the injection surface only needs the cut to
// land in the right place, not the exact token count.
const bytesPerToken = 4

// EstimateTokens is a record text's cost against an injection budget.
func EstimateTokens(text string) int { return (len(text) + bytesPerToken - 1) / bytesPerToken }

// fit returns how many of the leading results fit the budget, by binary search
// over the prefix costs — which are monotone, so the first prefix to overflow
// bounds every longer one.
func fit(ranked []eval.Result, cost map[model.EntityID]int, budget int) int {
	prefix := make([]int, len(ranked)+1)
	for i, r := range ranked {
		prefix[i+1] = prefix[i] + cost[r.ID]
	}
	return sort.Search(len(ranked), func(i int) bool { return prefix[i+1] > budget })
}
