// Command kgrank measures the cc-notes knowledge-graph ranker against the
// harness baselines: the lane ablation that decides whether the personalized
// PageRank layer earns its place, the weight sweep run on a selection fold and
// scored on a held-out one, the per-question paired tests behind each mean, the
// cross-repository pool, the abstention signals, and the index and query cost.
//
// Every configuration is measured twice where the graph lane is on: once from
// the zero session, and once from the session each question is asked from,
// which is what the product sets on every real query.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/yasyf/cc-notes/internal/kg"
	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/internal/kg/rank"
	"github.com/yasyf/cc-notes/internal/kg/snapshot"
	"github.com/yasyf/cc-notes/internal/kg/stale"
	"github.com/yasyf/cc-notes/internal/render"
)

// sweepWeights are the graph lane's fusion weights the selection fold chooses
// between, around the lexical lane's 1.0.
var sweepWeights = []float64{0, 0.25, 0.5, 1, 2, 3}

func main() {
	questions := flag.String("questions", "", "path to the question set JSON")
	root := flag.String("snapshot", "", "frozen corpus directory written by kgsnap")
	repo := flag.String("repo", "", "measure only this repository from the set")
	k := flag.Int("k", 10, "cutoff for the @k metrics")
	threshold := flag.Float64("threshold", 0.1, "score below which a result counts as abstention")
	seeds := flag.Int("seeds", eval.MinSeeds, "seeds per configuration")
	flag.Parse()

	if err := run(*questions, *root, *repo, *k, *threshold, *seeds); err != nil {
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

func run(path, root, only string, k int, threshold float64, seedCount int) error {
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
	p := newPool()
	for _, dir := range qs.Repos() {
		if only != "" && dir != only {
			continue
		}
		c, err := load(root, dir, path, qs.ForRepo(dir), eval.Options{K: k, Threshold: threshold, Seeds: seeds})
		if err != nil {
			return err
		}
		if err := c.report(p); err != nil {
			return err
		}
	}
	p.report()
	return nil
}

func load(root, dir, path string, questions []eval.Question, opts eval.Options) (corpus, error) {
	snap, err := snapshot.Open(root, dir, path)
	if err != nil {
		return corpus{}, err
	}
	m := snap.Manifest
	fmt.Printf("=== %s: %d questions (%d graded), %d entities\n", dir, len(questions), len(eval.Graded(questions)), len(snap.Entities))
	fmt.Printf("    snapshot captured %s at %s (%d nodes, %d edges, %d events)\n",
		m.CapturedAt.Format(time.RFC3339), render.ShortWireID(string(m.Head)),
		len(snap.Graph.Nodes), len(snap.Graph.Edges), len(snap.Graph.Events))
	fmt.Printf("    %d of %d questions carry session context; the rest are asked from the zero session, where the %s arm scores identically to %s\n\n",
		eval.Sessioned(questions), len(questions), armLabel(true), armLabel(false))
	return corpus{
		dir:       dir,
		questions: questions,
		entities:  snap.Entities,
		graph:     snap.Graph,
		assess:    snap.Assessments,
		options:   opts,
	}, nil
}

func (c corpus) report(p *pool) error {
	if err := c.compare(); err != nil {
		return err
	}
	if err := c.tuned(p); err != nil {
		return err
	}
	if err := c.paired(p); err != nil {
		return err
	}
	if err := c.abstention(); err != nil {
		return err
	}
	return c.timing()
}

// rankers memoizes one indexed ranker per seed and session. rank.New costs an
// index over the whole corpus, the graph, and the staleness assessments, and
// none of that depends on the session a question is asked from — only the
// walk's restart distribution does, so a session that repeats reuses its index.
type rankers struct {
	corpus corpus
	base   rank.Options
	built  map[string]*rank.Ranker
}

func newRankers(c corpus, base rank.Options) *rankers {
	return &rankers{corpus: c, base: base, built: map[string]*rank.Ranker{}}
}

func (m *rankers) get(seed int64, sess rank.Session) *rank.Ranker {
	key := fmt.Sprintf("%d\x00%s\x00%s", seed, sess.Branch, strings.Join(sess.Paths, "\x00"))
	if r, ok := m.built[key]; ok {
		return r
	}
	opts := m.base
	opts.Seed, opts.Session = seed, sess
	r := rank.New(m.corpus.entities, m.corpus.graph, m.corpus.assess, opts)
	m.built[key] = r
	return r
}

// arm is one configuration under test: the eval config the harness scores, plus
// whether it runs the graph lane and whether it threads the question's session.
// Those two say which questions could receive the treatment at all.
type arm struct {
	config  eval.Config
	rankers *rankers
	graph   bool
	session bool
}

// armLabel names which session a configuration is asked from.
func armLabel(session bool) string {
	if session {
		return "session-seeded"
	}
	return "session-free"
}

// ambient is the session a question is asked from, as the product builds it
// (internal/cli sets exactly this branch and these paths on every real query),
// or the zero session for the arm that measures the ranker without one.
func ambient(session bool, q eval.Question) rank.Session {
	if !session {
		return rank.Session{}
	}
	return rank.Session{Branch: q.Session.Branch, Paths: q.Session.Paths}
}

// arm builds a ranker configuration off the bare ranker — the lexical lane
// alone, undiversified — so each case shows exactly what it adds.
func (c corpus) arm(name string, session bool, mutate func(*rank.Options)) arm {
	base := rank.DefaultOptions()
	base.Lambda, base.Enrich, base.Graph = 1, false, false
	mutate(&base)
	m := newRankers(c, base)
	return arm{
		config: eval.Config{Name: name, Build: func(seed int64, q eval.Question) eval.Retriever {
			return m.get(seed, ambient(session, q))
		}},
		rankers: m,
		graph:   base.Graph,
		session: session,
	}
}

// retriever wraps a configuration the ranker does not build, so the baselines
// and the staleness-gated retriever join the same arm list.
func retriever(name string, build func(seed int64) eval.Retriever) arm {
	return arm{config: eval.Config{Name: name, Build: func(seed int64, _ eval.Question) eval.Retriever {
		return build(seed)
	}}}
}

// untreated reports the questions the arm's graph lane never ran on. rank.go
// appends the lane only when the personalized walk resolves seeds, so a
// question it resolves none for is scored by the lexical lane alone: it is
// structural non-treatment, and counting it as a tie reads as "the lane was
// tried and changed nothing".
func (a arm) untreated(ctx context.Context, questions []eval.Question, seed int64) (map[string]bool, error) {
	out := make(map[string]bool, len(questions))
	if !a.graph {
		return out, nil
	}
	for _, q := range questions {
		lanes, err := a.rankers.get(seed, ambient(a.session, q)).Lanes(ctx, q.Query)
		if err != nil {
			return nil, fmt.Errorf("lanes for %s: %w", q.ID, err)
		}
		out[q.ID] = len(lanes.Graph) == 0
	}
	return out, nil
}

func (c corpus) enriched() arm {
	return c.arm("lex+enrich", false, func(o *rank.Options) { o.Enrich = true })
}

func (c corpus) fused(session bool) arm {
	name := "lex+enrich+graph"
	if session {
		name += "+session"
	}
	return c.arm(name, session, func(o *rank.Options) { o.Enrich, o.Graph = true, true })
}

func (c corpus) weighted(w float64, session bool) arm {
	name := fmt.Sprintf("graph w=%.2f", w)
	if session {
		name += "+session"
	}
	return c.arm(name, session, func(o *rank.Options) { o.Enrich, o.Graph, o.GraphWeight = true, true, w })
}

func (c corpus) gated() arm {
	ranker := stale.NewRanker(c.entities, c.assess, stale.DefaultRetrieval())
	return retriever("gated", func(seed int64) eval.Retriever {
		return ranker.Wrap(eval.NewBM25(c.entities, seed), seed)
	})
}

// compare is the kill test: the harness baselines, then each lane the ranker
// adds, so a layer that does not pay is visible as the row that does not move.
// The graph rows run at DefaultGraphWeight, which was itself chosen on these
// questions; the held-out headline below is the out-of-sample reading.
func (c corpus) compare() error {
	return c.run("lane ablation (every question, graph lanes at the in-sample default weight)", []arm{
		retriever("bm25", func(seed int64) eval.Retriever { return eval.NewBM25(c.entities, seed) }),
		c.gated(),
		retriever("full-context", func(seed int64) eval.Retriever { return eval.NewFullContext(c.entities, seed) }),
		c.arm("lex", false, func(*rank.Options) {}),
		c.enriched(),
		c.arm("lex+graph", false, func(o *rank.Options) { o.Graph = true }),
		c.fused(false),
		c.fused(true),
		c.arm("lex+enrich+graph+mmr", false, func(o *rank.Options) {
			o.Enrich, o.Graph, o.Lambda = true, true, rank.DefaultLambda
		}),
	}, c.questions)
}

func (c corpus) run(title string, arms []arm, questions []eval.Question) error {
	report, err := c.evaluate(arms, questions)
	if err != nil {
		return err
	}
	fmt.Printf("--- %s\n", title)
	fmt.Println(report)
	return nil
}

func (c corpus) evaluate(arms []arm, questions []eval.Question) (eval.Report, error) {
	configs := make([]eval.Config, len(arms))
	for i, a := range arms {
		configs[i] = a.config
	}
	return eval.Run(context.Background(), questions, configs, c.options)
}

// timing is what the ranker costs: the one-off index over the corpus and graph,
// and the per-query lanes, fusion, and diversification — measured with the
// graph lane on and off, so what the lane costs is the difference between the
// two rows.
func (c corpus) timing() error {
	fmt.Println("--- cost")
	for _, graph := range []bool{false, true} {
		if err := c.measure(graph); err != nil {
			return err
		}
	}
	fmt.Println()
	return nil
}

func (c corpus) measure(graph bool) error {
	ctx := context.Background()
	opts := rank.DefaultOptions()
	opts.Seed, opts.Graph = c.options.Seeds[0], graph
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
	fmt.Printf("  graph=%-5t index %s; retrieve %s/query; fill %s for %d records at a %d-token budget\n",
		graph, index.Round(time.Millisecond), perQuery.Round(time.Microsecond),
		time.Since(start).Round(time.Microsecond), len(filled), budget)
	return nil
}
