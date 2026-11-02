package store

import (
	"context"
	"sync"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

const (
	cachePublicationRows  = 64
	cachePublicationBytes = 8 << 20
)

type cachePublication struct {
	name       string
	data       []byte
	supersedes []string
}

func foldPublication(tip model.SHA, snap model.Snapshot, top, floor model.Lamport, superseded ...model.SHA) *cachePublication {
	if snap.Meta().SkippedOps > 0 {
		return nil
	}
	data, ok := encodeFoldEntry(snap, top, floor)
	if !ok {
		return nil
	}
	supersedes := make([]string, len(superseded))
	for i, old := range superseded {
		supersedes[i] = string(old)
	}
	return &cachePublication{name: string(tip), data: data, supersedes: supersedes}
}

func foldChainPublication(tip model.SHA, snap model.Snapshot, chain []model.PackCommit, superseded ...model.SHA) *cachePublication {
	return foldPublication(tip, snap, topLamport(chain), fold.ExtendFloor(chain), superseded...)
}

type foldPublicationBatch struct {
	ctx    context.Context
	cache  *foldCache
	mu     sync.Mutex
	values []cachePublication
	bytes  int
}

func newFoldPublicationBatch(ctx context.Context, cache *foldCache) *foldPublicationBatch {
	return &foldPublicationBatch{ctx: ctx, cache: cache}
}

func (b *foldPublicationBatch) putChain(tip model.SHA, snap model.Snapshot, chain []model.PackCommit) {
	b.add(foldChainPublication(tip, snap, chain))
}

func (b *foldPublicationBatch) add(value *cachePublication) {
	if value == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ctx.Err() != nil {
		b.flushLocked()
		return
	}
	if len(value.data) > cachePublicationBytes {
		b.flushLocked()
		if b.ctx.Err() == nil {
			b.cache.writeBatch([]cachePublication{*value})
		}
		return
	}
	if len(value.data) > cachePublicationBytes-b.bytes {
		b.flushLocked()
	}
	if b.ctx.Err() != nil {
		return
	}
	b.values = append(b.values, *value)
	b.bytes += len(value.data)
	if len(b.values) == cachePublicationRows {
		b.flushLocked()
	}
}

func (b *foldPublicationBatch) flush() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.flushLocked()
}

func (b *foldPublicationBatch) flushLocked() {
	if len(b.values) != 0 && b.ctx.Err() == nil {
		b.cache.writeBatch(b.values)
	}
	clear(b.values)
	b.values = b.values[:0]
	b.bytes = 0
}

func (b *foldPublicationBatch) discard() {
	b.mu.Lock()
	defer b.mu.Unlock()
	clear(b.values)
	b.values = b.values[:0]
	b.bytes = 0
}
