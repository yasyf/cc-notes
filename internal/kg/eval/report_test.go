package eval

import (
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func reportFixture(t *testing.T) Report {
	t.Helper()
	questions := []Question{
		{ID: "q1", Query: "a", Category: "mechanism", GoldEntityIDs: []model.EntityID{"g"}, MustNotRetrieve: []model.EntityID{"x"}},
		{ID: "q2", Query: "b", Category: "history", GoldEntityIDs: []model.EntityID{"absent"}},
	}
	configs := []Config{
		{Name: "hit", Build: func(int64, Question) Retriever { return fixedRetriever{"g", "x"} }},
		{Name: "miss", Build: func(int64, Question) Retriever { return fixedRetriever{"x"} }},
	}
	report, err := Run(t.Context(), questions, configs, Options{K: 2, Threshold: 0.1, Seeds: []int64{1, 2, 3, 4, 5}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return report
}

func TestReportString(t *testing.T) {
	got := reportFixture(t).String()
	want := strings.Join([]string{
		"questions=2  k=2  threshold=0.10  seeds=[1 2 3 4 5]",
		"± is the spread across seeds only, not a confidence interval over questions",
		"",
		"overall (n=2)",
		"  retriever  ndcg@2        recall@2      mrr           leak          abstain",
		"  hit        0.500 ±0.000  0.500 ±0.000  0.500 ±0.000  0.500 ±0.000  n/a",
		"  miss       0.000 ±0.000  0.000 ±0.000  0.000 ±0.000  1.000 ±0.000  n/a",
		"",
		"category history (n=1)",
		"  retriever  ndcg@2        recall@2      mrr           leak  abstain",
		"  hit        0.000 ±0.000  0.000 ±0.000  0.000 ±0.000  n/a   n/a",
		"  miss       0.000 ±0.000  0.000 ±0.000  0.000 ±0.000  n/a   n/a",
		"",
		"category mechanism (n=1)",
		"  retriever  ndcg@2        recall@2      mrr           leak          abstain",
		"  hit        1.000 ±0.000  1.000 ±0.000  1.000 ±0.000  0.500 ±0.000  n/a",
		"  miss       0.000 ±0.000  0.000 ±0.000  0.000 ±0.000  1.000 ±0.000  n/a",
		"",
	}, "\n")
	if got != want {
		t.Errorf("Report.String() =\n%s\nwant\n%s", got, want)
	}
}

func TestReportAbstentionAndEmptyDenominators(t *testing.T) {
	questions := []Question{
		{ID: "q1", Query: "a", Category: "absent", ExpectAbstain: true},
		{ID: "q2", Query: "b", Category: "present", GoldEntityIDs: []model.EntityID{"g"}},
	}
	configs := []Config{
		{Name: "eager", Build: func(int64, Question) Retriever { return fixedRetriever{"g"} }},
		{Name: "silent", Build: func(int64, Question) Retriever { return fixedRetriever{} }},
	}
	report, err := Run(t.Context(), questions, configs, Options{K: 2, Threshold: 0.1, Seeds: []int64{1, 2, 3, 4, 5}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := report.String()
	want := strings.Join([]string{
		"questions=2  k=2  threshold=0.10  seeds=[1 2 3 4 5]",
		"± is the spread across seeds only, not a confidence interval over questions",
		"",
		"overall (n=2)",
		"  retriever  ndcg@2        recall@2      mrr           leak  abstain",
		"  eager      1.000 ±0.000  1.000 ±0.000  1.000 ±0.000  n/a   0.000 ±0.000",
		"  silent     0.000 ±0.000  0.000 ±0.000  0.000 ±0.000  n/a   1.000 ±0.000",
		"",
		"category absent (n=1)",
		"  retriever  ndcg@2  recall@2  mrr  leak  abstain",
		"  eager      n/a     n/a       n/a  n/a   0.000 ±0.000",
		"  silent     n/a     n/a       n/a  n/a   1.000 ±0.000",
		"",
		"category present (n=1)",
		"  retriever  ndcg@2        recall@2      mrr           leak  abstain",
		"  eager      1.000 ±0.000  1.000 ±0.000  1.000 ±0.000  n/a   n/a",
		"  silent     0.000 ±0.000  0.000 ±0.000  0.000 ±0.000  n/a   n/a",
		"",
	}, "\n")
	if got != want {
		t.Errorf("Report.String() =\n%s\nwant\n%s", got, want)
	}
}
