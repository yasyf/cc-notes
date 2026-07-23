package holderclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yasyf/daemonkit/codeidentity"
	"github.com/yasyf/daemonkit/fetch"
	"github.com/yasyf/daemonkit/proc"
	"github.com/yasyf/daemonkit/supervise"

	"github.com/yasyf/cc-notes/internal/holdercontract"
	"github.com/yasyf/cc-notes/internal/version"
)

const toolRunnerCloseTimeout = 30 * time.Second

// ToolRunner is one isolated durable daemonkit task runner.
type ToolRunner struct {
	pool      *supervise.Pool
	directory string
}

// NewToolRunner creates a disposable runner for signed-holder and packaging operations.
func NewToolRunner(ctx context.Context) (*ToolRunner, error) {
	directory, err := os.MkdirTemp("", "cc-notes-holder-tools-")
	if err != nil {
		return nil, fmt.Errorf("cc-notes holder: create tool recovery directory: %w", err)
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

// The holder ships in lockstep with the CLI under the same release tag, so
// version.Version names the release carrying the matching signed asset.
func installHolder(ctx context.Context) error {
	dir, err := InstalledDir()
	if err != nil {
		return err
	}
	asset := fmt.Sprintf(
		"https://github.com/yasyf/cc-notes/releases/download/%s/cc-notes-holder-%s-darwin.zip",
		version.Version, version.Version,
	)
	if _, err := fetch.New().Fetch(ctx, fetch.Config{
		AssetURL:     asset,
		ChecksumsURL: asset + ".sha256",
		Dir:          dir,
		AppName:      ExecutableName,
		Identity:     codeidentity.CodeIdentity{TeamID: TeamID, SigningIdentifier: BundleID},
	}); err != nil {
		return fmt.Errorf("cc-notes holder: fetch signed app %s: %w", version.Version, err)
	}
	return nil
}

// ProvisionRepository runs the sole signed-app repository provisioning operation.
func ProvisionRepository(ctx context.Context, repoRoot string) (resultErr error) {
	executable, err := ExecutablePath()
	if err != nil {
		return err
	}
	info, err := os.Stat(executable)
	if err != nil {
		if err := installHolder(ctx); err != nil {
			return err
		}
		info, err = os.Stat(executable)
		if err != nil {
			return fmt.Errorf("cc-notes holder: fixed signed app missing after install: %w", err)
		}
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return errors.New("cc-notes holder: fixed signed app executable is invalid")
	}
	runner, err := NewToolRunner(ctx)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, runner.Close(ctx)) }()
	if err := runner.Run(ctx, supervise.Task{
		RecoveryClass: proc.RecoveryTask, Path: executable,
		Args: holdercontract.ProvisionArguments(repoRoot), Stdout: os.Stdout, Stderr: os.Stderr,
	}); err != nil {
		return fmt.Errorf("cc-notes holder: provision repository through signed app: %w", err)
	}
	return nil
}

var _ supervise.TaskRunner = (*ToolRunner)(nil)
