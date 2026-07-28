package main

import (
	"cmp"
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/yasyf/cc-notes/internal/kg/rank"
	"github.com/yasyf/cc-notes/internal/kg/stale"
	"github.com/yasyf/cc-notes/model"
)

// signal names one query-time feature a ranker could withhold on.
type signal struct {
	name  string
	value func(lanes rank.Lanes, ideal float64) float64
}

// signals are every abstention feature the ranker can compute without a model:
// how much the graph lane found and how concentrated it was, and how strongly
// and how distinctly the lexical lane matched.
func signals() []signal {
	return []signal{
		{"graphCount", func(l rank.Lanes, _ float64) float64 { return float64(len(l.Graph)) }},
		{"graphTop", func(l rank.Lanes, _ float64) float64 { return best(l.Graph) }},
		{"graphEntropy", func(l rank.Lanes, _ float64) float64 { return entropy(l.Graph) }},
		{"lexAbsolute", func(l rank.Lanes, ideal float64) float64 {
			if ideal == 0 {
				return 0
			}
			return best(l.Lexical) / ideal
		}},
		{"lexMargin", func(l rank.Lanes, _ float64) float64 { return margin(l.Lexical) }},
		{"laneAgreement", func(l rank.Lanes, _ float64) float64 {
			return overlap(top(l.Lexical, 5), top(l.Graph, 5))
		}},
	}
}

// abstention scores every candidate withholding signal by the AUC with which it
// separates the questions the corpus cannot answer from the ones it can, and
// prints the operating points a threshold on the best one would buy.
func (c corpus) abstention() error {
	opts := rank.DefaultOptions()
	opts.Seed, opts.Lambda = c.options.Seeds[0], 1
	r := rank.New(c.entities, c.graph, c.assess, opts)
	cal := stale.NewCalibration(c.entities)

	features := signals()
	values := make([][]float64, len(features))
	var abstain []bool
	for _, q := range c.questions {
		lanes, err := r.Lanes(context.Background(), q.Query)
		if err != nil {
			return err
		}
		ideal := cal.Ideal(q.Query)
		for i, f := range features {
			values[i] = append(values[i], f.value(lanes, ideal))
		}
		abstain = append(abstain, q.ExpectAbstain)
	}

	fmt.Printf("--- abstention signals (%d unanswerable of %d questions)\n", count(abstain, true), len(abstain))
	best, bestAUC := -1, 0.5
	for i, f := range features {
		a := auc(values[i], abstain)
		fmt.Printf("  %-14s AUC=%.3f (inverted %.3f) SE=%.3f\n", f.name, a, 1-a, aucStdErr(a, abstain))
		if 1-a > bestAUC {
			best, bestAUC = i, 1-a
		}
	}
	if best < 0 {
		fmt.Println("  no signal beats chance in either direction")
		return nil
	}
	fmt.Printf("\n  withhold when %s is at or below a threshold:\n", features[best].name)
	for _, cut := range thresholds(values[best]) {
		caught, lost := 0, 0
		for i, v := range values[best] {
			if v > cut {
				continue
			}
			if abstain[i] {
				caught++
			} else {
				lost++
			}
		}
		fmt.Printf("    <=%.4f: %d/%d unanswerable caught, %d/%d answerable withheld\n",
			cut, caught, count(abstain, true), lost, count(abstain, false))
	}
	fmt.Println()
	return nil
}

// thresholds are the first few distinct cuts of a sample, ascending — the only
// ones that separate anything.
func thresholds(values []float64) []float64 {
	distinct := slices.Compact(slices.Sorted(slices.Values(values)))
	return distinct[:min(5, len(distinct))]
}

func best(scores map[model.EntityID]float64) float64 {
	out := 0.0
	for _, v := range scores {
		out = max(out, v)
	}
	return out
}

// margin is the top hit's lead over the runner-up, as a share of the top score.
func margin(scores map[model.EntityID]float64) float64 {
	sorted := slices.SortedFunc(values(scores), func(a, b float64) int { return cmp.Compare(b, a) })
	if len(sorted) < 2 || sorted[0] == 0 {
		return 0
	}
	return (sorted[0] - sorted[1]) / sorted[0]
}

// entropy is the Shannon entropy of a lane's scores, normalized by the log of
// its support so lanes of different sizes compare.
func entropy(scores map[model.EntityID]float64) float64 {
	if len(scores) < 2 {
		return 0
	}
	total := 0.0
	for _, v := range scores {
		total += v
	}
	if total == 0 {
		return 0
	}
	h := 0.0
	for _, v := range scores {
		if p := v / total; p > 0 {
			h -= p * math.Log(p)
		}
	}
	return h / math.Log(float64(len(scores)))
}

func top(scores map[model.EntityID]float64, n int) []model.EntityID {
	ids := make([]model.EntityID, 0, len(scores))
	for id := range scores {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b model.EntityID) int {
		if c := cmp.Compare(scores[b], scores[a]); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	return ids[:min(n, len(ids))]
}

func overlap(a, b []model.EntityID) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	shared := 0
	for _, id := range a {
		if slices.Contains(b, id) {
			shared++
		}
	}
	return float64(shared) / float64(max(len(a), len(b)))
}

func values(scores map[model.EntityID]float64) func(func(float64) bool) {
	return func(yield func(float64) bool) {
		for _, v := range scores {
			if !yield(v) {
				return
			}
		}
	}
}

// auc is the probability that a random unanswerable question scores above a
// random answerable one, ties counting a half.
func auc(scores []float64, positive []bool) float64 {
	var pos, neg []float64
	for i, v := range scores {
		if positive[i] {
			pos = append(pos, v)
		} else {
			neg = append(neg, v)
		}
	}
	if len(pos) == 0 || len(neg) == 0 {
		return 0.5
	}
	above := 0.0
	for _, p := range pos {
		for _, n := range neg {
			switch {
			case p > n:
				above++
			case p == n:
				above += 0.5
			}
		}
	}
	return above / float64(len(pos)*len(neg))
}

// aucStdErr is the Hanley-McNeil standard error, which is what says whether an
// AUC over a handful of positives is a signal or a coincidence.
func aucStdErr(a float64, positive []bool) float64 {
	p, n := float64(count(positive, true)), float64(count(positive, false))
	if p == 0 || n == 0 {
		return 0
	}
	q1, q2 := a/(2-a), 2*a*a/(1+a)
	return math.Sqrt(math.Max(0, (a*(1-a)+(p-1)*(q1-a*a)+(n-1)*(q2-a*a))/(p*n)))
}

func count(flags []bool, want bool) int {
	n := 0
	for _, f := range flags {
		if f == want {
			n++
		}
	}
	return n
}
