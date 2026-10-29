package gitobj

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"

	"github.com/yasyf/cc-notes/model"
)

// ReadChain walks every commit reachable from tip breadth-first, deduplicated,
// and decodes each one's ops.json into a model.PackCommit. A missing object
// is fetched through the BackfillWith hook and the walk retried once; one
// still missing fails with ErrIncompleteChain, and a commit with no ops.json
// with ErrCorruptCommit.
func (r *Repo) ReadChain(ctx context.Context, tip model.SHA) ([]model.PackCommit, error) {
	chain, err := r.readChain(ctx, tip)
	if r.backfill == nil || !errors.Is(err, ErrIncompleteChain) {
		return chain, err
	}
	if backfillErr := r.backfill(ctx, tip); backfillErr != nil {
		return nil, fmt.Errorf("%w: backfill: %w", err, backfillErr)
	}
	return r.readChain(ctx, tip)
}

func (r *Repo) readChain(ctx context.Context, tip model.SHA) ([]model.PackCommit, error) {
	if !plumbing.IsHash(string(tip)) {
		return nil, fmt.Errorf("invalid tip sha %q", tip)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.refreshGraft(); err != nil {
		return nil, err
	}
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
		commit, err := r.lookupCommit(hash)
		if errors.Is(err, plumbing.ErrObjectNotFound) {
			return nil, fmt.Errorf("%w: commit %s %s", ErrIncompleteChain, hash, r.missingSuffix())
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
	resolved, err := storer.ResolveReference(referenceLookup{ReferenceStorer: r.storage, repo: r, ctx: ctx}, plumbing.ReferenceName(ref))
	if errors.Is(err, plumbing.ErrReferenceNotFound) {
		return "", fmt.Errorf("%w: %s", ErrRefNotFound, ref)
	}
	if err != nil {
		return "", fmt.Errorf("resolve ref %s: %w", ref, err)
	}
	return model.SHA(resolved.Hash().String()), nil
}

// ListSymbolic returns every symbolic ref whose full name starts with prefix,
// mapped to the name of the ref it points at.
func (r *Repo) ListSymbolic(ctx context.Context, prefix string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	refs, err := retryEmptyRef(ctx, func() (map[string]*plumbing.Reference, error) {
		return r.referencePrefix(ctx, prefix)
	})
	if err != nil {
		return nil, fmt.Errorf("list refs: %w", err)
	}
	targets := make(map[string]string)
	for _, ref := range refs {
		if ref.Type() == plumbing.SymbolicReference {
			targets[string(ref.Name())] = string(ref.Target())
		}
	}
	return targets, nil
}

// ListPrefix returns every hash ref — loose and packed — whose full name
// starts with prefix, mapped to the commit it points at.
func (r *Repo) ListPrefix(ctx context.Context, prefix string) (map[string]model.SHA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	refs, err := retryEmptyRef(ctx, func() (map[string]*plumbing.Reference, error) {
		return r.referencePrefix(ctx, prefix)
	})
	if err != nil {
		return nil, fmt.Errorf("list refs: %w", err)
	}
	tips := make(map[string]model.SHA)
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ref.Type() != plumbing.HashReference {
			continue
		}
		name := string(ref.Name())
		tips[name] = model.SHA(ref.Hash().String())
	}
	return tips, nil
}

func (r *Repo) lookupCommit(hash plumbing.Hash) (*object.Commit, error) {
	return retry(r, func() (*object.Commit, error) {
		return object.GetCommit(r.storage, hash)
	})
}

func (r *Repo) commit(sha model.SHA) (*object.Commit, error) {
	if !plumbing.IsHash(string(sha)) {
		return nil, fmt.Errorf("invalid sha %q", sha)
	}
	commit, err := r.lookupCommit(plumbing.NewHash(string(sha)))
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return nil, fmt.Errorf("%w: %s", ErrCommitNotFound, sha)
	}
	if err != nil {
		return nil, fmt.Errorf("read commit %s: %w", sha, err)
	}
	return commit, nil
}

func (r *Repo) missingSuffix() string {
	if len(r.shallow) > 0 {
		return "missing (shallow clone)"
	}
	return "missing from object database"
}

func (r *Repo) packCommit(commit *object.Commit) (model.PackCommit, error) {
	tree, err := retry(r, commit.Tree)
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return model.PackCommit{}, fmt.Errorf("%w: tree of commit %s %s", ErrIncompleteChain, commit.Hash, r.missingSuffix())
	}
	if err != nil {
		return model.PackCommit{}, fmt.Errorf("read tree of commit %s: %w", commit.Hash, err)
	}
	entry, err := tree.FindEntry(opsFile)
	if err != nil {
		return model.PackCommit{}, fmt.Errorf("%w: commit %s has no %s", ErrCorruptCommit, commit.Hash, opsFile)
	}
	blob, err := retry(r, func() (*object.Blob, error) {
		return object.GetBlob(r.storage, entry.Hash)
	})
	if errors.Is(err, plumbing.ErrObjectNotFound) {
		return model.PackCommit{}, fmt.Errorf("%w: ops blob of commit %s %s", ErrIncompleteChain, commit.Hash, r.missingSuffix())
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
