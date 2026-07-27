package eval

import (
	"math"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

// Hand-derived discount terms: rank 2 contributes 1/log2(3), rank 3 contributes
// 1/log2(4).
const (
	discount2 = 0.63092975357145743
	discount3 = 0.5
)

func hits(ids ...model.EntityID) []Result {
	out := make([]Result, len(ids))
	for i, id := range ids {
		out[i] = Result{ID: id, Score: 1, Lane: "stub"}
	}
	return out
}

func TestScoreQuestion(t *testing.T) {
	cases := []struct {
		name      string
		question  Question
		results   []Result
		k         int
		threshold float64
		want      QuestionScore
	}{
		{
			name:     "single gold at rank one",
			question: Question{ID: "q", Category: "c", GoldEntityIDs: []model.EntityID{"g"}},
			results:  hits("g", "x", "y"),
			k:        3,
			want:     QuestionScore{ID: "q", Category: "c", Graded: true, NDCG: 1, Recall: 1, RR: 1},
		},
		{
			name:     "single gold at rank two",
			question: Question{ID: "q", GoldEntityIDs: []model.EntityID{"g"}},
			results:  hits("x", "g", "y"),
			k:        3,
			want:     QuestionScore{ID: "q", Graded: true, NDCG: discount2, Recall: 1, RR: 0.5},
		},
		{
			name:     "two gold at ranks one and three",
			question: Question{ID: "q", GoldEntityIDs: []model.EntityID{"g1", "g2"}},
			results:  hits("g1", "x", "g2"),
			k:        3,
			want: QuestionScore{
				ID: "q", Graded: true,
				NDCG:   (1 + discount3) / (1 + discount2),
				Recall: 1, RR: 1,
			},
		},
		{
			name:     "gold below the cutoff",
			question: Question{ID: "q", GoldEntityIDs: []model.EntityID{"g"}},
			results:  hits("x", "y", "g"),
			k:        2,
			want:     QuestionScore{ID: "q", Graded: true},
		},
		{
			name:     "half the results are forbidden",
			question: Question{ID: "q", GoldEntityIDs: []model.EntityID{"g"}, MustNotRetrieve: []model.EntityID{"s1", "s2"}},
			results:  hits("g", "s1", "x", "s2"),
			k:        4,
			want: QuestionScore{
				ID: "q", Graded: true, NDCG: 1, Recall: 1, RR: 1,
				LeakChecked: true, Leak: 0.5,
			},
		},
		{
			name:     "forbidden set with no results does not leak",
			question: Question{ID: "q", GoldEntityIDs: []model.EntityID{"g"}, MustNotRetrieve: []model.EntityID{"s1"}},
			results:  nil,
			k:        4,
			want:     QuestionScore{ID: "q", Graded: true, LeakChecked: true, Abstained: true},
		},
		{
			name:      "abstains when every result is below threshold",
			question:  Question{ID: "q", ExpectAbstain: true},
			results:   []Result{{ID: "x", Score: 0.05}},
			k:         3,
			threshold: 0.1,
			want:      QuestionScore{ID: "q", Abstain: true, Abstained: true},
		},
		{
			name:      "a result at the threshold is not abstention",
			question:  Question{ID: "q", ExpectAbstain: true},
			results:   []Result{{ID: "x", Score: 0.1}},
			k:         3,
			threshold: 0.1,
			want:      QuestionScore{ID: "q", Abstain: true},
		},
		{
			name:      "an above-threshold result past the cutoff does not count",
			question:  Question{ID: "q", ExpectAbstain: true},
			results:   []Result{{ID: "x", Score: 0.05}, {ID: "y", Score: 0.9}},
			k:         1,
			threshold: 0.1,
			want:      QuestionScore{ID: "q", Abstain: true, Abstained: true},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ScoreQuestion(tc.question, tc.results, tc.k, tc.threshold)
			if got.ID != tc.want.ID || got.Category != tc.want.Category || got.Axis != tc.want.Axis {
				t.Errorf("identity = %+v, want %+v", got, tc.want)
			}
			if got.Graded != tc.want.Graded || got.LeakChecked != tc.want.LeakChecked ||
				got.Abstain != tc.want.Abstain || got.Abstained != tc.want.Abstained {
				t.Errorf("flags = %+v, want %+v", got, tc.want)
			}
			for _, f := range []struct {
				name      string
				got, want float64
			}{
				{"NDCG", got.NDCG, tc.want.NDCG},
				{"Recall", got.Recall, tc.want.Recall},
				{"RR", got.RR, tc.want.RR},
				{"Leak", got.Leak, tc.want.Leak},
			} {
				if math.Abs(f.got-f.want) > 1e-12 {
					t.Errorf("%s = %.17f, want %.17f", f.name, f.got, f.want)
				}
			}
		})
	}
}

func TestAggregate(t *testing.T) {
	scores := []QuestionScore{
		{ID: "a", Graded: true, NDCG: 1, Recall: 1, RR: 1, LeakChecked: true, Leak: 0.25},
		{ID: "b", Graded: true, NDCG: 0.5, Recall: 0.5, RR: 0.5},
		{ID: "c", Abstain: true, Abstained: true, LeakChecked: true, Leak: 0.75},
		{ID: "d", Abstain: true},
	}
	got := Aggregate(scores)
	want := Metrics{
		Questions: 4, Graded: 2, LeakChecked: 2, AbstainQuestions: 2,
		NDCG: 0.75, Recall: 0.75, MRR: 0.75, LeakRate: 0.5, AbstentionAccuracy: 0.5,
	}
	if got != want {
		t.Errorf("Aggregate = %+v, want %+v", got, want)
	}
}

func TestAggregateEmptyDenominators(t *testing.T) {
	got := Aggregate([]QuestionScore{{ID: "a"}})
	want := Metrics{Questions: 1}
	if got != want {
		t.Errorf("Aggregate = %+v, want %+v", got, want)
	}
}
