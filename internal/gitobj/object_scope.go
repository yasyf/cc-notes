package gitobj

import (
	"context"
	"errors"
	"os"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5"

	"github.com/yasyf/cc-notes/internal/gitcmd"
)

const (
	// batchPoolCap bounds the idle readers one handle keeps between reads;
	// reads hold the handle's mu, so one serves them and a second covers a
	// stream left open across a lookup.
	batchPoolCap = 2
	// batchIdleTTL retires a pooled reader before a long-running command pins
	// pack files a repack has since deleted.
	batchIdleTTL = 10 * time.Second
)

// Readers owns the native readers each handle keeps between the reads of one
// command, so a command that reads thousands of objects starts one git child
// per handle, not one per read. Close reaps them.
type Readers struct {
	mu     sync.Mutex
	pools  map[*Repo]*batchPool
	closed bool
}

type readersKey struct{}

// WithReaders returns ctx carrying a fresh Readers for the reads made under it.
// Reads under a context without one start and reap a reader per operation.
func WithReaders(ctx context.Context) (context.Context, *Readers) {
	readers := &Readers{pools: make(map[*Repo]*batchPool)}
	return context.WithValue(ctx, readersKey{}, readers), readers
}

// ShareReaders returns ctx carrying Readers, so a run of separate reads shares
// pooled readers: ctx itself when it already carries one, otherwise ctx with a
// fresh one, along with the func that closes what it added.
func ShareReaders(ctx context.Context) (context.Context, func() error) {
	if readersFrom(ctx) != nil {
		return ctx, func() error { return nil }
	}
	ctx, readers := WithReaders(ctx)
	return ctx, readers.Close
}

func readersFrom(ctx context.Context) *Readers {
	readers, _ := ctx.Value(readersKey{}).(*Readers)
	return readers
}

func (rs *Readers) pool(r *Repo) *batchPool {
	if rs == nil {
		return nil
	}
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.closed {
		return nil
	}
	pool, ok := rs.pools[r]
	if !ok {
		pool = &batchPool{}
		rs.pools[r] = pool
	}
	return pool
}

// Close reaps every idle reader and stops pooling new ones.
func (rs *Readers) Close() error {
	rs.mu.Lock()
	pools := rs.pools
	rs.pools, rs.closed = nil, true
	rs.mu.Unlock()
	var result error
	for repo, pool := range pools {
		repo.mu.Lock()
		result = errors.Join(result, pool.drain())
		repo.mu.Unlock()
	}
	return result
}

// batchPool holds one handle's idle readers. Callers hold the handle's mu.
type batchPool struct {
	idle []idleBatch
}

type idleBatch struct {
	batch *gitcmd.ObjectBatch
	since time.Time
}

func (p *batchPool) take() (*gitcmd.ObjectBatch, error) {
	var result error
	for len(p.idle) > 0 {
		last := p.idle[len(p.idle)-1]
		p.idle = p.idle[:len(p.idle)-1]
		if !last.batch.Idle() {
			continue
		}
		if time.Since(last.since) > batchIdleTTL {
			result = errors.Join(result, last.batch.Close())
			continue
		}
		return last.batch, result
	}
	return nil, result
}

func (p *batchPool) put(batch *gitcmd.ObjectBatch) error {
	if len(p.idle) >= batchPoolCap {
		return batch.Close()
	}
	p.idle = append(p.idle, idleBatch{batch: batch, since: time.Now()})
	return nil
}

func (p *batchPool) drain() error {
	var result error
	for _, idle := range p.idle {
		result = errors.Join(result, idle.batch.Close())
	}
	p.idle = nil
	return result
}

type objectReadScope struct {
	ctx     context.Context
	pool    *batchPool
	batches []*gitcmd.ObjectBatch
	unbind  []func() bool
	err     error
	refresh bool
	closed  bool
}

func (s *objectReadScope) batch(ctx context.Context, git gitcmd.Git) (*gitcmd.ObjectBatch, error) {
	if s.closed {
		return nil, os.ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, batch := range s.batches {
		if batch.Idle() {
			return batch, nil
		}
	}
	if s.pool == nil {
		batch, err := git.ObjectBatch(ctx)
		if err != nil {
			return nil, err
		}
		s.batches = append(s.batches, batch)
		s.unbind = append(s.unbind, func() bool { return true })
		return batch, nil
	}
	batch, err := s.pool.take()
	s.err = errors.Join(s.err, err)
	if batch == nil {
		if batch, err = git.ObjectBatch(context.WithoutCancel(ctx)); err != nil {
			return nil, err
		}
	}
	s.batches = append(s.batches, batch)
	s.unbind = append(s.unbind, batch.Bind(ctx))
	return batch, nil
}

// release returns this operation's idle readers to the pool and hands each
// busy one, still bound to the operation, to the stream that holds it.
func (s *objectReadScope) release() error {
	result := s.err
	for i, batch := range s.batches {
		if s.pool != nil && batch.Idle() && s.ctx.Err() == nil && s.unbind[i]() {
			result = errors.Join(result, s.pool.put(batch))
			continue
		}
		result = errors.Join(result, batch.Release())
	}
	s.batches, s.unbind = nil, nil
	return result
}

// closeBatches retires every reader this operation and its pool hold, after a
// reindex showed the pack set moved under them.
func (s *objectReadScope) closeBatches() error {
	var result error
	if s.pool != nil {
		result = s.pool.drain()
	}
	for _, batch := range s.batches {
		result = errors.Join(result, batch.Release())
	}
	s.batches, s.unbind = nil, nil
	return result
}

type contextFile struct {
	billy.File
	ctx context.Context
}

func (f contextFile) Read(data []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Read(data)
}

func (f contextFile) ReadAt(data []byte, offset int64) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.ReadAt(data, offset)
}
