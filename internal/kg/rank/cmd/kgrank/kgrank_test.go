package main

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/kg"
	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/internal/kg/rank"
	"github.com/yasyf/cc-notes/internal/kg/stale"
	"github.com/yasyf/cc-notes/model"
)

// The fixture corpus: a note anchored to a path and a branch its prose never
// names, a task anchored to a commit, the project that task belongs to, and an
// unrelated record. A query that names none of the addresses reaches the graph
// lane only through the session, which is what the session arms turn on.
const (
	noteID    = model.EntityID("1111111111111111111111111111111111111111")
	taskID    = model.EntityID("2222222222222222222222222222222222222222")
	projectID = model.EntityID("3333333333333333333333333333333333333333")
	otherID   = model.EntityID("5555555555555555555555555555555555555555")

	sha    = model.SHA("8c07cba23a652b7129a0b77fd403801ef1661bd6")
	path   = "svc/cache/warmer.go"
	branch = model.Branch("yasyf/warm-start")
)

func fixture(questions []eval.Question) corpus {
	entities := []eval.Entity{
		{ID: noteID, Kind: model.KindNote, Title: "Warm start decision", Body: "the warmer refills on boot", UpdatedAt: 100},
		{ID: taskID, Kind: model.KindTask, Title: "Land the refill loop", Body: "ship the loop", UpdatedAt: 200},
		{ID: projectID, Kind: model.KindProject, Title: "Boot latency", Body: "cut boot latency", UpdatedAt: 300},
		{ID: otherID, Kind: model.KindNote, Title: "Unrelated", Body: "billing invoices reconcile nightly", UpdatedAt: 400},
	}
	note := kg.EntityNode(model.KindNote, noteID)
	task := kg.EntityNode(model.KindTask, taskID)
	project := kg.EntityNode(model.KindProject, projectID)
	g := &kg.Graph{
		Nodes: []kg.Node{
			{ID: note, Kind: kg.NodeNote, Value: string(noteID)},
			{ID: task, Kind: kg.NodeTask, Value: string(taskID)},
			{ID: project, Kind: kg.NodeProject, Value: string(projectID)},
			{ID: kg.EntityNode(model.KindNote, otherID), Kind: kg.NodeNote, Value: string(otherID)},
			{ID: kg.PathNode(path), Kind: kg.NodePath, Value: path},
			{ID: kg.DirNode("svc/cache"), Kind: kg.NodeDir, Value: "svc/cache"},
			{ID: kg.CommitNode(sha), Kind: kg.NodeCommit, Value: string(sha)},
			{ID: kg.BranchNode(branch), Kind: kg.NodeBranch, Value: string(branch)},
		},
		Edges: []kg.Edge{
			{From: note, To: kg.PathNode(path), Kind: kg.EdgeAnchor, Weight: 1},
			{From: note, To: kg.BranchNode(branch), Kind: kg.EdgeBranch, Weight: 1},
			{From: kg.PathNode(path), To: kg.DirNode("svc/cache"), Kind: kg.EdgeWithin, Weight: 1},
			{From: task, To: kg.CommitNode(sha), Kind: kg.EdgeCommit, Weight: 1},
			{From: task, To: project, Kind: kg.EdgeProject, Weight: 1},
		},
	}
	return corpus{
		dir:       "/repo",
		questions: questions,
		entities:  entities,
		graph:     g,
		assess: []stale.Assessment{
			{ID: noteID, Kind: model.KindNote, Weight: 1},
			{ID: taskID, Kind: model.KindTask, Weight: 1},
			{ID: projectID, Kind: model.KindProject, Weight: 1},
			{ID: otherID, Kind: model.KindNote, Weight: 1},
		},
		options: eval.Options{K: 5, Threshold: 0.1, Seeds: []int64{1, 2, 3, 4, 5}},
	}
}

func graded(id, query string, session eval.Session) eval.Question {
	return eval.Question{
		ID: id, Repo: "/repo", Query: query, Category: "mechanism",
		Session: session, GoldEntityIDs: []model.EntityID{noteID},
	}
}

func seatedSession() eval.Session {
	return eval.Session{Branch: branch, Paths: []string{path}}
}

func retrieve(t *testing.T, r eval.Retriever, query string) []eval.Result {
	t.Helper()
	got, err := r.Retrieve(t.Context(), query, 5)
	if err != nil {
		t.Fatalf("Retrieve(%q): %v", query, err)
	}
	return got
}

// TestArmAsksEachQuestionFromItsOwnSession is the defect this whole change
// exists for: the driver used to build every configuration from
// rank.DefaultOptions with a zero Session, so the eval measured the one
// configuration the product never runs. The session arm must produce exactly
// what a ranker constructed with the question's own branch and paths produces,
// and the session-free arm must keep producing the zero-session ranking.
func TestArmAsksEachQuestionFromItsOwnSession(t *testing.T) {
	const query = "warm loop"
	questions := []eval.Question{graded("seated", query, seatedSession()), graded("unseated", query, eval.Session{})}
	c := fixture(questions)

	opts := rank.DefaultOptions()
	opts.Seed, opts.Lambda = 1, 1
	opts.Session = rank.Session{Branch: branch, Paths: []string{path}}
	want := retrieve(t, rank.New(c.entities, c.graph, c.assess, opts), query)

	seeded, free := c.fused(true), c.fused(false)
	seatedRanking := retrieve(t, seeded.config.Build(1, questions[0]), query)
	unseatedRanking := retrieve(t, seeded.config.Build(1, questions[1]), query)
	freeRanking := retrieve(t, free.config.Build(1, questions[0]), query)

	if !slices.Equal(seatedRanking, want) {
		t.Errorf("session arm ranking = %v, want the hand-built session ranker's %v", seatedRanking, want)
	}
	if slices.Equal(seatedRanking, unseatedRanking) {
		t.Errorf("both questions ranked %v: the arm is not reading each question's own session", seatedRanking)
	}
	if !slices.Equal(freeRanking, unseatedRanking) {
		t.Errorf("session-free arm ranked %v, want the zero-session ranking %v", freeRanking, unseatedRanking)
	}
}

// TestRankersMemoizeOnePerSession pins the cache key. The index costs a scan of
// the whole corpus and graph and does not depend on the session, so it is
// shared — but sharing it across two different sessions would silently hand a
// question the wrong personalization and re-open the defect above.
func TestRankersMemoizeOnePerSession(t *testing.T) {
	c := fixture(nil)
	base := rank.DefaultOptions()
	base.Lambda = 1
	m := newRankers(c, base)
	seated := rank.Session{Branch: branch, Paths: []string{path}}

	first := m.get(1, seated)
	if again := m.get(1, seated); again != first {
		t.Error("the same seed and session built two rankers")
	}
	if other := m.get(1, rank.Session{}); other == first {
		t.Error("the zero session reused the seated session's ranker")
	}
	if otherSeed := m.get(2, seated); otherSeed == first {
		t.Error("a second seed reused the first seed's ranker")
	}
	if len(m.built) != 3 {
		t.Errorf("built %d rankers, want one each for (1, seated), (1, zero) and (2, seated)", len(m.built))
	}
}

// TestUntreatedNamesTheQuestionsTheGraphLaneNeverRanOn covers the honesty fix
// for rank.go's `if len(walk) > 0`: a question whose walk resolves no seeds is
// scored by the lexical lane alone, so counting it as a tie reports the graph
// lane as tried-and-ineffective when it was never appended at all.
func TestUntreatedNamesTheQuestionsTheGraphLaneNeverRanOn(t *testing.T) {
	const query = "warm loop"
	questions := []eval.Question{graded("seated", query, seatedSession()), graded("unseated", query, eval.Session{})}
	c := fixture(questions)
	cases := []struct {
		name string
		arm  arm
		want map[string]bool
	}{
		{"session-seeded", c.fused(true), map[string]bool{"seated": false, "unseated": true}},
		{"session-free", c.fused(false), map[string]bool{"seated": true, "unseated": true}},
		{"no graph lane at all", c.enriched(), map[string]bool{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.arm.untreated(t.Context(), questions, 1)
			if err != nil {
				t.Fatalf("untreated: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("untreated = %v, want %v", got, tc.want)
			}
			for id, want := range tc.want {
				if got[id] != want {
					t.Errorf("untreated[%s] = %t, want %t", id, got[id], want)
				}
			}
		})
	}
}

// TestContrastMarksUntreatedQuestionsSeparately joins the two: an untreated
// question is a delta of exactly zero, and the summary has to say so rather
// than letting it pad the tie count.
func TestContrastMarksUntreatedQuestionsSeparately(t *testing.T) {
	const query = "warm loop"
	questions := []eval.Question{graded("seated", query, seatedSession()), graded("unseated", query, eval.Session{})}
	c := fixture(questions)
	cmp, err := c.contrast(c.enriched(), c.fused(true), questions)
	if err != nil {
		t.Fatalf("contrast: %v", err)
	}
	if cmp.name != "lex+enrich -> lex+enrich+graph+session" {
		t.Errorf("name = %q, want the two arm names", cmp.name)
	}
	summary := eval.Compare(cmp.deltas)
	if summary.N != 2 || summary.Untreated != 1 {
		t.Fatalf("summary = %d deltas / %d untreated, want 2 and 1", summary.N, summary.Untreated)
	}
	for _, d := range cmp.deltas {
		if (d.Question == "unseated") != d.Untreated {
			t.Errorf("delta %+v: only the unseated question's graph lane was skipped", d)
		}
		if d.Repo != "/repo" {
			t.Errorf("delta %+v carries repo %q, want /repo so it can be pooled", d, d.Repo)
		}
	}
	if got := treated(cmp.deltas); len(got) != 1 || got[0].Question != "seated" {
		t.Errorf("treated = %+v, want only the seated question", got)
	}
}

// TestTuneRefusesWithoutAHeldOutFold is the in-sample guard. Five of the six
// repositories in the shipped question set are too small to fill both folds, so
// this refusal fires on real data — and where it fires there is no headline to
// report, only the selection fold that chose the weight.
func TestTuneRefusesWithoutAHeldOutFold(t *testing.T) {
	cases := []struct {
		name      string
		questions []eval.Question
	}{
		{"a single graded question", []eval.Question{graded("q049", "warm loop", eval.Session{})}},
		{"every question hashes into the same fold", []eval.Question{
			graded("q001", "warm loop", eval.Session{}),
			graded("q049", "refill warm", eval.Session{}),
		}},
		{"no graded question at all", []eval.Question{
			{ID: "abstain", Repo: "/repo", Query: "zzqqxx", ExpectAbstain: true},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, session := range []bool{false, true} {
				if _, err := fixture(tc.questions).tune(session); !errors.Is(err, errNoHoldout) {
					t.Fatalf("tune(session=%t) error = %v, want errNoHoldout", session, err)
				}
			}
		})
	}
}

// TestTuneChoosesOnOneFoldAndHandsBackTheOther pins the split the headline
// depends on: the questions a weight is scored on are never the questions that
// chose it.
func TestTuneChoosesOnOneFoldAndHandsBackTheOther(t *testing.T) {
	questions := make([]eval.Question, 0, 8)
	for i := range 8 {
		questions = append(questions, graded(fmt.Sprintf("q%03d", i), "warm loop", eval.Session{}))
	}
	got, err := fixture(questions).tune(false)
	if err != nil {
		t.Fatalf("tune: %v", err)
	}
	if !slices.Contains(sweepWeights, got.weight) {
		t.Errorf("weight = %v, want one of %v", got.weight, sweepWeights)
	}
	if len(got.selection)+len(got.holdout) != len(questions) {
		t.Fatalf("folds hold %d+%d, want all %d graded questions", len(got.selection), len(got.holdout), len(questions))
	}
	for _, held := range got.holdout {
		if slices.ContainsFunc(got.selection, func(q eval.Question) bool { return q.ID == held.ID }) {
			t.Errorf("%s chose the weight and would also score it", held.ID)
		}
	}
	best := got.sweep.Summaries[0].Overall.NDCG.Mean
	for _, s := range got.sweep.Summaries {
		if s.Overall.NDCG.Mean > best {
			best = s.Overall.NDCG.Mean
		}
	}
	for i, w := range sweepWeights {
		if w == got.weight && got.sweep.Summaries[i].Overall.NDCG.Mean != best {
			t.Errorf("selected w=%v scored %v on the selection fold, want the best %v",
				w, got.sweep.Summaries[i].Overall.NDCG.Mean, best)
		}
	}
}

// TestPoolReportsBothWeightingsBecauseTheyDisagree is the pooling fix. Twenty
// questions from one repository against one from another: question-weighted,
// the large repository decides and the pooled mean is zero; repo-balanced, the
// small one gets an equal say and the sign flips. Reporting only either number
// hides half the answer, so the driver has to carry both.
func TestPoolReportsBothWeightingsBecauseTheyDisagree(t *testing.T) {
	deltas := make([]eval.Delta, 0, 21)
	for i := range 20 {
		deltas = append(deltas, eval.Delta{Repo: "/big", Question: fmt.Sprintf("b%02d", i), Value: 0.1})
	}
	deltas = append(deltas, eval.Delta{Repo: "/small", Question: "s0", Value: -2.0})

	weighted := eval.Compare(deltas)
	if math.Abs(weighted.Mean) > 1e-15 {
		t.Errorf("question-weighted mean = %v, want 0: twenty +0.1 against one -2.0 cancels", weighted.Mean)
	}
	if weighted.Wins != 20 || weighted.Losses != 1 {
		t.Errorf("question-weighted record = %d/%d, want 20/1", weighted.Wins, weighted.Losses)
	}
	mean, significance, method := balanced(deltas)
	if want := -0.95; mean != want {
		t.Errorf("repo-balanced mean = %v, want %v: each repository's own mean counts once", mean, want)
	}
	if method != eval.Exact {
		t.Errorf("repo-balanced method = %q, want %q", method, eval.Exact)
	}
	if significance <= 0 || significance > 1 {
		t.Errorf("repo-balanced p = %v, want a probability", significance)
	}
	if got := repos(deltas); !slices.Equal(got, []string{"/big", "/small"}) {
		t.Errorf("repos = %v, want both, sorted", got)
	}
}

func TestPoolGroupsDeltasByContrastAcrossRepositories(t *testing.T) {
	p := newPool()
	p.add("lex+enrich -> lex+enrich+graph", []eval.Delta{{Repo: "/big", Question: "b0", Value: 0.2}})
	p.add("gated -> lex+enrich", []eval.Delta{{Repo: "/big", Question: "b0", Value: 0.1}})
	p.add("lex+enrich -> lex+enrich+graph", []eval.Delta{{Repo: "/small", Question: "s0", Value: -0.3}})

	if !slices.Equal(p.order, []string{"lex+enrich -> lex+enrich+graph", "gated -> lex+enrich"}) {
		t.Errorf("order = %v, want first-seen contrast order", p.order)
	}
	graph := p.byName["lex+enrich -> lex+enrich+graph"]
	if len(graph) != 2 || graph[0].Repo != "/big" || graph[1].Repo != "/small" {
		t.Errorf("pooled contrast = %+v, want both repositories' deltas in repository order", graph)
	}
}
