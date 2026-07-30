package eval

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
)

// Sign-flip enumeration limits. A comparison with at most SignFlipExactLimit
// contributing differences has its null distribution enumerated exactly; a
// longer one is sampled at SignFlipResamples draws, because 2^n assignments
// stop fitting in a run.
const (
	SignFlipExactLimit = 24
	SignFlipResamples  = 200000
)

// signFlipSeed pins the sampled null distribution, so a re-run of the harness
// reports the same p-value.
const signFlipSeed = 0x63636e6f746573

// tieEpsilon is how close two per-question scores must be to count as equal.
// Both sides are means over the same seeds of the same metric, so a difference
// this small is floating-point noise, not a ranking that moved.
const tieEpsilon = 1e-9

// sumEpsilon absorbs the rounding a partial sum accumulates along one branch of
// the sign-flip enumeration, so an assignment that mirrors the observed one is
// counted as tied with it rather than dropped.
const sumEpsilon = 1e-12

// normal95 is the standard normal two-sided 95 % quantile.
const normal95 = 1.959963984540054

// Method names how a sign-flip null distribution was obtained.
type Method string

// The two ways SignFlipWeighted resolves a p-value.
const (
	Exact      Method = "exact"
	MonteCarlo Method = "monte-carlo"
)

// Delta is one question's paired difference: the treatment configuration's
// score minus the baseline's. Untreated records that the treatment never
// actually applied to this question — the ranker's graph lane is appended only
// when the personalized walk resolves something, so a question it resolves
// nothing for is structural non-treatment, not evidence that the lane changes
// nothing.
type Delta struct {
	Repo      string
	Question  string
	Value     float64
	Untreated bool
}

// Paired summarizes a set of paired differences: the win/loss/tie counts with
// the untreated questions called out beside them, the mean difference with a
// question-level standard error and normal 95 % interval, and three tests of
// the null that the treatment changed nothing.
//
// The three disagree by construction and all three are reported. Sign discards
// every tie and reads only the direction of what is left, so a treatment that
// moves few questions a long way looks like nothing to it. Wilcoxon reads the
// ranks of the magnitudes, and SignFlip reads the magnitudes themselves.
type Paired struct {
	N         int
	Untreated int
	Wins      int
	Losses    int
	Ties      int
	Mean      float64
	StdErr    float64
	CILow     float64
	CIHigh    float64
	Sign      float64
	Wilcoxon  float64
	SignFlip  float64
	Method    Method
}

// String renders a paired comparison as one report line.
func (p Paired) String() string {
	return fmt.Sprintf(
		"n=%d  %d win / %d loss / %d tie (%d untreated)  mean %+.4f  SE %.4f  95%% CI [%+.4f, %+.4f]  sign p=%.4f  wilcoxon p=%.4f  sign-flip p=%.4f (%s)",
		p.N, p.Wins, p.Losses, p.Ties, p.Untreated, p.Mean, p.StdErr, p.CILow, p.CIHigh,
		p.Sign, p.Wilcoxon, p.SignFlip, p.Method)
}

// Compare summarizes paired deltas. Wins, losses, and ties partition every
// delta; Untreated overlays them, counting the questions the treatment never
// reached. Comparing nothing is the zero comparison, at p=1 on every test.
func Compare(deltas []Delta) Paired {
	p := Paired{N: len(deltas), Sign: 1, Wilcoxon: 1, SignFlip: 1, Method: Exact}
	if len(deltas) == 0 {
		return p
	}
	values := make([]float64, len(deltas))
	weights := make([]float64, len(deltas))
	for i, d := range deltas {
		values[i], weights[i] = d.Value, 1/float64(len(deltas))
		if d.Untreated {
			p.Untreated++
		}
		switch {
		case d.Value > tieEpsilon:
			p.Wins++
		case d.Value < -tieEpsilon:
			p.Losses++
		default:
			p.Ties++
		}
	}
	p.Mean, p.StdErr = meanStdErr(values)
	p.CILow, p.CIHigh = p.Mean-normal95*p.StdErr, p.Mean+normal95*p.StdErr
	p.Sign = SignTest(p.Wins, p.Losses)
	p.Wilcoxon = WilcoxonTest(values)
	p.SignFlip, p.Method = SignFlipWeighted(values, weights)
	return p
}

// meanStdErr is the sample mean and the standard error of that mean over the
// questions. A single question carries no spread, so its standard error is 0.
func meanStdErr(values []float64) (float64, float64) {
	total := 0.0
	for _, v := range values {
		total += v
	}
	avg := total / float64(len(values))
	if len(values) < 2 {
		return avg, 0
	}
	sumsq := 0.0
	for _, v := range values {
		sumsq += (v - avg) * (v - avg)
	}
	variance := sumsq / float64(len(values)-1)
	return avg, math.Sqrt(variance / float64(len(values)))
}

// SignTest is the exact two-sided binomial p-value for wins against losses
// under the null that either is equally likely. It discards every tie, which is
// why a comparison whose treatment leaves most questions untouched can carry a
// large effect and still land near p=1.
func SignTest(wins, losses int) float64 {
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

// WilcoxonTest is the exact two-sided Wilcoxon signed-rank p-value: the
// probability, under independent sign flips of the observed ranks, of a
// signed-rank sum at least as extreme as the observed one. Zero differences
// drop out and tied magnitudes share a midrank; doubling every rank keeps the
// convolution over integers, so ties cost no exactness.
func WilcoxonTest(values []float64) float64 {
	nonzero := make([]float64, 0, len(values))
	for _, v := range values {
		if math.Abs(v) > tieEpsilon {
			nonzero = append(nonzero, v)
		}
	}
	if len(nonzero) == 0 {
		return 1
	}
	ranks := doubledMidranks(nonzero)
	total, plus := 0, 0
	for i, r := range ranks {
		total += r
		if nonzero[i] > 0 {
			plus += r
		}
	}
	dist := signedRankDistribution(ranks, total)
	tail := 0.0
	for s := range min(plus, total-plus) + 1 {
		tail += dist[s]
	}
	return math.Min(1, 2*tail)
}

// doubledMidranks ranks the magnitudes ascending from 1, averaging the ranks of
// a tied group, and returns every rank doubled so the set is integral.
func doubledMidranks(values []float64) []int {
	order := make([]int, len(values))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int {
		return cmpFloat(math.Abs(values[a]), math.Abs(values[b]))
	})
	out := make([]int, len(values))
	for i := 0; i < len(order); {
		j := i
		for j+1 < len(order) && math.Abs(values[order[j+1]])-math.Abs(values[order[i]]) <= tieEpsilon {
			j++
		}
		doubled := i + j + 2
		for _, at := range order[i : j+1] {
			out[at] = doubled
		}
		i = j + 1
	}
	return out
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// signedRankDistribution is the null probability of every signed-rank sum: each
// rank joins the positive side with probability one half, independently.
func signedRankDistribution(ranks []int, total int) []float64 {
	dist := make([]float64, total+1)
	dist[0] = 1
	for _, r := range ranks {
		for s := total; s >= r; s-- {
			dist[s] = 0.5*dist[s] + 0.5*dist[s-r]
		}
		for s := r - 1; s >= 0; s-- {
			dist[s] *= 0.5
		}
	}
	return dist
}

// SignFlipWeighted is the two-sided p-value of the weighted sum of values under
// the null that the treatment changed nothing, so every question's sign is
// independently plus or minus one. Unlike the sign test it reads the
// magnitudes, and unlike Wilcoxon it reads them unranked, which is what lets a
// weighting express that repositories, not questions, are the unit being pooled.
//
// The returned Method says whether the null distribution was enumerated over
// every assignment or sampled: beyond SignFlipExactLimit contributing terms,
// exact enumeration stops being affordable and the p-value is the sampled one,
// biased up by one draw so it can never read zero.
func SignFlipWeighted(values, weights []float64) (float64, Method) {
	terms := make([]float64, 0, len(values))
	for i, v := range values {
		if term := v * weights[i]; math.Abs(term) > tieEpsilon {
			terms = append(terms, term)
		}
	}
	if len(terms) == 0 {
		return 1, Exact
	}
	observed := 0.0
	for _, t := range terms {
		observed += t
	}
	if len(terms) <= SignFlipExactLimit {
		return exactSignFlip(terms, math.Abs(observed)), Exact
	}
	return sampleSignFlip(terms, math.Abs(observed)), MonteCarlo
}

// exactSignFlip counts, over all 2^n sign assignments, those whose sum is at
// least as far from zero as threshold. The two bounds prune whole subtrees the
// remaining magnitudes can no longer move across the threshold, and every leaf
// re-sums from the root, so no rounding accumulates between branches.
func exactSignFlip(terms []float64, threshold float64) float64 {
	n := len(terms)
	suffix := make([]float64, n+1)
	pow := make([]float64, n+1)
	pow[n] = 1
	for i := n - 1; i >= 0; i-- {
		suffix[i] = suffix[i+1] + math.Abs(terms[i])
		pow[i] = 2 * pow[i+1]
	}
	cut := threshold - sumEpsilon
	var count float64
	var walk func(i int, sum float64)
	walk = func(i int, sum float64) {
		switch reach := math.Abs(sum); {
		case reach-suffix[i] >= cut:
			count += pow[i]
		case reach+suffix[i] < cut:
		default:
			walk(i+1, sum+terms[i])
			walk(i+1, sum-terms[i])
		}
	}
	walk(0, 0)
	return math.Min(1, count/pow[0])
}

// sampleSignFlip estimates the same tail by drawing SignFlipResamples sign
// assignments from a pinned generator. Counting the observed assignment in both
// numerator and denominator keeps the estimate valid as a p-value.
func sampleSignFlip(terms []float64, threshold float64) float64 {
	//nolint:gosec // G404: this draws a null distribution, not a secret, and the generator is pinned on purpose so a re-run reports the same p-value.
	rng := rand.New(rand.NewPCG(signFlipSeed, signFlipSeed))
	cut := threshold - sumEpsilon
	hits := 0
	for range SignFlipResamples {
		sum := 0.0
		for _, t := range terms {
			if rng.Uint64()&1 == 0 {
				sum += t
				continue
			}
			sum -= t
		}
		if math.Abs(sum) >= cut {
			hits++
		}
	}
	return float64(hits+1) / float64(SignFlipResamples+1)
}
