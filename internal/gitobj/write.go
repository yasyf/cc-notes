package gitobj

import (
	"context"
	"fmt"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/yasyf/cc-notes/internal/model"
)

// encoder is the slice of object.Tree and object.Commit that writeObject
// needs: serialization into an encoded object.
type encoder interface {
	Encode(plumbing.EncodedObject) error
}

// WriteOpsCommit stores pack as a commit object: a blob holding the pack's
// canonical JSON, a tree with the single entry ops.json (mode 100644), and a
// commit with author == committer == sig. It only writes objects — pointing a
// ref at the result is gitcmd's job. The write is deterministic: identical
// parents, signature, message, and pack produce the identical commit id.
func (r *Repo) WriteOpsCommit(ctx context.Context, parents []model.SHA, sig Signature, message string, pack model.Pack) (model.SHA, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	parentHashes := make([]plumbing.Hash, len(parents))
	for i, parent := range parents {
		if !plumbing.IsHash(string(parent)) {
			return "", fmt.Errorf("invalid parent sha %q", parent)
		}
		parentHashes[i] = plumbing.NewHash(string(parent))
	}
	data, err := pack.MarshalJSON()
	if err != nil {
		return "", fmt.Errorf("marshal pack: %w", err)
	}
	blob, err := r.writeBlob(data)
	if err != nil {
		return "", fmt.Errorf("write ops blob: %w", err)
	}
	tree, err := r.writeObject(&object.Tree{
		Entries: []object.TreeEntry{{Name: opsFile, Mode: filemode.Regular, Hash: blob}},
	})
	if err != nil {
		return "", fmt.Errorf("write ops tree: %w", err)
	}
	author := object.Signature{Name: sig.Name, Email: sig.Email, When: sig.When}
	sha, err := r.writeObject(&object.Commit{
		Author:       author,
		Committer:    author,
		Message:      message,
		TreeHash:     tree,
		ParentHashes: parentHashes,
	})
	if err != nil {
		return "", fmt.Errorf("write ops commit: %w", err)
	}
	return model.SHA(sha.String()), nil
}

func (r *Repo) writeBlob(data []byte) (plumbing.Hash, error) {
	obj := r.repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close() // best-effort: the write error is the one to report
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return r.repo.Storer.SetEncodedObject(obj)
}

func (r *Repo) writeObject(o encoder) (plumbing.Hash, error) {
	obj := r.repo.Storer.NewEncodedObject()
	if err := o.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return r.repo.Storer.SetEncodedObject(obj)
}
