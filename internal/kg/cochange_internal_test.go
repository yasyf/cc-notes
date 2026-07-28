package kg

import (
	"maps"
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
)

func churnScan(commits ...[]gitcmd.FileChurn) []gitcmd.CommitChurn {
	out := make([]gitcmd.CommitChurn, len(commits))
	for i, files := range commits {
		out[i] = gitcmd.CommitChurn{Files: files}
	}
	return out
}

func TestFoldCochangeCountsRevisionsAndChurn(t *testing.T) {
	commits := churnScan(
		[]gitcmd.FileChurn{{Path: "a.go", Added: 10, Deleted: 2}, {Path: "a_test.go", Added: 3, Deleted: 1}},
		[]gitcmd.FileChurn{{Path: "a.go", Added: 5, Deleted: 5}, {Path: "vendored/skip.go", Deleted: 7}},
		[]gitcmd.FileChurn{{Path: "logo.png"}, {Path: "a.go", Added: 1}},
	)
	got := foldCochange(commits, []string{"a.go", "a_test.go", "logo.png"})

	want := map[string]pathHistory{
		"a.go":      {revisions: []int{0, 1, 2}, churn: 23},
		"a_test.go": {revisions: []int{0}, churn: 4},
		"logo.png":  {revisions: []int{2}, churn: 0},
	}
	if !slices.Equal(slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want))) {
		t.Fatalf("paths = %v, want %v", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
	for path, w := range want {
		if !slices.Equal(got[path].revisions, w.revisions) || got[path].churn != w.churn {
			t.Errorf("%s = %+v, want %+v", path, got[path], w)
		}
	}
}

func TestFoldCochangeIgnoresUnrequestedPaths(t *testing.T) {
	commits := churnScan([]gitcmd.FileChurn{{Path: "wanted.go", Added: 1, Deleted: 1}, {Path: "unwanted.go", Added: 9, Deleted: 9}})
	got := foldCochange(commits, []string{"wanted.go"})
	if len(got) != 1 || got["wanted.go"].churn != 2 {
		t.Fatalf("foldCochange = %+v, want only wanted.go with churn 2", got)
	}
}

func TestCouplingsSkipsSweepingCommits(t *testing.T) {
	sweeping := make([]string, cochangeCommitPaths+1)
	history := map[string]pathHistory{}
	for i := range sweeping {
		path := "swept/f" + string(rune('a'+i%26)) + string(rune('a'+i/26)) + ".go"
		sweeping[i] = path
		history[path] = pathHistory{revisions: []int{0}}
	}
	history["a.go"] = pathHistory{revisions: []int{1, 2}}
	history["b.go"] = pathHistory{revisions: []int{1, 2}}

	shared := couplings(history)
	if got := shared[[2]string{"a.go", "b.go"}]; got != 2 {
		t.Errorf("a.go/b.go share %d revisions, want 2", got)
	}
	for pair := range shared {
		if pair[0] != "a.go" {
			t.Fatalf("a %d-path sweep coupled %v", len(sweeping), pair)
		}
	}
}

// TestAddCochangeEdgesAreAdvisory pins the schema contract: a co-change edge
// is derived and advisory, so a ranker may only adjust a score by it. Code and
// its test always co-change, which is exactly the pair this builds.
func TestAddCochangeEdgesAreAdvisory(t *testing.T) {
	b := newBuilder(nil)
	for _, path := range []string{"a.go", "a_test.go", "lonely.go"} {
		b.addNode(Node{ID: PathNode(path), Kind: NodePath, Value: path})
	}
	history := map[string]pathHistory{
		"a.go":      {revisions: []int{0, 1, 2}, churn: 30},
		"a_test.go": {revisions: []int{0, 1, 2}, churn: 12},
		"lonely.go": {revisions: []int{9}, churn: 4},
	}
	b.applyCochange(history)

	e, ok := b.edges[edgeKey{from: PathNode("a.go"), to: PathNode("a_test.go"), kind: EdgeCochange}]
	if !ok {
		t.Fatal("no cochange edge between a file and its test")
	}
	if !e.Advisory || !e.Derived {
		t.Errorf("cochange edge = %+v, want Advisory and Derived", e)
	}
	if e.Weight != 1 {
		t.Errorf("weight = %v, want 1: the two share every revision", e.Weight)
	}
	if node := b.nodes[PathNode("a.go")]; node.Revisions != 3 || node.Churn != 30 {
		t.Errorf("a.go node = %+v, want 3 revisions and 30 churn", node)
	}
	if _, ok := b.edges[edgeKey{from: PathNode("a.go"), to: PathNode("lonely.go"), kind: EdgeCochange}]; ok {
		t.Error("coupled a path that shares no revision")
	}
}
