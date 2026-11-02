package gitobj

import (
	"context"
	"sync/atomic"
)

type readWorkKey struct{}

// ReadWork records the native ref and pack work performed by one operation.
type ReadWork struct {
	refListings atomic.Int64
	refEntries  atomic.Int64
	packDecodes atomic.Int64
}

// WithReadWork attaches a fresh counter set to the operation context.
func WithReadWork(ctx context.Context) (context.Context, *ReadWork) {
	work := &ReadWork{}
	return context.WithValue(ctx, readWorkKey{}, work), work
}

// Snapshot returns completed native listings, their ref entries, and pack decodes.
func (w *ReadWork) Snapshot() (refListings, refEntries, packDecodes int64) {
	return w.refListings.Load(), w.refEntries.Load(), w.packDecodes.Load()
}

func recordRefListing(ctx context.Context, count int) {
	if work, ok := ctx.Value(readWorkKey{}).(*ReadWork); ok {
		work.refListings.Add(1)
		work.refEntries.Add(int64(count))
	}
}

func recordPackDecode(ctx context.Context) {
	if work, ok := ctx.Value(readWorkKey{}).(*ReadWork); ok {
		work.packDecodes.Add(1)
	}
}
