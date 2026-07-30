package eval

import (
	"math"
	"testing"
)

// The golden p-values below come from an independent brute-force oracle: a
// Python script that enumerates all 2^n sign assignments directly, with no
// dynamic program, no pruning, and no shared code with this package.
const tol = 5e-10

// monorepoMarginal is the graph lane's per-question NDCG marginal over the
// lexical lane on the monorepo question set, 2026-07-29: twelve questions
// moved, twenty-three did not. It is the vector the kill test turned on, and
// the one where the three tests disagree most.
func monorepoMarginal() []float64 {
	return append([]float64{0.148, 0.021, 0.631, 0.118, -0.261, -0.080, -0.018, 0.259, -0.043, 0.333, 0.080, 0.327},
		make([]float64, 23)...)
}

func TestSignFlipWeightedMatchesExhaustiveEnumeration(t *testing.T) {
	cases := []struct {
		name    string
		values  []float64
		weights []float64
		want    float64
		method  Method
	}{
		{
			name:   "the monorepo graph marginal",
			values: monorepoMarginal(),
			want:   0.0849609375,
			method: Exact,
		},
		{
			name:   "five large wins against four negligible losses",
			values: append(append([]float64{0.5, 0.5, 0.5, 0.5, 0.5}, -0.001, -0.001, -0.001, -0.001), make([]float64, 10)...),
			want:   0.0625,
			method: Exact,
		},
		{
			name:   "tied magnitudes",
			values: []float64{0.2, 0.2, 0.2, -0.1, 0.05},
			want:   0.1875,
			method: Exact,
		},
		{
			name:   "a single difference can never be extreme",
			values: []float64{0.5},
			want:   1,
			method: Exact,
		},
		{
			name:   "no difference at all",
			values: []float64{0, 0, 0},
			want:   1,
			method: Exact,
		},
		{
			name:    "repo-balanced weights let one question outweigh four",
			values:  []float64{0.10, 0.20, -0.05, 0.15, -0.40},
			weights: []float64{1. / 8, 1. / 8, 1. / 8, 1. / 8, 1. / 2},
			want:    0.9375,
			method:  Exact,
		},
		{
			name:    "the same five questions weighted equally cancel",
			values:  []float64{0.10, 0.20, -0.05, 0.15, -0.40},
			weights: []float64{0.2, 0.2, 0.2, 0.2, 0.2},
			want:    1,
			method:  Exact,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			weights := tc.weights
			if weights == nil {
				weights = uniform(len(tc.values))
			}
			got, method := SignFlipWeighted(tc.values, weights)
			if math.Abs(got-tc.want) > tol {
				t.Errorf("SignFlipWeighted = %.10f, want %.10f", got, tc.want)
			}
			if method != tc.method {
				t.Errorf("method = %q, want %q", method, tc.method)
			}
		})
	}
}

// TestSignFlipWeightedSamplesBeyondTheExactLimit pins the one case that is not
// exact. Twenty-five equal magnitudes make the null distribution binomial, so
// the truth is a closed form the sampler can be held against: 2 * P(at most 5
// of 25 signs flip) = 0.0040773153.
func TestSignFlipWeightedSamplesBeyondTheExactLimit(t *testing.T) {
	values := make([]float64, 25)
	for i := range values {
		values[i] = 1
		if i >= 20 {
			values[i] = -1
		}
	}
	if len(values) <= SignFlipExactLimit {
		t.Fatalf("fixture has %d values, want more than the %d exact limit", len(values), SignFlipExactLimit)
	}
	got, method := SignFlipWeighted(values, uniform(len(values)))
	if method != MonteCarlo {
		t.Fatalf("method = %q, want %q past the exact limit", method, MonteCarlo)
	}
	const want = 0.0040773153
	if math.Abs(got-want) > 1e-3 {
		t.Errorf("SignFlipWeighted = %.6f, want %.6f within 1e-3", got, want)
	}
	again, _ := SignFlipWeighted(values, uniform(len(values)))
	if again != got {
		t.Errorf("second call = %.10f, want the pinned generator to reproduce %.10f", again, got)
	}
}

func TestWilcoxonTestMatchesExhaustiveEnumeration(t *testing.T) {
	cases := []struct {
		name   string
		values []float64
		want   float64
	}{
		{"the monorepo graph marginal", monorepoMarginal(), 0.0961914062},
		{"five large wins against four negligible losses", append(append([]float64{0.5, 0.5, 0.5, 0.5, 0.5}, -0.001, -0.001, -0.001, -0.001), make([]float64, 10)...), 0.16015625},
		{"tied magnitudes share a midrank", []float64{0.2, 0.2, 0.2, -0.1, 0.05}, 0.1875},
		{"tied magnitudes on both signs", []float64{0.2, -0.2, 0.2, 0.1, -0.05, 0}, 0.625},
		{"a single difference can never be extreme", []float64{0.5}, 1},
		{"no difference at all", []float64{0, 0, 0}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := WilcoxonTest(tc.values); math.Abs(got-tc.want) > tol {
				t.Errorf("WilcoxonTest = %.10f, want %.10f", got, tc.want)
			}
		})
	}
}

func TestSignTest(t *testing.T) {
	cases := []struct {
		name         string
		wins, losses int
		want         float64
	}{
		{"nothing moved", 0, 0, 1},
		{"one win", 1, 0, 1},
		{"the monorepo graph marginal", 8, 4, 0.3876953125},
		{"five wins against four losses", 5, 4, 1},
		{"six clean wins", 6, 0, 0.03125},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SignTest(tc.wins, tc.losses); math.Abs(got-tc.want) > tol {
				t.Errorf("SignTest(%d, %d) = %.10f, want %.10f", tc.wins, tc.losses, got, tc.want)
			}
		})
	}
}

// TestCompareSeesWhatTheSignTestCannot is the defect this file exists for. Five
// questions the treatment carried a long way against four it barely moved is a
// 5-4 record, which the sign test reads as pure chance; both magnitude-aware
// tests see it. Reporting only the sign test is how a real effect was written
// off, so all three are asserted at once.
func TestCompareSeesWhatTheSignTestCannot(t *testing.T) {
	deltas := make([]Delta, 0, 19)
	for range 5 {
		deltas = append(deltas, Delta{Question: "win", Value: 0.5, Repo: "r"})
	}
	for range 4 {
		deltas = append(deltas, Delta{Question: "loss", Value: -0.001, Repo: "r"})
	}
	for range 10 {
		deltas = append(deltas, Delta{Question: "tie", Value: 0, Repo: "r"})
	}
	got := Compare(deltas)
	if got.Wins != 5 || got.Losses != 4 || got.Ties != 10 {
		t.Fatalf("record = %d/%d/%d, want 5 win / 4 loss / 10 tie", got.Wins, got.Losses, got.Ties)
	}
	if got.Sign != 1 {
		t.Errorf("Sign = %.4f, want exactly 1: a 5-4 record is what the sign test cannot resolve", got.Sign)
	}
	if math.Abs(got.Wilcoxon-0.16015625) > tol {
		t.Errorf("Wilcoxon = %.10f, want 0.16015625", got.Wilcoxon)
	}
	if math.Abs(got.SignFlip-0.0625) > tol {
		t.Errorf("SignFlip = %.10f, want 0.0625", got.SignFlip)
	}
	if got.SignFlip >= got.Sign || got.Wilcoxon >= got.Sign {
		t.Errorf("magnitude-aware p-values %.4f/%.4f do not beat the sign test's %.4f", got.SignFlip, got.Wilcoxon, got.Sign)
	}
	if want := 0.13136842105263158; math.Abs(got.Mean-want) > 1e-12 {
		t.Errorf("Mean = %.17f, want %.17f", got.Mean, want)
	}
	if want := 0.05192517167396048; math.Abs(got.StdErr-want) > 1e-12 {
		t.Errorf("StdErr = %.17f, want %.17f", got.StdErr, want)
	}
	if lo, hi := got.Mean-normal95*got.StdErr, got.Mean+normal95*got.StdErr; got.CILow != lo || got.CIHigh != hi {
		t.Errorf("CI = [%.6f, %.6f], want [%.6f, %.6f]", got.CILow, got.CIHigh, lo, hi)
	}
}

// TestCompareCountsUntreatedApartFromTies pins the honesty fix: a question the
// ranker's graph lane never ran on is structural non-treatment, and folding it
// into the tie count reads as "the lane was tried and did nothing".
func TestCompareCountsUntreatedApartFromTies(t *testing.T) {
	deltas := []Delta{
		{Question: "moved", Value: 0.3},
		{Question: "moved back", Value: -0.2},
		{Question: "genuinely unchanged", Value: 0},
		{Question: "never walked", Value: 0, Untreated: true},
		{Question: "never walked either", Value: 0, Untreated: true},
	}
	got := Compare(deltas)
	if got.N != 5 || got.Untreated != 2 {
		t.Fatalf("Compare = n %d / untreated %d, want 5 and 2", got.N, got.Untreated)
	}
	if got.Wins+got.Losses+got.Ties != got.N {
		t.Errorf("wins+losses+ties = %d, want them to partition all %d deltas", got.Wins+got.Losses+got.Ties, got.N)
	}
	if got.Wins != 1 || got.Losses != 1 || got.Ties != 3 {
		t.Errorf("record = %d/%d/%d, want 1 win / 1 loss / 3 tie", got.Wins, got.Losses, got.Ties)
	}
}

func TestCompareOverNothing(t *testing.T) {
	got := Compare(nil)
	if got.N != 0 || got.Mean != 0 || got.StdErr != 0 {
		t.Errorf("Compare(nil) = %+v, want the zero comparison", got)
	}
	if got.Sign != 1 || got.Wilcoxon != 1 || got.SignFlip != 1 || got.Method != Exact {
		t.Errorf("Compare(nil) tests = %.1f/%.1f/%.1f (%s), want 1/1/1 exact", got.Sign, got.Wilcoxon, got.SignFlip, got.Method)
	}
}

func TestCompareSingleDeltaHasNoSpread(t *testing.T) {
	got := Compare([]Delta{{Question: "only", Value: 0.25}})
	if got.Mean != 0.25 || got.StdErr != 0 || got.CILow != 0.25 || got.CIHigh != 0.25 {
		t.Errorf("Compare = mean %v se %v CI [%v, %v], want 0.25 with no spread", got.Mean, got.StdErr, got.CILow, got.CIHigh)
	}
}

func uniform(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 1 / float64(n)
	}
	return out
}
