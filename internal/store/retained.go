package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/yasyf/cc-notes/internal/gitobj"
)

var errDiscoveryMoved = errors.New("checkout no longer discovers the opened repository")

// Retained keeps one Store open for a long-running process and reopens it when
// the checkout at its directory, or that checkout's repository state, moves.
type Retained struct {
	dir  string
	open func(context.Context, string) (*Store, error)

	mu    sync.Mutex
	store *Store
	fence fence
}

type fence struct {
	checkout checkoutFence
	repo     repoFence
}

type checkoutFence struct {
	path   string
	levels []checkoutLevel
}

type checkoutLevel struct {
	dir fileID
	git gitEntry
}

type gitEntry struct {
	present bool
	kind    fs.FileMode
	id      fileID
	link    string
}

type repoFence struct {
	gitDir, commonDir                fileID
	commonLink                       string
	head, branch, packedRefs, config configStamp
	backend                          fileID
	backendEntries                   [len(backendEntries)]fileID
	backendConfig                    configStamp
}

type retainedKey struct{}

// Retain opens and keeps the store for the repository containing an absolute
// dir. An open whose .git or commondir stops leading to it fails.
func Retain(ctx context.Context, dir string) (*Retained, error) {
	return retain(ctx, dir, OpenContext)
}

func retain(ctx context.Context, dir string, open func(context.Context, string) (*Store, error)) (*Retained, error) {
	r := &Retained{dir: dir, open: open}
	if _, err := r.reopen(ctx); err != nil {
		return nil, err
	}
	return r, nil
}

// Store returns a view of the retained store for one operation. The view
// resolves Root and LocalPolicy afresh, as a newly opened store would; the
// object, pack, and fold caches carry over from earlier operations.
func (r *Retained) Store(ctx context.Context) (*Store, error) {
	r.mu.Lock()
	s, opened := r.store, r.fence
	r.mu.Unlock()
	if live, err := fenceOf(s, r.dir); err == nil && live.equal(opened) {
		return s.operation(), nil
	}
	return r.reopen(ctx)
}

func (r *Retained) reopen(ctx context.Context) (*Store, error) {
	s, err := r.open(ctx, r.dir)
	if err != nil {
		return nil, err
	}
	f, err := fenceOf(s, r.dir)
	if err != nil {
		return nil, err
	}
	f.repo.config, f.repo.backendConfig = s.storage.stamps()
	r.mu.Lock()
	r.store, r.fence = s, f
	r.mu.Unlock()
	return s.operation(), nil
}

// WithRetained returns ctx carrying r: commands run under it take their store
// from r instead of opening the repository themselves.
func WithRetained(ctx context.Context, r *Retained) context.Context {
	return context.WithValue(ctx, retainedKey{}, r)
}

// RetainedFrom returns the Retained ctx carries, if any.
func RetainedFrom(ctx context.Context) (*Retained, bool) {
	r, ok := ctx.Value(retainedKey{}).(*Retained)
	return r, ok
}

func (s *Store) operation() *Store {
	view := *s
	view.root, view.policy = &rootMemo{}, &policyMemo{}
	return &view
}

func fenceOf(s *Store, dir string) (fence, error) {
	repo, err := s.repoFence()
	if err != nil {
		return fence{}, err
	}
	checkout, err := checkoutFenceOf(dir, repo.gitDir)
	if err != nil {
		return fence{}, err
	}
	return fence{checkout: checkout, repo: repo}, nil
}

func (f fence) equal(g fence) bool {
	return f.repo == g.repo && f.checkout.path == g.checkout.path && slices.Equal(f.checkout.levels, g.checkout.levels)
}

func checkoutFenceOf(dir string, gitDir fileID) (checkoutFence, error) {
	path, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return checkoutFence{}, err
	}
	f := checkoutFence{path: path}
	for level := path; ; level = filepath.Dir(level) {
		id, err := fileIDOf(level)
		if err != nil {
			return checkoutFence{}, err
		}
		git, err := gitEntryAt(filepath.Join(level, ".git"))
		if err != nil {
			return checkoutFence{}, err
		}
		f.levels = append(f.levels, checkoutLevel{dir: id, git: git})
		switch {
		case git.present:
			if err := discovers(level, git, gitDir); err != nil {
				return checkoutFence{}, err
			}
			return f, nil
		case id == gitDir:
			return f, nil
		case filepath.Dir(level) == level:
			return checkoutFence{}, fmt.Errorf("%w: no git directory at or above %s", errDiscoveryMoved, path)
		}
	}
}

func discovers(level string, git gitEntry, gitDir fileID) error {
	path, err := gitDirAt(level, git)
	if err != nil {
		return err
	}
	id, err := fileIDOf(path)
	if err != nil {
		return err
	}
	if id != gitDir {
		return fmt.Errorf("%w: %s/.git leads to %s", errDiscoveryMoved, level, path)
	}
	return nil
}

func gitDirAt(level string, git gitEntry) (string, error) {
	path := filepath.Join(level, ".git")
	gitfile := git.link
	switch {
	case git.kind.IsDir():
		return path, nil
	case git.kind&fs.ModeSymlink != 0:
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			return path, nil
		}
		if gitfile, err = linkAt(path); err != nil {
			return "", err
		}
	}
	target, ok := strings.CutPrefix(gitfile, "gitdir: ")
	if !ok {
		return "", fmt.Errorf("%w: %s is not a gitfile", errDiscoveryMoved, path)
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(level, target)
	}
	return target, nil
}

func gitEntryAt(path string) (gitEntry, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return gitEntry{}, nil
	}
	if err != nil {
		return gitEntry{}, err
	}
	device, inode := gitobj.FileID(info)
	e := gitEntry{present: true, kind: info.Mode().Type(), id: fileID{device: device, inode: inode}}
	switch {
	case info.Mode().IsRegular():
		e.link, err = linkAt(path)
	case info.Mode()&fs.ModeSymlink != 0:
		e.link, err = os.Readlink(path)
	}
	return e, err
}

func linkAt(path string) (string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	return strings.TrimSpace(string(data)), err
}

func (s *Store) repoFence() (repoFence, error) {
	var f repoFence
	var err error
	if f.gitDir, err = fileIDOf(s.gitDir); err != nil {
		return repoFence{}, err
	}
	if f.commonDir, err = fileIDOf(s.commonDir); err != nil {
		return repoFence{}, err
	}
	if f.commonLink, err = linkAt(filepath.Join(s.gitDir, "commondir")); err != nil {
		return repoFence{}, err
	}
	linked := commonDirOf(s.gitDir, f.commonLink)
	common, err := fileIDOf(linked)
	if err != nil {
		return repoFence{}, err
	}
	if common != f.commonDir {
		return repoFence{}, fmt.Errorf("%w: %s/commondir leads to %s, not %s", errDiscoveryMoved, s.gitDir, linked, s.commonDir)
	}
	if f.head, f.branch, err = headStamps(s.gitDir, s.commonDir); err != nil {
		return repoFence{}, err
	}
	if f.packedRefs, err = stampAt(filepath.Join(s.commonDir, "packed-refs")); err != nil {
		return repoFence{}, err
	}
	if f.config, err = stampAt(filepath.Join(s.commonDir, "config")); err != nil {
		return repoFence{}, err
	}
	if !s.storage.bound {
		return f, nil
	}
	backend := s.storage.binding.CommonDir
	if f.backend, err = fileIDOf(backend); err != nil {
		return repoFence{}, err
	}
	switch _, err := os.Lstat(filepath.Join(backend, "commondir")); {
	case err == nil:
		return repoFence{}, fmt.Errorf("%w: %s now carries a commondir file", ErrBackendRedirects, backend)
	case !errors.Is(err, fs.ErrNotExist):
		return repoFence{}, err
	}
	if f.backendEntries, err = backendEntryIDs(backend); err != nil {
		return repoFence{}, err
	}
	if f.backendConfig, err = stampAt(filepath.Join(backend, "config")); err != nil {
		return repoFence{}, err
	}
	return f, nil
}

func commonDirOf(gitDir, link string) string {
	switch {
	case link == "":
		return gitDir
	case filepath.IsAbs(link):
		return link
	}
	return filepath.Join(gitDir, link)
}

func headStamps(gitDir, commonDir string) (head, branch configStamp, err error) {
	path := filepath.Join(gitDir, "HEAD")
	if head, err = stampAt(path); err != nil {
		return configStamp{}, configStamp{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return configStamp{}, configStamp{}, err
	}
	target, symbolic := strings.CutPrefix(strings.TrimSpace(string(data)), "ref: ")
	if !symbolic {
		return head, configStamp{}, nil
	}
	branch, err = stampAt(filepath.Join(commonDir, filepath.FromSlash(target)))
	return head, branch, err
}

func stampAt(path string) (configStamp, error) {
	info, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return configStamp{}, nil
	}
	if err != nil {
		return configStamp{}, err
	}
	return stampOf(info), nil
}
