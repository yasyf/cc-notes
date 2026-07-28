package rank

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/model"
)

func TestJaccard(t *testing.T) {
	cases := []struct {
		name string
		a, z string
		want float64
	}{
		{"identical", "cache warm boot", "cache warm boot", 1},
		{"disjoint", "cache warm", "invoice billing", 0},
		{"half", "cache warm", "cache invoice", 1.0 / 3},
		{"empty", "", "cache", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jaccard(tokenSet(tc.a), tokenSet(tc.z)); got != tc.want {
				t.Errorf("jaccard(%q, %q) = %v, want %v", tc.a, tc.z, got, tc.want)
			}
		})
	}
}

func TestDiversifyPromotesTheDissimilarCandidate(t *testing.T) {
	tokens := map[model.EntityID]map[string]struct{}{
		"a":    tokenSet("cache warm boot latency"),
		"twin": tokenSet("cache warm boot latency"),
		"new":  tokenSet("invoice billing reconcile nightly"),
	}
	results := []eval.Result{
		{ID: "a", Score: 1}, {ID: "twin", Score: 0.9}, {ID: "new", Score: 0.8},
	}
	got := diversify(results, tokens, DefaultLambda, 3)
	if got[0].ID != "a" || got[1].ID != "new" || got[2].ID != "twin" {
		t.Errorf("diversify order = %v %v %v, want a new twin", got[0].ID, got[1].ID, got[2].ID)
	}
	for i := 1; i < len(got); i++ {
		if got[i].Score > got[i-1].Score {
			t.Errorf("scores = %v, want them non-increasing so a descending sort keeps the order", got)
		}
	}
}

func TestDiversifyAtLambdaOneKeepsRelevanceOrder(t *testing.T) {
	tokens := map[model.EntityID]map[string]struct{}{
		"a": tokenSet("cache"), "b": tokenSet("cache"), "c": tokenSet("invoice"),
	}
	results := []eval.Result{{ID: "a", Score: 1}, {ID: "b", Score: 0.9}, {ID: "c", Score: 0.8}}
	got := diversify(results, tokens, 1, 3)
	for i, want := range []model.EntityID{"a", "b", "c"} {
		if got[i].ID != want {
			t.Errorf("diversify at lambda 1 = %v, want relevance order", got)
		}
	}
}
