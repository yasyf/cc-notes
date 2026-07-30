package main

import (
	"errors"
	"fmt"

	"github.com/yasyf/cc-notes/internal/kg/eval"
)

// errNoHoldout is why a repository gets no selected-weight headline: one of its
// folds holds no graded question, so the only questions left to score a swept
// weight on are the ones that chose it.
var errNoHoldout = errors.New("one fold holds no graded question")

// tuning is one arm's weight selection: the sweep over the selection fold, the
// weight that won it, and the held-out questions that weight may be scored on.
type tuning struct {
	arm       string
	selection []eval.Question
	holdout   []eval.Question
	sweep     eval.Report
	weight    float64
}

// tune sweeps the graph lane's fusion weight on the selection fold and returns
// the weight that won there together with the held-out questions it may be
// scored on. It refuses when either fold holds no graded question: a weight
// scored on the questions that chose it re-confirms its own choice, which is
// how the shipped default was set in the first place.
func (c corpus) tune(session bool) (tuning, error) {
	selection, holdout := eval.Split(c.questions)
	graded, held := eval.Graded(selection), eval.Graded(holdout)
	if len(graded) == 0 || len(held) == 0 {
		return tuning{}, fmt.Errorf("%w: %d graded in the selection fold, %d held out",
			errNoHoldout, len(graded), len(held))
	}
	arms := make([]arm, len(sweepWeights))
	for i, w := range sweepWeights {
		arms[i] = c.weighted(w, session)
	}
	t := tuning{arm: armLabel(session), selection: graded, holdout: held}
	report, err := c.evaluate(arms, graded)
	if err != nil {
		return tuning{}, err
	}
	best := 0
	for i, s := range report.Summaries {
		if s.Overall.NDCG.Mean > report.Summaries[best].Overall.NDCG.Mean {
			best = i
		}
	}
	t.sweep, t.weight = report, sweepWeights[best]
	return t, nil
}

// tuned selects the graph weight on one fold and scores it on the other, for
// both the session-free arm and the session-seeded one the product runs. A
// repository that cannot fill both folds gets a refusal in place of a headline
// rather than an in-sample number.
//
// Known limitation, in the pooled row rather than the per-repository ones:
// sweepWeights includes 0, so a repository whose selection fold prefers the
// graph lane off selects an arm that scores the baseline back against itself,
// contributing deltas at or near zero. The pooled sign-flip test reads those as
// ties and drops them from its null distribution, so the pooled "held-out
// headline" is a policy estimate — what selecting a weight per repository buys
// over never running the lane — and not the graph lane's own effect, which the
// fixed-arm lex+enrich -> lex+enrich+graph contrast measures without any
// selection in front of it. Read the two together: on the 2026-07-29 snapshot
// four of six repositories selected w=0 session-free and contributed exactly
// zero, and the pooled held-out row reported p=0.0430 where the fixed-arm
// contrast over every graded question in the same repositories reported
// p=0.0905.
func (c corpus) tuned(p *pool) error {
	for _, session := range []bool{false, true} {
		t, err := c.tune(session)
		if errors.Is(err, errNoHoldout) {
			fmt.Printf("--- graph fusion weight, %s: no selected-weight headline — %v\n\n", armLabel(session), err)
			continue
		}
		if err != nil {
			return err
		}
		fmt.Printf("--- graph fusion weight, %s (selection fold, %d graded questions)\n", t.arm, len(t.selection))
		fmt.Println(t.sweep)
		cmp, err := c.contrast(c.enriched(), c.weighted(t.weight, session), t.holdout)
		if err != nil {
			return err
		}
		fmt.Printf("--- selected-weight headline, %s: w=%.2f chose on %d questions, scored on the %d held out\n",
			t.arm, t.weight, len(t.selection), len(t.holdout))
		cmp.print()
		fmt.Println()
		p.add(fmt.Sprintf("held-out headline, %s", t.arm), cmp.deltas)
	}
	return nil
}
