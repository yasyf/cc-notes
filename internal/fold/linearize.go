package fold

import (
	"container/heap"
	"errors"
	"fmt"

	"github.com/yasyf/cc-notes/internal/model"
)

var (
	// ErrEmptyChain reports a chain with no commits.
	ErrEmptyChain = errors.New("empty chain")
	// ErrMissingParent reports a commit whose parent is not in the chain:
	// a shallow or corrupt history.
	ErrMissingParent = errors.New("missing parent commit")
	// ErrMultipleRoots reports a chain with more than one parentless commit.
	ErrMultipleRoots = errors.New("multiple root commits")
	// ErrMultipleHeads reports a chain with more than one childless commit.
	ErrMultipleHeads = errors.New("multiple head commits")
	// ErrCorruptChain reports a chain that is not a single-entity DAG:
	// duplicate commit ids, no root, or a parent cycle.
	ErrCorruptChain = errors.New("corrupt chain")
)

// Linearize totally orders an entity's commit DAG using Kahn's algorithm with
// a deterministic ready frontier: among commits whose parents have all been
// emitted, the least by (pack lamport, author time, sha) goes next. Every
// replica therefore linearizes the same set of commits identically,
// regardless of input order. The chain must contain exactly one root (the
// create commit) and exactly one head (the tip), with every referenced
// parent present. The input slice is not modified.
func Linearize(commits []model.PackCommit) ([]model.PackCommit, error) {
	if len(commits) == 0 {
		return nil, ErrEmptyChain
	}
	byID := make(map[model.SHA]model.PackCommit, len(commits))
	for _, c := range commits {
		if _, ok := byID[c.SHA]; ok {
			return nil, fmt.Errorf("%w: duplicate commit %s", ErrCorruptChain, c.SHA)
		}
		byID[c.SHA] = c
	}
	children := make(map[model.SHA][]model.SHA, len(commits))
	indegree := make(map[model.SHA]int, len(commits))
	roots := 0
	for _, c := range commits {
		if len(c.Parents) == 0 {
			roots++
		}
		indegree[c.SHA] = len(c.Parents)
		for _, p := range c.Parents {
			if _, ok := byID[p]; !ok {
				return nil, fmt.Errorf("%w: %s referenced by %s", ErrMissingParent, p, c.SHA)
			}
			children[p] = append(children[p], c.SHA)
		}
	}
	switch {
	case roots == 0:
		return nil, fmt.Errorf("%w: no root commit", ErrCorruptChain)
	case roots > 1:
		return nil, fmt.Errorf("%w: found %d", ErrMultipleRoots, roots)
	}
	heads := 0
	for _, c := range commits {
		if len(children[c.SHA]) == 0 {
			heads++
		}
	}
	if heads > 1 {
		return nil, fmt.Errorf("%w: found %d", ErrMultipleHeads, heads)
	}
	frontier := &commitHeap{}
	for _, c := range commits {
		if indegree[c.SHA] == 0 {
			heap.Push(frontier, c)
		}
	}
	ordered := make([]model.PackCommit, 0, len(commits))
	for frontier.Len() > 0 {
		c := heap.Pop(frontier).(model.PackCommit)
		ordered = append(ordered, c)
		for _, child := range children[c.SHA] {
			indegree[child]--
			if indegree[child] == 0 {
				heap.Push(frontier, byID[child])
			}
		}
	}
	if len(ordered) != len(commits) {
		return nil, fmt.Errorf("%w: cycle among %d commits", ErrCorruptChain, len(commits)-len(ordered))
	}
	return ordered, nil
}

// commitHeap is the ready frontier of Kahn's algorithm: a min-heap keyed by
// (pack lamport, author time, sha). The sha tiebreak makes the order total,
// so linearization is deterministic even for concurrent same-second commits.
type commitHeap []model.PackCommit

func (h commitHeap) Len() int { return len(h) }

func (h commitHeap) Less(i, j int) bool {
	a, b := h[i], h[j]
	if a.Pack.Lamport != b.Pack.Lamport {
		return a.Pack.Lamport < b.Pack.Lamport
	}
	if a.AuthorTime != b.AuthorTime {
		return a.AuthorTime < b.AuthorTime
	}
	return a.SHA < b.SHA
}

func (h commitHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *commitHeap) Push(x any) { *h = append(*h, x.(model.PackCommit)) }

func (h *commitHeap) Pop() any {
	old := *h
	n := len(old) - 1
	c := old[n]
	*h = old[:n]
	return c
}
