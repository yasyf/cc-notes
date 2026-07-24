package helperclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/worker"

	"github.com/yasyf/cc-notes/internal/helpercontract"
)

const (
	toolRunnerCloseTimeout   = 30 * time.Second
	toolRunnerMaxTotalRun    = 15 * time.Minute
	toolRunnerMaxOutputBytes = 1 << 20
)

// ToolRunner is one isolated durable daemonkit task runner.
type ToolRunner struct {
	claim     *worker.RuntimeClaim
	directory string
}

// NewToolRunner creates a disposable runner for signed-helper operations.
func NewToolRunner(ctx context.Context) (*ToolRunner, error) {
	directory, generation, err := newToolIdentity("cc-notes-helper-tools-")
	if err != nil {
		return nil, err
	}
	reaper := &proc.Reaper{
		Store:      &proc.FileStore{Path: filepath.Join(directory, "processes.db")},
		Generation: generation,
	}
	pool, err := worker.NewPool(worker.Config{
		Capacity: 1, QueueCapacity: 0, MaxTotalRun: toolRunnerMaxTotalRun,
		MaxStdinBytes: 0, MaxStdoutBytes: toolRunnerMaxOutputBytes, MaxStderrBytes: toolRunnerMaxOutputBytes,
	}, reaper)
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(directory))
	}
	claim, err := pool.ClaimRuntime()
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(directory))
	}
	if err := claim.Recover(ctx); err != nil {
		return nil, errors.Join(err, claim.Release(context.WithoutCancel(ctx)), os.RemoveAll(directory))
	}
	if err := claim.Activate(); err != nil {
		return nil, errors.Join(err, claim.Release(context.WithoutCancel(ctx)), os.RemoveAll(directory))
	}
	return &ToolRunner{claim: claim, directory: directory}, nil
}

// Run executes one daemonkit task.
func (r *ToolRunner) Run(ctx context.Context, request worker.CommandRequest) (worker.CommandResult, error) {
	return r.claim.Product().Run(ctx, request)
}

// Close settles all work and removes the private recovery directory.
func (r *ToolRunner) Close(ctx context.Context) error {
	if r == nil || r.claim == nil {
		return nil
	}
	waitCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), toolRunnerCloseTimeout)
	defer cancel()
	err := r.claim.Close(waitCtx)
	r.claim = nil
	return errors.Join(err, os.RemoveAll(r.directory))
}

// RunProvision invokes the exact deployed helper for one repository provisioning operation.
func RunProvision(ctx context.Context, executable, repoRoot string) (resultErr error) {
	result, err := runInstalledRequest(
		ctx, executable, helpercontract.ProvisionArguments(repoRoot), toolRunnerMaxTotalRun,
	)
	outputErr := errors.Join(writeAll(os.Stdout, result.Stdout), writeAll(os.Stderr, result.Stderr))
	if err := errors.Join(err, outputErr); err != nil {
		return fmt.Errorf("cc-notes helper: provision repository through signed app: %w", err)
	}
	return nil
}

func runInstalledRequest(
	ctx context.Context,
	executable string,
	arguments []string,
	timeout time.Duration,
) (result worker.CommandResult, resultErr error) {
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return worker.CommandResult{}, errors.New("cc-notes helper: fixed signed app executable path is not exact and absolute")
	}
	info, err := os.Lstat(executable)
	if err != nil {
		return worker.CommandResult{}, fmt.Errorf("cc-notes helper: fixed signed app is not installed; run `cc-notes package install`: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode()&0o111 == 0 {
		return worker.CommandResult{}, errors.New("cc-notes helper: fixed signed app executable is invalid")
	}
	runner, err := NewToolRunner(ctx)
	if err != nil {
		return worker.CommandResult{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, runner.Close(ctx)) }()
	result, err = runner.Run(ctx, worker.CommandRequest{
		Path: executable, Dir: "/", Args: arguments, TotalTimeout: timeout,
	})
	if err != nil {
		return result, fmt.Errorf("signed app request: %w: %s", err, strings.TrimSpace(string(result.Stderr)))
	}
	return result, nil
}

func newToolIdentity(prefix string) (string, proc.OwnerGeneration, error) {
	directory, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", proc.OwnerGeneration{}, fmt.Errorf("cc-notes helper: create tool recovery directory: %w", err)
	}
	generation, err := proc.ProcessGeneration()
	if err != nil {
		return "", proc.OwnerGeneration{}, errors.Join(err, os.RemoveAll(directory))
	}
	return directory, generation, nil
}

func writeAll(file *os.File, content []byte) error {
	for len(content) != 0 {
		written, err := file.Write(content)
		content = content[written:]
		if err != nil {
			return err
		}
	}
	return nil
}

var _ interface {
	Run(context.Context, worker.CommandRequest) (worker.CommandResult, error)
} = (*ToolRunner)(nil)
