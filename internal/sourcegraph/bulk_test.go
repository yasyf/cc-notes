package sourcegraph

import (
	"bytes"
	"fmt"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

func TestSourceGraphBulkCanonicalAndLinearWrites(t *testing.T) {
	repo, _ := graphFixture(t)
	for _, count := range []int{32, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			entries := make([]Entry, 0, count+5)
			for i := range count {
				entries = append(entries, Entry{Key: fmt.Sprintf("refs/cc-notes/notes/%040x", count-i), Value: []byte(fmt.Sprint(i))})
			}
			for _, key := range []string{"", "a", "a\x00", "é", strings.Repeat("z", MaxKeyBytes)} {
				entries = append(entries, Entry{Key: key, Value: []byte{}})
			}
			var root model.SHA
			graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
				graph := New(objects)
				var err error
				root, err = graph.Build(entries)
				if err != nil {
					return err
				}
				work := graph.Stats()
				if work.NodeWrites > uint64(2*len(entries)-1) || work.ValueWrites != uint64(len(entries)) || work.NodeReads != 0 {
					t.Fatalf("bulk work=%+v entries=%d", work, len(entries))
				}
				t.Logf("entries=%d writes=%d values=%d", len(entries), work.NodeWrites, work.ValueWrites)
				if count == 32 {
					var sequential model.SHA
					for _, entry := range entries {
						sequential, err = graph.Put(sequential, entry.Key, entry.Value)
						if err != nil {
							return err
						}
					}
					if sequential != root {
						t.Fatalf("bulk root=%s sequential=%s", root, sequential)
					}
				}
				return nil
			})
			graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
				graph := New(objects)
				if err := graph.Validate(root); err != nil {
					return err
				}
				for _, entry := range entries {
					value, found, err := graph.Get(root, entry.Key)
					if err != nil {
						return err
					}
					if !found || !bytes.Equal(value, entry.Value) {
						t.Fatalf("fresh key %q=%q/%t", entry.Key, value, found)
					}
				}
				return nil
			})
		})
	}
}

func TestSourceGraphBulkRetentionCanonical(t *testing.T) {
	repo, _ := graphFixture(t)
	tip := rawRetentionTip(t, repo, "bulk", strings.Repeat("9", 32))
	entries := []RetentionEntry{{Key: "a", Entity: RetainedEntity{Tip: tip}}, {Key: "a\x00", Entity: RetainedEntity{Tip: tip}}, {Key: "", Entity: RetainedEntity{Tip: tip}}, {Key: "é", Entity: RetainedEntity{Tip: tip}}}
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		graph := NewRetention(objects)
		bulk, err := graph.Build(entries)
		if err != nil {
			return err
		}
		if graph.Stats().NodeWrites > uint64(2*len(entries)-1) {
			t.Fatalf("retention bulk work=%+v", graph.Stats())
		}
		var sequential RetentionRoot
		for _, entry := range entries {
			sequential, err = graph.Put(sequential, entry.Key, entry.Entity)
			if err != nil {
				return err
			}
		}
		if bulk != sequential {
			t.Fatalf("bulk=%+v sequential=%+v", bulk, sequential)
		}
		return NewRetention(objects).Validate(bulk)
	})
}

func TestSourceGraphBulkRejectsDuplicateAndInvalidKeys(t *testing.T) {
	repo, _ := graphFixture(t)
	tip := rawRetentionTip(t, repo, "duplicate", strings.Repeat("8", 32))
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		for _, entries := range [][]Entry{{{Key: "same"}, {Key: "same"}}, {{Key: strings.Repeat("x", MaxKeyBytes+1)}}} {
			if _, err := New(objects).Build(entries); err == nil {
				t.Fatalf("accepted entries=%v", entries)
			}
		}
		if _, err := NewRetention(objects).Build([]RetentionEntry{{Key: "same", Entity: RetainedEntity{Tip: tip}}, {Key: "same", Entity: RetainedEntity{Tip: tip}}}); err == nil {
			t.Fatal("accepted duplicate retention keys")
		}
		return nil
	})
}

func TestSourceGraphApplyMatchesSequentialMutations(t *testing.T) {
	repo, _ := graphFixture(t)
	random := rand.New(rand.NewPCG(1, 2))
	keys := make([]string, 0, 137)
	keys = append(keys, "", "a", "a\x00", "ab", "abc", "abd", "b", "é", strings.Repeat("z", MaxKeyBytes))
	for i := range 64 {
		keys = append(keys, fmt.Sprintf("rows/%03d", i), fmt.Sprintf("labels/%03d", i))
	}
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		graph := New(objects)
		var batched, sequential model.SHA
		for round := range 200 {
			chosen := make(map[string]Mutation)
			for range 1 + random.IntN(32) {
				key := keys[random.IntN(len(keys))]
				if random.IntN(2) == 0 {
					chosen[key] = Mutation{Key: key, Delete: true}
				} else {
					chosen[key] = Mutation{Key: key, Value: []byte(fmt.Sprint(random.IntN(4)))}
				}
			}
			mutations := slices.Collect(maps.Values(chosen))
			var err error
			batched, err = graph.Apply(batched, mutations)
			if err != nil {
				return err
			}
			for _, mutation := range mutations {
				if mutation.Delete {
					sequential, err = graph.Delete(sequential, mutation.Key)
				} else {
					sequential, err = graph.Put(sequential, mutation.Key, mutation.Value)
				}
				if err != nil {
					return err
				}
			}
			if batched != sequential {
				t.Fatalf("round %d batched=%s sequential=%s mutations=%+v", round, batched, sequential, mutations)
			}
			if err := graph.Validate(batched); err != nil {
				return err
			}
		}
		return nil
	})
}

func TestSourceGraphApplyWritesEachTouchedNodeOnce(t *testing.T) {
	repo, _ := graphFixture(t)
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		mutations := make([]Mutation, 4096)
		for i := range mutations {
			mutations[i] = Mutation{Key: fmt.Sprintf("rows/%040x", i), Value: []byte(fmt.Sprint(i))}
		}
		graph := New(objects)
		root, err := graph.Apply("", mutations)
		if err != nil {
			return err
		}
		if writes := graph.Stats().NodeWrites; writes > uint64(2*len(mutations)-1) {
			t.Fatalf("empty root rewrote %d nodes for %d keys", writes, len(mutations))
		}
		before := graph.Stats().NodeWrites
		same, err := graph.Apply(root, mutations)
		if err != nil {
			return err
		}
		if same != root || graph.Stats().NodeWrites != before {
			t.Fatalf("identical batch root=%s want=%s writes=%d", same, root, graph.Stats().NodeWrites-before)
		}
		return nil
	})
}
