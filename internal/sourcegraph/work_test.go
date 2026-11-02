package sourcegraph

import (
	"testing"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

func TestSourceGraphWorkAcrossIndependentScopes(t *testing.T) {
	repo, _ := graphFixture(t)
	ctx, counter := WithWork(t.Context())
	var root model.SHA
	var expected Work
	add := func(work Work) {
		expected.NodeReads += work.NodeReads
		expected.NodeWrites += work.NodeWrites
		expected.ValueReads += work.ValueReads
		expected.ValueWrites += work.ValueWrites
		expected.KeysVisited += work.KeysVisited
		expected.DiffPairs += work.DiffPairs
	}
	if err := repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
		graph := New(objects)
		var err error
		root, err = graph.Put(root, "one", []byte("body"))
		add(graph.Stats())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.SourceObjects(ctx, func(objects *gitobj.SourceObjects) error {
		first, second := New(objects), New(objects)
		if _, _, err := first.Get(root, "one"); err != nil {
			return err
		}
		if err := second.Diff("", root, func(Change) error { return nil }); err != nil {
			return err
		}
		add(first.Stats())
		add(second.Stats())
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if actual := counter.Snapshot(); actual != expected || actual.NodeReads == 0 || actual.NodeWrites == 0 || actual.KeysVisited == 0 || actual.DiffPairs == 0 {
		t.Fatalf("actual=%+v expected=%+v", actual, expected)
	}
	_, isolated := WithWork(t.Context())
	if actual := isolated.Snapshot(); actual != (Work{}) {
		t.Fatalf("isolated=%+v", actual)
	}
}
