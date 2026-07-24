package helperclient

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/yasyf/fusekit/fuset"
)

// FUSEToolPool owns one temporary FuseKit packaging and verification worker generation.
type FUSEToolPool struct {
	pool      *fuset.ToolPool
	directory string
}

// NewFUSEToolPool constructs the exact FuseKit-owned tool runtime.
func NewFUSEToolPool(ctx context.Context) (*FUSEToolPool, error) {
	directory, generation, err := newToolIdentity("cc-notes-fuse-tools-")
	if err != nil {
		return nil, err
	}
	pool, err := fuset.NewToolPool(ctx, fuset.ToolPoolConfig{
		ProcessStorePath: filepath.Join(directory, "processes.db"),
		Generation:       generation,
	})
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(directory))
	}
	return &FUSEToolPool{pool: pool, directory: directory}, nil
}

// Pool returns the FuseKit-owned tool runtime.
func (p *FUSEToolPool) Pool() *fuset.ToolPool { return p.pool }

// Close terminally settles the tool generation and removes its derived state.
func (p *FUSEToolPool) Close(ctx context.Context) error {
	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	err := p.pool.Close(waitCtx)
	p.pool = nil
	return errors.Join(err, os.RemoveAll(p.directory))
}
