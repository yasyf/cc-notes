package sourcegraph

import (
	"context"
	"sync/atomic"
)

type workKey struct{}

// WorkCounter aggregates graph work across every map in an operation context.
type WorkCounter struct {
	nodeReads   atomic.Uint64
	nodeWrites  atomic.Uint64
	valueReads  atomic.Uint64
	valueWrites atomic.Uint64
	keysVisited atomic.Uint64
	diffPairs   atomic.Uint64
}

// WithWork attaches independent graph counters to an operation context.
func WithWork(ctx context.Context) (context.Context, *WorkCounter) {
	counter := &WorkCounter{}
	return context.WithValue(ctx, workKey{}, counter), counter
}

// Snapshot returns the actual graph work observed so far.
func (c *WorkCounter) Snapshot() Work {
	return Work{
		NodeReads: c.nodeReads.Load(), NodeWrites: c.nodeWrites.Load(),
		ValueReads: c.valueReads.Load(), ValueWrites: c.valueWrites.Load(),
		KeysVisited: c.keysVisited.Load(), DiffPairs: c.diffPairs.Load(),
	}
}
