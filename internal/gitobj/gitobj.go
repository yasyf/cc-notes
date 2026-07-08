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

// opsFile is the single tree entry every entity commit carries.
const opsFile = "ops.json"

var (
	// ErrRefNotFound reports a ref that does not exist in the repository.
	ErrRefNotFound = errors.New("ref not found")
	// ErrIncompleteChain reports a chain commit whose object (or tree or
	// ops blob) is absent from the object database even after a reindex.
	// The message names the commit and reports whether the repository is a
	// shallow clone (the usual cause) or the object is missing from an
	// otherwise complete object database.
	ErrIncompleteChain = errors.New("incomplete chain")
	// ErrCorruptCommit reports a chain commit whose tree has no ops.json
	// entry: it was not written by cc-notes.
	ErrCorruptCommit = errors.New("corrupt commit")
	// ErrCommitNotFound reports a commit whose object is absent from the
	// repository — distinct from a malformed sha, which is a caller error.
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
// It is safe for concurrent use: go-git's filesystem storage builds lazy
// caches (DotGit object/pack lists, ObjectStorage pack indexes) with no
// locking of its own, so every method serializes on mu. The pack index is
// seeded on the first pack-touching read and never rescanned, so a pack
// landed afterward (a fetch round, the mount holder, an external repack/gc)
// is invisible until reindexed: every object read goes through retry or
// lookupCommit, which reindexes and retries once on a miss.
type Repo struct {
	mu      sync.Mutex
	repo    *gogit.Repository
	storage *filesystem.Storage
}

// Open opens the git repository containing dir. It detects the .git
// directory from any subdirectory and follows the .git file plus commondir
// indirection of a linked worktree (git worktree add), so refs and objects
// are the shared ones from the main repository.
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

// staleIndex reports whether err signals a packfile index out of date with the
// packs on disk — go-git seeds the index once and never rescans it. An object
// in an unindexed pack reads as ErrObjectNotFound; an index entry pointing at a
// pack an external repack/gc removed reads as ErrPackfileNotFound. Both heal
// with a reindex.
func staleIndex(err error) bool {
	return errors.Is(err, plumbing.ErrObjectNotFound) || errors.Is(err, dotgit.ErrPackfileNotFound)
}

// retry runs lookup and, on a stale-index miss, reindexes the packfiles once
// and re-runs — mirroring git's own object database, which rescans packs and
// retries once before reporting a missing object. The second answer is
// authoritative. The caller must hold r.mu: go-git's index rebuild is not
// concurrency-safe.
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

// retryEmptyRef runs lookup, retrying while it fails with
// dotgit.ErrEmptyRefFile: go-git's refs walk reads every file under refs/,
// including the <ref>.lock a concurrent git ref write creates empty before
// filling it in and renaming it over the ref. That window lasts microseconds,
// so a few spaced attempts outlast any healthy writer; once they exhaust, the
// error surfaces — a persistently empty file under refs/ means a crashed
// writer, not a race.
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
