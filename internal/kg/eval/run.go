package eval

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
)

// MinSeeds is the smallest number of seeds a run accepts: a single number hides
// the spread that ranking ties and stochastic lanes introduce.
const MinSeeds = 5

// Errors returned when a run is misconfigured.
var (
	ErrNoQuestions  = errors.New("run has no questions")
	ErrNoRetrievers = errors.New("run has no retrievers")
	ErrInvalidK     = errors.New("k must be positive")
	ErrTooFewSeeds  = errors.New("run needs at least MinSeeds seeds")
)

// Config names one retriever under test. Build constructs a fresh retriever per
// seed, so a stochastic ranker threads the seed through its own randomness.
type Config struct {
	Name  string
	Build func(seed int64) Retriever
}

// Options configures a run: the cutoff k for the @k metrics, the score below
// which a result counts as abstention, and the seeds to repeat every
// configuration over.
type Options struct {
	K         int
	Threshold float64
	Seeds     []int64
}

// SeedRun is one configuration's aggregated metrics at one seed, overall and
// per category.
type SeedRun struct {
	Seed       int64
	Overall    Metrics
	Categories map[string]Metrics
}

// Stat is a metric's mean and sample standard deviation across seeds.
type Stat struct {
	Mean   float64
	StdDev float64
}

// MetricStats is a metric set summarized across seeds; the counts are the
// per-seed denominators, identical across seeds.
type MetricStats struct {
	Questions          int
	Graded             int
	LeakChecked        int
	AbstainQuestions   int
	NDCG               Stat
	Recall             Stat
	MRR                Stat
	LeakRate           Stat
	AbstentionAccuracy Stat
}

// Summary is one configuration's results across every seed.
type Summary struct {
	Name       string
	Overall    MetricStats
	Categories map[string]MetricStats
	Seeds      []SeedRun
}

// Report is a whole run: the options it ran under and one Summary per
// configuration, in configuration order.
type Report struct {
	K          int
	Threshold  float64
	Seeds      []int64
	Questions  int
	Categories []string
	Summaries  []Summary
}

// Run evaluates every configuration over every question at every seed and
// summarizes the result. Each seed builds a fresh retriever.
func Run(ctx context.Context, questions []Question, configs []Config, opts Options) (Report, error) {
	if len(questions) == 0 {
		return Report{}, ErrNoQuestions
	}
	if len(configs) == 0 {
		return Report{}, ErrNoRetrievers
	}
	if opts.K <= 0 {
		return Report{}, fmt.Errorf("%w: %d", ErrInvalidK, opts.K)
	}
	if len(opts.Seeds) < MinSeeds {
		return Report{}, fmt.Errorf("%w: got %d, want %d", ErrTooFewSeeds, len(opts.Seeds), MinSeeds)
	}
	report := Report{
		K:          opts.K,
		Threshold:  opts.Threshold,
		Seeds:      opts.Seeds,
		Questions:  len(questions),
		Categories: categories(questions),
	}
	for _, cfg := range configs {
		summary, err := runConfig(ctx, questions, cfg, opts, report.Categories)
		if err != nil {
			return Report{}, fmt.Errorf("retriever %s: %w", cfg.Name, err)
		}
		report.Summaries = append(report.Summaries, summary)
	}
	return report, nil
}

func runConfig(ctx context.Context, questions []Question, cfg Config, opts Options, cats []string) (Summary, error) {
	summary := Summary{Name: cfg.Name, Categories: map[string]MetricStats{}}
	for _, seed := range opts.Seeds {
		r := cfg.Build(seed)
		scores := make([]QuestionScore, len(questions))
		for i, q := range questions {
			results, err := r.Retrieve(ctx, q.Query, opts.K)
			if err != nil {
				return Summary{}, fmt.Errorf("seed %d question %s: %w", seed, q.ID, err)
			}
			scores[i] = ScoreQuestion(q, results, opts.K, opts.Threshold)
		}
		run := SeedRun{Seed: seed, Overall: Aggregate(scores), Categories: map[string]Metrics{}}
		for _, cat := range cats {
			run.Categories[cat] = Aggregate(byCategory(scores, cat))
		}
		summary.Seeds = append(summary.Seeds, run)
	}
	overall := make([]Metrics, len(summary.Seeds))
	for i, run := range summary.Seeds {
		overall[i] = run.Overall
	}
	summary.Overall = summarize(overall)
	for _, cat := range cats {
		per := make([]Metrics, len(summary.Seeds))
		for i, run := range summary.Seeds {
			per[i] = run.Categories[cat]
		}
		summary.Categories[cat] = summarize(per)
	}
	return summary, nil
}

func byCategory(scores []QuestionScore, cat string) []QuestionScore {
	var out []QuestionScore
	for _, s := range scores {
		if s.Category == cat {
			out = append(out, s)
		}
	}
	return out
}

func categories(questions []Question) []string {
	var out []string
	for _, q := range questions {
		if !slices.Contains(out, q.Category) {
			out = append(out, q.Category)
		}
	}
	slices.Sort(out)
	return out
}

func summarize(runs []Metrics) MetricStats {
	stats := MetricStats{
		Questions:        runs[0].Questions,
		Graded:           runs[0].Graded,
		LeakChecked:      runs[0].LeakChecked,
		AbstainQuestions: runs[0].AbstainQuestions,
	}
	stats.NDCG = stat(runs, func(m Metrics) float64 { return m.NDCG })
	stats.Recall = stat(runs, func(m Metrics) float64 { return m.Recall })
	stats.MRR = stat(runs, func(m Metrics) float64 { return m.MRR })
	stats.LeakRate = stat(runs, func(m Metrics) float64 { return m.LeakRate })
	stats.AbstentionAccuracy = stat(runs, func(m Metrics) float64 { return m.AbstentionAccuracy })
	return stats
}

// stat computes the mean and the sample standard deviation over the seeds.
func stat(runs []Metrics, pick func(Metrics) float64) Stat {
	total := 0.0
	for _, m := range runs {
		total += pick(m)
	}
	avg := total / float64(len(runs))
	sumsq := 0.0
	for _, m := range runs {
		d := pick(m) - avg
		sumsq += d * d
	}
	return Stat{Mean: avg, StdDev: math.Sqrt(sumsq / float64(len(runs)-1))}
}
