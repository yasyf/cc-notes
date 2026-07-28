// Command kgrank measures the cc-notes knowledge-graph ranker against the
// harness baselines: the lane ablation that decides whether the personalized
// PageRank layer earns its place, the sweep over its fusion weight, the
// per-question sign tests behind each mean, the abstention signals, and the
// index and query cost.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/yasyf/cc-notes/internal/kg"
	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/internal/kg/rank"
	"github.com/yasyf/cc-notes/internal/kg/stale"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/notes"
)

func main() {
	questions := flag.String("questions", "", "path to the question set JSON")
	repo := flag.String("repo", "", "measure only this repository from the set")
	k := flag.Int("k", 10, "cutoff for the @k metrics")
	threshold := flag.Float64("threshold", 0.1, "score below which a result counts as abstention")
	seeds := flag.Int("seeds", eval.MinSeeds, "seeds per configuration")
	flag.Parse()

	if err := run(*questions, *repo, *k, *threshold, *seeds); err != nil {
		fmt.Fprintln(os.Stderr, "kgrank:", err)
		os.Exit(1)
	}
}

// corpus is one repository's evaluation inputs: the flattened records, the
// graph over them, and their staleness assessments.
type corpus struct {
	dir       string
	questions []eval.Question
	entities  []eval.Entity
	graph     *kg.Graph
	assess    []stale.Assessment
	options   eval.Options
}

func run(path, only string, k int, threshold float64, seedCount int) error {
	qs, err := eval.LoadQuestions(path)
	if err != nil {
		return err
	}
	seeds := make([]int64, seedCount)
	for i := range seeds {
		seeds[i] = int64(i + 1)
	}
	for _, dir := range qs.Repos() {
		if only != "" && dir != only {
			continue
		}
		c, err := load(dir, qs.ForRepo(dir), eval.Options{K: k, Threshold: threshold, Seeds: seeds})
		if err != nil {
			return err
		}
		if err := c.report(); err != nil {
			return err
		}
	}
	return nil
}

func load(dir string, questions []eval.Question, opts eval.Options) (corpus, error) {
	ctx := context.Background()
	client, err := notes.Open(dir)
	if err != nil {
		return corpus{}, fmt.Errorf("open %s: %w", dir, err)
	}
	entities, err := eval.LoadCorpus(ctx, client)
	if err != nil {
		return corpus{}, fmt.Errorf("load corpus %s: %w", dir, err)
	}
	s, err := store.Open(dir)
	if err != nil {
		return corpus{}, fmt.Errorf("open store %s: %w", dir, err)
	}
	start := time.Now()
	g, err := kg.Build(ctx, s)
	if err != nil {
		return corpus{}, fmt.Errorf("build graph %s: %w", dir, err)
	}
	build := time.Since(start)
	policy, err := stale.DefaultPolicy(ctx, client, time.Now())
	if err != nil {
		return corpus{}, fmt.Errorf("policy %s: %w", dir, err)
	}
	start = time.Now()
	assess, err := stale.New(client, dir, policy).Assess(ctx)
	if err != nil {
		return corpus{}, fmt.Errorf("assess %s: %w", dir, err)
	}
	fmt.Printf("=== %s: %d questions, %d entities\n", dir, len(questions), len(entities))
	fmt.Printf("    graph build %s (%d nodes, %d edges, %d events); staleness assess %s\n\n",
		build.Round(time.Millisecond), len(g.Nodes), len(g.Edges), len(g.Events),
		time.Since(start).Round(time.Millisecond))
	return corpus{dir: dir, questions: questions, entities: entities, graph: g, assess: assess, options: opts}, nil
}

func (c corpus) report() error {
	if err := c.compare(); err != nil {
		return err
	}
	if err := c.sweep(); err != nil {
		return err
	}
	if err := c.paired(); err != nil {
		return err
	}
	if err := c.abstention(); err != nil {
		return err
	}
	return c.timing()
}

// config names one ranker configuration, built off the bare ranker — the
// lexical lane alone, undiversified — so each case shows exactly what it adds.
func (c corpus) config(name string, mutate func(*rank.Options)) eval.Config {
	return eval.Config{Name: name, Build: func(seed int64) eval.Retriever {
		opts := rank.DefaultOptions()
		opts.Seed, opts.Lambda = seed, 1
		opts.Enrich, opts.Graph = false, false
		mutate(&opts)
		return rank.New(c.entities, c.graph, c.assess, opts)
	}}
}

func (c corpus) enriched() eval.Config {
	return c.config("lex+enrich", func(o *rank.Options) { o.Enrich = true })
}

func (c corpus) fused() eval.Config {
	return c.config("lex+enrich+graph", func(o *rank.Options) { o.Enrich, o.Graph = true, true })
}

func (c corpus) gated() eval.Config {
	ranker := stale.NewRanker(c.entities, c.assess, stale.DefaultRetrieval())
	return eval.Config{Name: "gated", Build: func(seed int64) eval.Retriever {
		return ranker.Wrap(eval.NewBM25(c.entities, seed), seed)
	}}
}

// compare is the kill test: the harness baselines, then each lane the ranker
// adds, so a layer that does not pay is visible as the row that does not move.
func (c corpus) compare() error {
	return c.run("lane ablation", []eval.Config{
		{Name: "bm25", Build: func(seed int64) eval.Retriever { return eval.NewBM25(c.entities, seed) }},
		c.gated(),
		{Name: "full-context", Build: func(seed int64) eval.Retriever { return eval.NewFullContext(c.entities, seed) }},
		c.config("lex", func(*rank.Options) {}),
		c.enriched(),
		c.config("lex+graph", func(o *rank.Options) { o.Graph = true }),
		c.fused(),
		c.config("lex+enrich+graph+mmr", func(o *rank.Options) {
			o.Enrich, o.Graph, o.Lambda = true, true, rank.DefaultLambda
		}),
	})
}

// sweep is the fused ranking's sensitivity to the graph lane's RRF weight
// around the lexical lane's 1.0.
func (c corpus) sweep() error {
	weights := []float64{0.25, 0.5, 1, 2, 3}
	configs := make([]eval.Config, 0, len(weights)+1)
	configs = append(configs, c.enriched())
	for _, w := range weights {
		configs = append(configs, c.config(fmt.Sprintf("graph w=%.2f", w), func(o *rank.Options) {
			o.Enrich, o.Graph, o.GraphWeight = true, true, w
		}))
	}
	return c.run("graph fusion weight", configs)
}

func (c corpus) run(title string, configs []eval.Config) error {
	report, err := eval.Run(context.Background(), c.questions, configs, c.options)
	if err != nil {
		return err
	}
	fmt.Printf("--- %s\n", title)
	fmt.Println(report)
	return nil
}

// timing is what the ranker costs: the one-off index over the corpus and graph,
// and the per-query lanes, fusion, and diversification.
func (c corpus) timing() error {
	ctx := context.Background()
	opts := rank.DefaultOptions()
	opts.Seed = c.options.Seeds[0]
	start := time.Now()
	r := rank.New(c.entities, c.graph, c.assess, opts)
	index := time.Since(start)

	const repeats = 10
	start = time.Now()
	for range repeats {
		for _, q := range c.questions {
			if _, err := r.Retrieve(ctx, q.Query, c.options.K); err != nil {
				return err
			}
		}
	}
	perQuery := time.Since(start) / time.Duration(repeats*len(c.questions))

	const budget = 4000
	start = time.Now()
	filled, err := r.Fill(ctx, c.questions[0].Query, budget)
	if err != nil {
		return err
	}
	fmt.Printf("--- cost\n  index %s; retrieve %s/query; fill %s for %d records at a %d-token budget\n\n",
		index.Round(time.Millisecond), perQuery.Round(time.Microsecond),
		time.Since(start).Round(time.Microsecond), len(filled), budget)
	return nil
}
