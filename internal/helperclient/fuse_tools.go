package helperclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/fusekit/fuset"
)

const fuseToolLockTimeout = 30 * time.Second

// FUSEToolPool owns one durable FuseKit packaging and verification worker generation.
type FUSEToolPool struct {
	pool *fuset.ToolPool
	lock *proc.FileLockHandle
}

// NewFUSEToolPool constructs the exact FuseKit-owned tool runtime.
func NewFUSEToolPool(ctx context.Context) (*FUSEToolPool, error) {
	directory, err := fuseToolStateDirectory()
	if err != nil {
		return nil, err
	}
	lock, err := (proc.FileLockSpec{
		Path: filepath.Join(directory, "owner.lock"), Mode: proc.FileLockExclusive, Deadline: fuseToolLockTimeout,
	}).Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("cc-notes helper: acquire FUSE tool ownership: %w", err)
	}
	generation, err := proc.ProcessGeneration()
	if err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	pool, err := fuset.NewToolPool(ctx, fuset.ToolPoolConfig{
		ProcessStorePath: filepath.Join(directory, "processes.db"),
		Generation:       generation,
	})
	if err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	return &FUSEToolPool{pool: pool, lock: lock}, nil
}

// Pool returns the FuseKit-owned tool runtime.
func (p *FUSEToolPool) Pool() *fuset.ToolPool { return p.pool }

// Close terminally settles the tool generation and releases durable ownership.
func (p *FUSEToolPool) Close(ctx context.Context) error {
	if p == nil || p.pool == nil {
		return nil
	}
	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	err := p.pool.Close(waitCtx)
	p.pool = nil
	err = errors.Join(err, p.lock.Close())
	p.lock = nil
	return err
}

func fuseToolStateDirectory() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cc-notes helper: resolve home directory: %w", err)
	}
	directory := filepath.Join(home, ".cc-notes", "helper-tools")
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return "", errors.New("cc-notes helper: FUSE tool state directory is not exact and absolute")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("cc-notes helper: create FUSE tool state directory: %w", err)
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return "", fmt.Errorf("cc-notes helper: inspect FUSE tool state directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("cc-notes helper: FUSE tool state directory is not a real directory")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", fmt.Errorf("cc-notes helper: resolve FUSE tool state directory: %w", err)
	}
	if resolved != directory {
		return "", errors.New("cc-notes helper: FUSE tool state directory is not canonical")
	}
	return directory, nil
}
