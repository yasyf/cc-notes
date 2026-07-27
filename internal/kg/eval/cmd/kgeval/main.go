// Command kgeval runs the cc-notes retrieval evaluation harness: it loads a
// question set, folds each named repository's cc-notes corpus, and prints the
// baseline comparison table for BM25 and the full-context control.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/notes"
)

func main() {
	questions := flag.String("questions", "", "path to the question set JSON")
	k := flag.Int("k", 10, "cutoff for the @k metrics")
	threshold := flag.Float64("threshold", 0.1, "score below which a result counts as abstention")
	seeds := flag.Int("seeds", eval.MinSeeds, "seeds per configuration")
	flag.Parse()

	if err := run(*questions, *k, *threshold, *seeds); err != nil {
		fmt.Fprintln(os.Stderr, "kgeval:", err)
		os.Exit(1)
	}
}

func run(path string, k int, threshold float64, seedCount int) error {
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
		client, err := notes.Open(dir)
		if err != nil {
			return fmt.Errorf("open %s: %w", dir, err)
		}
		corpus, err := eval.LoadCorpus(ctx, client)
		if err != nil {
			return fmt.Errorf("load corpus %s: %w", dir, err)
		}
		report, err := eval.Run(ctx, qs.ForRepo(dir), []eval.Config{
			{Name: "bm25", Build: func(seed int64) eval.Retriever { return eval.NewBM25(corpus, seed) }},
			{Name: "full-context", Build: func(seed int64) eval.Retriever { return eval.NewFullContext(corpus, seed) }},
		}, eval.Options{K: k, Threshold: threshold, Seeds: seeds})
		if err != nil {
			return err
		}
		fmt.Printf("=== %s: %d entities\n", dir, len(corpus))
		fmt.Println(report)
	}
	return nil
}
