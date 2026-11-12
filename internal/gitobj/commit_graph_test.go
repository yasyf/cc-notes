package gitobj_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func writeCommitGraph(t *testing.T, dir string, tips ...model.SHA) {
	t.Helper()
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	cmd := exec.Command("git", "commit-graph", "write", "--stdin-commits")
	cmd.Dir = dir
	var stdin strings.Builder
	for _, tip := range tips {
		stdin.WriteString(string(tip) + "\n")
	}
	cmd.Stdin = strings.NewReader(stdin.String())
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git commit-graph write: %v\n%s", err, out)
	}
}

// TestIsAncestorAcrossCommitGraphChanges pins verdicts against git's while the
// commit-graph appears and disappears under a live handle. A frontier expanded
// under one graph carries that graph's generations; pruned against the
// unknown generation a commit has once the graph is gone, it would refuse a
// real ancestor.
func TestIsAncestorAcrossCommitGraphChanges(t *testing.T) {
	dir := initRepo(t)
	writer := open(t, dir)
	chain := opsChain(t, writer, 4)
	root, mid, tip := chain[0], chain[2], chain[3]
	var stranger model.SHA
	for i := range 3 {
		var parents []model.SHA
		if stranger != "" {
			parents = []model.SHA{stranger}
		}
		pack := model.Pack{Lamport: model.Lamport(i + 1), Ops: []model.Op{model.SetTitle{Title: "stranger " + strconv.Itoa(i)}}}
		stranger = write(t, writer, parents, t0.Add(time.Hour+time.Duration(i)*time.Minute), pack)
	}
	graph := filepath.Join(dir, ".git", "objects", "info", "commit-graph")
	repo := open(t, dir)
	ctx := t.Context()

	for _, step := range []struct {
		name  string
		setup func()
		a, b  model.SHA
	}{
		{"with a graph, the frontier stops short of the root", func() { writeCommitGraph(t, dir, tip, stranger) }, mid, tip},
		{"with a graph, an unrelated commit above the frontier is pruned", func() {}, stranger, tip},
		{"after the graph is removed", func() {
			if err := os.Remove(graph); err != nil {
				t.Fatal(err)
			}
		}, root, tip},
		{"after the graph is rewritten", func() { writeCommitGraph(t, dir, tip, stranger) }, root, tip},
	} {
		step.setup()
		got, err := repo.IsAncestor(ctx, step.a, step.b)
		if err != nil {
			t.Fatalf("%s: IsAncestor(%s, %s): %v", step.name, step.a, step.b, err)
		}
		if want := gitIsAncestor(t, dir, step.a, step.b); got != want {
			t.Fatalf("%s: IsAncestor(%s, %s) = %v, want git's %v", step.name, step.a, step.b, got, want)
		}
	}
}

// TestAncestryAnswersGraphHeldCommitsWithoutReadingThem pins that a commit the
// commit-graph holds is never read as an object: the graph here comes from
// another repository, so an object read for either endpoint would miss.
func TestAncestryAnswersGraphHeldCommitsWithoutReadingThem(t *testing.T) {
	source := initRepo(t)
	gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "parent")
	parent := model.SHA(gittest.Git(t, source, "rev-parse", "HEAD"))
	gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "child")
	child := model.SHA(gittest.Git(t, source, "rev-parse", "HEAD"))
	writeCommitGraph(t, source, child)
	data, err := os.ReadFile(filepath.Join(source, ".git", "objects", "info", "commit-graph"))
	if err != nil {
		t.Fatal(err)
	}
	dir := initRepo(t)
	if err := os.WriteFile(filepath.Join(dir, ".git", "objects", "info", "commit-graph"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := open(t, dir).Ancestry(t.Context(), parent, child); err != nil || got != gitobj.Ancestor {
		t.Fatalf("Ancestry(%s, %s) = %v, %v, want Ancestor from the graph alone", parent, child, got, err)
	}
}
