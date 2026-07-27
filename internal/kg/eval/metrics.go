package eval

import (
	"math"
	"slices"

	"github.com/yasyf/cc-notes/model"
)

// QuestionScore is one question's metric values at one k and threshold. Graded
// and LeakChecked report whether the question names gold entities and a
// must-not-retrieve set, so NDCG/Recall/RR and Leak are defined; Abstained
// reports that no returned result scored at or above the threshold.
type QuestionScore struct {
	ID          string
	Category    string
	Axis        string
	Graded      bool
	NDCG        float64
	Recall      float64
	RR          float64
	LeakChecked bool
	Leak        float64
	Abstain     bool
	Abstained   bool
}

// ScoreQuestion evaluates one retrieval against one question, truncated to the
// top k with binary relevance: NDCG@k discounted by 1/log2(rank+1) against the
// ideal ranking of min(len(gold), k) hits, recall@k, the reciprocal rank of the
// first gold hit, the share of returned results the question forbids (the
// temporal-correctness metric), and whether every result fell below threshold.
func ScoreQuestion(q Question, results []Result, k int, threshold float64) QuestionScore {
	top := results
	if len(top) > k {
		top = top[:k]
	}
	score := QuestionScore{
		ID:          q.ID,
		Category:    q.Category,
		Axis:        q.Axis,
		Graded:      len(q.GoldEntityIDs) > 0,
		LeakChecked: len(q.MustNotRetrieve) > 0,
		Abstain:     q.ExpectAbstain,
		Abstained:   true,
	}
	for _, r := range top {
		if r.Score >= threshold {
			score.Abstained = false
			break
		}
	}
	if score.Graded {
		score.NDCG, score.Recall, score.RR = gradedMetrics(q.GoldEntityIDs, top, k)
	}
	if score.LeakChecked && len(top) > 0 {
		leaked := 0
		for _, r := range top {
			if slices.Contains(q.MustNotRetrieve, r.ID) {
				leaked++
			}
		}
		score.Leak = float64(leaked) / float64(len(top))
	}
	return score
}

func gradedMetrics(gold []model.EntityID, top []Result, k int) (ndcg, recall, rr float64) {
	dcg, hits := 0.0, 0
	for i, r := range top {
		if !slices.Contains(gold, r.ID) {
			continue
		}
		hits++
		dcg += 1 / math.Log2(float64(i)+2)
		if rr == 0 {
			rr = 1 / float64(i+1)
		}
	}
	idcg := 0.0
	for i := range min(len(gold), k) {
		idcg += 1 / math.Log2(float64(i)+2)
	}
	return dcg / idcg, float64(hits) / float64(len(gold)), rr
}

// Metrics is a metric set aggregated over questions. Graded, LeakChecked, and
// AbstainQuestions are the denominators: NDCG/Recall/MRR average over graded
// questions, LeakRate over questions carrying a must-not-retrieve set, and
// AbstentionAccuracy over questions expecting abstention; a zero denominator
// reports a zero mean.
type Metrics struct {
	Questions          int
	Graded             int
	LeakChecked        int
	AbstainQuestions   int
	NDCG               float64
	Recall             float64
	MRR                float64
	LeakRate           float64
	AbstentionAccuracy float64
}

// Aggregate averages per-question scores into one metric set.
func Aggregate(scores []QuestionScore) Metrics {
	m := Metrics{Questions: len(scores)}
	var ndcg, recall, rr, leak, abstained float64
	for _, s := range scores {
		if s.Graded {
			m.Graded++
			ndcg += s.NDCG
			recall += s.Recall
			rr += s.RR
		}
		if s.LeakChecked {
			m.LeakChecked++
			leak += s.Leak
		}
		if s.Abstain {
			m.AbstainQuestions++
			if s.Abstained {
				abstained++
			}
		}
	}
	m.NDCG = mean(ndcg, m.Graded)
	m.Recall = mean(recall, m.Graded)
	m.MRR = mean(rr, m.Graded)
	m.LeakRate = mean(leak, m.LeakChecked)
	m.AbstentionAccuracy = mean(abstained, m.AbstainQuestions)
	return m
}

func mean(total float64, n int) float64 {
	if n == 0 {
		return 0
	}
	return total / float64(n)
}
