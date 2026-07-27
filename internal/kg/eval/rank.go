package eval

import (
	"cmp"
	"hash/fnv"
	"slices"
	"strconv"

	"github.com/yasyf/cc-notes/model"
)

// tieBreak is the seed-dependent order of an entity among equally-scored peers.
// Ranking ties are arbitrary by definition, so the harness randomizes them per
// seed instead of freezing one lucky order: a retriever whose score function
// does not discriminate shows the resulting spread in its standard deviation.
func tieBreak(seed int64, id model.EntityID) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(strconv.FormatInt(seed, 10)))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(id))
	return h.Sum64()
}

// Rank sorts results by descending score, breaking ties by seed, and truncates
// to k. Every retriever scored by this harness orders through it, so the
// tie-break is identical across the configurations a run compares.
func Rank(results []Result, seed int64, k int) []Result {
	slices.SortFunc(results, func(a, b Result) int {
		if c := cmp.Compare(b.Score, a.Score); c != 0 {
			return c
		}
		return cmp.Compare(tieBreak(seed, a.ID), tieBreak(seed, b.ID))
	})
	if len(results) > k {
		results = results[:k]
	}
	return results
}
