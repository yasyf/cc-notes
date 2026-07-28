package rank_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/kg"
	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/internal/kg/rank"
	"github.com/yasyf/cc-notes/internal/kg/stale"
	"github.com/yasyf/cc-notes/model"
)

// The fixture corpus: a note whose prose never names the path it is anchored
// to, a task whose prose never names the commit it landed, the project that
// task belongs to, a superseded predecessor, and an unrelated record.
const (
	noteID    = model.EntityID("1111111111111111111111111111111111111111")
	taskID    = model.EntityID("2222222222222222222222222222222222222222")
	projectID = model.EntityID("3333333333333333333333333333333333333333")
	deadID    = model.EntityID("4444444444444444444444444444444444444444")
	otherID   = model.EntityID("5555555555555555555555555555555555555555")

	sha    = model.SHA("8c07cba23a652b7129a0b77fd403801ef1661bd6")
	path   = "svc/cache/warmer.go"
	branch = model.Branch("yasyf/warm-start")
)

func fixture() ([]eval.Entity, *kg.Graph) {
	corpus := []eval.Entity{
		{ID: noteID, Kind: model.KindNote, Title: "Warm start decision", Body: "the warmer refills on boot", UpdatedAt: 100},
		{ID: taskID, Kind: model.KindTask, Title: "Land the refill loop", Body: "ship the loop", UpdatedAt: 200},
		{ID: projectID, Kind: model.KindProject, Title: "Boot latency", Body: "cut boot latency", UpdatedAt: 300},
		{ID: deadID, Kind: model.KindNote, Title: "Warm start decision (old)", Body: "the warmer refills lazily", UpdatedAt: 50, SupersededBy: []model.EntityID{noteID}},
		{ID: otherID, Kind: model.KindNote, Title: "Unrelated", Body: "billing invoices reconcile nightly", UpdatedAt: 400},
	}
	note := kg.EntityNode(model.KindNote, noteID)
	task := kg.EntityNode(model.KindTask, taskID)
	project := kg.EntityNode(model.KindProject, projectID)
	g := &kg.Graph{
		Nodes: []kg.Node{
			{ID: note, Kind: kg.NodeNote, Value: string(noteID), Title: "Warm start decision"},
			{ID: task, Kind: kg.NodeTask, Value: string(taskID), Title: "Land the refill loop"},
			{ID: project, Kind: kg.NodeProject, Value: string(projectID), Title: "Boot latency"},
			{ID: kg.EntityNode(model.KindNote, deadID), Kind: kg.NodeNote, Value: string(deadID)},
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
	return corpus, g
}

func assessments() []stale.Assessment {
	return []stale.Assessment{
		{ID: noteID, Kind: model.KindNote, Weight: 1},
		{ID: taskID, Kind: model.KindTask, Weight: 1},
		{ID: projectID, Kind: model.KindProject, Weight: 1},
		{ID: deadID, Kind: model.KindNote, Gated: true, Signal: stale.SignalSuperseded},
		{ID: otherID, Kind: model.KindNote, Weight: 1},
	}
}

func options() rank.Options {
	opts := rank.DefaultOptions()
	opts.Seed, opts.Lambda = 1, 1
	return opts
}

func ids(results []eval.Result) []model.EntityID {
	out := make([]model.EntityID, len(results))
	for i, r := range results {
		out[i] = r.ID
	}
	return out
}

func TestEnrichAppendsAnchorAddresses(t *testing.T) {
	corpus, g := fixture()
	enriched := rank.Enrich(corpus, g)
	var body string
	for _, e := range enriched {
		if e.ID == noteID {
			body = e.Body
		}
	}
	for _, want := range []string{path, "warmer warmer", "svc cache", string(branch)} {
		if !strings.Contains(body, want) {
			t.Errorf("enriched body %q missing %q", body, want)
		}
	}
	if got := enriched[4].Body; got != corpus[4].Body {
		t.Errorf("unanchored record body = %q, want it untouched (%q)", got, corpus[4].Body)
	}
}

func TestRetrieveWithholdsSupersededRecord(t *testing.T) {
	corpus, g := fixture()
	r := rank.New(corpus, g, assessments(), options())
	got, err := r.Retrieve(t.Context(), "warm start decision warmer refills", 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if slices.Contains(ids(got), deadID) {
		t.Errorf("Retrieve = %v, want the superseded record withheld", ids(got))
	}
	if len(got) == 0 || got[0].ID != noteID {
		t.Errorf("Retrieve = %v, want the live successor first", ids(got))
	}
}

func TestRetrieveFindsRecordByAnchoredAddress(t *testing.T) {
	cases := []struct {
		name  string
		query string
		want  model.EntityID
	}{
		{"commit sha the prose never names", "which task landed " + string(sha) + "?", taskID},
		{"short sha prefix", "what landed 8c07cba?", taskID},
		{"anchored path the prose never names", "what applies to svc/cache/warmer.go?", noteID},
		{"branch the prose never names", "anything recorded on yasyf/warm-start?", noteID},
	}
	corpus, g := fixture()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := rank.New(corpus, g, assessments(), options())
			got, err := r.Retrieve(t.Context(), tc.query, 5)
			if err != nil {
				t.Fatalf("Retrieve: %v", err)
			}
			if len(got) == 0 || got[0].ID != tc.want {
				t.Errorf("Retrieve(%q) = %v, want %s first", tc.query, ids(got), tc.want.Short())
			}
		})
	}
}

func TestRetrieveWalksSessionStateToNeighbours(t *testing.T) {
	corpus, g := fixture()
	opts := options()
	opts.Session = rank.Session{Entities: []model.EntityID{taskID}}
	r := rank.New(corpus, g, assessments(), opts)
	got, err := r.Retrieve(t.Context(), "boot", 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if len(got) == 0 || got[0].ID != projectID {
		t.Errorf("Retrieve = %v, want the held task's project first", ids(got))
	}
}

func TestRetrieveLaneAttribution(t *testing.T) {
	corpus, g := fixture()
	r := rank.New(corpus, g, assessments(), options())
	got, err := r.Retrieve(t.Context(), "warmer "+path, 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if got[0].Lane != rank.FusedLane {
		t.Errorf("lane = %q, want %q", got[0].Lane, rank.FusedLane)
	}
	plain := options()
	plain.Graph = false
	solo, err := rank.New(corpus, g, assessments(), plain).Retrieve(t.Context(), "warmer", 5)
	if err != nil {
		t.Fatalf("Retrieve: %v", err)
	}
	if solo[0].Lane != rank.LexLane {
		t.Errorf("lane = %q, want %q with the graph lane off", solo[0].Lane, rank.LexLane)
	}
}

func TestFillStopsAtTheTokenBudget(t *testing.T) {
	corpus, g := fixture()
	r := rank.New(corpus, g, assessments(), options())
	sizes := map[int]int{}
	for _, budget := range []int{0, 20, 40, 1000} {
		got, err := r.Fill(t.Context(), "warm start decision", budget)
		if err != nil {
			t.Fatalf("Fill: %v", err)
		}
		cost := 0
		for _, res := range got {
			for _, e := range corpus {
				if e.ID == res.ID {
					cost += rank.EstimateTokens(e.Text())
				}
			}
		}
		if cost > budget {
			t.Errorf("Fill(%d) returned %d tokens, over budget", budget, cost)
		}
		sizes[budget] = len(got)
	}
	if sizes[0] != 0 {
		t.Errorf("Fill(0) returned %d records, want none", sizes[0])
	}
	if sizes[1000] <= sizes[20] {
		t.Errorf("Fill sizes = %v, want a larger budget to admit more", sizes)
	}
}
