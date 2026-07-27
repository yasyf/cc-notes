package kg

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os/exec"
	"slices"
	"strconv"
	"strings"
)

const (
	// cochangeMinRevisions is the smallest revision count a path needs before
	// its coupling is measured: one shared commit between two paths each seen
	// once is a coincidence.
	cochangeMinRevisions = 2
	// cochangeMinWeight is the smallest coupling worth an edge.
	cochangeMinWeight = 0.2
	// cochangeCommitPaths caps how many scanned paths one commit may couple. A
	// commit that swept most of the graph's paths — a rename, a formatting
	// pass — couples none of them.
	cochangeCommitPaths = 32
	// cochangeWindow caps the commits the scan reads, newest first. --numstat
	// diffs every commit it reports, which is the build's dominant cost
	// (2.0 s of 2.5 s over ~100 matching monorepo commits), and coupling from
	// far enough back is stale anyway.
	cochangeWindow = 1000
)

// pathHistory is one path's history volume: the commits that touched it, and
// the lines it gained and lost across them.
type pathHistory struct {
	revisions []int
	churn     int64
}

// cochangeLog reads the revision and churn history of paths in one git
// invocation scoped to exactly those paths. Scoping matters: an unscoped
// --numstat over a monorepo diffs every file of every commit.
func cochangeLog(ctx context.Context, dir string, paths []string) (map[string]pathHistory, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	flags := []string{
		"-C", dir, "log", "--no-merges", "--no-renames", "--numstat",
		"--format=%x00", "-n", strconv.Itoa(cochangeWindow), "--",
	}
	args := make([]string, 0, len(flags)+len(paths))
	args = append(append(args, flags...), paths...)
	//nolint:gosec // G204: git is a fixed argv[0], every flag is a literal, and the operands are repository paths the graph already holds.
	cmd := exec.CommandContext(ctx, "git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git log --numstat: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseNumstat(stdout.String(), paths), nil
}

// parseNumstat reads `git log --numstat --format=%x00` output: a NUL line
// opens each commit, then one "<added>\t<deleted>\t<path>" line per changed
// file. Git reports a commit's whole numstat even under a pathspec, so only
// the requested paths are kept; a binary file's "-" counts as no churn.
func parseNumstat(out string, paths []string) map[string]pathHistory {
	wanted := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		wanted[p] = struct{}{}
	}
	history := make(map[string]pathHistory, len(paths))
	revision := -1
	for line := range strings.SplitSeq(out, "\n") {
		if line == "\x00" {
			revision++
			continue
		}
		added, deleted, path, ok := numstatFields(line)
		if !ok {
			continue
		}
		if _, want := wanted[path]; !want {
			continue
		}
		h := history[path]
		if len(h.revisions) == 0 || h.revisions[len(h.revisions)-1] != revision {
			h.revisions = append(h.revisions, revision)
		}
		h.churn += added + deleted
		history[path] = h
	}
	return history
}

func numstatFields(line string) (added, deleted int64, path string, ok bool) {
	fields := strings.SplitN(line, "\t", 3)
	if len(fields) != 3 || fields[2] == "" {
		return 0, 0, "", false
	}
	added, _ = strconv.ParseInt(fields[0], 10, 64)
	deleted, _ = strconv.ParseInt(fields[1], 10, 64)
	return added, deleted, fields[2], true
}

// addCochange couples every pair of path nodes that keep changing in the same
// commit and stamps each path node with its own revision count and churn.
//
// The coupling is advisory. A source file and its test always co-change, so
// the signal carries a structural false-positive rate: it may adjust a rank,
// never assert that two paths are related.
func (b *builder) addCochange(ctx context.Context, dir string) error {
	history, err := cochangeLog(ctx, dir, b.pathValues())
	if err != nil {
		return err
	}
	b.applyCochange(history)
	return nil
}

// applyCochange stamps the scanned history onto the path nodes and emits the
// coupling edges it implies.
func (b *builder) applyCochange(history map[string]pathHistory) {
	for path, h := range history {
		node := b.nodes[PathNode(path)]
		node.Revisions, node.Churn = len(h.revisions), h.churn
		b.nodes[node.ID] = node
	}
	for pair, shared := range couplings(history) {
		a, z := history[pair[0]], history[pair[1]]
		if len(a.revisions) < cochangeMinRevisions || len(z.revisions) < cochangeMinRevisions {
			continue
		}
		weight := float64(shared) / float64(len(a.revisions)+len(z.revisions)-shared)
		if weight < cochangeMinWeight {
			continue
		}
		b.addEdge(Edge{
			From: PathNode(pair[0]), To: PathNode(pair[1]), Kind: EdgeCochange,
			Weight: weight, Derived: true, Advisory: true,
		})
	}
}

// couplings counts the revisions each pair of paths shares, keyed by the pair
// in sorted order.
func couplings(history map[string]pathHistory) map[[2]string]int {
	touched := map[int][]string{}
	for _, path := range slices.Sorted(maps.Keys(history)) {
		for _, revision := range history[path].revisions {
			touched[revision] = append(touched[revision], path)
		}
	}
	shared := map[[2]string]int{}
	for _, paths := range touched {
		if len(paths) < 2 || len(paths) > cochangeCommitPaths {
			continue
		}
		for i, a := range paths {
			for _, z := range paths[i+1:] {
				shared[[2]string{a, z}]++
			}
		}
	}
	return shared
}

// pathValues lists the repository paths the graph already holds a node for,
// sorted. They bound the scan: cc-notes couples the code its entities anchor,
// not the whole repository.
func (b *builder) pathValues() []string {
	var paths []string
	for _, n := range b.nodes {
		if n.Kind == NodePath {
			paths = append(paths, n.Value)
		}
	}
	slices.Sort(paths)
	return paths
}
