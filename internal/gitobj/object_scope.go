package gitobj

import (
	"context"
	"errors"
	"os"
	"time"

	"github.com/go-git/go-billy/v5"

	"github.com/yasyf/cc-notes/internal/gitcmd"
)

const (
	// batchPoolCap bounds the idle readers a handle keeps between
	// operations; reads hold mu, so one serves them and a second covers a
	// stream left open across a lookup.
	batchPoolCap = 2
	// batchIdleTTL retires a pooled reader before a long-lived handle pins
	// pack files a repack has since deleted.
	batchIdleTTL = 10 * time.Second
)

// batchPool keeps native readers alive across operations, so a command that
// performs thousands of reads starts one git child, not one per read.
// Callers hold the Repo's mu.
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
	unbind  []func()
	err     error
	refresh bool
	closed  bool
}

func (s *objectReadScope) batch(git gitcmd.Git) (*gitcmd.ObjectBatch, error) {
	if s.closed {
		return nil, os.ErrClosed
	}
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	for _, batch := range s.batches {
		if batch.Idle() {
			return batch, nil
		}
	}
	batch, err := s.pool.take()
	s.err = errors.Join(s.err, err)
	if batch == nil {
		batch, err = git.ObjectBatch(context.WithoutCancel(s.ctx))
		if err != nil {
			return nil, err
		}
	}
	s.batches = append(s.batches, batch)
	s.unbind = append(s.unbind, batch.Bind(s.ctx))
	return batch, nil
}

// release returns this operation's idle readers to the pool and hands each
// busy one, still bound to the operation, to the stream that holds it.
func (s *objectReadScope) release() error {
	result := s.err
	for i, batch := range s.batches {
		if batch.Idle() && s.ctx.Err() == nil {
			s.unbind[i]()
			result = errors.Join(result, s.pool.put(batch))
			continue
		}
		result = errors.Join(result, batch.Release())
	}
	s.batches, s.unbind = nil, nil
	return result
}

// closeBatches retires every reader this operation and the pool hold, after a
// reindex showed the pack set moved under them.
func (s *objectReadScope) closeBatches() error {
	result := s.pool.drain()
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
