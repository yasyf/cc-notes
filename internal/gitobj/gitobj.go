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
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	formatcfg "github.com/go-git/go-git/v5/plumbing/format/config"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"

	"github.com/yasyf/cc-notes/model"
)

const (
	opsFile = "ops.json"
	// shallowFile is the graft boundary's path in the shared git directory,
	// the one go-git's ShallowStorage reads. go-git keeps its own spelling
	// unexported.
	shallowFile = "shallow"
	// openPacks bounds the packfile descriptors go-git keeps open across
	// reads. KeepDescriptors is the alternative and is unusable here: it never
	// closes, and no cc-notes type owns a lifecycle that could call Close.
	openPacks = 8
)

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
	// ErrPathEscapesRoot reports a ./ or ../ anchored lookup path whose ".."
	// components climb above the repository root. git reports this shape as a
	// fatal rather than a miss.
	ErrPathEscapesRoot = errors.New("relative path escapes the repository root")
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
	// mu guards storage and every memo below it, shallow and graftAt included.
	// go-git's filesystem storage builds lazy caches with no locking of its
	// own — DotGit object/pack lists, ObjectStorage pack indexes, and now its
	// packfile descriptor ring. shallow is a memo like the rest: the graft
	// boundary moves under a live handle, so refreshGraft re-reads it whenever
	// graftAt shows the shallow file changed, and every entry point that
	// consults shallow calls refreshGraft first.
	mu         sync.Mutex
	storage    *filesystem.Storage
	fs         *dotgit.RepositoryFilesystem
	reaches    map[plumbing.Hash]*reach
	reachOrder []plumbing.Hash
	treeRev    model.SHA
	treeVal    *treeIndex
	subtrees   map[plumbing.Hash]*treeIndex
	shallow    map[plumbing.Hash]bool
	graftAt    graftStamp
}

// graftStamp identifies one version of the shallow file. Its zero value is the
// absent file, which is also the un-grafted repository, so a handle opened
// against one reads the file only once something writes it.
type graftStamp struct {
	exists  bool
	size    int64
	modNano int64
}

// Open opens filesystem storage at the discovered per-worktree and shared git
// directories.
func Open(gitDir, commonDir string) (*Repo, error) {
	fs := dotgit.NewRepositoryFilesystem(unlockedFS{osfs.New(gitDir)}, unlockedFS{osfs.New(commonDir)})
	storage := filesystem.NewStorageWithOptions(fs, cache.NewObjectLRUDefault(), filesystem.Options{MaxOpenDescriptors: openPacks})
	if err := verifyLayout(storage); err != nil {
		return nil, err
	}
	r := &Repo{
		storage:  storage,
		fs:       fs,
		reaches:  make(map[plumbing.Hash]*reach, reachCap),
		subtrees: make(map[plumbing.Hash]*treeIndex),
	}
	if err := r.refreshGraft(); err != nil {
		return nil, err
	}
	return r, nil
}

// refreshGraft re-reads the shallow file when a stat shows it changed — git
// fetch --unshallow removes it, a deepen rewrites it — and discards the
// frontiers reachable expanded under the old boundary, which would otherwise
// keep serving verdicts that boundary alone justified. Callers hold mu.
func (r *Repo) refreshGraft() error {
	stamp, err := r.statGraft()
	if err != nil {
		return err
	}
	if stamp == r.graftAt {
		return nil
	}
	grafted, err := r.storage.Shallow()
	if err != nil {
		return fmt.Errorf("read shallow boundary: %w", err)
	}
	shallow := make(map[plumbing.Hash]bool, len(grafted))
	for _, hash := range grafted {
		shallow[hash] = true
	}
	r.shallow, r.graftAt = shallow, stamp
	clear(r.reaches)
	r.reachOrder = r.reachOrder[:0]
	return nil
}

func (r *Repo) statGraft() (graftStamp, error) {
	info, err := r.fs.Stat(shallowFile)
	if errors.Is(err, os.ErrNotExist) {
		return graftStamp{}, nil
	}
	if err != nil {
		return graftStamp{}, fmt.Errorf("stat shallow boundary: %w", err)
	}
	return graftStamp{exists: true, size: info.Size(), modNano: info.ModTime().UnixNano()}, nil
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
