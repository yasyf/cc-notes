package eval_test

import (
	"math"
	"regexp"
	"testing"

	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/model"
)

var testClusters = []eval.Cluster{
	{Name: "sandsql", Pattern: regexp.MustCompile(`(?i)\bsandsql\b`)},
	{Name: "pulumi", Pattern: regexp.MustCompile(`(?i)\bpulumi\b`)},
}

func TestLabelKeepsOnlySingleTopicEntities(t *testing.T) {
	corpus := []eval.Entity{
		{ID: "a", Kind: model.KindNote, Title: "sandsql planner", Body: "notes"},
		{ID: "b", Kind: model.KindNote, Title: "deploy", Body: "the pulumi stack"},
		{ID: "c", Kind: model.KindNote, Title: "both", Body: "sandsql behind pulumi"},
		{ID: "d", Kind: model.KindNote, Title: "unrelated", Body: "nothing here"},
	}
	labels := eval.Label(corpus, testClusters)
	want := map[model.EntityID]string{"a": "sandsql", "b": "pulumi"}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
	for id, name := range want {
		if labels[id] != name {
			t.Errorf("label[%s] = %q, want %q", id, labels[id], name)
		}
	}
}

// TestLabelIgnoresTags pins the ground truth against leakage: a topic term that
// appears only as a tag must not label the entity, or the tag signal is scored
// against a ground truth it wrote itself.
func TestLabelIgnoresTags(t *testing.T) {
	corpus := []eval.Entity{
		{ID: "a", Kind: model.KindNote, Title: "opaque", Body: "no topic in the prose", Tags: []string{"sandsql"}},
	}
	if labels := eval.Label(corpus, testClusters); len(labels) != 0 {
		t.Fatalf("labelled %v from a tag alone", labels)
	}
}

func TestScorePairsCountsEveryPair(t *testing.T) {
	labels := map[model.EntityID]string{
		"a": "sandsql", "b": "sandsql", "c": "sandsql", "d": "pulumi",
	}
	// Links a-b (same topic), a-d (cross topic); leaves a-c and b-c unlinked.
	links := map[[2]model.EntityID]bool{{"a", "b"}: true, {"a", "d"}: true}
	score := eval.ScorePairs(labels, func(x, z model.EntityID) bool { return links[[2]model.EntityID{x, z}] })

	want := eval.PairScore{Pairs: 6, TP: 1, FP: 1, FN: 2, TN: 2}
	if score != want {
		t.Fatalf("score = %+v, want %+v", score, want)
	}
	if got := score.Precision(); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("precision = %v, want 0.5", got)
	}
	if got := score.Recall(); math.Abs(got-1.0/3) > 1e-9 {
		t.Errorf("recall = %v, want %v", got, 1.0/3)
	}
	if got := score.F1(); math.Abs(got-0.4) > 1e-9 {
		t.Errorf("F1 = %v, want 0.4", got)
	}
}

func TestScorePairsReadsLinksInEitherDirection(t *testing.T) {
	labels := map[model.EntityID]string{"a": "x", "b": "x"}
	reverse := func(from, to model.EntityID) bool { return from == "b" && to == "a" }
	if score := eval.ScorePairs(labels, reverse); score.TP != 1 {
		t.Fatalf("score = %+v, want the reverse edge counted", score)
	}
}

func TestPairScoreZeroDenominators(t *testing.T) {
	var empty eval.PairScore
	if empty.Precision() != 0 || empty.Recall() != 0 || empty.F1() != 0 {
		t.Fatalf("an empty score is not zero: %+v", empty)
	}
}
