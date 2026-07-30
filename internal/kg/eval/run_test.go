package eval

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

// fixedRetriever returns the same ranking for every query, scored strictly
// descending so the order it declares is the order scored.
type fixedRetriever []model.EntityID

func (f fixedRetriever) Retrieve(_ context.Context, _ string, k int) ([]Result, error) {
	out := make([]Result, 0, len(f))
	for i, id := range f {
		out = append(out, Result{ID: id, Score: 1 / float64(i+1), Lane: "stub"})
	}
	if len(out) > k {
		out = out[:k]
	}
	return out, nil
}

// oddSeedRetriever finds the gold entity on odd seeds and nothing on even ones,
// so its per-seed metrics alternate between 1 and 0.
type oddSeedRetriever int64

func (s oddSeedRetriever) Retrieve(_ context.Context, _ string, _ int) ([]Result, error) {
	if s%2 == 0 {
		return nil, nil
	}
	return []Result{{ID: "g", Score: 1, Lane: "stub"}}, nil
}

type failingRetriever struct{}

var errRetrieve = errors.New("retrieve exploded")

func (failingRetriever) Retrieve(context.Context, string, int) ([]Result, error) {
	return nil, errRetrieve
}

func runQuestions() []Question {
	return []Question{
		{ID: "q1", Query: "a", Category: "mechanism", GoldEntityIDs: []model.EntityID{"g"}},
		{ID: "q2", Query: "b", Category: "history", GoldEntityIDs: []model.EntityID{"g"}},
	}
}

func runOpts() Options {
	return Options{K: 2, Threshold: 0.1, Seeds: []int64{1, 2, 3, 4, 5}}
}

func TestRunDeterministicRetrieverHasZeroSpread(t *testing.T) {
	cfg := Config{Name: "fixed", Build: func(int64, Question) Retriever { return fixedRetriever{"x", "g"} }}
	report, err := Run(t.Context(), runQuestions(), []Config{cfg}, runOpts())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if report.Questions != 2 || report.K != 2 {
		t.Errorf("report = %+v, want 2 questions at k=2", report)
	}
	if len(report.Summaries) != 1 || len(report.Summaries[0].Seeds) != 5 {
		t.Fatalf("want one summary over 5 seeds, got %+v", report.Summaries)
	}
	overall := report.Summaries[0].Overall
	if math.Abs(overall.NDCG.Mean-discount2) > 1e-12 {
		t.Errorf("NDCG mean = %.17f, want %.17f", overall.NDCG.Mean, discount2)
	}
	if overall.NDCG.StdDev != 0 {
		t.Errorf("NDCG stddev = %v, want exactly 0 for a deterministic retriever", overall.NDCG.StdDev)
	}
	if overall.MRR.Mean != 0.5 || overall.Recall.Mean != 1 {
		t.Errorf("MRR/Recall = %v/%v, want 0.5/1", overall.MRR.Mean, overall.Recall.Mean)
	}
	if overall.AbstainQuestions != 0 || overall.LeakChecked != 0 {
		t.Errorf("denominators = %+v, want no abstain or leak questions", overall)
	}
}

func TestRunSeedSpread(t *testing.T) {
	cfg := Config{Name: "odd", Build: func(seed int64, _ Question) Retriever { return oddSeedRetriever(seed) }}
	report, err := Run(t.Context(), runQuestions(), []Config{cfg}, runOpts())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	overall := report.Summaries[0].Overall
	if math.Abs(overall.NDCG.Mean-0.6) > 1e-12 {
		t.Errorf("NDCG mean = %.17f, want 0.6 over seeds [1 0 1 0 1]", overall.NDCG.Mean)
	}
	if want := math.Sqrt(0.3); math.Abs(overall.NDCG.StdDev-want) > 1e-12 {
		t.Errorf("NDCG stddev = %.17f, want %.17f", overall.NDCG.StdDev, want)
	}
	for _, run := range report.Summaries[0].Seeds {
		want := 0.0
		if run.Seed%2 == 1 {
			want = 1
		}
		if run.Overall.NDCG != want {
			t.Errorf("seed %d NDCG = %v, want %v", run.Seed, run.Overall.NDCG, want)
		}
	}
}

func TestRunCategoryBreakdown(t *testing.T) {
	cfg := Config{Name: "fixed", Build: func(int64, Question) Retriever { return fixedRetriever{"g"} }}
	questions := append(runQuestions(), Question{
		ID: "q3", Query: "c", Category: "history", GoldEntityIDs: []model.EntityID{"absent"},
	})
	report, err := Run(t.Context(), questions, []Config{cfg}, runOpts())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := report.Categories; len(got) != 2 || got[0] != "history" || got[1] != "mechanism" {
		t.Fatalf("Categories = %v, want [history mechanism]", got)
	}
	cats := report.Summaries[0].Categories
	if got := cats["mechanism"]; got.Questions != 1 || got.NDCG.Mean != 1 {
		t.Errorf("mechanism = %+v, want 1 question at NDCG 1", got)
	}
	if got := cats["history"]; got.Questions != 2 || got.NDCG.Mean != 0.5 {
		t.Errorf("history = %+v, want 2 questions at NDCG 0.5", got)
	}
}

// sessionRetriever finds the gold entity only when the question was asked from
// a branch, so a harness that drops the session scores it at zero.
type sessionRetriever Session

func (s sessionRetriever) Retrieve(context.Context, string, int) ([]Result, error) {
	if s.Branch == "" {
		return nil, nil
	}
	return []Result{{ID: "g", Score: 1, Lane: "stub"}}, nil
}

// TestRunBuildsEachRetrieverFromItsQuestionsSession is the harness half of the
// defect this change fixes: Run has to hand Build the question, or every
// configuration is measured from the zero session while the product runs from
// the agent's actual branch and paths.
func TestRunBuildsEachRetrieverFromItsQuestionsSession(t *testing.T) {
	questions := []Question{
		{ID: "seated", Query: "a", Category: "mechanism", Session: Session{Branch: "yasyf/pulumi"}, GoldEntityIDs: []model.EntityID{"g"}},
		{ID: "unseated", Query: "b", Category: "mechanism", GoldEntityIDs: []model.EntityID{"g"}},
	}
	seen := map[string]Session{}
	cfg := Config{Name: "session", Build: func(_ int64, q Question) Retriever {
		seen[q.ID] = q.Session
		return sessionRetriever(q.Session)
	}}
	report, err := Run(t.Context(), questions, []Config{cfg}, runOpts())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if seen["seated"].Branch != "yasyf/pulumi" || !seen["unseated"].Empty() {
		t.Fatalf("Build saw sessions %+v, want the seated question's branch and the unseated question's zero session", seen)
	}
	if got := report.Summaries[0].Overall.NDCG.Mean; got != 0.5 {
		t.Errorf("NDCG mean = %v, want 0.5: exactly the seated question should score", got)
	}
}

func TestRunRejects(t *testing.T) {
	cfg := Config{Name: "fixed", Build: func(int64, Question) Retriever { return fixedRetriever{"g"} }}
	cases := []struct {
		name      string
		questions []Question
		configs   []Config
		opts      Options
		want      error
	}{
		{"no questions", nil, []Config{cfg}, runOpts(), ErrNoQuestions},
		{"no retrievers", runQuestions(), nil, runOpts(), ErrNoRetrievers},
		{"zero k", runQuestions(), []Config{cfg}, Options{K: 0, Seeds: runOpts().Seeds}, ErrInvalidK},
		{"negative k", runQuestions(), []Config{cfg}, Options{K: -1, Seeds: runOpts().Seeds}, ErrInvalidK},
		{"four seeds", runQuestions(), []Config{cfg}, Options{K: 2, Seeds: []int64{1, 2, 3, 4}}, ErrTooFewSeeds},
		{"no seeds", runQuestions(), []Config{cfg}, Options{K: 2}, ErrTooFewSeeds},
		{
			"retriever failure", runQuestions(),
			[]Config{{Name: "boom", Build: func(int64, Question) Retriever { return failingRetriever{} }}},
			runOpts(), errRetrieve,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Run(t.Context(), tc.questions, tc.configs, tc.opts)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Run error = %v, want %v", err, tc.want)
			}
		})
	}
}
