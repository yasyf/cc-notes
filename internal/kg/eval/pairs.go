package eval

import (
	"cmp"
	"regexp"
	"slices"

	"github.com/yasyf/cc-notes/model"
)

// Cluster is one labelled topic: its name and the pattern that identifies a
// member by mention. The pattern is the weak ground truth — an entity that
// says "sandsql" is about sandsql — so it must be specific enough that a
// mention is not a coincidence.
type Cluster struct {
	Name    string
	Pattern *regexp.Regexp
}

// Label assigns each corpus entity the one cluster its prose mentions. An
// entity mentioning several clusters is dropped: a pair over it is neither
// unambiguously same-topic nor cross-topic, so it can only blur the score.
//
// Only the title and body are read. Matching Text() would fold tags into the
// ground truth and hand the tag signal a score it did not earn.
func Label(corpus []Entity, clusters []Cluster) map[model.EntityID]string {
	labels := map[model.EntityID]string{}
	for _, e := range corpus {
		text := e.Title + "\n" + e.Body
		var matched []string
		for _, c := range clusters {
			if c.Pattern.MatchString(text) {
				matched = append(matched, c.Name)
			}
		}
		if len(matched) == 1 {
			labels[e.ID] = matched[0]
		}
	}
	return labels
}

// PairScore counts how a signal classified every labelled pair: a true
// positive is a same-topic pair the signal linked, a false positive a
// cross-topic pair it linked anyway.
type PairScore struct {
	Pairs int
	TP    int
	FP    int
	FN    int
	TN    int
}

// Precision is the share of linked pairs that are same-topic.
func (s PairScore) Precision() float64 { return ratio(s.TP, s.TP+s.FP) }

// Recall is the share of same-topic pairs the signal linked.
func (s PairScore) Recall() float64 { return ratio(s.TP, s.TP+s.FN) }

// F1 is the harmonic mean of precision and recall — the discrimination
// objective, which connectivity measures like component count do not capture.
func (s PairScore) F1() float64 {
	p, r := s.Precision(), s.Recall()
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

func ratio(num, den int) float64 {
	if den == 0 {
		return 0
	}
	return float64(num) / float64(den)
}

// ScorePairs classifies every pair of labelled entities: same-topic when the
// two carry one label, linked when linked reports an edge between them in
// either direction.
func ScorePairs(labels map[model.EntityID]string, linked func(a, z model.EntityID) bool) PairScore {
	ids := make([]model.EntityID, 0, len(labels))
	for id := range labels {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, z model.EntityID) int { return cmp.Compare(a, z) })

	var score PairScore
	for i, a := range ids {
		for _, z := range ids[i+1:] {
			score.Pairs++
			same, link := labels[a] == labels[z], linked(a, z) || linked(z, a)
			switch {
			case same && link:
				score.TP++
			case same:
				score.FN++
			case link:
				score.FP++
			default:
				score.TN++
			}
		}
	}
	return score
}
