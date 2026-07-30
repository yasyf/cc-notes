// Command kgeval runs the cc-notes retrieval evaluation harness: it loads a
// question set and the frozen corpus snapshot kgsnap wrote for each named
// repository, and prints the comparison table for BM25, the full-context
// control, and the staleness-gated retriever under both its default and strict
// withholding policies.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/internal/kg/snapshot"
	"github.com/yasyf/cc-notes/internal/kg/stale"
)

func main() {
	questions := flag.String("questions", "", "path to the question set JSON")
	root := flag.String("snapshot", "", "frozen corpus directory written by kgsnap")
	k := flag.Int("k", 10, "cutoff for the @k metrics")
	threshold := flag.Float64("threshold", 0.1, "score below which a result counts as abstention")
	seeds := flag.Int("seeds", eval.MinSeeds, "seeds per configuration")
	flag.Parse()

	if err := run(*questions, *root, *k, *threshold, *seeds); err != nil {
		fmt.Fprintln(os.Stderr, "kgeval:", err)
		os.Exit(1)
	}
}

func run(path, root string, k int, threshold float64, seedCount int) error {
	if root == "" {
		return errors.New("-snapshot is required: measure a frozen corpus written by kgsnap, never live refs")
	}
	qs, err := eval.LoadQuestions(path)
	if err != nil {
		return err
	}
	seeds := make([]int64, seedCount)
	for i := range seeds {
		seeds[i] = int64(i + 1)
	}
	ctx := context.Background()
	for _, dir := range qs.Repos() {
		snap, err := snapshot.Open(root, dir, path)
		if err != nil {
			return err
		}
		corpus, assessments := snap.Entities, snap.Assessments
		configs := []eval.Config{
			{Name: "bm25", Build: func(seed int64, _ eval.Question) eval.Retriever { return eval.NewBM25(corpus, seed) }},
			{Name: "full-context", Build: func(seed int64, _ eval.Question) eval.Retriever { return eval.NewFullContext(corpus, seed) }},
		}
		for _, v := range []struct {
			name   string
			policy stale.Retrieval
		}{
			{"gated", stale.DefaultRetrieval()},
			{"gated-demote", stale.DemoteRetrieval()},
			{"gated-decay", stale.DecayRetrieval()},
			{"gated-strict", stale.StrictRetrieval()},
		} {
			ranker := stale.NewRanker(corpus, assessments, v.policy)
			configs = append(configs, eval.Config{
				Name:  v.name,
				Build: func(seed int64, _ eval.Question) eval.Retriever { return ranker.Wrap(eval.NewBM25(corpus, seed), seed) },
			})
		}
		report, err := eval.Run(ctx, qs.ForRepo(dir), configs, eval.Options{K: k, Threshold: threshold, Seeds: seeds})
		if err != nil {
			return err
		}
		fmt.Printf("=== %s: %d entities (snapshot captured %s)\n", dir, len(corpus), snap.Manifest.CapturedAt.Format(time.RFC3339))
		fmt.Println(report)
	}
	return nil
}
