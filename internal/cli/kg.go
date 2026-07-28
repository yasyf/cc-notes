package cli

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/yasyf/cc-notes/internal/ccnhome"
	"github.com/yasyf/cc-notes/internal/kg"
	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/internal/kg/rank"
	"github.com/yasyf/cc-notes/internal/kg/stale"
	"github.com/yasyf/cc-notes/internal/render"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
	"github.com/yasyf/cc-notes/notes"
)

// Where a kg command's graph came from: the build that just wrote it, a stored
// index folded from the repository's current ref tips, or the in-process
// rebuild a read falls back to when no stored index matches those tips.
const (
	kgIndexWritten = "written"
	kgIndexHit     = "hit"
	kgIndexMiss    = "miss"
)

func newKGCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kg",
		Short: "Derived knowledge graph over refs/cc-notes/*: build it, inspect it, query it, assess staleness",
		Args:  noUnknownSubcommand,
		RunE:  runHelp,
	}
	cmd.AddCommand(newKGBuildCmd(), newKGStatCmd(), newKGQueryCmd(), newKGStaleCmd())
	return cmd
}

func newKGBuildCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Fold refs/cc-notes/* into the knowledge graph and persist it under the per-user state root",
		Long: `Fold every live entity under refs/cc-notes/ into the knowledge graph and store
it at ~/.cc-notes/repos/<key>/graph-v1, keyed by the repository's git common
directory so every linked worktree shares one index (CC_NOTES_HOME overrides
the root). Nothing lands on refs/cc-notes/* or inside the git directory: the
graph is derived state, rebuilt rather than synced, and it carries the digest
of the ref tips it was folded from, so a later read either matches those tips
exactly or misses. The report is the same one "kg stat" prints.`,
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			s, err := openStore(cmd)
			if err != nil {
				return err
			}
			root, err := s.Git.Root(ctx)
			if err != nil {
				return err
			}
			repo, err := ccnhome.RecordWorktree(s.CommonDir(), root)
			if err != nil {
				return err
			}
			g, err := kg.Build(ctx, s)
			if err != nil {
				return err
			}
			kg.Save(repo.Graph(), g)
			// Save is best-effort and reports nothing, so the writer reads its
			// own write back rather than claim a persistence that never landed.
			index, ok := kg.Load(repo.Graph(), g.Source)
			if !ok {
				return fmt.Errorf("persist graph to %s: the stored index does not read back", repo.Graph())
			}
			if err := index.Close(); err != nil {
				return err
			}
			return printKGStat(cmd, newKGStatDTO(g, repo.Graph(), kgIndexWritten), jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

func newKGStatCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "stat",
		Short: "Report the graph's node counts by node kind, edge counts by edge kind, and totals",
		Long: `Report the knowledge graph: node counts per node kind, edge counts per edge
kind, the lifecycle-event total, and when the graph was built. The stored index
answers when it was folded from the repository's current ref tips; any other
stored graph — absent, unreadable, or built from older tips — is a miss, which
the report names in its index line and announces on stderr before folding the
graph in process to answer. A read never writes: only "kg build" persists.`,
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := openStore(cmd)
			if err != nil {
				return err
			}
			loaded, err := loadKGGraph(cmd, s)
			if err != nil {
				return err
			}
			return printKGStat(cmd, newKGStatDTO(loaded.graph, loaded.dir, loaded.provenance), jsonOut)
		},
	}
	bindJSON(cmd.Flags(), &jsonOut)
	return cmd
}

// kgQueryOptions collects the query command's flags. branch and paths seed the
// graph lane with where the agent is standing; explain adds the per-lane scores
// and staleness cause behind each hit.
type kgQueryOptions struct {
	branch  string
	paths   []string
	limit   int
	explain bool
	jsonOut bool
}

func newKGQueryCmd() *cobra.Command {
	var opts kgQueryOptions
	cmd := &cobra.Command{
		Use:   "query QUERY",
		Short: "Rank the cc-notes corpus against a query, fusing the lexical and graph lanes",
		Long: `Rank every cc-notes record against QUERY over two lanes — BM25 across the
anchor-enriched record text, and a personalized walk over the knowledge graph —
fused by weighted reciprocal rank, gated by the staleness assessment, and
diversified. Every result names the lane that produced it ("lex", "graph",
"lex+graph", suffixed "/SIGNAL" when the record carries a staleness signal);
--explain adds each lane's raw score and the cause behind that signal, before
the title so the title stays the trailing field. No language model is called:
every score is read from the corpus text or the graph's shape.

The graph lane walks out from where the query is asked: --branch, defaulting to
the current HEAD branch, plus every --path. That position only reorders a
ranking — the query's own words are what admit a record, through a term the
corpus holds or an address the graph does. A query that reaches neither lane
returns nothing.`,
		Args: exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runKGQuery(cmd, args[0], opts)
		},
	}
	flags := cmd.Flags()
	flags.StringVar(&opts.branch, "branch", "", "branch the query is asked from, seeding the graph lane (default: current HEAD branch)")
	flags.StringArrayVar(&opts.paths, "path", nil, "repository path the query is asked from, seeding the graph lane (repeatable)")
	bindLimit(flags, &opts.limit, 10)
	flags.BoolVar(&opts.explain, "explain", false, "add each lane's raw score and the staleness cause behind every result")
	bindJSON(flags, &opts.jsonOut)
	return cmd
}

func runKGQuery(cmd *cobra.Command, query string, opts kgQueryOptions) error {
	ctx := cmd.Context()
	s, c, err := openStoreClient(cmd)
	if err != nil {
		return err
	}
	branch, _, err := resolveBranchOrBacklog(ctx, s, opts.branch, cmd.Flags().Changed("branch"))
	if err != nil {
		return err
	}
	corpus, err := eval.LoadCorpus(ctx, c)
	if err != nil {
		return err
	}
	loaded, err := loadKGGraph(cmd, s)
	if err != nil {
		return err
	}
	assessments, err := assessKGStale(cmd, c)
	if err != nil {
		return err
	}
	options := rank.DefaultOptions()
	options.Session = rank.Session{Branch: branch, Paths: opts.paths}
	ranker := rank.New(corpus, loaded.graph, assessments, options)

	limit := opts.limit
	if limit <= 0 {
		limit = len(corpus)
	}
	results, err := ranker.Retrieve(ctx, query, limit)
	if err != nil {
		return err
	}
	var lanes rank.Lanes
	if opts.explain {
		if lanes, err = ranker.Lanes(ctx, query); err != nil {
			return err
		}
	}
	return printKGResults(cmd, newKGResultDTOs(results, corpus, assessments, lanes, opts.explain), opts.jsonOut)
}

func newKGStaleCmd() *cobra.Command {
	var jsonOut, explain bool
	cmd := &cobra.Command{
		Use:   "stale",
		Short: "Assess every record's staleness: the gate and penalty distribution by kind and by signal",
		Long: `Assess every cc-notes record against the staleness signals a retrieval ranker
gates and demotes with, and report the distribution: per kind how many records
are gated, fresh, penalized, queued for re-verification, or promoted, plus each
signal's count and whether it gates outright or only costs rank. --explain adds
one row per record a signal fired on, naming the signal and its cause. The
verdict is read through the staleness evaluator, which composes the notes-side
freshness verdict rather than recomputing it.`,
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := openClient(cmd)
			if err != nil {
				return err
			}
			assessments, err := assessKGStale(cmd, c)
			if err != nil {
				return err
			}
			return printKGStale(cmd, newKGStaleDTO(assessments, explain), explain, jsonOut)
		},
	}
	flags := cmd.Flags()
	flags.BoolVar(&explain, "explain", false, "list every record a signal fired on, naming the signal and its cause")
	bindJSON(flags, &jsonOut)
	return cmd
}

// kgGraph is a graph a read command answered from, and where it came from.
type kgGraph struct {
	graph      *kg.Graph
	dir        string
	provenance string
}

// loadKGGraph reads the stored index when it was folded from the repository's
// current ref tips, and otherwise folds the graph in process — announcing the
// miss on stderr and discarding the result when the command exits, since only
// "kg build" writes.
func loadKGGraph(cmd *cobra.Command, s *store.Store) (kgGraph, error) {
	ctx := cmd.Context()
	repo, err := ccnhome.ForRepo(s.CommonDir())
	if err != nil {
		return kgGraph{}, err
	}
	source, err := kg.SourceDigest(ctx, s)
	if err != nil {
		return kgGraph{}, err
	}
	dir := repo.Graph()
	if index, ok := kg.Load(dir, source); ok {
		g, decoded := index.Graph()
		if err := index.Close(); err != nil {
			return kgGraph{}, err
		}
		if decoded {
			return kgGraph{graph: g, dir: dir, provenance: kgIndexHit}, nil
		}
	}
	_, _ = fmt.Fprintf(cmd.ErrOrStderr(),
		"cc-notes: no stored graph at %s matches the current refs; folding one in process (run `cc-notes kg build` to persist it)\n", dir)
	g, err := kg.Build(ctx, s)
	if err != nil {
		return kgGraph{}, err
	}
	return kgGraph{graph: g, dir: dir, provenance: kgIndexMiss}, nil
}

// assessKGStale evaluates the whole corpus against the repository's own
// configured staleness thresholds.
func assessKGStale(cmd *cobra.Command, c *notes.Client) ([]stale.Assessment, error) {
	ctx := cmd.Context()
	dir, err := repoDir(cmd)
	if err != nil {
		return nil, err
	}
	policy, err := stale.DefaultPolicy(ctx, c, time.Now())
	if err != nil {
		return nil, err
	}
	return stale.New(c, dir, policy).Assess(ctx)
}

// kgStatDTO fixes the JSON field order for a graph report: where the graph came
// from and the directory the stored index lives in, when it was built, the
// per-kind node and edge counts, and the totals.
type kgStatDTO struct {
	Index   string       `json:"index"`
	Path    string       `json:"path"`
	BuiltAt string       `json:"built_at"`
	Nodes   []kgCountDTO `json:"nodes"`
	Edges   []kgCountDTO `json:"edges"`
	Totals  kgTotalsDTO  `json:"totals"`
}

// kgCountDTO is one kind's population of the graph.
type kgCountDTO struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// kgTotalsDTO is the graph's size across every kind.
type kgTotalsDTO struct {
	Nodes  int `json:"nodes"`
	Edges  int `json:"edges"`
	Events int `json:"events"`
}

func newKGStatDTO(g *kg.Graph, dir, provenance string) kgStatDTO {
	nodes := map[kg.NodeKind]int{}
	for _, n := range g.Nodes {
		nodes[n.Kind]++
	}
	edges := map[kg.EdgeKind]int{}
	for _, e := range g.Edges {
		edges[e.Kind]++
	}
	return kgStatDTO{
		Index:   provenance,
		Path:    dir,
		BuiltAt: render.RFC3339(g.BuiltAt),
		Nodes:   kgCountDTOs(nodes),
		Edges:   kgCountDTOs(edges),
		Totals:  kgTotalsDTO{Nodes: len(g.Nodes), Edges: len(g.Edges), Events: len(g.Events)},
	}
}

func kgCountDTOs[K ~string](counts map[K]int) []kgCountDTO {
	out := make([]kgCountDTO, 0, len(counts))
	for _, kind := range slices.Sorted(maps.Keys(counts)) {
		out = append(out, kgCountDTO{Kind: string(kind), Count: counts[kind]})
	}
	return out
}

func printKGStat(cmd *cobra.Command, dto kgStatDTO, jsonOut bool) error {
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), dto)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "index\t%s\t%s\n", dto.Index, dto.Path)
	fmt.Fprintf(&b, "built\t%s\n", dto.BuiltAt)
	b.WriteString("nodes\n")
	for _, c := range dto.Nodes {
		fmt.Fprintf(&b, "  %s\t%d\n", c.Kind, c.Count)
	}
	b.WriteString("edges\n")
	for _, c := range dto.Edges {
		fmt.Fprintf(&b, "  %s\t%d\n", c.Kind, c.Count)
	}
	fmt.Fprintf(&b, "nodes: %d total across %d kinds\n", dto.Totals.Nodes, len(dto.Nodes))
	fmt.Fprintf(&b, "edges: %d total across %d kinds\n", dto.Totals.Edges, len(dto.Edges))
	fmt.Fprintf(&b, "events: %d total\n", dto.Totals.Events)
	_, err := fmt.Fprint(cmd.OutOrStdout(), b.String())
	return err
}

// kgResultDTO is one ranked hit: the entity's full id, kind, and title, the
// composed relevance, and the lane attribution that says why it ranked — the
// lanes that produced it, suffixed with the staleness signal the surfaced
// record carries. Explain is present only under --explain.
type kgResultDTO struct {
	ID      string        `json:"id"`
	Kind    string        `json:"kind"`
	Title   string        `json:"title"`
	Score   float64       `json:"score"`
	Lane    string        `json:"lane"`
	Explain *kgExplainDTO `json:"explain,omitempty"`
}

// kgExplainDTO is the evidence behind one hit: each lane's raw score before
// fusion — BM25 relevance and the graph walk's mass lift — and the staleness
// signal and cause the lane attribution names.
type kgExplainDTO struct {
	Lexical float64 `json:"lexical"`
	Graph   float64 `json:"graph"`
	Signal  string  `json:"signal,omitempty"`
	Detail  string  `json:"detail,omitempty"`
}

func newKGResultDTOs(results []eval.Result, corpus []eval.Entity, assessments []stale.Assessment, lanes rank.Lanes, explain bool) []kgResultDTO {
	entities := make(map[model.EntityID]eval.Entity, len(corpus))
	for _, e := range corpus {
		entities[e.ID] = e
	}
	assessed := stale.Index(assessments)
	out := make([]kgResultDTO, len(results))
	for i, r := range results {
		entity := entities[r.ID]
		out[i] = kgResultDTO{ID: string(r.ID), Kind: string(entity.Kind), Title: entity.Title, Score: r.Score, Lane: r.Lane}
		if explain {
			a := assessed[r.ID]
			out[i].Explain = &kgExplainDTO{
				Lexical: lanes.Lexical[r.ID],
				Graph:   lanes.Graph[r.ID],
				Signal:  string(a.Signal),
				Detail:  a.Detail,
			}
		}
	}
	return out
}

// printKGResults writes the ranking as kgResultDTOs in JSON, or as lean lines:
// <short7>\t<kind>\t<score>\t<lane>\t<title>, with each lane's raw score and the
// staleness cause inserted before the title under --explain. The title is a
// record's own text and may hold a tab, so it stays the trailing field and an
// embedded tab only widens it; every cause the staleness evaluator writes is
// built from bounded phrases, identifiers, branch names, and short ids, none of
// which can carry one.
func printKGResults(cmd *cobra.Command, dtos []kgResultDTO, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		return printJSON(out, dtos)
	}
	for _, d := range dtos {
		line := fmt.Sprintf("%s\t%s\t%.3f\t%s", model.EntityID(d.ID).Short(), d.Kind, d.Score, d.Lane)
		if d.Explain != nil {
			line += fmt.Sprintf("\tlex=%.3f\tgraph=%.3f\t%s", d.Explain.Lexical, d.Explain.Graph, orDash(d.Explain.Detail))
		}
		if _, err := fmt.Fprintln(out, line+"\t"+d.Title); err != nil {
			return err
		}
	}
	return nil
}

// kgStaleDTO fixes the JSON field order for a staleness report: the corpus-wide
// verdict counts, the per-kind and per-signal breakdowns, and — under --explain
// — one row per record a signal fired on.
type kgStaleDTO struct {
	Total     int                `json:"total"`
	Gated     int                `json:"gated"`
	Fresh     int                `json:"fresh"`
	Penalized int                `json:"penalized"`
	Reverify  int                `json:"reverify"`
	Promoted  int                `json:"promoted"`
	Kinds     []kgStaleKindDTO   `json:"kinds"`
	Signals   []kgStaleSignalDTO `json:"signals"`
	Explain   []kgStaleRecordDTO `json:"explain,omitempty"`
}

// kgStaleKindDTO is one entity kind's verdict distribution and mean rank weight.
type kgStaleKindDTO struct {
	Kind       string  `json:"kind"`
	Total      int     `json:"total"`
	Gated      int     `json:"gated"`
	Fresh      int     `json:"fresh"`
	Penalized  int     `json:"penalized"`
	Reverify   int     `json:"reverify"`
	Promoted   int     `json:"promoted"`
	MeanWeight float64 `json:"mean_weight"`
}

// kgStaleSignalDTO is how often one signal fired and what it costs a record:
// "gate" withholds it from injection outright, "penalty" only reorders.
type kgStaleSignalDTO struct {
	Signal string `json:"signal"`
	Count  int    `json:"count"`
	Role   string `json:"role"`
}

// kgStaleRecordDTO is one record's verdict: the triggering signal and its cause,
// the rank weight that survives, and every penalty charged against it.
type kgStaleRecordDTO struct {
	ID        string              `json:"id"`
	Kind      string              `json:"kind"`
	Gated     bool                `json:"gated"`
	Weight    float64             `json:"weight"`
	Signal    string              `json:"signal"`
	Detail    string              `json:"detail,omitempty"`
	Verdict   string              `json:"verdict,omitempty"`
	Reverify  bool                `json:"reverify"`
	Promoted  bool                `json:"promoted"`
	Penalties []kgStalePenaltyDTO `json:"penalties"`
}

// kgStalePenaltyDTO is one rank demotion: the signal, its multiplicative weight,
// and the cause in words.
type kgStalePenaltyDTO struct {
	Signal string  `json:"signal"`
	Weight float64 `json:"weight"`
	Detail string  `json:"detail"`
}

func newKGStaleDTO(assessments []stale.Assessment, explain bool) kgStaleDTO {
	dto := kgStaleDTO{Total: len(assessments), Reverify: len(stale.ReverifyQueue(assessments))}
	kinds := map[model.Kind]*kgStaleKindDTO{}
	weights := map[model.Kind]float64{}
	gates := map[stale.Signal]int{}
	penalties := map[stale.Signal]int{}
	for _, a := range assessments {
		kind, seen := kinds[a.Kind]
		if !seen {
			kind = &kgStaleKindDTO{Kind: string(a.Kind)}
			kinds[a.Kind] = kind
		}
		kind.Total++
		weights[a.Kind] += a.Weight
		switch {
		case a.Gated:
			dto.Gated, kind.Gated = dto.Gated+1, kind.Gated+1
			gates[a.Signal]++
		case len(a.Penalties) == 0:
			dto.Fresh, kind.Fresh = dto.Fresh+1, kind.Fresh+1
		default:
			dto.Penalized, kind.Penalized = dto.Penalized+1, kind.Penalized+1
			for _, p := range a.Penalties {
				penalties[p.Signal]++
			}
		}
		if a.Reverify {
			kind.Reverify++
		}
		if a.Promoted {
			dto.Promoted, kind.Promoted = dto.Promoted+1, kind.Promoted+1
		}
	}
	for _, k := range model.Kinds() {
		kind, seen := kinds[k]
		if !seen {
			continue
		}
		kind.MeanWeight = weights[k] / float64(kind.Total)
		dto.Kinds = append(dto.Kinds, *kind)
	}
	dto.Signals = append(kgStaleSignalDTOs(gates, "gate"), kgStaleSignalDTOs(penalties, "penalty")...)
	if explain {
		dto.Explain = kgStaleRecordDTOs(assessments)
	}
	return dto
}

// kgStaleSignalDTOs sorts one role's signal counts by name. The role is read
// from where the signal was counted — a gate verdict or a rank penalty — rather
// than from a second table of which signal means which.
func kgStaleSignalDTOs(counts map[stale.Signal]int, role string) []kgStaleSignalDTO {
	out := make([]kgStaleSignalDTO, 0, len(counts))
	for _, signal := range slices.Sorted(maps.Keys(counts)) {
		out = append(out, kgStaleSignalDTO{Signal: string(signal), Count: counts[signal], Role: role})
	}
	return out
}

func kgStaleRecordDTOs(assessments []stale.Assessment) []kgStaleRecordDTO {
	out := make([]kgStaleRecordDTO, 0, len(assessments))
	for _, a := range assessments {
		if a.Signal == "" {
			continue
		}
		row := kgStaleRecordDTO{
			ID: string(a.ID), Kind: string(a.Kind), Gated: a.Gated, Weight: a.Weight,
			Signal: string(a.Signal), Detail: a.Detail, Verdict: string(a.Verdict),
			Reverify: a.Reverify, Promoted: a.Promoted,
			Penalties: make([]kgStalePenaltyDTO, len(a.Penalties)),
		}
		for i, p := range a.Penalties {
			row.Penalties[i] = kgStalePenaltyDTO{Signal: string(p.Signal), Weight: p.Weight, Detail: p.Detail}
		}
		out = append(out, row)
	}
	return out
}

func printKGStale(cmd *cobra.Command, dto kgStaleDTO, explain, jsonOut bool) error {
	if jsonOut {
		return printJSON(cmd.OutOrStdout(), dto)
	}
	var b strings.Builder
	b.WriteString("kinds\n")
	for _, k := range dto.Kinds {
		fmt.Fprintf(&b, "  %s\t%d total\t%d gated\t%d fresh\t%d penalized\t%d reverify\t%d promoted\t%.2f weight\n",
			k.Kind, k.Total, k.Gated, k.Fresh, k.Penalized, k.Reverify, k.Promoted, k.MeanWeight)
	}
	b.WriteString("signals\n")
	for _, s := range dto.Signals {
		fmt.Fprintf(&b, "  %s\t%d\t%s\n", s.Signal, s.Count, s.Role)
	}
	if explain {
		b.WriteString("records\n")
		for _, r := range dto.Explain {
			fmt.Fprintf(&b, "  %s\t%s\t%s\t%.2f\t%s\t%s\n",
				model.EntityID(r.ID).Short(), r.Kind, gatedFlag(r.Gated), r.Weight, r.Signal, orDash(r.Detail))
		}
	}
	fmt.Fprintf(&b, "records: %d total, %d gated, %d penalized, %d fresh\n", dto.Total, dto.Gated, dto.Penalized, dto.Fresh)
	fmt.Fprintf(&b, "re-verify queue: %d records\n", dto.Reverify)
	_, err := fmt.Fprint(cmd.OutOrStdout(), b.String())
	return err
}

// gatedFlag renders a staleness verdict as the report's withheld/kept column.
func gatedFlag(gated bool) string {
	if gated {
		return "gated"
	}
	return "kept"
}
