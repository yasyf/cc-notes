// Package rank is the cc-notes retrieval ranker: two lanes over one corpus —
// BM25 over anchor-enriched record text and personalized PageRank over the
// knowledge graph — fused by weighted reciprocal rank, gated by the staleness
// assessment, diversified by maximal marginal relevance, and cut to a token
// budget.
//
// Nothing here calls a language model. Every score is read from the corpus
// text or the graph's shape.
package rank

import (
	"context"

	"github.com/yasyf/cc-notes/internal/kg"
	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/internal/kg/stale"
	"github.com/yasyf/cc-notes/model"
)

// Lane names each retrieval lane's attribution on a result.
const (
	LexLane   = "lex"
	GraphLane = "graph"
	FusedLane = "lex+graph"
)

// Ranker defaults. The fusion is cc-context's weighted RRF
// (internal/web/fusion.go), degrading to one lane when the other resolves
// nothing — but with the weights inverted. Measured over the evaluation corpus
// on 2026-07-27, the graph lane pays only below the lexical lane: at
// cc-context's 3.0 it costs 0.108 NDCG, at 0.5 it earns 0.027.
const (
	DefaultRRFK        = 60.0
	DefaultLexWeight   = 1.0
	DefaultGraphWeight = 0.5
	DefaultHops        = 2
	DefaultDamping     = 0.85
	DefaultLambda      = 0.7
)

// Session is the ambient state a query is asked from: the branch the agent is
// on, the paths it has touched, and the records it already holds. Every field
// seeds the graph lane, so a query that names nothing structural still walks
// out from where the agent is standing.
type Session struct {
	Branch   model.Branch
	Paths    []string
	Entities []model.EntityID
}

// Options configures a Ranker. Enrich and Graph select the lanes, Retrieval is
// the staleness policy consumed from internal/kg/stale, and Lambda is the MMR
// tradeoff — 1 leaves the ranking undiversified.
type Options struct {
	Seed      int64
	Session   Session
	Retrieval stale.Retrieval

	Enrich bool
	Graph  bool

	LexWeight   float64
	GraphWeight float64
	Hops        int
	Damping     float64

	Lambda float64
}

// DefaultOptions is the full ranker: both lanes at their measured weights, the
// staleness retrieval policy, and MMR diversification.
func DefaultOptions() Options {
	return Options{
		Retrieval:   stale.DefaultRetrieval(),
		Enrich:      true,
		Graph:       true,
		LexWeight:   DefaultLexWeight,
		GraphWeight: DefaultGraphWeight,
		Hops:        DefaultHops,
		Damping:     DefaultDamping,
		Lambda:      DefaultLambda,
	}
}

// Ranker ranks one repository's corpus. It holds the lane indexes, the
// staleness assessments, and the graph-derived importance prior, so a query
// costs two lane scans and a bounded walk.
type Ranker struct {
	opts       Options
	ids        []model.EntityID
	lex        *eval.BM25
	graph      *graphLane
	assessment map[model.EntityID]stale.Assessment
	tokens     map[model.EntityID]map[string]struct{}
	cost       map[model.EntityID]int
	importance map[model.EntityID]float64
}

// New indexes the corpus, the graph over it, and its staleness assessments. The
// graph may be nil when opts.Graph is false.
func New(corpus []eval.Entity, g *kg.Graph, as []stale.Assessment, opts Options) *Ranker {
	indexed := corpus
	if opts.Enrich {
		indexed = Enrich(corpus, g)
	}
	r := &Ranker{
		opts:       opts,
		ids:        make([]model.EntityID, len(corpus)),
		lex:        eval.NewBM25(indexed, opts.Seed),
		assessment: stale.Index(as),
		tokens:     make(map[model.EntityID]map[string]struct{}, len(corpus)),
		cost:       make(map[model.EntityID]int, len(corpus)),
	}
	for i, e := range corpus {
		text := e.Text()
		r.ids[i] = e.ID
		r.tokens[e.ID] = tokenSet(text)
		r.cost[e.ID] = EstimateTokens(text)
	}
	if opts.Graph {
		r.graph = newGraphLane(g)
		r.importance = r.graph.importance()
	}
	return r
}

// Retrieve ranks the corpus against query and returns the top k. It satisfies
// eval.Retriever, so the harness scores it against the same baselines.
func (r *Ranker) Retrieve(ctx context.Context, query string, k int) ([]eval.Result, error) {
	scored, err := r.rank(ctx, query)
	if err != nil {
		return nil, err
	}
	if r.opts.Lambda < 1 {
		scored = diversify(scored, r.tokens, r.opts.Lambda, k)
	}
	return eval.Rank(scored, r.opts.Seed, k), nil
}

// Lanes is what each lane found for a query before fusion: the lexical lane's
// BM25 scores, and the graph lane's mass lift over the unpersonalized walk.
type Lanes struct {
	Lexical map[model.EntityID]float64
	Graph   map[model.EntityID]float64
}

// Lanes reports the per-lane scores behind a ranking, which is how a caller
// tells a query the corpus addresses from one it only shares vocabulary with.
func (r *Ranker) Lanes(ctx context.Context, query string) (Lanes, error) {
	candidates, _ := r.admit()
	lexical, err := r.lexical(ctx, query, candidates)
	if err != nil {
		return Lanes{}, err
	}
	out := Lanes{Lexical: lexical}
	if r.graph != nil {
		seeds, _ := r.graph.personalize(query, r.opts.Session)
		out.Graph = lift(r.graph.walk(seeds, r.opts.Hops, r.opts.Damping), r.importance)
	}
	return out, nil
}

// Fill returns the longest prefix of the ranking whose estimated token cost
// fits budget, found by binary search over the ranked candidates.
func (r *Ranker) Fill(ctx context.Context, query string, budget int) ([]eval.Result, error) {
	scored, err := r.rank(ctx, query)
	if err != nil {
		return nil, err
	}
	if r.opts.Lambda < 1 {
		scored = diversify(scored, r.tokens, r.opts.Lambda, len(scored))
	}
	ranked := eval.Rank(scored, r.opts.Seed, len(scored))
	return ranked[:fit(ranked, r.cost, budget)], nil
}

// rank runs both lanes, fuses them, and applies the staleness weight. The
// returned results are unordered.
func (r *Ranker) rank(ctx context.Context, query string) ([]eval.Result, error) {
	candidates, weights := r.admit()
	lexical, err := r.lexical(ctx, query, candidates)
	if err != nil {
		return nil, err
	}
	lanes := []lane{{name: LexLane, weight: r.opts.LexWeight, score: lexical, addressed: true}}
	if r.graph != nil {
		seeds, named := r.graph.personalize(query, r.opts.Session)
		walk := lift(r.graph.walk(seeds, r.opts.Hops, r.opts.Damping), r.importance)
		if len(walk) > 0 {
			lanes = append(lanes, lane{name: GraphLane, weight: r.opts.GraphWeight, score: walk, addressed: named})
		}
	}
	relevance := fuse(lanes, candidates, DefaultRRFK)
	return r.compose(relevance, candidates, weights, lanes), nil
}

// admit drops the records the staleness policy withholds and resolves the rank
// weight of the ones that survive.
func (r *Ranker) admit() ([]model.EntityID, map[model.EntityID]float64) {
	ids := make([]model.EntityID, 0, len(r.ids))
	weights := make(map[model.EntityID]float64, len(r.ids))
	for _, id := range r.ids {
		w, kept := r.opts.Retrieval.Weight(r.assessment[id])
		if !kept {
			continue
		}
		ids = append(ids, id)
		weights[id] = w
	}
	return ids, weights
}

// lexical scores every admitted candidate with BM25 over the whole corpus.
func (r *Ranker) lexical(ctx context.Context, query string, candidates []model.EntityID) (map[model.EntityID]float64, error) {
	hits, err := r.lex.Retrieve(ctx, query, len(r.ids))
	if err != nil {
		return nil, err
	}
	admitted := make(map[model.EntityID]struct{}, len(candidates))
	for _, id := range candidates {
		admitted[id] = struct{}{}
	}
	out := make(map[model.EntityID]float64, len(hits))
	for _, h := range hits {
		if _, ok := admitted[h.ID]; ok {
			out[h.ID] = h.Score
		}
	}
	return out, nil
}

// compose keeps the candidates an addressed lane found, scales their fused
// relevance onto [0,1], and applies the staleness weight. Fused
// reciprocal-rank scores carry no absolute scale, so the best candidate
// anchors the top of the range — which is also why the score cannot decide
// admission and lane membership does. A query the corpus does not answer
// reaches no candidate and composes nothing: the ranker abstains.
func (r *Ranker) compose(relevance map[model.EntityID]float64, candidates []model.EntityID, weights map[model.EntityID]float64, lanes []lane) []eval.Result {
	support := found(lanes, candidates)
	name := laneName(lanes)
	rel := normalize(relevance, candidates)
	out := make([]eval.Result, 0, len(support))
	for _, id := range candidates {
		if _, ok := support[id]; !ok {
			continue
		}
		score := rel[id] * weights[id]
		if score <= 0 {
			continue
		}
		out = append(out, eval.Result{ID: id, Score: score, Lane: attribute(name, r.assessment[id].Signal)})
	}
	return out
}

// laneName attributes a result to the lanes that produced the ranking.
func laneName(lanes []lane) string {
	if len(lanes) > 1 {
		return FusedLane
	}
	return lanes[0].name
}

// attribute tags the lane with the staleness signal a surfaced record carries,
// so a caller can show why a demoted record is still here.
func attribute(name string, sig stale.Signal) string {
	if sig == "" {
		return name
	}
	return name + "/" + string(sig)
}
