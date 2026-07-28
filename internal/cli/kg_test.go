package cli_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/ccnhome"
	"github.com/yasyf/cc-notes/internal/cli"
	"github.com/yasyf/cc-notes/internal/gittest"
)

// kgCountJSON mirrors one per-kind population row of a kg stat report.
type kgCountJSON struct {
	Kind  string `json:"kind"`
	Count int    `json:"count"`
}

// kgStatJSON mirrors the kg build / kg stat --json DTO.
type kgStatJSON struct {
	Index   string        `json:"index"`
	Path    string        `json:"path"`
	BuiltAt string        `json:"built_at"`
	Nodes   []kgCountJSON `json:"nodes"`
	Edges   []kgCountJSON `json:"edges"`
	Totals  struct {
		Nodes  int `json:"nodes"`
		Edges  int `json:"edges"`
		Events int `json:"events"`
	} `json:"totals"`
}

// kgResultJSON mirrors one ranked hit of kg query --json.
type kgResultJSON struct {
	ID      string  `json:"id"`
	Kind    string  `json:"kind"`
	Title   string  `json:"title"`
	Score   float64 `json:"score"`
	Lane    string  `json:"lane"`
	Explain *struct {
		Lexical float64 `json:"lexical"`
		Graph   float64 `json:"graph"`
		Signal  string  `json:"signal"`
		Detail  string  `json:"detail"`
	} `json:"explain"`
}

// kgStaleJSON mirrors the kg stale --json DTO.
type kgStaleJSON struct {
	Total     int `json:"total"`
	Gated     int `json:"gated"`
	Fresh     int `json:"fresh"`
	Penalized int `json:"penalized"`
	Reverify  int `json:"reverify"`
	Promoted  int `json:"promoted"`
	Kinds     []struct {
		Kind       string  `json:"kind"`
		Total      int     `json:"total"`
		Gated      int     `json:"gated"`
		Fresh      int     `json:"fresh"`
		Penalized  int     `json:"penalized"`
		MeanWeight float64 `json:"mean_weight"`
	} `json:"kinds"`
	Signals []struct {
		Signal string `json:"signal"`
		Count  int    `json:"count"`
		Role   string `json:"role"`
	} `json:"signals"`
	Explain []struct {
		ID        string  `json:"id"`
		Kind      string  `json:"kind"`
		Gated     bool    `json:"gated"`
		Weight    float64 `json:"weight"`
		Signal    string  `json:"signal"`
		Detail    string  `json:"detail"`
		Penalties []struct {
			Signal string  `json:"signal"`
			Weight float64 `json:"weight"`
			Detail string  `json:"detail"`
		} `json:"penalties"`
	} `json:"explain"`
}

// initKGRepo is initRepo plus an isolated CC_NOTES_HOME, so a kg command's
// derived index lands in the test's own temp root even when the ambient
// environment points the state root somewhere else.
func initKGRepo(t *testing.T) string {
	t.Helper()
	dir := initRepo(t)
	t.Setenv(ccnhome.Env, t.TempDir())
	return dir
}

// kgGraphDir resolves where the repository's index belongs, without creating it.
func kgGraphDir(t *testing.T, dir string) string {
	t.Helper()
	_, commonDir := gittest.Dirs(t, dir)
	repo, err := ccnhome.ForRepo(commonDir)
	if err != nil {
		t.Fatalf("ForRepo: %v", err)
	}
	return repo.Graph()
}

func countKind(counts []kgCountJSON, kind string) int {
	for _, c := range counts {
		if c.Kind == kind {
			return c.Count
		}
	}
	return 0
}

func TestKGBuildPersistsOutsideTheRepository(t *testing.T) {
	dir := initKGRepo(t)
	commitFile(t, dir, "internal/parser/lex.go", "package parser\n")
	mustRun(t, dir, "note", "add", "The lexer owns whitespace", "--path", "internal/parser/lex.go")
	mustRun(t, dir, "task", "add", "Rewrite the lexer", "--no-validation-criteria")

	refsBefore := gittest.Git(t, dir, "for-each-ref", "--format=%(refname)", "refs/cc-notes/")
	stat := mustJSON[kgStatJSON](t, mustRun(t, dir, "kg", "build", "--json"))

	if stat.Index != "written" {
		t.Fatalf("index = %q, want written", stat.Index)
	}
	graphDir := kgGraphDir(t, dir)
	if stat.Path != graphDir {
		t.Fatalf("path = %q, want %q", stat.Path, graphDir)
	}
	if _, err := os.Stat(filepath.Join(graphDir, "graph.db")); err != nil {
		t.Fatalf("stat graph.db: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(graphDir), "repo.json")); err != nil {
		t.Fatalf("stat repo.json descriptor: %v", err)
	}
	if got, want := countKind(stat.Nodes, "note"), 1; got != want {
		t.Fatalf("note nodes = %d, want %d", got, want)
	}
	if got, want := countKind(stat.Nodes, "task"), 1; got != want {
		t.Fatalf("task nodes = %d, want %d", got, want)
	}
	if got := countKind(stat.Nodes, "path"); got == 0 {
		t.Fatal("path nodes = 0, want the anchored file")
	}
	if got := countKind(stat.Edges, "anchor"); got == 0 {
		t.Fatal("anchor edges = 0, want the note's path anchor")
	}

	// Criterion: no derived index reaches refs/cc-notes/* or the git directory.
	if refsAfter := gittest.Git(t, dir, "for-each-ref", "--format=%(refname)", "refs/cc-notes/"); refsAfter != refsBefore {
		t.Fatalf("refs/cc-notes/* changed across kg build:\nbefore %q\nafter  %q", refsBefore, refsAfter)
	}
	_, commonDir := gittest.Dirs(t, dir)
	if strings.HasPrefix(graphDir, commonDir) {
		t.Fatalf("graph dir %s sits inside the git dir %s", graphDir, commonDir)
	}
	walkAssertNoGraphDB(t, commonDir)
}

func walkAssertNoGraphDB(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, _ os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if filepath.Base(path) == "graph.db" {
			t.Errorf("graph.db written inside the git dir at %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func TestKGStatSaysWhetherTheStoredIndexMatched(t *testing.T) {
	dir := initKGRepo(t)
	mustRun(t, dir, "note", "add", "Fold order is deterministic")

	cases := []struct {
		name  string
		setup func(t *testing.T)
		want  string
	}{
		{name: "no index yet", setup: func(*testing.T) {}, want: "miss"},
		{name: "built from the current tips", setup: func(t *testing.T) { mustRun(t, dir, "kg", "build") }, want: "hit"},
		{name: "tips moved since the build", setup: func(t *testing.T) {
			mustRun(t, dir, "note", "add", "A second note moves the tips")
		}, want: "miss"},
		{name: "rebuilt onto the new tips", setup: func(t *testing.T) { mustRun(t, dir, "kg", "build") }, want: "hit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)
			stdout, stderr, err := runCLI(t, dir, "kg", "stat", "--json")
			if err != nil {
				t.Fatalf("kg stat: %v (stderr %q)", err, stderr)
			}
			stat := mustJSON[kgStatJSON](t, stdout)
			if stat.Index != tc.want {
				t.Fatalf("index = %q, want %q", stat.Index, tc.want)
			}
			advised := strings.Contains(stderr, "no stored graph")
			if advised != (tc.want == "miss") {
				t.Fatalf("stderr = %q, want a miss advisory only on a miss (index %q)", stderr, stat.Index)
			}
		})
	}
}

func TestKGStatCountsByKindSumToTotals(t *testing.T) {
	dir := initKGRepo(t)
	commitFile(t, dir, "internal/store/store.go", "package store\n")
	mustRun(t, dir, "note", "add", "Store owns CAS", "--path", "internal/store/store.go", "--label", "store")
	mustRun(t, dir, "note", "add", "Second store note", "--path", "internal/store/store.go")
	mustRun(t, dir, "doc", "add", "Store guide", "--when", "editing the store", "--body", "read store.go first")
	mustRun(t, dir, "kg", "build")

	stat := mustJSON[kgStatJSON](t, mustRun(t, dir, "kg", "stat", "--json"))
	nodes, edges := 0, 0
	for _, c := range stat.Nodes {
		nodes += c.Count
	}
	for _, c := range stat.Edges {
		edges += c.Count
	}
	if nodes != stat.Totals.Nodes {
		t.Fatalf("node kinds sum to %d, totals say %d", nodes, stat.Totals.Nodes)
	}
	if edges != stat.Totals.Edges {
		t.Fatalf("edge kinds sum to %d, totals say %d", edges, stat.Totals.Edges)
	}
	if got, want := countKind(stat.Nodes, "note"), 2; got != want {
		t.Fatalf("note nodes = %d, want %d", got, want)
	}
	if got, want := countKind(stat.Nodes, "doc"), 1; got != want {
		t.Fatalf("doc nodes = %d, want %d", got, want)
	}

	text := mustRun(t, dir, "kg", "stat")
	for _, want := range []string{"index\thit\t", "  note\t2\n", "nodes: ", "edges: ", "events: "} {
		if !strings.Contains(text, want) {
			t.Fatalf("kg stat text = %q, want a %q row", text, want)
		}
	}
}

func TestKGQueryAttributesEveryResultToALane(t *testing.T) {
	dir := initKGRepo(t)
	commitFile(t, dir, "internal/parser/lex.go", "package parser\n")
	mustRun(t, dir, "note", "add", "The lexer normalizes whitespace before tokenizing",
		"--path", "internal/parser/lex.go", "--body", "whitespace collapses to a single token boundary")
	mustRun(t, dir, "note", "add", "Unrelated: release tags are GPG signed")
	mustRun(t, dir, "kg", "build")

	results := mustJSON[[]kgResultJSON](t, mustRun(t, dir, "kg", "query", "whitespace tokenizing", "--json"))
	if len(results) == 0 {
		t.Fatal("kg query returned nothing for a term the corpus carries")
	}
	for _, r := range results {
		if r.Lane == "" {
			t.Fatalf("result %s carries no lane attribution", r.ID)
		}
		if r.Explain != nil {
			t.Fatalf("result %s carries an explain block without --explain", r.ID)
		}
		if r.Kind == "" || r.Title == "" {
			t.Fatalf("result %+v is missing its kind or title", r)
		}
	}
	if top := results[0]; !strings.Contains(top.Title, "lexer normalizes whitespace") {
		t.Fatalf("top hit = %q, want the whitespace note", top.Title)
	}

	explained := mustJSON[[]kgResultJSON](t, mustRun(t, dir, "kg", "query", "whitespace tokenizing", "--explain", "--json"))
	if len(explained) != len(results) {
		t.Fatalf("--explain changed the result count: %d vs %d", len(explained), len(results))
	}
	if explained[0].Explain == nil {
		t.Fatal("--explain produced no explain block")
	}
	if explained[0].Explain.Lexical <= 0 {
		t.Fatalf("lexical lane score = %v, want the BM25 score behind the top hit", explained[0].Explain.Lexical)
	}

	line, _, err := runCLI(t, dir, "kg", "query", "whitespace tokenizing", "--limit", "1")
	if err != nil {
		t.Fatalf("kg query: %v", err)
	}
	fields := strings.Split(strings.TrimSuffix(line, "\n"), "\t")
	if len(fields) != 5 {
		t.Fatalf("lean line %q split into %d fields, want 5", line, len(fields))
	}
	if fields[0] != results[0].ID[:7] || fields[1] != "note" || fields[3] != results[0].Lane {
		t.Fatalf("lean line %q disagrees with the JSON result %+v", line, results[0])
	}
}

// TestKGQueryExplainKeepsTheTitleLast pins the lean-line convention against a
// title carrying a tab: every machine field sits at a fixed index and only the
// trailing free text widens, so a consumer reading lex= by index never reads
// the record's own words instead.
func TestKGQueryExplainKeepsTheTitleLast(t *testing.T) {
	dir := initKGRepo(t)
	// The branch-reconciliation signal resolves trunk, so the fixture needs a commit.
	commitFile(t, dir, "internal/release/tag.go", "package release\n")
	const title = "alpha\tbeta gpg"
	mustRun(t, dir, "note", "add", title, "--body", "release tags are signed")
	mustRun(t, dir, "kg", "build")

	line, _, err := runCLI(t, dir, "kg", "query", "gpg", "--explain", "--limit", "1")
	if err != nil {
		t.Fatalf("kg query: %v", err)
	}
	results := mustJSON[[]kgResultJSON](t, mustRun(t, dir, "kg", "query", "gpg", "--explain", "--limit", "1", "--json"))
	if len(results) != 1 {
		t.Fatalf("kg query --json = %+v, want the one note", results)
	}
	fields := strings.Split(strings.TrimSuffix(line, "\n"), "\t")
	want := []string{
		results[0].ID[:7],
		"note",
		fmt.Sprintf("%.3f", results[0].Score),
		results[0].Lane,
		fmt.Sprintf("lex=%.3f", results[0].Explain.Lexical),
		fmt.Sprintf("graph=%.3f", results[0].Explain.Graph),
		results[0].Explain.Detail,
		"alpha",
		"beta gpg",
	}
	if !slices.Equal(fields, want) {
		t.Fatalf("lean line fields = %q, want %q", fields, want)
	}
}

// TestKGQueryAbstainsOnAQueryTheCorpusDoesNotAnswer pins the documented empty
// result against the shipped path, where --branch defaults to HEAD and seeds
// the graph lane: an ambient seed reorders a ranking, it does not conjure one.
func TestKGQueryAbstainsOnAQueryTheCorpusDoesNotAnswer(t *testing.T) {
	dir := initKGRepo(t)
	commitFile(t, dir, "internal/parser/lex.go", "package parser\n")
	mustRun(t, dir, "note", "add", "The lexer normalizes whitespace", "--path", "internal/parser/lex.go")
	mustRun(t, dir, "note", "add", "Release tags are GPG signed")
	mustRun(t, dir, "kg", "build")

	if got := mustJSON[[]kgResultJSON](t, mustRun(t, dir, "kg", "query", "whitespace", "--json")); len(got) == 0 {
		t.Fatal("kg query returned nothing for a term the corpus carries")
	}
	if got := mustJSON[[]kgResultJSON](t, mustRun(t, dir, "kg", "query", "zzqqxxnonexistentterm", "--json")); len(got) != 0 {
		t.Fatalf("kg query on a term nothing carries = %+v, want []", got)
	}
	if got := mustRun(t, dir, "kg", "query", "zzqqxxnonexistentterm", "--explain"); got != "" {
		t.Fatalf("kg query text on a term nothing carries = %q, want nothing", got)
	}
}

func TestKGQueryPathSeedsTheGraphLane(t *testing.T) {
	dir := initKGRepo(t)
	commitFile(t, dir, "internal/lfs/batch.go", "package lfs\n")
	mustRun(t, dir, "note", "add", "Batch transfers reuse one connection", "--path", "internal/lfs/batch.go")
	mustRun(t, dir, "note", "add", "Endpoint discovery reads the remote url", "--path", "internal/lfs/batch.go")
	mustRun(t, dir, "kg", "build")

	seeded := mustJSON[[]kgResultJSON](t, mustRun(t, dir,
		"kg", "query", "transfers", "--path", "internal/lfs/batch.go", "--explain", "--json"))
	if len(seeded) == 0 {
		t.Fatal("kg query returned nothing")
	}
	if !strings.HasPrefix(seeded[0].Lane, "lex+graph") {
		t.Fatalf("lane = %q, want the graph lane fused in once --path seeds it", seeded[0].Lane)
	}
	if seeded[0].Explain.Graph <= 0 {
		t.Fatalf("graph lane score = %v, want the seeded walk's lift", seeded[0].Explain.Graph)
	}

	unseeded := map[string]kgResultJSON{}
	for _, r := range mustJSON[[]kgResultJSON](t, mustRun(t, dir, "kg", "query", "transfers", "--explain", "--json")) {
		unseeded[r.ID] = r
	}
	bare, found := unseeded[seeded[0].ID]
	if !found {
		t.Fatalf("top seeded hit %s is absent from the unseeded ranking", seeded[0].ID[:7])
	}
	if seeded[0].Explain.Graph <= bare.Explain.Graph {
		t.Fatalf("graph lane scored %v seeded vs %v unseeded for %s; --path moved nothing",
			seeded[0].Explain.Graph, bare.Explain.Graph, seeded[0].ID[:7])
	}
	if seeded[0].Explain.Lexical != bare.Explain.Lexical {
		t.Fatalf("lexical lane moved with --path: %v vs %v", seeded[0].Explain.Lexical, bare.Explain.Lexical)
	}
}

func TestKGStaleExplainNamesTheTriggeringSignal(t *testing.T) {
	dir := initKGRepo(t)
	// The branch-reconciliation signal resolves trunk, so the fixture needs a commit.
	commitFile(t, dir, "internal/api/api.go", "package api\n")
	decayed := jsonID(t, mustRun(t, dir, "note", "add", "Still current", "--json"))
	expired := jsonID(t, mustRun(t, dir, "note", "add", "Out of date", "--json"))
	mustRun(t, dir, "note", "expire", expired, "--reason", "the API it describes is gone")

	report := mustJSON[kgStaleJSON](t, mustRun(t, dir, "kg", "stale", "--explain", "--json"))
	if report.Total != 2 || report.Gated != 1 || report.Penalized != 1 || report.Fresh != 0 {
		t.Fatalf("verdict counts = %d total / %d gated / %d penalized / %d fresh, want 2/1/1/0",
			report.Total, report.Gated, report.Penalized, report.Fresh)
	}
	signals := map[string]string{}
	details := map[string]string{}
	gated := map[string]bool{}
	weights := map[string]float64{}
	for _, r := range report.Explain {
		signals[r.ID], details[r.ID], gated[r.ID], weights[r.ID] = r.Signal, r.Detail, r.Gated, r.Weight
	}
	// The gate signal names an expired record; a record no gate fired on is
	// named by its heaviest penalty, which for an unverified note is time decay.
	if got := signals[expired]; got != "EXPIRED" {
		t.Fatalf("expired note signal = %q, want EXPIRED (rows %+v)", got, report.Explain)
	}
	if !gated[expired] || weights[expired] != 0 {
		t.Fatalf("expired note gated=%v weight=%v, want withheld at weight 0", gated[expired], weights[expired])
	}
	if got := signals[decayed]; got != "DECAY" {
		t.Fatalf("unverified note signal = %q, want DECAY (rows %+v)", got, report.Explain)
	}
	if gated[decayed] {
		t.Fatal("a time-decayed note was gated; decay only costs rank")
	}
	for id, want := range map[string]string{expired: "marked out of date", decayed: "last attested"} {
		if !strings.Contains(details[id], want) {
			t.Fatalf("detail for %s = %q, want it to name %q", id[:7], details[id], want)
		}
	}
	roles := map[string]string{}
	counts := map[string]int{}
	for _, s := range report.Signals {
		roles[s.Signal], counts[s.Signal] = s.Role, s.Count
	}
	if roles["EXPIRED"] != "gate" || counts["EXPIRED"] != 1 {
		t.Fatalf("signals = %+v, want EXPIRED counted once as a gate", report.Signals)
	}
	if roles["DECAY"] != "penalty" || counts["DECAY"] != 1 {
		t.Fatalf("signals = %+v, want DECAY counted once as a penalty", report.Signals)
	}

	text := mustRun(t, dir, "kg", "stale", "--explain")
	if !strings.Contains(text, "  "+expired[:7]+"\tnote\tgated\t0.00\tEXPIRED\t") {
		t.Fatalf("kg stale --explain text = %q, want the expired record row", text)
	}
	if !strings.Contains(text, "records: 2 total, 1 gated") {
		t.Fatalf("kg stale --explain text = %q, want the record summary", text)
	}

	bare := mustRun(t, dir, "kg", "stale")
	if strings.Contains(bare, "\nrecords\n") {
		t.Fatalf("kg stale without --explain = %q, want no per-record section", bare)
	}
	if !strings.Contains(bare, "  note\t2 total\t1 gated\t") {
		t.Fatalf("kg stale = %q, want the per-kind distribution", bare)
	}
}

func TestKGOnAnEmptyRepository(t *testing.T) {
	dir := initKGRepo(t)

	stat := mustJSON[kgStatJSON](t, mustRun(t, dir, "kg", "build", "--json"))
	if stat.Totals.Nodes != 0 || stat.Totals.Edges != 0 || stat.Totals.Events != 0 {
		t.Fatalf("empty repository built %+v, want an empty graph", stat.Totals)
	}
	if len(stat.Nodes) != 0 || len(stat.Edges) != 0 {
		t.Fatalf("empty graph reported kinds %+v / %+v", stat.Nodes, stat.Edges)
	}

	reread := mustJSON[kgStatJSON](t, mustRun(t, dir, "kg", "stat", "--json"))
	if reread.Index != "hit" {
		t.Fatalf("index = %q, want an empty graph to still read back", reread.Index)
	}

	results := mustJSON[[]kgResultJSON](t, mustRun(t, dir, "kg", "query", "anything", "--json"))
	if len(results) != 0 {
		t.Fatalf("kg query on an empty corpus = %+v, want nothing", results)
	}
	if got := mustRun(t, dir, "kg", "query", "anything"); got != "" {
		t.Fatalf("kg query text on an empty corpus = %q, want nothing", got)
	}

	report := mustJSON[kgStaleJSON](t, mustRun(t, dir, "kg", "stale", "--explain", "--json"))
	if report.Total != 0 || len(report.Kinds) != 0 || len(report.Signals) != 0 {
		t.Fatalf("kg stale on an empty corpus = %+v, want an empty report", report)
	}
	text := mustRun(t, dir, "kg", "stale")
	if !strings.Contains(text, "records: 0 total, 0 gated, 0 penalized, 0 fresh") {
		t.Fatalf("kg stale text = %q, want a zeroed summary", text)
	}
}

func TestKGUnknownSubcommandExits2(t *testing.T) {
	dir := initKGRepo(t)
	_, _, err := runCLI(t, dir, "kg", "rebuild")
	if got := cli.ExitCode(err); got != 2 {
		t.Fatalf("kg rebuild exit = %d, want 2 (err %v)", got, err)
	}
	if _, _, err := runCLI(t, dir, "kg", "stat", "extra"); cli.ExitCode(err) != 2 {
		t.Fatalf("kg stat extra exit = %d, want 2", cli.ExitCode(err))
	}
	if _, _, err := runCLI(t, dir, "kg", "query"); cli.ExitCode(err) != 2 {
		t.Fatalf("kg query with no QUERY exit = %d, want 2", cli.ExitCode(err))
	}
}
