// Package index maps a working directory to the registered repository that
// owns it. Resolution runs on every daemon query and has to fit inside a hook's
// millisecond budget, so it reads only the registry `cc-notes init` writes plus
// directory metadata: no git subprocess, no repository read.
package index

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yasyf/cc-notes/internal/ccnhome"
)

// unknownTTL bounds how long a working directory that belongs to no registered
// repository keeps that answer. Every unknown answer costs one read of the
// registry directory, so the cache is what keeps a hook firing from an
// unregistered checkout off the disk on every query.
const unknownTTL = 30 * time.Second

// ErrUnknownRepo reports that a working directory belongs to no registered
// repository: the upward walk either reached a repository boundary that was
// never registered, or reached the filesystem root without reaching one.
// Registration is explicit, so an unregistered boundary is answered, never
// adopted.
var ErrUnknownRepo = errors.New("unknown repository")

// Resolver answers which registered repository owns a working directory, from
// an in-memory snapshot of the registry under ~/.cc-notes/repos.
type Resolver struct {
	// registry holds an immutable snapshot, replaced wholesale. Readers load it
	// without a lock; two goroutines reloading concurrently build equal
	// snapshots, so the last store wins and neither reader sees a partial one.
	registry atomic.Pointer[snapshot]

	// mu guards unknown and nothing else. No I/O happens under it.
	mu      sync.Mutex
	unknown map[string]time.Time

	now func() time.Time
}

// snapshot is one read of the registry: every registered repository indexed by
// its resolved worktree roots and by its resolved git common directory. It is
// never mutated after construction.
type snapshot struct {
	worktrees  map[string]owner
	commonDirs map[string]owner
}

// owner is a path's registered repository plus the stamp of the descriptor that
// claimed it, which is what makes a hit re-checkable without re-reading the
// registry.
type owner struct {
	repo  ccnhome.Repo
	stamp ccnhome.Stamp
}

// NewResolver returns a resolver holding an empty registry snapshot. The first
// working directory that does not resolve against it loads the registry.
func NewResolver() *Resolver { return newResolver(time.Now) }

func newResolver(now func() time.Time) *Resolver {
	r := &Resolver{unknown: map[string]time.Time{}, now: now}
	r.registry.Store(&snapshot{})
	return r
}

// Resolve returns the registered repository that owns dir, which must be
// absolute. It walks dir and each parent, consulting at every level the
// registered worktree roots, then the registered git common directories, then
// whether the level is a repository boundary at all — a .git entry, or the
// HEAD, objects and refs of a bare repository. An unregistered boundary ends
// the walk with ErrUnknownRepo: walking past it would attribute a nested
// repository to its parent. A walk that resolves against a stale snapshot —
// finding nothing, or landing on a claim the descriptor behind it no longer
// makes — reloads the registry once and retries before answering
// ErrUnknownRepo.
func (r *Resolver) Resolve(dir string) (ccnhome.Repo, error) {
	if !filepath.IsAbs(dir) {
		return ccnhome.Repo{}, fmt.Errorf("resolve %s: working directory is not absolute", dir)
	}
	start, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return ccnhome.Repo{}, fmt.Errorf("resolve working directory %s: %w", dir, err)
	}
	if r.cachedUnknown(start) {
		return ccnhome.Repo{}, ErrUnknownRepo
	}
	if own, ok := walk(r.registry.Load(), start); ok && own.stamp.Current() {
		return own.repo, nil
	}
	reloaded, err := load()
	if err != nil {
		return ccnhome.Repo{}, err
	}
	r.registry.Store(reloaded)
	if own, ok := walk(reloaded, start); ok {
		return own.repo, nil
	}
	r.cacheUnknown(start)
	return ccnhome.Repo{}, ErrUnknownRepo
}

func walk(reg *snapshot, dir string) (owner, bool) {
	for {
		if own, ok := reg.worktrees[dir]; ok {
			return own, true
		}
		if own, ok := reg.commonDirs[dir]; ok {
			return own, true
		}
		if isBoundary(dir) {
			return owner{}, false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return owner{}, false
		}
		dir = parent
	}
}

func isBoundary(dir string) bool {
	if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
		return true
	}
	return ccnhome.IsGitDir(dir)
}

func load() (*snapshot, error) {
	entries, err := ccnhome.List()
	if err != nil {
		return nil, err
	}
	reg := &snapshot{
		worktrees:  make(map[string]owner, len(entries)),
		commonDirs: make(map[string]owner, len(entries)),
	}
	for _, entry := range entries {
		own := owner{repo: entry.Repo, stamp: entry.Stamp}
		reg.commonDirs[entry.Info.CommonDir] = own
		for _, worktree := range entry.Info.Worktrees {
			reg.worktrees[worktree] = own
		}
	}
	return reg, nil
}

func (r *Resolver) cachedUnknown(dir string) bool {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	expiry, ok := r.unknown[dir]
	return ok && now.Before(expiry)
}

func (r *Resolver) cacheUnknown(dir string) {
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for cached, expiry := range r.unknown {
		if !now.Before(expiry) {
			delete(r.unknown, cached)
		}
	}
	r.unknown[dir] = now.Add(unknownTTL)
}
