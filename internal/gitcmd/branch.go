package gitcmd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/yasyf/cc-notes/model"
)

// ErrNoTrunk reports that no trunk branch could be resolved: origin/HEAD is
// unset and neither a local main nor master ref exists.
var ErrNoTrunk = errors.New("cannot determine trunk")

// TrunkBranch resolves the repository's trunk: the remote default branch
// (origin/HEAD) when set, else a probe of local main then master. It wraps
// ErrNoTrunk when none of those resolve.
func (g Git) TrunkBranch(ctx context.Context) (model.Branch, error) {
	switch branch, err := g.DefaultBranch(ctx); {
	case err == nil:
		return branch, nil
	case !errors.Is(err, ErrNoDefaultBranch):
		return "", fmt.Errorf("trunk branch: %w", err)
	}
	for _, name := range []string{"main", "master"} {
		_, err := g.run(ctx, "", "rev-parse", "--verify", "--quiet", "refs/heads/"+name)
		var cmdErr *commandError
		if errors.As(err, &cmdErr) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("probe trunk %s: %w", name, err)
		}
		return model.Branch(name), nil
	}
	return "", ErrNoTrunk
}

// CurrentBranch resolves the branch the working copy is on, aware of
// jj-colocated repos that keep git HEAD detached at the working-copy parent
// (@-) while exporting bookmarks as refs/heads/*. An attached HEAD returns its
// branch. A detached HEAD resolves to the nearest bookmark ancestor not already
// merged into trunk — the jj `trunk()..@ & bookmarks()` revset, computed
// git-natively — and falls back to trunk when that set is empty or ambiguous.
// With no trunk it returns the sole branch pointing at HEAD, else ErrDetachedHead.
func (g Git) CurrentBranch(ctx context.Context) (model.Branch, error) {
	switch b, err := g.HeadBranch(ctx); {
	case err == nil:
		return b, nil
	case !errors.Is(err, ErrDetachedHead):
		return "", err
	}
	switch trunk, err := g.TrunkBranch(ctx); {
	case err == nil:
		return g.nearestBookmarkAncestor(ctx, trunk)
	case errors.Is(err, ErrNoTrunk):
		return g.soleBranchAtHead(ctx)
	default:
		return "", err
	}
}

func (g Git) nearestBookmarkAncestor(ctx context.Context, trunk model.Branch) (model.Branch, error) {
	mergedHead, err := g.branchesMergedInto(ctx, "HEAD")
	if err != nil {
		return "", err
	}
	trunkRef, err := g.resolveTrunkRef(ctx, trunk)
	if err != nil {
		return "", err
	}
	mergedTrunk, err := g.branchesMergedInto(ctx, trunkRef)
	if err != nil {
		return "", err
	}
	inTrunk := make(map[string]bool, len(mergedTrunk))
	for _, name := range mergedTrunk {
		inTrunk[name] = true
	}
	var candidates []string
	for _, name := range mergedHead {
		if !inTrunk[name] {
			candidates = append(candidates, name)
		}
	}
	if len(candidates) == 0 {
		return trunk, nil
	}

	var maximal []string
	for _, x := range candidates {
		isMax := true
		for _, y := range candidates {
			if x == y {
				continue
			}
			anc, err := g.isAncestorOrEqual(ctx, "refs/heads/"+y, "refs/heads/"+x)
			if err != nil {
				return "", err
			}
			if !anc {
				isMax = false
				break
			}
		}
		if isMax {
			maximal = append(maximal, x)
		}
	}
	if len(maximal) == 1 {
		return model.Branch(maximal[0]), nil
	}
	return trunk, nil
}

func (g Git) soleBranchAtHead(ctx context.Context) (model.Branch, error) {
	out, err := g.run(ctx, "", "for-each-ref", "--points-at", "HEAD", "--format=%(refname:lstrip=2)", "refs/heads/")
	if err != nil {
		return "", fmt.Errorf("branches at HEAD: %w", err)
	}
	names := nonEmptyLines(out)
	if len(names) == 1 {
		return model.Branch(names[0]), nil
	}
	return "", fmt.Errorf("current branch: %w", ErrDetachedHead)
}

func (g Git) resolveTrunkRef(ctx context.Context, trunk model.Branch) (string, error) {
	for _, ref := range []string{"refs/heads/" + string(trunk), "refs/remotes/origin/" + string(trunk)} {
		_, err := g.run(ctx, "", "rev-parse", "--verify", "--quiet", ref)
		if err == nil {
			return ref, nil
		}
		var cmdErr *commandError
		if errors.As(err, &cmdErr) {
			continue
		}
		return "", fmt.Errorf("resolve trunk ref %s: %w", ref, err)
	}
	return "", fmt.Errorf("resolve trunk %s: %w", trunk, ErrNoTrunk)
}

func (g Git) branchesMergedInto(ctx context.Context, rev string) ([]string, error) {
	out, err := g.run(ctx, "", "for-each-ref", "--merged", rev, "--format=%(refname:lstrip=2)", "refs/heads/")
	if err != nil {
		return nil, fmt.Errorf("branches merged into %s: %w", rev, err)
	}
	return nonEmptyLines(out), nil
}

func (g Git) isAncestorOrEqual(ctx context.Context, ref, of string) (bool, error) {
	_, err := g.run(ctx, "", "merge-base", "--is-ancestor", ref, of)
	if err == nil {
		return true, nil
	}
	var cmdErr *commandError
	if errors.As(err, &cmdErr) && cmdErr.exitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("is-ancestor %s %s: %w", ref, of, err)
}

// nonEmptyLines splits raw git output on "\n" and returns every non-empty
// element verbatim, stripping only a trailing "\r" so Windows line endings
// don't leak. It never trims other whitespace: a Unicode space (e.g. U+00A0)
// is legal in a ref name and must survive intact.
func nonEmptyLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSuffix(line, "\r"); line != "" {
			out = append(out, line)
		}
	}
	return out
}
