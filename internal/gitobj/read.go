package gitobj

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/yasyf/cc-notes/internal/model"
)

// ReadChain walks every commit reachable from tip — breadth-first from the
// tip, deduplicated, so diamond merges yield each commit once — and decodes
// each one's ops.json into a model.PackCommit. Callers needing a total order
// feed the result to fold.Linearize. A commit, tree, or ops blob absent from
// the object database fails with ErrIncompleteChain naming the commit; a
// commit whose tree has no ops.json entry fails with ErrCorruptCommit.
func (r *Repo) ReadChain(ctx context.Context, tip model.SHA) ([]model.PackCommit, error) {
	if !plumbing.IsHash(string(tip)) {
		return nil, fmt.Errorf("invalid tip sha %q", tip)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	start := plumbing.NewHash(string(tip))
	queue := []plumbing.Hash{start}
	seen := map[plumbing.Hash]bool{start: true}
	chain := make([]model.PackCommit, 0, 1)
	for len(queue) > 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		hash := queue[0]
		queue = queue[1:]
		commit, err := object.GetCommit(r.repo.Storer, hash)
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			return nil, fmt.Errorf("%w: commit %s missing (shallow clone?)", ErrIncompleteChain, hash)
		}
		if err != nil {
			return nil, fmt.Errorf("read commit %s: %w", hash, err)
		}
		pc, err := r.packCommit(commit)
		if err != nil {
			return nil, err
		}
		chain = append(chain, pc)
		for _, parent := range commit.ParentHashes {
			if !seen[parent] {
				seen[parent] = true
				queue = append(queue, parent)
			}
		}
	}
	return chain, nil
}

// Tip resolves ref (symbolic refs included) to the commit it points at. It
// fails with ErrRefNotFound when the ref does not exist.
func (r *Repo) Tip(ctx context.Context, ref string) (model.SHA, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	resolved, err := r.repo.Reference(plumbing.ReferenceName(ref), true)
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return "", fmt.Errorf("%w: %s", ErrRefNotFound, ref)
	}
	if err != nil {
		return "", fmt.Errorf("resolve ref %s: %w", ref, err)
	}
	return model.SHA(resolved.Hash().String()), nil
}

// ListPrefix returns every hash ref — loose and packed — whose full name
// starts with prefix, mapped to the commit it points at.
func (r *Repo) ListPrefix(ctx context.Context, prefix string) (map[string]model.SHA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	iter, err := r.repo.References()
	if err != nil {
		return nil, fmt.Errorf("list refs: %w", err)
	}
	tips := make(map[string]model.SHA)
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if ref.Type() != plumbing.HashReference {
			return nil
		}
		if name := string(ref.Name()); strings.HasPrefix(name, prefix) {
			tips[name] = model.SHA(ref.Hash().String())
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list refs with prefix %s: %w", prefix, err)
	}
	return tips, nil
}

// IsAncestor reports whether a is an ancestor of — or equal to — b.
func (r *Repo) IsAncestor(ctx context.Context, a, b model.SHA) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	ancestor, err := r.commit(a)
	if err != nil {
		return false, err
	}
	descendant, err := r.commit(b)
	if err != nil {
		return false, err
	}
	ok, err := ancestor.IsAncestor(descendant)
	if err != nil {
		return false, fmt.Errorf("walk ancestry of %s: %w", b, err)
	}
	return ok, nil
}

func (r *Repo) commit(sha model.SHA) (*object.Commit, error) {
	if !plumbing.IsHash(string(sha)) {
		return nil, fmt.Errorf("invalid sha %q", sha)
	}
	commit, err := object.GetCommit(r.repo.Storer, plumbing.NewHash(string(sha)))
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return nil, fmt.Errorf("%w: %s", ErrCommitNotFound, sha)
	}
	if err != nil {
		return nil, fmt.Errorf("read commit %s: %w", sha, err)
	}
	return commit, nil
}

func (r *Repo) packCommit(commit *object.Commit) (model.PackCommit, error) {
	tree, err := commit.Tree()
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return model.PackCommit{}, fmt.Errorf("%w: tree of commit %s missing (shallow clone?)", ErrIncompleteChain, commit.Hash)
	}
	if err != nil {
		return model.PackCommit{}, fmt.Errorf("read tree of commit %s: %w", commit.Hash, err)
	}
	entry, err := tree.FindEntry(opsFile)
	if err != nil {
		return model.PackCommit{}, fmt.Errorf("%w: commit %s has no %s", ErrCorruptCommit, commit.Hash, opsFile)
	}
	blob, err := object.GetBlob(r.repo.Storer, entry.Hash)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return model.PackCommit{}, fmt.Errorf("%w: ops blob of commit %s missing (shallow clone?)", ErrIncompleteChain, commit.Hash)
	}
	if err != nil {
		return model.PackCommit{}, fmt.Errorf("read ops blob of commit %s: %w", commit.Hash, err)
	}
	reader, err := blob.Reader()
	if err != nil {
		return model.PackCommit{}, fmt.Errorf("open ops blob of commit %s: %w", commit.Hash, err)
	}
	data, err := io.ReadAll(reader)
	_ = reader.Close() // best-effort: data is fully read or err already reports the failure
	if err != nil {
		return model.PackCommit{}, fmt.Errorf("read ops blob of commit %s: %w", commit.Hash, err)
	}
	pack, err := model.DecodePack(data)
	if err != nil {
		return model.PackCommit{}, fmt.Errorf("commit %s: %w", commit.Hash, err)
	}
	var parents []model.SHA
	for _, parent := range commit.ParentHashes {
		parents = append(parents, model.SHA(parent.String()))
	}
	return model.PackCommit{
		SHA:        model.SHA(commit.Hash.String()),
		Parents:    parents,
		Author:     model.Actor(fmt.Sprintf("%s <%s>", commit.Author.Name, commit.Author.Email)),
		AuthorTime: commit.Author.When.Unix(),
		Pack:       pack,
	}, nil
}
