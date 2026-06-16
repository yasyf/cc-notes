// Package gitobj is the go-git half of the repository access split: it owns
// object writes (content-addressed, so no locking is needed) and all reads —
// ref tips, prefix listings, ancestry, and entity commit chains. Ref writes,
// fetch/push, and config live in internal/gitcmd, which execs the system git
// binary for real ref locks, reflogs, and credential handling. Neither
// package imports the other.
package gitobj

import (
	"errors"
	"fmt"
	"sync"
	"time"

	gogit "github.com/go-git/go-git/v5"
)

// opsFile is the single tree entry every entity commit carries.
const opsFile = "ops.json"

var (
	// ErrRefNotFound reports a ref that does not exist in the repository.
	ErrRefNotFound = errors.New("ref not found")
	// ErrIncompleteChain reports a chain commit whose object (or tree or
	// ops blob) is absent from the object database — typically a shallow
	// or partial clone.
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
// locking of its own, so every method serializes on mu.
type Repo struct {
	mu   sync.Mutex
	repo *gogit.Repository
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
	return &Repo{repo: repo}, nil
}
