package main

import (
	"context"
	"fmt"
	"math"

	"github.com/yasyf/cc-notes/internal/kg/eval"
)

// paired reports each stage's per-question win/loss record over the one before
// it. A mean over thirty-odd graded questions moves on one question, so the
// sign test is what says whether a difference is a result.
func (c corpus) paired() error {
	fmt.Println("--- paired comparison (mean NDCG@k per question, across seeds)")
	stages := []eval.Config{c.gated(), c.enriched(), c.fused()}
	for i := range stages[:len(stages)-1] {
		if err := c.sign(stages[i], stages[i+1]); err != nil {
			return err
		}
	}
	if err := c.sign(stages[0], stages[len(stages)-1]); err != nil {
		return err
	}
	fmt.Println()
	return nil
}

func (c corpus) sign(a, z eval.Config) error {
	before, err := c.perQuestion(a)
	if err != nil {
		return err
	}
	after, err := c.perQuestion(z)
	if err != nil {
		return err
	}
	wins, losses := 0, 0
	var moved []string
	for _, q := range c.questions {
		d := after[q.ID] - before[q.ID]
		switch {
		case d > 1e-9:
			wins++
		case d < -1e-9:
			losses++
		default:
			continue
		}
		moved = append(moved, fmt.Sprintf("      %s %-12s %.3f -> %.3f (%+.3f)", q.ID, q.Category, before[q.ID], after[q.ID], d))
	}
	fmt.Printf("  %s -> %s: %d win / %d loss / %d tie, sign-test p=%.4f\n",
		a.Name, z.Name, wins, losses, len(before)-wins-losses, signTest(wins, losses))
	for _, line := range moved {
		fmt.Println(line)
	}
	return nil
}

// perQuestion is one configuration's mean NDCG@k for every graded question.
func (c corpus) perQuestion(cfg eval.Config) (map[string]float64, error) {
	ctx := context.Background()
	out := map[string]float64{}
	for _, seed := range c.options.Seeds {
		r := cfg.Build(seed)
		for _, q := range c.questions {
			if len(q.GoldEntityIDs) == 0 {
				continue
			}
			results, err := r.Retrieve(ctx, q.Query, c.options.K)
			if err != nil {
				return nil, err
			}
			score := eval.ScoreQuestion(q, results, c.options.K, c.options.Threshold)
			out[q.ID] += score.NDCG / float64(len(c.options.Seeds))
		}
	}
	return out, nil
}

// signTest is the exact two-sided binomial p-value for wins against losses
// under the null that either is equally likely.
func signTest(wins, losses int) float64 {
	n := wins + losses
	if n == 0 {
		return 1
	}
	tail := 0.0
	for i := 0; i <= min(wins, losses); i++ {
		tail += choose(n, i)
	}
	return math.Min(1, 2*tail/math.Pow(2, float64(n)))
}

func choose(n, k int) float64 {
	out := 1.0
	for i := range k {
		out = out * float64(n-i) / float64(i+1)
	}
	return out
}
