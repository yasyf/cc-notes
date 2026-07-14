package notes

import (
	"context"
	"errors"
	"fmt"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/model"
)

// AnchorSpec is the set of anchors to attach to a note or doc, grouped by kind.
// Commit values are revisions or abbreviated shas resolved to a full 40-char
// sha at write time; the other values are stored verbatim.
type AnchorSpec struct {
	Commits  []string
	Paths    []string
	Dirs     []string
	Branches []string
}

// AnchorFilter narrows a note or doc list to entries carrying a given anchor.
// An empty field imposes no constraint on that anchor kind.
type AnchorFilter struct {
	Commit string
	Path   string
	Dir    string
	Branch string
}

// resolveCommits expands every commit anchor — an abbreviated sha or a revision
// like HEAD — to its full 40-char sha, so the stored value is what every read
// path can resolve. A revision naming no commit fails wrapping ErrNotFound. The
// result preserves order and is freshly allocated.
func (c *Client) resolveCommits(ctx context.Context, commits []string) ([]string, error) {
	if len(commits) == 0 {
		return commits, nil
	}
	full := make([]string, len(commits))
	for i, rev := range commits {
		sha, err := c.s.Git.CommitSHA(ctx, rev)
		if errors.Is(err, gitcmd.ErrRevNotFound) {
			return nil, fmt.Errorf("%w: no commit %s", ErrNotFound, rev)
		}
		if err != nil {
			return nil, err
		}
		full[i] = string(sha)
	}
	return full, nil
}

// buildAnchors flattens a spec into anchors in commit, path, dir, then branch
// order — the order the entity chain records them.
func buildAnchors(spec AnchorSpec) []model.Anchor {
	anchors := make([]model.Anchor, 0, len(spec.Commits)+len(spec.Paths)+len(spec.Dirs)+len(spec.Branches))
	for _, v := range spec.Commits {
		anchors = append(anchors, model.Anchor{Kind: model.AnchorCommit, Value: v})
	}
	for _, v := range spec.Paths {
		anchors = append(anchors, model.Anchor{Kind: model.AnchorPath, Value: v})
	}
	for _, v := range spec.Dirs {
		anchors = append(anchors, model.Anchor{Kind: model.AnchorDir, Value: v})
	}
	for _, v := range spec.Branches {
		anchors = append(anchors, model.Anchor{Kind: model.AnchorBranch, Value: v})
	}
	return anchors
}

// anchorEditOps appends the anchor ops every edit shares: one AddAnchor per
// resolved add, then one RemoveAnchor per flattened remove (matched verbatim).
func anchorEditOps(ops []model.Op, addAnchors []model.Anchor, removeAnchors AnchorSpec) []model.Op {
	for _, a := range addAnchors {
		ops = append(ops, model.AddAnchor{Anchor: a})
	}
	for _, a := range buildAnchors(removeAnchors) {
		ops = append(ops, model.RemoveAnchor{Anchor: a})
	}
	return ops
}

// buildWitness computes the per-anchor content witness against head: a path
// anchor's content oid and a directory anchor's tree oid (both skipped when
// HEAD is unborn or the path is absent), and a commit anchor's own oid. Branch
// anchors carry no witness. The result tracks anchor order.
func (c *Client) buildWitness(ctx context.Context, head model.SHA, anchors []model.Anchor) ([]model.AnchorWitness, error) {
	var witness []model.AnchorWitness
	for _, a := range anchors {
		switch a.Kind {
		case model.AnchorPath, model.AnchorDir:
			if head == "" {
				continue
			}
			oid, err := c.s.Git.PathOID(ctx, string(head), a.Value)
			if errors.Is(err, gitcmd.ErrPathNotFound) {
				continue
			}
			if err != nil {
				return nil, err
			}
			witness = append(witness, model.AnchorWitness{Anchor: a, OID: model.SHA(oid)})
		case model.AnchorCommit:
			witness = append(witness, model.AnchorWitness{Anchor: a, OID: model.SHA(a.Value)})
		case model.AnchorBranch:
		}
	}
	return witness, nil
}
