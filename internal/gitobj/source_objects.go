package gitobj

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/yasyf/cc-notes/model"
)

// SourceCommit carries immutable commit metadata without decoding entity ops.
type SourceCommit struct {
	Tree      model.SHA
	Parents   []model.SHA
	Author    Signature
	Committer Signature
	Message   string
}

// SourceObjects owns one synchronous repository object scope.
type SourceObjects struct {
	repo   *Repo
	ctx    context.Context
	closed bool
}

// SourceObjects shares one native read scope across an immutable graph operation.
// Objects it writes stay in memory until run succeeds, then land as one pack
// before it returns; on failure they are never written.
func (r *Repo) SourceObjects(ctx context.Context, run func(*SourceObjects) error) (err error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.beginRead(ctx)
	defer r.endRead(&err)
	r.storage.buffer = newObjectBuffer()
	defer func() { r.storage.buffer = nil }()
	objects := &SourceObjects{repo: r, ctx: ctx}
	defer func() { objects.closed = true }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := errors.Join(run(objects), ctx.Err()); err != nil {
		return err
	}
	return r.storage.flushBuffer()
}

func (s *SourceObjects) check() error {
	if s.closed {
		return fs.ErrClosed
	}
	return s.ctx.Err()
}

// KeepOnly limits the final write to what the roots reach, dropping the
// intermediate graph nodes this scope wrote and replaced. Only a scope that
// publishes exactly these roots may call it.
func (s *SourceObjects) KeepOnly(roots ...model.SHA) error {
	if err := s.check(); err != nil {
		return err
	}
	hashes := make([]plumbing.Hash, len(roots))
	for i, root := range roots {
		if !plumbing.IsHash(string(root)) {
			return fmt.Errorf("invalid root sha %q", root)
		}
		hashes[i] = plumbing.NewHash(string(root))
	}
	s.repo.storage.buffer.roots = hashes
	return nil
}

// HoldPacks protects each pack this scope writes with a keep file until
// ReleaseHeldPacks, so `git repack -a -d` cannot delete it before its ref lands.
func (s *SourceObjects) HoldPacks() { s.repo.storage.buffer.hold = true }

// Err reports cancellation or use after the owning callback has returned.
func (s *SourceObjects) Err() error { return s.check() }

// Context returns the operation context for scoped work accounting.
func (s *SourceObjects) Context() context.Context { return s.ctx }

// ObjectType verifies an object's existence and type without reading its body.
func (s *SourceObjects) ObjectType(sha model.SHA) (plumbing.ObjectType, error) {
	if err := s.check(); err != nil {
		return plumbing.InvalidObject, err
	}
	if !plumbing.IsHash(string(sha)) {
		return plumbing.InvalidObject, fmt.Errorf("invalid object sha %q", sha)
	}
	hash := plumbing.NewHash(string(sha))
	value, err := s.repo.storage.bufferedObject(hash)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		value, err = s.repo.storage.managedObject(hash)
	}
	if err == nil {
		return value.Type(), nil
	}
	if !errors.Is(err, plumbing.ErrObjectNotFound) {
		return plumbing.InvalidObject, err
	}
	kind, err := s.repo.storage.looseObjectType(hash)
	if err == nil {
		return kind, nil
	}
	if !errors.Is(err, plumbing.ErrObjectNotFound) {
		return plumbing.InvalidObject, err
	}
	batch, err := s.repo.storage.scope.batch(s.repo.storage.backend)
	if err != nil {
		return plumbing.InvalidObject, err
	}
	info, err := batch.Info(hash)
	if err != nil {
		return plumbing.InvalidObject, err
	}
	return info.Type, nil
}

// ReadBlob returns the exact bytes of an immutable blob.
func (s *SourceObjects) ReadBlob(sha model.SHA) ([]byte, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	if !plumbing.IsHash(string(sha)) {
		return nil, fmt.Errorf("invalid blob sha %q", sha)
	}
	blob, err := object.GetBlob(s.repo.storage, plumbing.NewHash(string(sha)))
	if err != nil {
		return nil, err
	}
	reader, err := blob.Reader()
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(reader)
	return data, errors.Join(readErr, reader.Close())
}

// WriteBlob stores exact bytes without publishing a ref.
func (s *SourceObjects) WriteBlob(data []byte) (model.SHA, error) {
	if err := s.check(); err != nil {
		return "", err
	}
	hash, err := s.repo.writeBlob(data)
	return model.SHA(hash.String()), err
}

// ReadTree returns detached entries from one immutable Git tree.
func (s *SourceObjects) ReadTree(sha model.SHA) ([]object.TreeEntry, error) {
	if err := s.check(); err != nil {
		return nil, err
	}
	if !plumbing.IsHash(string(sha)) {
		return nil, fmt.Errorf("invalid tree sha %q", sha)
	}
	tree, err := object.GetTree(s.repo.storage, plumbing.NewHash(string(sha)))
	if err != nil {
		return nil, err
	}
	return slices.Clone(tree.Entries), nil
}

// WriteTree canonicalizes entries and stores their immutable tree.
func (s *SourceObjects) WriteTree(entries []object.TreeEntry) (model.SHA, error) {
	if err := s.check(); err != nil {
		return "", err
	}
	entries = slices.Clone(entries)
	sort.Sort(object.TreeEntrySorter(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.Name == "" || entry.Name == "." || entry.Name == ".." || strings.ContainsAny(entry.Name, "/\x00") || entry.Hash == plumbing.ZeroHash {
			return "", fmt.Errorf("invalid source tree entry %q", entry.Name)
		}
		if _, duplicate := seen[entry.Name]; duplicate {
			return "", fmt.Errorf("duplicate source tree entry %q", entry.Name)
		}
		seen[entry.Name] = struct{}{}
	}
	hash, err := s.repo.writeObject(&object.Tree{Entries: entries})
	return model.SHA(hash.String()), err
}

// ReadCommit returns immutable headers without walking parents or entity packs.
func (s *SourceObjects) ReadCommit(sha model.SHA) (SourceCommit, error) {
	if err := s.check(); err != nil {
		return SourceCommit{}, err
	}
	commit, err := s.repo.commit(sha)
	if err != nil {
		return SourceCommit{}, err
	}
	parents := make([]model.SHA, len(commit.ParentHashes))
	for i, parent := range commit.ParentHashes {
		parents[i] = model.SHA(parent.String())
	}
	return SourceCommit{
		Tree: model.SHA(commit.TreeHash.String()), Parents: parents, Message: commit.Message,
		Author:    Signature{Name: commit.Author.Name, Email: commit.Author.Email, When: commit.Author.When},
		Committer: Signature{Name: commit.Committer.Name, Email: commit.Committer.Email, When: commit.Committer.When},
	}, nil
}

// WriteCommit stores a commit with the exact supplied tree, parents and identity.
func (s *SourceObjects) WriteCommit(commit SourceCommit) (model.SHA, error) {
	if err := s.check(); err != nil {
		return "", err
	}
	if !plumbing.IsHash(string(commit.Tree)) {
		return "", fmt.Errorf("invalid commit tree %q", commit.Tree)
	}
	parents := make([]plumbing.Hash, len(commit.Parents))
	for i, parent := range commit.Parents {
		if !plumbing.IsHash(string(parent)) {
			return "", fmt.Errorf("invalid commit parent %q", parent)
		}
		parents[i] = plumbing.NewHash(string(parent))
	}
	hash, err := s.repo.writeObject(&object.Commit{
		TreeHash: plumbing.NewHash(string(commit.Tree)), ParentHashes: parents, Message: commit.Message,
		Author:    object.Signature{Name: commit.Author.Name, Email: commit.Author.Email, When: commit.Author.When},
		Committer: object.Signature{Name: commit.Committer.Name, Email: commit.Committer.Email, When: commit.Committer.When},
	})
	return model.SHA(hash.String()), err
}
