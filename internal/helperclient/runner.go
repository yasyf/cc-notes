package helperclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/supervise"

	"github.com/yasyf/cc-notes/internal/helpercontract"
)

const toolRunnerCloseTimeout = 30 * time.Second

// ToolRunner is one isolated durable daemonkit task runner.
type ToolRunner struct {
	pool      *supervise.Pool
	directory string
}

// NewToolRunner creates a disposable runner for signed-helper and packaging operations.
func NewToolRunner(ctx context.Context) (*ToolRunner, error) {
	directory, err := os.MkdirTemp("", "cc-notes-helper-tools-")
	if err != nil {
		return nil, fmt.Errorf("cc-notes helper: create tool recovery directory: %w", err)
	}
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return nil, errors.Join(err, os.RemoveAll(directory))
	}
	reaper := &proc.Reaper{
		Store:      &proc.FileStore{Path: filepath.Join(directory, "processes.db")},
		Generation: hex.EncodeToString(generation[:]),
	}
	pool, err := supervise.NewPool(1, reaper)
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(directory))
	}
	if err := pool.Recover(ctx); err != nil {
		pool.Close()
		pool.Cancel()
		waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), toolRunnerCloseTimeout)
		defer cancel()
		return nil, errors.Join(err, pool.Wait(waitCtx), os.RemoveAll(directory))
	}
	return &ToolRunner{pool: pool, directory: directory}, nil
}

// Run executes one daemonkit task.
func (r *ToolRunner) Run(ctx context.Context, task supervise.Task) error {
	return r.pool.Run(ctx, task)
}

// Close settles all work and removes the private recovery directory.
func (r *ToolRunner) Close(ctx context.Context) error {
	if r == nil || r.pool == nil {
		return nil
	}
	r.pool.Close()
	r.pool.Cancel()
	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), toolRunnerCloseTimeout)
	defer cancel()
	err := r.pool.Wait(waitCtx)
	r.pool = nil
	return errors.Join(err, os.RemoveAll(r.directory))
}

// RunProvision invokes the exact deployed helper for one repository provisioning operation.
func RunProvision(ctx context.Context, executable, repoRoot string) (resultErr error) {
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return errors.New("cc-notes helper: fixed signed app executable path is not exact and absolute")
	}
	info, err := os.Lstat(executable)
	if err != nil {
		return fmt.Errorf("cc-notes helper: fixed signed app is not installed; run `cc-notes service install`: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&0o111 == 0 {
		return errors.New("cc-notes helper: fixed signed app executable is invalid")
	}
	runner, err := NewToolRunner(ctx)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, runner.Close(ctx)) }()
	if err := runner.Run(ctx, supervise.Task{
		RecoveryClass: proc.RecoveryTask, Path: executable,
		Args: helpercontract.ProvisionArguments(repoRoot), Stdout: os.Stdout, Stderr: os.Stderr,
	}); err != nil {
		return fmt.Errorf("cc-notes helper: provision repository through signed app: %w", err)
	}
	return nil
}

var _ supervise.TaskRunner = (*ToolRunner)(nil)
