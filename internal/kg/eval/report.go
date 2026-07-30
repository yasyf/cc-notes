package eval

import (
	"fmt"
	"strings"
	"text/tabwriter"
)

// String renders the report as a comparison table across retrievers, followed
// by one table per question category.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "questions=%d  k=%d  threshold=%.2f  seeds=%v\n", r.Questions, r.K, r.Threshold, r.Seeds)
	fmt.Fprint(&b, "± is the spread across seeds only, not a confidence interval over questions\n\n")
	fmt.Fprintf(&b, "overall (n=%d)\n", r.Questions)
	writeTable(&b, r.K, r.Summaries, func(s Summary) MetricStats { return s.Overall })
	for _, cat := range r.Categories {
		fmt.Fprintf(&b, "\ncategory %s (n=%d)\n", cat, r.Summaries[0].Categories[cat].Questions)
		writeTable(&b, r.K, r.Summaries, func(s Summary) MetricStats { return s.Categories[cat] })
	}
	return b.String()
}

func writeTable(b *strings.Builder, k int, summaries []Summary, pick func(Summary) MetricStats) {
	w := tabwriter.NewWriter(b, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(w, "  retriever\tndcg@%d\trecall@%d\tmrr\tleak\tabstain\n", k, k)
	for _, s := range summaries {
		m := pick(s)
		_, _ = fmt.Fprintf(w, "  %s\t%s\t%s\t%s\t%s\t%s\n", s.Name,
			cell(m.NDCG, m.Graded),
			cell(m.Recall, m.Graded),
			cell(m.MRR, m.Graded),
			cell(m.LeakRate, m.LeakChecked),
			cell(m.AbstentionAccuracy, m.AbstainQuestions))
	}
	_ = w.Flush()
}

// cell renders a stat, or "n/a" when the metric had no questions to average.
func cell(s Stat, n int) string {
	if n == 0 {
		return "n/a"
	}
	return fmt.Sprintf("%.3f ±%.3f", s.Mean, s.StdDev)
}
