package main

import (
	"fmt"
	"os"
	"slices"
	"text/tabwriter"

	"github.com/yasyf/cc-notes/internal/kg/eval"
)

// pool accumulates the paired deltas every repository reported, keyed by the
// contrast that produced them, so the cross-repository view is assembled from
// exactly the numbers the per-repository sections printed.
type pool struct {
	order  []string
	byName map[string][]eval.Delta
}

func newPool() *pool {
	return &pool{byName: map[string][]eval.Delta{}}
}

func (p *pool) add(contrast string, deltas []eval.Delta) {
	if _, seen := p.byName[contrast]; !seen {
		p.order = append(p.order, contrast)
	}
	p.byName[contrast] = append(p.byName[contrast], deltas...)
}

// report prints every contrast pooled across repositories under two weightings,
// because neither alone is honest here. Question-weighted counts every question
// once, which is the powerful reading and the one a 35-question repository
// dominates by construction. Repo-balanced makes each repository's own mean the
// unit, which is the reading a one-question repository can swing. Both are
// exact under the same per-question sign flips, so the pair differing is itself
// the finding: an effect that survives only question-weighted is one
// repository's, and one that survives only repo-balanced rests on the small
// repositories.
func (p *pool) report() {
	fmt.Println("=== pooled across repositories")
	for _, name := range p.order {
		deltas := p.byName[name]
		fmt.Printf("\n--- %s\n", name)
		writeRepoTable(deltas)
		fmt.Printf("  question-weighted  %s\n", eval.Compare(deltas))
		mean, significance, method := balanced(deltas)
		fmt.Printf("  repo-balanced      repos=%d  mean %+.4f  sign-flip p=%.4f (%s)\n",
			len(repos(deltas)), mean, significance, method)
	}
	fmt.Println()
}

// balanced is the mean of the per-repository mean differences, and its exact
// two-sided p-value under per-question sign flips. Weighting each question by
// one over its repository's size times the repository count makes the weighted
// sum the balanced mean, so one permutation covers both.
func balanced(deltas []eval.Delta) (float64, float64, eval.Method) {
	counts := map[string]int{}
	for _, d := range deltas {
		counts[d.Repo]++
	}
	values := make([]float64, len(deltas))
	weights := make([]float64, len(deltas))
	mean := 0.0
	for i, d := range deltas {
		values[i] = d.Value
		weights[i] = 1 / float64(len(counts)*counts[d.Repo])
		mean += values[i] * weights[i]
	}
	significance, method := eval.SignFlipWeighted(values, weights)
	return mean, significance, method
}

func repos(deltas []eval.Delta) []string {
	var out []string
	for _, d := range deltas {
		if !slices.Contains(out, d.Repo) {
			out = append(out, d.Repo)
		}
	}
	slices.Sort(out)
	return out
}

func writeRepoTable(deltas []eval.Delta) {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprint(w, "  repository\tquestions\tuntreated\tmean\n")
	for _, repo := range repos(deltas) {
		var own []eval.Delta
		for _, d := range deltas {
			if d.Repo == repo {
				own = append(own, d)
			}
		}
		summary := eval.Compare(own)
		_, _ = fmt.Fprintf(w, "  %s\t%d\t%d\t%+.4f\n", repo, summary.N, summary.Untreated, summary.Mean)
	}
	_ = w.Flush()
}
