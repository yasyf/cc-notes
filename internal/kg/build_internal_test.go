package kg

import (
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

// TestAddEdgePrefersWrittenAnchor pins the rule the task backfill depends on:
// a path an agent anchored outranks the same path derived from a commit, in
// either arrival order.
func TestAddEdgePrefersWrittenAnchor(t *testing.T) {
	from, to := EntityNode(model.KindTask, "t"), PathNode("internal/kg/build.go")
	written := Edge{From: from, To: to, Kind: EdgeAnchor, Weight: 1, OID: "abc"}
	derived := Edge{From: from, To: to, Kind: EdgeAnchor, Weight: 1, Derived: true}

	for _, tc := range []struct {
		name  string
		order []Edge
	}{
		{"written first", []Edge{written, derived}},
		{"derived first", []Edge{derived, written}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBuilder(nil)
			for _, e := range tc.order {
				b.addEdge(e)
			}
			got := b.edges[edgeKey{from: from, to: to, kind: EdgeAnchor}]
			if got != written {
				t.Fatalf("edge = %+v, want %+v", got, written)
			}
		})
	}
}

func TestDerivedPathsSkipsOversizedCommits(t *testing.T) {
	huge := make([]string, derivedPathLimit+1)
	for i := range huge {
		huge[i] = "generated/file" + string(rune('a'+i%26)) + string(rune('a'+i/26))
	}
	touched := map[model.SHA][]string{
		"aaa": {"internal/kg/build.go"},
		"bbb": huge,
	}
	got := derivedPaths([]model.SHA{"aaa", "bbb"}, touched)
	if want := []string{"internal/kg/build.go"}; !slices.Equal(got, want) {
		t.Fatalf("derivedPaths = %v, want %v", got, want)
	}
}
