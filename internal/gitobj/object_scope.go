package gitobj

import (
	"context"
	"errors"
	"os"

	"github.com/go-git/go-billy/v5"

	"github.com/yasyf/cc-notes/internal/gitcmd"
)

type objectReadScope struct {
	ctx     context.Context
	batches []*gitcmd.ObjectBatch
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
	batch, err := git.ObjectBatch(s.ctx)
	if err != nil {
		return nil, err
	}
	s.batches = append(s.batches, batch)
	return batch, nil
}

func (s *objectReadScope) closeBatches() error {
	var result error
	for _, batch := range s.batches {
		result = errors.Join(result, batch.Release())
	}
	s.batches = nil
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
