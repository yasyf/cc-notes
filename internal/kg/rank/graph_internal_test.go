package rank

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/kg"
	"github.com/yasyf/cc-notes/model"
)

// chain is a graph shaped seed-hub -> near -> far, the smallest shape that
// tells an accumulated walk from a walk truncated at its last step.
func chain() *kg.Graph {
	near := kg.EntityNode(model.KindTask, "near")
	far := kg.EntityNode(model.KindTask, "far")
	return &kg.Graph{
		Nodes: []kg.Node{
			{ID: near, Kind: kg.NodeTask, Value: "near"},
			{ID: far, Kind: kg.NodeTask, Value: "far"},
			{ID: kg.PathNode("a/b.go"), Kind: kg.NodePath, Value: "a/b.go"},
		},
		Edges: []kg.Edge{
			{From: near, To: kg.PathNode("a/b.go"), Kind: kg.EdgeAnchor, Weight: 1},
			{From: far, To: near, Kind: kg.EdgeDep, Weight: 1},
		},
	}
}

func TestWalkAccumulatesOverHops(t *testing.T) {
	l := newGraphLane(chain())
	seeds := l.querySeeds("what touches a/b.go?")
	got := l.walk(seeds, DefaultHops, DefaultDamping)
	if got["near"] <= got["far"] {
		t.Errorf("walk = %v, want the directly anchored record above the one behind it", got)
	}
}

func TestQuerySeedsOnlyResolveAddresses(t *testing.T) {
	g := &kg.Graph{Nodes: []kg.Node{
		{ID: kg.PathNode("a/b.go"), Kind: kg.NodePath, Value: "a/b.go"},
		{ID: kg.DirNode("build"), Kind: kg.NodeDir, Value: "build"},
		{ID: kg.ConceptNode("escape-hatch"), Kind: kg.NodeConcept, Value: "escape-hatch"},
		{ID: kg.CommitNode("8c07cba23a652b7129a0b77fd403801ef1661bd6"), Kind: kg.NodeCommit, Value: "8c07cba23a652b7129a0b77fd403801ef1661bd6"},
	}}
	l := newGraphLane(g)
	cases := []struct {
		name  string
		query string
		want  int
	}{
		{"bare word is not an address", "how do I build the thing?", 0},
		{"path is", "what covers a/b.go?", 1},
		{"path inside a longer token is not", "what covers xa/b.gox?", 0},
		{"kebab identifier is", "the escape-hatch fence", 1},
		{"sha prefix is", "who landed 8c07cba?", 1},
		{"short hex run is not", "issue 8c07cb", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(l.querySeeds(tc.query)); got != tc.want {
				t.Errorf("querySeeds(%q) resolved %d seeds, want %d", tc.query, got, tc.want)
			}
		})
	}
}

func TestSpecificityFallsWithSharedMembers(t *testing.T) {
	shared, lonely := kg.PathNode("shared.go"), kg.PathNode("lonely.go")
	g := &kg.Graph{
		Nodes: []kg.Node{
			{ID: kg.EntityNode(model.KindNote, "a"), Kind: kg.NodeNote, Value: "a"},
			{ID: kg.EntityNode(model.KindNote, "b"), Kind: kg.NodeNote, Value: "b"},
			{ID: kg.EntityNode(model.KindNote, "c"), Kind: kg.NodeNote, Value: "c"},
			{ID: shared, Kind: kg.NodePath, Value: "shared.go"},
			{ID: lonely, Kind: kg.NodePath, Value: "lonely.go"},
		},
		Edges: []kg.Edge{
			{From: kg.EntityNode(model.KindNote, "a"), To: shared, Kind: kg.EdgeAnchor, Weight: 1},
			{From: kg.EntityNode(model.KindNote, "b"), To: shared, Kind: kg.EdgeAnchor, Weight: 1},
			{From: kg.EntityNode(model.KindNote, "c"), To: lonely, Kind: kg.EdgeAnchor, Weight: 1},
		},
	}
	l := newGraphLane(g)
	if got := l.spec[l.index[shared]]; got != 0.5 {
		t.Errorf("specificity of a node two records share = %v, want 0.5", got)
	}
	if got := l.spec[l.index[lonely]]; got != 1 {
		t.Errorf("specificity of a node one record holds = %v, want 1", got)
	}
}

func TestLiftIsMassOverBackground(t *testing.T) {
	walk := map[model.EntityID]float64{"moved": 0.4, "still": 0.1}
	background := map[model.EntityID]float64{"moved": 0.1, "still": 0.2}
	got := lift(walk, background)
	if got["moved"] != 4 || got["still"] != 0.5 {
		t.Errorf("lift = %v, want moved=4 still=0.5", got)
	}
}

func TestAdvisoryEdgesDoNotCarryMass(t *testing.T) {
	a, z := kg.EntityNode(model.KindNote, "a"), kg.EntityNode(model.KindNote, "z")
	g := &kg.Graph{
		Nodes: []kg.Node{
			{ID: a, Kind: kg.NodeNote, Value: "a"},
			{ID: z, Kind: kg.NodeNote, Value: "z"},
			{ID: kg.PathNode("a/b.go"), Kind: kg.NodePath, Value: "a/b.go"},
		},
		Edges: []kg.Edge{
			{From: a, To: kg.PathNode("a/b.go"), Kind: kg.EdgeAnchor, Weight: 1},
			{From: a, To: z, Kind: kg.EdgeCochange, Weight: 5, Advisory: true},
		},
	}
	l := newGraphLane(g)
	got := l.walk(l.querySeeds("a/b.go"), DefaultHops, DefaultDamping)
	if _, reached := got["z"]; reached {
		t.Errorf("walk = %v, want the advisory edge to carry no mass", got)
	}
}
