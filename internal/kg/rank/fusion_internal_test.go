package rank

import (
	"math"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func TestRanksAreCompetitionRanks(t *testing.T) {
	candidates := []model.EntityID{"a", "b", "c", "d"}
	got := ranks(map[model.EntityID]float64{"a": 3, "b": 1, "c": 1}, candidates)
	want := map[model.EntityID]int{"a": 1, "b": 2, "c": 2, "d": 4}
	for id, rank := range want {
		if got[id] != rank {
			t.Errorf("rank(%s) = %d, want %d (got %v)", id, got[id], rank, got)
		}
	}
}

func TestFuseWithOneLaneRanksByThatLane(t *testing.T) {
	candidates := []model.EntityID{"a", "b", "c"}
	lex := lane{name: LexLane, weight: DefaultLexWeight, score: map[model.EntityID]float64{"a": 9, "b": 4}}
	got := fuse([]lane{lex}, candidates, DefaultRRFK)
	if !(got["a"] > got["b"] && got["b"] > got["c"]) {
		t.Errorf("fuse = %v, want a > b > c", got)
	}
	if want := 1 / (DefaultRRFK + 1); math.Abs(got["a"]-want) > 1e-12 {
		t.Errorf("fuse[a] = %v, want %v", got["a"], want)
	}
}

func TestFuseLetsTheWeightedLaneOverrule(t *testing.T) {
	candidates := []model.EntityID{"a", "b"}
	lanes := []lane{
		{name: LexLane, weight: 1, score: map[model.EntityID]float64{"a": 9}},
		{name: GraphLane, weight: 4, score: map[model.EntityID]float64{"b": 9}},
	}
	got := fuse(lanes, candidates, DefaultRRFK)
	if got["b"] <= got["a"] {
		t.Errorf("fuse = %v, want the heavier lane's pick first", got)
	}
	lanes[1].weight = 0.5
	if got := fuse(lanes, candidates, DefaultRRFK); got["a"] <= got["b"] {
		t.Errorf("fuse = %v, want the lexical pick first at a supporting weight", got)
	}
}

func TestNormalizeAnchorsTheBestCandidateAtOne(t *testing.T) {
	candidates := []model.EntityID{"a", "b", "c"}
	got := normalize(map[model.EntityID]float64{"a": 4, "b": 2, "c": 1}, candidates)
	want := map[model.EntityID]float64{"a": 1, "b": 0.5, "c": 0.25}
	for id, score := range want {
		if got[id] != score {
			t.Errorf("normalize(%s) = %v, want %v", id, got[id], score)
		}
	}
}
