package kg

import (
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

func TestParseDiffTree(t *testing.T) {
	const root = "1111111111111111111111111111111111111111"
	const edit = "2222222222222222222222222222222222222222"
	const merge = "3333333333333333333333333333333333333333"
	out := join0(
		root,
		":000000 100644 0000000000000000000000000000000000000000 444fc085d2de861a2546dc539e9cc0e3532172e6 A", "internal/kg/build.go",
		":000000 100644 0000000000000000000000000000000000000000 496d255e8e2cd0f4891c4dece94721f36d614109 A", "internal/kg/index.go",
		edit,
		":100644 100644 444fc085d2de861a2546dc539e9cc0e3532172e6 75421aa830bbe82b681db812b942c790242c9e56 M", "internal/kg/build.go",
		merge,
	)

	got := parseDiffTree(out)
	want := map[model.SHA][]string{
		root: {"internal/kg/build.go", "internal/kg/index.go"},
		edit: {"internal/kg/build.go"},
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d commits, want %d: %v", len(got), len(want), got)
	}
	for sha, paths := range want {
		if !slices.Equal(got[sha], paths) {
			t.Errorf("%s = %v, want %v", sha, got[sha], paths)
		}
	}
}

func TestParseDiffTreeEmpty(t *testing.T) {
	if got := parseDiffTree(""); len(got) != 0 {
		t.Fatalf("parsed %v from empty output", got)
	}
}

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

func join0(records ...string) string {
	out := ""
	for _, r := range records {
		out += r + "\x00"
	}
	return out
}
