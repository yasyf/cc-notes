package stale

import (
	"context"
	"fmt"
	"math"
	"slices"

	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/model"
)

// GateDemotion is the rank cost of a gate signal that does not withhold: one
// half-life, the unit S7-S9 already measure in.
const GateDemotion = 0.5

// AbstainBelow is the default calibrated score under which a result is not
// worth injecting. It matches the harness's own abstention threshold.
const AbstainBelow = 0.1

// Replaced is the gate signal whose correction lives on a different record the
// corpus still holds: a live supersede edge. Withholding it loses no answer,
// because the successor is retrievable in its place.
func Replaced() []Signal {
	return []Signal{SignalSuperseded}
}

// Corrected is the gate signals whose correction lives on the record itself — a
// stale reason, a refuted premise. Withholding one withholds the correction
// with it, so these are surfaced and flagged, never suppressed.
func Corrected() []Signal {
	return []Signal{SignalExpired, SignalExonerated}
}

// Displacing is the gate signals that move a record's address or end its
// lifecycle without touching the truth of its content: a drifted anchor, a
// closed task, a reconciled branch.
func Displacing() []Signal {
	return []Signal{SignalDrift, SignalClosed, SignalReconciled}
}

// GateSignals is every hard gate signal, S1 through S6.
func GateSignals() []Signal {
	return slices.Concat(Replaced(), Corrected(), Displacing())
}

// Retrieval is the gate's retrieval-side policy. Withhold names the gate
// signals that suppress a record outright and Demote the ones that only cost it
// rank; Decay is the exponent the S7-S9 penalty weight enters ranking with, 0
// keeping those out of rank; AbstainBelow is the calibrated relevance a result
// must clear to be admitted.
type Retrieval struct {
	Withhold     []Signal
	Demote       []Signal
	Decay        float64
	AbstainBelow float64
}

// DefaultRetrieval withholds the replaced records and nothing else. Measured
// over the evaluation corpus, a displacing signal is uncorrelated with being
// the wrong answer, and the S7-S9 penalties carry S8's own precision ceiling,
// so neither is allowed to move rank.
func DefaultRetrieval() Retrieval {
	return Retrieval{Withhold: Replaced(), AbstainBelow: AbstainBelow}
}

// DemoteRetrieval also charges every displacing signal one half-life of rank.
func DemoteRetrieval() Retrieval {
	return Retrieval{Withhold: Replaced(), Demote: Displacing(), AbstainBelow: AbstainBelow}
}

// DecayRetrieval is DemoteRetrieval plus the literal ranker contract, with the
// penalty weight multiplying rank at full strength.
func DecayRetrieval() Retrieval {
	return Retrieval{Withhold: Replaced(), Demote: Displacing(), Decay: 1, AbstainBelow: AbstainBelow}
}

// StrictRetrieval withholds every gated record — the literal reading of S1-S6
// as a non-injection decision.
func StrictRetrieval() Retrieval {
	return Retrieval{Withhold: GateSignals(), AbstainBelow: AbstainBelow}
}

// Calibration converts a lexical retriever's raw scores onto a scale that does
// not move with corpus size or query length.
type Calibration struct {
	idf map[string]float64
}

// NewCalibration indexes the corpus's document frequencies.
func NewCalibration(corpus []eval.Entity) *Calibration {
	df := map[string]int{}
	for _, e := range corpus {
		seen := map[string]struct{}{}
		for _, t := range eval.Tokenize(e.Text()) {
			if _, ok := seen[t]; ok {
				continue
			}
			seen[t] = struct{}{}
			df[t]++
		}
	}
	n := float64(len(corpus))
	idf := make(map[string]float64, len(df))
	for t, d := range df {
		idf[t] = math.Log(1 + (n-float64(d)+0.5)/(float64(d)+0.5))
	}
	return &Calibration{idf: idf}
}

// Ideal is what BM25 awards an average-length document holding each of the
// query's term occurrences once, so dividing by it puts a score of 1 at that
// document on every corpus and query length. It sums occurrences rather than
// distinct terms because that is what the scorer it divides sums.
func (c *Calibration) Ideal(query string) float64 {
	var total float64
	for _, t := range eval.Tokenize(query) {
		total += c.idf[t]
	}
	return total
}

// Ranker holds the corpus-level state a gated retrieval needs, so a multi-seed
// run mints one retriever per seed without re-assessing the corpus.
type Ranker struct {
	cal      *Calibration
	idx      map[model.EntityID]Assessment
	entities int
	policy   Retrieval
}

// NewRanker indexes the corpus and its assessments under the retrieval policy.
func NewRanker(corpus []eval.Entity, as []Assessment, p Retrieval) *Ranker {
	return &Ranker{cal: NewCalibration(corpus), idx: Index(as), entities: len(corpus), policy: p}
}

// Wrap builds the seeded gated retriever over inner, which must rank the same
// corpus the assessments cover.
func (r *Ranker) Wrap(inner eval.Retriever, seed int64) *Gated {
	return &Gated{inner: inner, ranker: r, seed: seed}
}

// Gated is the staleness-gated retriever: it withholds the records the corpus
// declares invalid, demotes the ones whose address or lifecycle went stale,
// calibrates the surviving scores, and abstains when nothing clears the floor.
type Gated struct {
	inner  eval.Retriever
	ranker *Ranker
	seed   int64
}

// Retrieve ranks the corpus through the inner retriever and applies the gate.
// Calibrated relevance decides admission, the staleness weight decides order. A
// query whose every term is absent from the corpus is unanswerable by
// construction and abstains outright.
func (g *Gated) Retrieve(ctx context.Context, query string, k int) ([]eval.Result, error) {
	ideal := g.ranker.cal.Ideal(query)
	if ideal == 0 {
		return nil, nil
	}
	raw, err := g.inner.Retrieve(ctx, query, g.ranker.entities)
	if err != nil {
		return nil, fmt.Errorf("inner retrieve: %w", err)
	}
	out := make([]eval.Result, 0, len(raw))
	for _, r := range raw {
		a := g.ranker.idx[r.ID]
		weight, kept := g.ranker.policy.Weight(a)
		if !kept {
			continue
		}
		relevance := r.Score / ideal
		if relevance < g.ranker.policy.AbstainBelow {
			continue
		}
		out = append(out, eval.Result{ID: r.ID, Score: relevance * weight, Lane: lane(r.Lane, a.Signal)})
	}
	return eval.Rank(out, g.seed, k), nil
}

// Weight resolves a record's rank multiplier under this policy and whether it
// survives at all: a withheld gate signal drops it, a demoted one costs a
// half-life, and an ungated record carries its penalty product raised to the
// decay exponent.
func (p Retrieval) Weight(a Assessment) (float64, bool) {
	if !a.Gated {
		return math.Pow(a.Weight, p.Decay), true
	}
	if slices.Contains(p.Withhold, a.Signal) {
		return 0, false
	}
	if slices.Contains(p.Demote, a.Signal) {
		return GateDemotion, true
	}
	return 1, true
}

// lane tags the inner lane with the signal that demoted the hit.
func lane(inner string, sig Signal) string {
	if sig == "" {
		return inner
	}
	return inner + "/" + string(sig)
}
