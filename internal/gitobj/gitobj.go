// Package gitobj owns object writes and all reads through go-git's filesystem
// ODB storage. Repository discovery belongs to the caller, which uses real git
// rev-parse. This bypasses go-git's repository open, whose extension allowlist
// rejects extensions.worktreeConfig repositories. Ref writes, fetch/push, and
// config live outside this package.
package gitobj

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	formatcfg "github.com/go-git/go-git/v5/plumbing/format/config"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"
)

const opsFile = "ops.json"

var (
	// ErrRefNotFound reports a ref that does not exist in the repository.
	ErrRefNotFound = errors.New("ref not found")
	// ErrIncompleteChain reports a chain commit, tree, or ops blob absent from
	// the object database even after a reindex.
	ErrIncompleteChain = errors.New("incomplete chain")
	// ErrCorruptCommit reports a chain commit whose tree has no ops.json entry.
	ErrCorruptCommit = errors.New("corrupt commit")
	// ErrCommitNotFound reports a commit whose object is absent from the repository.
	ErrCommitNotFound = errors.New("commit not found")
)

// Signature identifies the author and committer of an ops commit. When is
// part of the commit hash, offset included: identical inputs (same instant,
// same zone offset) produce identical commit ids.
type Signature struct {
	Name  string
	Email string
	When  time.Time
}

// Repo is a read/object-write handle on a git repository, backed by go-git.
// It is safe for concurrent use.
type Repo struct {
	// go-git's filesystem storage builds lazy caches (DotGit object/pack
	// lists, ObjectStorage pack indexes) with no locking of its own, so every
	// method serializes on mu.
	mu      sync.Mutex
	storage *filesystem.Storage
}

// Open opens filesystem storage at the discovered per-worktree and shared git
// directories.
func Open(gitDir, commonDir string) (*Repo, error) {
	fs := dotgit.NewRepositoryFilesystem(osfs.New(gitDir), osfs.New(commonDir))
	storage := filesystem.NewStorage(fs, cache.NewObjectLRUDefault())
	if err := verifyLayout(storage); err != nil {
		return nil, err
	}
	return &Repo{storage: storage}, nil
}

func verifyLayout(storage *filesystem.Storage) error {
	cfg, err := storage.Config()
	if err != nil {
		return fmt.Errorf("read repository config: %w", err)
	}
	version := strings.ToLower(lastOption(cfg.Raw.Section("core").Options, "repositoryformatversion"))
	switch version {
	case "", "0", "1":
	default:
		return fmt.Errorf("unsupported core.repositoryformatversion %q", version)
	}
	objectFormat := lastOption(cfg.Raw.Section("extensions").Options, "objectformat")
	switch strings.ToLower(objectFormat) {
	case "", "sha1":
	default:
		return fmt.Errorf("unsupported extensions.objectformat %q: cc-notes reads sha1 object databases", objectFormat)
	}
	refStorage := lastOption(cfg.Raw.Section("extensions").Options, "refstorage")
	switch strings.ToLower(refStorage) {
	case "", "files":
	default:
		return fmt.Errorf("unsupported extensions.refstorage %q: cc-notes reads the files ref backend", refStorage)
	}
	return nil
}

func lastOption(options formatcfg.Options, key string) string {
	value := ""
	for _, option := range options {
		if strings.EqualFold(option.Key, key) {
			value = option.Value
		}
	}
	return value
}

func staleIndex(err error) bool {
	return errors.Is(err, plumbing.ErrObjectNotFound) || errors.Is(err, dotgit.ErrPackfileNotFound)
}

func retry[T any](r *Repo, lookup func() (T, error)) (T, error) {
	v, err := lookup()
	if !staleIndex(err) {
		return v, err
	}
	r.storage.Reindex()
	return lookup()
}

const (
	emptyRefAttempts   = 10
	emptyRefRetryDelay = time.Millisecond
)

func retryEmptyRef[T any](ctx context.Context, lookup func() (T, error)) (T, error) {
	for attempt := 1; ; attempt++ {
		v, err := lookup()
		if !errors.Is(err, dotgit.ErrEmptyRefFile) || attempt == emptyRefAttempts {
			return v, err
		}
		select {
		case <-ctx.Done():
			return v, ctx.Err()
		case <-time.After(emptyRefRetryDelay):
		}
	}
}
