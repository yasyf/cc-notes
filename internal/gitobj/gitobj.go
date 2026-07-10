// Package gitobj is the go-git half of the repository access split: it owns
// object writes (content-addressed, so no locking is needed) and all reads —
// ref tips, prefix listings, ancestry, and entity commit chains. Ref writes,
// fetch/push, and config live in internal/gitcmd, which execs the system git
// binary for real ref locks, reflogs, and credential handling. Neither
// package imports the other.
package gitobj

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
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
	repo    *gogit.Repository
	storage *filesystem.Storage
}

// Open opens the git repository containing dir, following worktree and
// subdirectory indirection so refs and objects are the main repository's.
func Open(dir string) (*Repo, error) {
	repo, err := gogit.PlainOpenWithOptions(dir, &gogit.PlainOpenOptions{
		DetectDotGit:          true,
		EnableDotGitCommonDir: true,
	})
	if err != nil {
		return nil, fmt.Errorf("open git repository at %s: %w", dir, err)
	}
	return &Repo{repo: repo, storage: repo.Storer.(*filesystem.Storage)}, nil
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
