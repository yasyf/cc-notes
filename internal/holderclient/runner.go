package holderclient

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yasyf/daemonkit/bundle"
	"github.com/yasyf/daemonkit/codeidentity"
	"github.com/yasyf/daemonkit/daemon"
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

type holderFetcher interface {
	Fetch(context.Context, fetch.Config) (fetch.Installation, error)
}

func holderRelease() (fetch.Release, error) {
	if version.HolderVersion == "" || strings.TrimSpace(version.HolderVersion) != version.HolderVersion {
		return fetch.Release{}, errors.New("cc-notes holder: release bundle version is not exact")
	}
	digest, err := fetch.ParseSHA256(version.HolderSHA256)
	if err != nil {
		return fetch.Release{}, fmt.Errorf("cc-notes holder: parse release digest: %w", err)
	}
	return fetch.Release{
		Version: version.HolderVersion,
		URL: fmt.Sprintf(
			"https://github.com/yasyf/cc-notes/releases/download/%s/cc-notes-holder-%s-darwin.zip",
			version.Version, version.Version,
		),
		SHA256: digest,
	}, nil
}

func reconcileHolder(ctx context.Context, fetcher holderFetcher) (string, error) {
	dir, err := InstalledDir()
	if err != nil {
		return "", err
	}
	if err := ensureInstallDirectory(dir); err != nil {
		return "", err
	}
	release, err := holderRelease()
	if err != nil {
		return "", err
	}
	installation, err := fetcher.Fetch(ctx, fetch.Config{
		Release: release,
		Dir:     dir,
		AppName: ExecutableName,
		Identity: codeidentity.CodeIdentity{
			TeamID: TeamID, SigningIdentifier: BundleID,
		},
	})
	if err != nil {
		return "", fmt.Errorf("cc-notes holder: fetch signed app %s: %w", version.Version, err)
	}
	return bundle.ExePath(installation.Path, ExecutableName), nil
}

func ensureInstallDirectory(path string) error {
	for _, directory := range []string{filepath.Dir(path), path} {
		created := false
		if err := os.Mkdir(directory, 0o700); err != nil {
			if !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("cc-notes holder: create install directory %q: %w", directory, err)
			}
		} else {
			created = true
		}
		info, err := os.Lstat(directory)
		if err != nil {
			return fmt.Errorf("cc-notes holder: inspect install directory %q: %w", directory, err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("cc-notes holder: install path %q is not a real directory", directory)
		}
		if info.Mode().Perm() != 0o700 {
			//nolint:gosec // Private directories require the owner execute bit.
			if err := os.Chmod(directory, 0o700); err != nil {
				return fmt.Errorf("cc-notes holder: protect install directory %q: %w", directory, err)
			}
			if err := daemon.SyncDir(directory); err != nil {
				return fmt.Errorf("cc-notes holder: persist install directory permissions: %w", err)
			}
		}
		if created {
			if err := daemon.SyncDir(filepath.Dir(directory)); err != nil {
				return fmt.Errorf("cc-notes holder: persist install directory: %w", err)
			}
		}
	}
	return nil
}

// ProvisionRepository runs the sole signed-app repository provisioning operation.
func ProvisionRepository(ctx context.Context, repoRoot string) (resultErr error) {
	executable, err := reconcileHolder(ctx, fetch.New())
	if err != nil {
		return err
	}
	info, err := os.Stat(executable)
	if err != nil {
		return fmt.Errorf("cc-notes holder: fixed signed app missing after reconciliation: %w", err)
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
