package sourcegraph

import (
	"bytes"
	"fmt"
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
