package sourcegraph

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/homeguard"
	"github.com/yasyf/cc-notes/model"
)

func TestMain(m *testing.M) { homeguard.Main(m) }

func graphFixture(t *testing.T) (*gitobj.Repo, string) {
	t.Helper()
	dir := gittest.InitRepo(t)
	repo, err := gitobj.Open(gittest.Dirs(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	return repo, dir
}

func graphScope(t *testing.T, repo *gitobj.Repo, run func(*gitobj.SourceObjects) error) {
	t.Helper()
	if err := repo.SourceObjects(t.Context(), run); err != nil {
		t.Fatal(err)
	}
}

func TestSourceGraphCanonicalByteOrderAndFreshRead(t *testing.T) {
	repo, dir := graphFixture(t)
	keys := []string{"", "A", "B", "a", "a\x00", "a\x00b", "aa", "ab", "b", "é", "ÿ", strings.Repeat("x", MaxKeyBytes)}
	values := make(map[string][]byte, len(keys))
	for i, key := range keys {
		values[key] = []byte{byte(i), 0, byte(i + 1)}
	}
	values["a"] = []byte{}
	var root model.SHA
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		graph := New(objects)
		var err error
		for _, key := range keys {
			root, err = graph.Put(root, key, values[key])
			if err != nil {
				return err
			}
		}
		var reverse model.SHA
		for i := len(keys) - 1; i >= 0; i-- {
			reverse, err = graph.Put(reverse, keys[i], values[keys[i]])
			if err != nil {
				return err
			}
		}
		if root != reverse {
			t.Fatalf("canonical roots differ: forward=%s reverse=%s", root, reverse)
		}
		checkGraphOracle(t, graph, root, values)
		if err := graph.Validate(root); err != nil {
			return err
		}
		return nil
	})
	gittest.Git(t, dir, "update-ref", "refs/cc-notes-sourcegraph/test", string(root))
	gittest.Git(t, dir, "repack", "-adq")
	repo, err := gitobj.Open(gittest.Dirs(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		checkGraphOracle(t, New(objects), root, values)
		return nil
	})
}

func TestSourceGraphMutationDiffProperty(t *testing.T) {
	repo, _ := graphFixture(t)
	random := rand.New(rand.NewPCG(20261102, 73))
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		graph := New(objects)
		var root model.SHA
		oracle := make(map[string][]byte)
		for step := range 192 {
			before, previous := root, maps.Clone(oracle)
			key := fmt.Sprintf("prefix/%02x", random.IntN(40))
			if step%11 == 0 {
				key = "prefix"
			}
			if step%17 == 0 {
				key = ""
			}
			var err error
			if random.IntN(3) == 0 {
				root, err = graph.Delete(root, key)
				delete(oracle, key)
			} else {
				value := []byte{byte(step), byte(step >> 8)}
				if step%13 == 0 {
					value = nil
				}
				root, err = graph.Put(root, key, value)
				oracle[key] = bytes.Clone(value)
			}
			if err != nil {
				return err
			}
			checkGraphOracle(t, graph, root, oracle)
			checkGraphDiff(t, graph, before, root, previous, oracle)
			checkGraphDiff(t, graph, root, before, oracle, previous)
			if step%16 == 0 {
				if err := graph.Validate(root); err != nil {
					return err
				}
			}
		}
		for _, key := range slices.Sorted(maps.Keys(oracle)) {
			var err error
			root, err = graph.Delete(root, key)
			if err != nil {
				return err
			}
		}
		if root != "" {
			t.Fatalf("deleting final membership retained root %s", root)
		}
		return nil
	})
}

func TestSourceGraphCompressedDiffAlignments(t *testing.T) {
	repo, _ := graphFixture(t)
	sets := [][]string{{"a"}, {"aa", "ab"}, {"a", "aa", "b"}, {"b"}, {"", "a", "aa", "ab", "b"}, {}}
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		graph := New(objects)
		roots := make([]model.SHA, len(sets))
		oracles := make([]map[string][]byte, len(sets))
		for i, keys := range sets {
			oracles[i] = make(map[string][]byte)
			for _, key := range keys {
				value := []byte(key + " value")
				next, err := graph.Put(roots[i], key, value)
				if err != nil {
					return err
				}
				roots[i], oracles[i][key] = next, value
			}
		}
		for i := range roots {
			for j := range roots {
				checkGraphDiff(t, graph, roots[i], roots[j], oracles[i], oracles[j])
			}
		}
		return nil
	})
}

func TestSourceGraphRejectsCorruptCountsAndEdges(t *testing.T) {
	repo, _ := graphFixture(t)
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		graph := New(objects)
		root, err := graph.Apply("", []Mutation{{Key: "a", Value: []byte("a")}, {Key: "b", Value: []byte("b")}})
		if err != nil {
			return err
		}
		original, err := graph.load(root)
		if err != nil {
			return err
		}
		for _, kind := range []string{"total", "edge", "prefix", "extra"} {
			h := original.header
			if kind == "total" {
				h.Count++
			}
			if kind == "edge" {
				for i := range h.Children {
					if h.Children[i].Hash != "" {
						h.Children[i].Count++
						h.Count++
						break
					}
				}
			}
			if kind == "prefix" {
				h.Prefix += "0"
			}
			data, err := json.Marshal(h)
			if err != nil {
				return err
			}
			blob, err := objects.WriteBlob(data)
			if err != nil {
				return err
			}
			entries, err := objects.ReadTree(root)
			if err != nil {
				return err
			}
			for i := range entries {
				if entries[i].Name == "node" {
					entries[i].Hash = plumbing.NewHash(string(blob))
				}
			}
			if kind == "extra" {
				entries = append(entries, object.TreeEntry{Name: "extra", Mode: filemode.Regular, Hash: plumbing.NewHash(string(blob))})
			}
			broken, err := objects.WriteTree(entries)
			if err != nil {
				return err
			}
			if err := New(objects).Validate(broken); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("%s corruption error=%v, want ErrCorrupt", kind, err)
			}
		}
		return nil
	})
}

func TestSourceGraphOperationLifetimeAndCancellation(t *testing.T) {
	repo, _ := graphFixture(t)
	var graph *Map
	graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
		graph = New(objects)
		if _, err := graph.Put("", strings.Repeat("x", MaxKeyBytes+1), nil); err == nil {
			t.Fatal("accepted oversized key")
		}
		if _, err := graph.Apply("", []Mutation{{Key: "duplicate"}, {Key: "duplicate"}}); err == nil {
			t.Fatal("accepted duplicate mutation key")
		}
		return nil
	})
	if _, err := graph.Count(""); !errors.Is(err, fs.ErrClosed) {
		t.Fatalf("released scope count=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := repo.SourceObjects(ctx, func(*gitobj.SourceObjects) error { t.Fatal("entered cancelled scope"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled scope=%v", err)
	}
}

func TestSourceGraphSingleKeyWork(t *testing.T) {
	if os.Getenv("CCN_REQUIRE_SOURCEGRAPH_COST") != "1" {
		t.Skip("strict hosted sourcegraph cardinality proof")
	}
	for _, count := range []int{32, 10000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			repo, _ := graphFixture(t)
			var root, changed model.SHA
			graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
				graph := New(objects)
				for i := range count {
					var err error
					root, err = graph.Put(root, fmt.Sprintf("entity/%08x", i), []byte("fixed"))
					if err != nil {
						return err
					}
				}
				return nil
			})
			graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
				graph := New(objects)
				value, found, err := graph.Get(root, "entity/00000000")
				if err != nil || !found || string(value) != "fixed" {
					t.Fatalf("fixed get=%q/%t/%v", value, found, err)
				}
				changed, err = graph.Put(root, "entity/00000000", []byte("changed"))
				if err != nil {
					return err
				}
				return nil
			})
			graphScope(t, repo, func(objects *gitobj.SourceObjects) error {
				graph := New(objects)
				changes := 0
				if err := graph.Diff(root, changed, func(change Change) error {
					changes++
					if change.Key != "entity/00000000" || string(change.Before) != "fixed" || string(change.After) != "changed" {
						t.Fatalf("unexpected delta=%+v", change)
					}
					return nil
				}); err != nil {
					return err
				}
				work := graph.Stats()
				t.Logf("refs=%d changed_keys=%d work=%+v", count, changes, work)
				if changes != 1 || work.KeysVisited != 2 || work.ValueReads != 2 || work.NodeReads > 32 || work.DiffPairs > 16 {
					t.Fatalf("SOURCEGRAPH_CORPUS_WORK_REGRESSION: refs=%d work=%+v changes=%d", count, work, changes)
				}
				return nil
			})
		})
	}
}

func checkGraphOracle(t *testing.T, graph *Map, root model.SHA, oracle map[string][]byte) {
	t.Helper()
	keys := slices.Sorted(maps.Keys(oracle))
	count, err := graph.Count(root)
	if err != nil || count != uint64(len(keys)) {
		t.Fatalf("count=%d/%v want=%d", count, err, len(keys))
	}
	entries, err := graph.Range(root, 0, len(keys)+2)
	if err != nil || len(entries) != len(keys) {
		t.Fatalf("range=%v/%v want=%v", entries, err, keys)
	}
	for i, key := range keys {
		if entries[i].Key != key || !bytes.Equal(entries[i].Value, oracle[key]) {
			t.Fatalf("ordered entry[%d]=%+v, want key=%q value=%v", i, entries[i], key, oracle[key])
		}
		value, found, err := graph.Get(root, key)
		if err != nil || !found || !bytes.Equal(value, oracle[key]) {
			t.Fatalf("get(%q)=%v/%t/%v, want %v", key, value, found, err, oracle[key])
		}
		if len(value) != 0 {
			value[0] ^= 0xff
			second, _, err := graph.Get(root, key)
			if err != nil || !bytes.Equal(second, oracle[key]) {
				t.Fatalf("get(%q) aliased the immutable value", key)
			}
		}
		rank, present, err := graph.Rank(root, key)
		if err != nil || !present || rank != uint64(i) {
			t.Fatalf("rank(%q)=%d/%t/%v, want %d/true", key, rank, present, err, i)
		}
		page, err := graph.Range(root, uint64(i), 1)
		if err != nil || len(page) != 1 || page[0].Key != key {
			t.Fatalf("page(%d)=%v/%v", i, page, err)
		}
	}
	for _, probe := range []string{"", "A", "prefix", "prefix/0", "prefix/ff", "zzzz"} {
		wantRank, wantPresent := slices.BinarySearch(keys, probe)
		rank, present, err := graph.Rank(root, probe)
		if err != nil || rank != uint64(wantRank) || present != wantPresent {
			t.Fatalf("probe rank(%q)=%d/%t/%v, want %d/%t", probe, rank, present, err, wantRank, wantPresent)
		}
		var actual []string
		if err := graph.Prefix(root, probe, func(entry Entry) error { actual = append(actual, entry.Key); return nil }); err != nil {
			t.Fatal(err)
		}
		var expected []string
		for _, key := range keys {
			if strings.HasPrefix(key, probe) {
				expected = append(expected, key)
			}
		}
		if !slices.Equal(actual, expected) {
			t.Fatalf("prefix(%q)=%q want=%q", probe, actual, expected)
		}
	}
}

func checkGraphDiff(t *testing.T, graph *Map, before, after model.SHA, old, next map[string][]byte) {
	t.Helper()
	union := maps.Clone(old)
	maps.Copy(union, next)
	expected := make([]Change, 0)
	for _, key := range slices.Sorted(maps.Keys(union)) {
		left, leftOK := old[key]
		right, rightOK := next[key]
		if leftOK != rightOK || !bytes.Equal(left, right) {
			expected = append(expected, Change{Key: key, Before: left, After: right, BeforeExists: leftOK, AfterExists: rightOK})
		}
	}
	var actual []Change
	if err := graph.Diff(before, after, func(change Change) error { actual = append(actual, change); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(actual) != len(expected) {
		t.Fatalf("delta=%+v, want=%+v", actual, expected)
	}
	for i, change := range actual {
		want := expected[i]
		if change.Key != want.Key || change.BeforeExists != want.BeforeExists || change.AfterExists != want.AfterExists || !bytes.Equal(change.Before, want.Before) || !bytes.Equal(change.After, want.After) {
			t.Fatalf("delta[%d]=%+v, want=%+v", i, change, want)
		}
	}
}
