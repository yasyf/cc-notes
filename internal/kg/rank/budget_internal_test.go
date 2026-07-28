package rank

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/kg/eval"
	"github.com/yasyf/cc-notes/model"
)

func TestEstimateTokensRoundsUp(t *testing.T) {
	cases := map[string]int{"": 0, "a": 1, "abcd": 1, "abcde": 2, "abcdefgh": 2}
	for text, want := range cases {
		if got := EstimateTokens(text); got != want {
			t.Errorf("EstimateTokens(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestFitTakesTheLongestAffordablePrefix(t *testing.T) {
	ranked := []eval.Result{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	cost := map[model.EntityID]int{"a": 10, "b": 20, "c": 5}
	cases := []struct {
		budget, want int
	}{{0, 0}, {9, 0}, {10, 1}, {29, 1}, {30, 2}, {34, 2}, {35, 3}, {1000, 3}}
	for _, tc := range cases {
		if got := fit(ranked, cost, tc.budget); got != tc.want {
			t.Errorf("fit(budget=%d) = %d, want %d", tc.budget, got, tc.want)
		}
	}
}
