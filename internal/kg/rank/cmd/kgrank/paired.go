package main

import (
	"context"
	"fmt"

	"github.com/yasyf/cc-notes/internal/kg/eval"
)

// comparison is one paired contrast on one repository: the per-question
// differences the tests run over, and the scores behind them so a reader can
// see which questions moved.
type comparison struct {
	name      string
	questions []eval.Question
	deltas    []eval.Delta
	before    map[string]float64
	after     map[string]float64
}

// paired reports each layer's per-question record over the one below it, and
// the product's session-seeded configuration against the session-free one the
// eval measured in its place. Three tests are printed for every contrast: they
// disagree by construction, and the disagreement is the point — the sign test
// throws away every tie and every magnitude, which is how a lane that moves a
// handful of questions a long way reads as no result.
func (c corpus) paired(p *pool) error {
	fmt.Println("--- paired comparison over graded questions (mean NDCG@k per question, across seeds)")
	graded := eval.Graded(c.questions)
	enriched, fused, seeded := c.enriched(), c.fused(false), c.fused(true)
	for _, stage := range [][2]arm{
		{c.gated(), enriched},
		{enriched, fused},
		{enriched, seeded},
		{fused, seeded},
	} {
		cmp, err := c.contrast(stage[0], stage[1], graded)
		if err != nil {
			return err
		}
		cmp.print()
		p.add(cmp.name, cmp.deltas)
	}
	fmt.Println()
	return nil
}

// contrast scores both arms over the same questions and pairs them per
// question, marking the ones the treatment's graph lane never ran on.
func (c corpus) contrast(base, treat arm, questions []eval.Question) (comparison, error) {
	ctx := context.Background()
	before, err := c.perQuestion(base, questions)
	if err != nil {
		return comparison{}, err
	}
	after, err := c.perQuestion(treat, questions)
	if err != nil {
		return comparison{}, err
	}
	skipped, err := treat.untreated(ctx, questions, c.options.Seeds[0])
	if err != nil {
		return comparison{}, err
	}
	deltas := make([]eval.Delta, len(questions))
	for i, q := range questions {
		deltas[i] = eval.Delta{
			Repo:      c.dir,
			Question:  q.ID,
			Value:     after[q.ID] - before[q.ID],
			Untreated: skipped[q.ID],
		}
	}
	return comparison{
		name:      base.config.Name + " -> " + treat.config.Name,
		questions: questions,
		deltas:    deltas,
		before:    before,
		after:     after,
	}, nil
}

// print renders a contrast twice: over every graded question, and over only the
// ones the treatment actually reached. The second is the effect where the lane
// fires; the first is what a reader gets if untreated questions are allowed to
// count as evidence that it does nothing.
func (cm comparison) print() {
	fmt.Printf("  %s\n", cm.name)
	fmt.Printf("    every question  %s\n", eval.Compare(cm.deltas))
	if treated := treated(cm.deltas); len(treated) != len(cm.deltas) {
		fmt.Printf("    treated only    %s\n", eval.Compare(treated))
	}
	for _, q := range cm.questions {
		d := cm.after[q.ID] - cm.before[q.ID]
		if d > -1e-9 && d < 1e-9 {
			continue
		}
		fmt.Printf("      %s %-12s %.3f -> %.3f (%+.3f)\n", q.ID, q.Category, cm.before[q.ID], cm.after[q.ID], d)
	}
}

// treated drops the questions the treatment never reached.
func treated(deltas []eval.Delta) []eval.Delta {
	out := make([]eval.Delta, 0, len(deltas))
	for _, d := range deltas {
		if !d.Untreated {
			out = append(out, d)
		}
	}
	return out
}

// perQuestion is one arm's mean NDCG@k for every question, averaged over the
// seeds.
func (c corpus) perQuestion(a arm, questions []eval.Question) (map[string]float64, error) {
	ctx := context.Background()
	out := make(map[string]float64, len(questions))
	for _, seed := range c.options.Seeds {
		for _, q := range questions {
			results, err := a.config.Build(seed, q).Retrieve(ctx, q.Query, c.options.K)
			if err != nil {
				return nil, fmt.Errorf("%s seed %d question %s: %w", a.config.Name, seed, q.ID, err)
			}
			score := eval.ScoreQuestion(q, results, c.options.K, c.options.Threshold)
			out[q.ID] += score.NDCG / float64(len(c.options.Seeds))
		}
	}
	return out, nil
}
