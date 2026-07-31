package gitobj

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/yasyf/cc-notes/model"
)

// PathOID returns the git object id of path's content at rev — a blob oid for
// a file, a tree oid for a directory, the recorded commit oid for a submodule
// gitlink — resolving path exactly as git resolves <rev>:<path>. Anchor values
// are stored verbatim, so the shapes git itself accepts resolve here too: the
// empty path is the root tree, one trailing slash on a directory is that
// directory, and a leading ./ or ../ normalizes against the repository root.
// A path with no content at rev wraps model.ErrPathNotFound, an unborn HEAD
// (an empty rev) included; a ./ or ../ path climbing above the root fails with
// ErrPathEscapesRoot, which is git's fatal rather than its miss.
func (r *Repo) PathOID(ctx context.Context, rev model.SHA, path string) (model.SHA, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if rev == "" {
		return "", fmt.Errorf("path %s at unborn HEAD: %w", path, model.ErrPathNotFound)
	}
	lookup, err := normalizeTreePath(path)
	if err != nil {
		return "", fmt.Errorf("path oid %s:%s: %w", rev, path, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	tree, err := r.rootTree(rev)
	if err != nil {
		return "", err
	}
	hash, err := r.treeOID(tree, lookup)
	if errors.Is(err, model.ErrPathNotFound) {
		return "", fmt.Errorf("path %s at %s: %w", path, rev, model.ErrPathNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("path oid %s:%s: %w", rev, path, err)
	}
	return model.SHA(hash.String()), nil
}

// normalizeTreePath applies git's resolve_relative_path: only a ./ or ../
// prefixed path is normalized, and against the repository root, because
// PathOID has no cwd to anchor it to. Every other path is walked verbatim —
// git leaves "d/./f.go" and "d//f.go" to miss. Normalization drops "."
// components, collapses repeated slashes, and resolves ".." against the
// preceding component, which need not exist: git's normalize_path_copy_len
// pops it from the buffer without ever looking it up.
//
// The separator the dropped component sat behind stays: that routine consumes
// "." and ".." out of the source and leaves the destination ending at the "/"
// it already wrote, so "./f.go/." and "./f.go/git~1/.." both normalize to
// "f.go/". The walk honors a trailing separator only on a directory, which is
// what keeps those a miss on a blob, symlink, or gitlink — git's verdict too.
func normalizeTreePath(path string) (string, error) {
	if !strings.HasPrefix(path, "./") && !strings.HasPrefix(path, "../") {
		return path, nil
	}
	components := strings.Split(path, "/")
	var parts []string
	for _, component := range components {
		switch component {
		case "", ".":
		case "..":
			if len(parts) == 0 {
				return "", ErrPathEscapesRoot
			}
			parts = parts[:len(parts)-1]
		default:
			parts = append(parts, component)
		}
	}
	if len(parts) == 0 {
		return "", nil
	}
	normalized := strings.Join(parts, "/")
	switch components[len(components)-1] {
	case "", ".", "..":
		normalized += "/"
	}
	return normalized, nil
}

// treeOID resolves an already-normalized path against tree the way git's
// find_tree_entry does: an exact name match yields that entry whatever its
// mode, a name with path left over descends only through a directory, and one
// trailing slash yields the directory itself. Every shape git reports as a
// plain miss — an absent or empty component, a descent through a blob,
// symlink, or gitlink — returns model.ErrPathNotFound, so a genuinely absent
// tree object stays a loud error.
func (r *Repo) treeOID(tree *treeIndex, path string) (plumbing.Hash, error) {
	if path == "" {
		return tree.hash, nil
	}
	for {
		name, rest, descend := strings.Cut(path, "/")
		entry, ok := tree.entries[name]
		switch {
		case !ok:
			return plumbing.ZeroHash, model.ErrPathNotFound
		case !descend:
			return entry.Hash, nil
		case entry.Mode != filemode.Dir:
			return plumbing.ZeroHash, model.ErrPathNotFound
		case rest == "":
			return entry.Hash, nil
		}
		sub, err := r.subtree(entry.Hash)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		tree, path = sub, rest
	}
}

type treeIndex struct {
	hash    plumbing.Hash
	entries map[string]object.TreeEntry
}

func newTreeIndex(tree *object.Tree) *treeIndex {
	entries := make(map[string]object.TreeEntry, len(tree.Entries))
	for _, entry := range tree.Entries {
		entries[entry.Name] = entry
	}
	return &treeIndex{hash: tree.Hash, entries: entries}
}

// rootTree memoizes one rev, because a review sweep resolves every anchor of
// every entity against the same head.
func (r *Repo) rootTree(rev model.SHA) (*treeIndex, error) {
	if r.treeVal != nil && r.treeRev == rev {
		return r.treeVal, nil
	}
	commit, err := r.commit(rev)
	if err != nil {
		return nil, err
	}
	tree, err := retry(r, commit.Tree)
	if err != nil {
		return nil, fmt.Errorf("read tree of commit %s: %w", rev, err)
	}
	clear(r.subtrees)
	r.treeRev, r.treeVal = rev, newTreeIndex(tree)
	return r.treeVal, nil
}

// subtree memoizes every tree the walk descends into for the life of the
// root-tree memo, and reads it through retry so a pack index gone stale under
// a long-lived process is repaired rather than reported as a missing object.
func (r *Repo) subtree(hash plumbing.Hash) (*treeIndex, error) {
	if indexed, ok := r.subtrees[hash]; ok {
		return indexed, nil
	}
	tree, err := retry(r, func() (*object.Tree, error) {
		return object.GetTree(r.storage, hash)
	})
	if err != nil {
		return nil, fmt.Errorf("read tree %s: %w", hash, err)
	}
	indexed := newTreeIndex(tree)
	r.subtrees[hash] = indexed
	return indexed, nil
}
