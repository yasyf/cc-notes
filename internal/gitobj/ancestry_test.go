package gitobj_test

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/plumbing"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

// gitIsAncestor is the oracle these tests pin gitobj against: git merge-base
// --is-ancestor reports its verdict as an exit status, 0 for an ancestor and 1
// for anything else, and any other status is a broken fixture.
func gitIsAncestor(t *testing.T, dir string, a, b model.SHA) bool {
	t.Helper()
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	cmd := exec.Command("git", "merge-base", "--is-ancestor", string(a), string(b))
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err == nil {
		return true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false
	}
	t.Fatalf("git merge-base --is-ancestor %s %s: %v\n%s", a, b, err, out)
	return false
}

func opsChain(t *testing.T, repo *gitobj.Repo, depth int) []model.SHA {
	t.Helper()
	chain := make([]model.SHA, 0, depth)
	var parents []model.SHA
	for i := range depth {
		pack := model.Pack{Lamport: model.Lamport(i + 1), Ops: []model.Op{model.SetTitle{Title: strconv.Itoa(i)}}}
		sha := write(t, repo, parents, t0.Add(time.Duration(i)*time.Minute), pack)
		chain = append(chain, sha)
		parents = []model.SHA{sha}
	}
	return chain
}

// TestIsAncestorShallowBoundary grafts a six-commit chain two commits back from
// its tip, leaving every object present, and requires gitobj's verdict to equal
// real git's for each case. The boundary commit itself is what separates a
// graft from an ignore list: git reaches it and stops there, so it is an
// ancestor while its own parents are not.
func TestIsAncestorShallowBoundary(t *testing.T) {
	const depth = 6
	dir := initRepo(t)
	writer := open(t, dir)
	chain := opsChain(t, writer, depth)
	unrelated := write(t, writer, nil, t3, tagPack)
	boundary, tip := chain[depth-3], chain[depth-1]
	gittest.Shallow(t, dir, string(boundary))

	repo := open(t, dir)
	cases := []struct {
		name string
		a, b model.SHA
		want bool
	}{
		{"root, past the boundary", chain[0], tip, false},
		{"the boundary's own parent", chain[depth-4], tip, false},
		{"the boundary itself", boundary, tip, true},
		{"inside the window", chain[depth-2], tip, true},
		{"self", tip, tip, true},
		{"descendant against its ancestor", tip, boundary, false},
		{"unrelated root", unrelated, tip, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if oracle := gitIsAncestor(t, dir, tc.a, tc.b); oracle != tc.want {
				t.Fatalf("fixture invalid: git merge-base --is-ancestor %s %s = %t, want %t", tc.a, tc.b, oracle, tc.want)
			}
			got, err := repo.IsAncestor(t.Context(), tc.a, tc.b)
			if err != nil {
				t.Fatalf("IsAncestor(%s, %s): %v", tc.a, tc.b, err)
			}
			if got != tc.want {
				t.Errorf("IsAncestor(%s, %s) = %t, git merge-base --is-ancestor = %t", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestIsAncestorGraftMovesUnderLiveHandle pins a handle against a graft that
// disappears out from under it, which is what git fetch --unshallow does. The
// boundary is not a fact of the handle: the truncated verdict has to stop being
// served once the file is gone, and the frontier that memoized it — drained,
// so cacheable — has to be discarded with it, or a long-lived Repo (one store
// per MCP server, one per viz watcher) keeps answering from a graft the
// repository no longer has.
func TestIsAncestorGraftMovesUnderLiveHandle(t *testing.T) {
	const depth = 6
	dir := initRepo(t)
	chain := opsChain(t, open(t, dir), depth)
	root, boundary, tip := chain[0], chain[depth-3], chain[depth-1]
	gittest.Shallow(t, dir, string(boundary))

	repo := open(t, dir)
	if oracle := gitIsAncestor(t, dir, root, tip); oracle {
		t.Fatalf("fixture invalid: git merge-base --is-ancestor %s %s = true under the graft, want false", root, tip)
	}
	got, err := repo.IsAncestor(t.Context(), root, tip)
	if err != nil {
		t.Fatalf("IsAncestor(%s, %s) under the graft: %v", root, tip, err)
	}
	if got {
		t.Fatalf("IsAncestor(%s, %s) = true under the graft, want false", root, tip)
	}

	gittest.Unshallow(t, dir)
	if oracle := gitIsAncestor(t, dir, root, tip); !oracle {
		t.Fatalf("fixture invalid: git merge-base --is-ancestor %s %s = false after unshallow, want true", root, tip)
	}
	got, err = repo.IsAncestor(t.Context(), root, tip)
	if err != nil {
		t.Fatalf("IsAncestor(%s, %s) after unshallow: %v", root, tip, err)
	}
	if !got {
		t.Errorf("IsAncestor(%s, %s) = false on the handle that saw the graft, git merge-base --is-ancestor = true", root, tip)
	}
}

// TestIsAncestorMissingObject clones one commit of a two-commit history over
// file://, so git writes the shallow file itself and the parent object is
// genuinely absent rather than merely grafted. The graft must not paper that
// over: a sha the object database does not hold fails loudly instead of
// answering false.
func TestIsAncestorMissingObject(t *testing.T) {
	origin := initRepo(t)
	writer := open(t, origin)
	c1 := write(t, writer, nil, t0, createPack)
	c2 := write(t, writer, []model.SHA{c1}, t1, retitlePack)
	git(t, origin, "update-ref", "refs/heads/main", string(c2))

	clone := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(clone), "-c", "protocol.file.allow=always", "clone", "-q", "--depth", "1", "file://"+origin, clone)
	if shallow := git(t, clone, "rev-parse", "--is-shallow-repository"); shallow != "true" {
		t.Fatalf("fixture invalid: clone reports --is-shallow-repository %q, want true", shallow)
	}

	repo := open(t, clone)
	if _, err := repo.IsAncestor(t.Context(), c1, c2); !errors.Is(err, gitobj.ErrCommitNotFound) {
		t.Errorf("IsAncestor with absent ancestor %s = %v, want ErrCommitNotFound", c1, err)
	}
	if _, err := repo.IsAncestor(t.Context(), c2, c1); !errors.Is(err, gitobj.ErrCommitNotFound) {
		t.Errorf("IsAncestor with absent descendant %s = %v, want ErrCommitNotFound", c1, err)
	}
	got, err := repo.IsAncestor(t.Context(), c2, c2)
	if err != nil {
		t.Fatalf("IsAncestor(%s, %s): %v", c2, c2, err)
	}
	if !got {
		t.Errorf("IsAncestor(%s, %s) = false, want true: the graft boundary is its own ancestor", c2, c2)
	}
}

// TestIsAncestorFrontierSurvivesFailedRead pins reachable's peek-then-dequeue
// rule. A commit leaves the frontier only once its object has been read, so a
// read that fails leaves the frontier exactly where it was; dequeuing first
// drops the commit before it is expanded, and the next call resumes past it and
// answers a false negative from the memo. The chain here is deep enough that
// the removed commit is reached with the frontier neither empty nor exhausted,
// and the ancestor sits behind it, so only a resumed walk can find it.
func TestIsAncestorFrontierSurvivesFailedRead(t *testing.T) {
	const depth = 6
	dir := initRepo(t)
	chain := opsChain(t, open(t, dir), depth)
	root, head, unread := chain[0], chain[depth-1], chain[depth-3]
	if !gitIsAncestor(t, dir, root, head) {
		t.Fatalf("fixture invalid: git merge-base --is-ancestor %s %s = false, want true", root, head)
	}

	repo := open(t, dir)
	restore := stashLooseObject(t, dir, string(unread))
	if _, err := repo.IsAncestor(t.Context(), root, head); !errors.Is(err, plumbing.ErrObjectNotFound) {
		t.Fatalf("IsAncestor(%s, %s) with commit %s removed = %v, want plumbing.ErrObjectNotFound", root, head, unread, err)
	}
	restore()

	got, err := repo.IsAncestor(t.Context(), root, head)
	if err != nil {
		t.Fatalf("IsAncestor(%s, %s) after restoring %s: %v", root, head, unread, err)
	}
	if !got {
		t.Errorf("IsAncestor(%s, %s) = false, git merge-base --is-ancestor = true: the failed read dropped %s from the frontier, so the resumed walk never expanded past it", root, head, unread)
	}
}

// TestIsAncestorFrontierEvictionIsLRU pins reachOf's recency bump. A long-lived
// process interleaves one hot descendant — the head every drift sweep checks
// its anchors against — with a stream of one-shot descendants, one per ref,
// from sync's ancestry checks. Least-recently-used eviction keeps the hot
// frontier through that stream; plain FIFO drops it every reachCap insertions
// and re-walks the whole history. A mid-chain commit is removed once the two
// warm frontiers are complete, which turns that difference into a verdict: a
// frontier that survived answers from its memo without reading anything, and
// one that was evicted walks into the removed commit and fails.
func TestIsAncestorFrontierEvictionIsLRU(t *testing.T) {
	const (
		depth = 8
		// coldFrontiers must exceed the memo's capacity, or the canary would
		// survive under either policy and prove nothing.
		coldFrontiers = 24
	)
	dir := initRepo(t)
	writer := open(t, dir)
	chain := opsChain(t, writer, depth)
	root, tip := chain[0], chain[depth-1]
	descendant := func(i int) model.SHA {
		return write(t, writer, []model.SHA{tip}, t0.Add(time.Duration(depth+i)*time.Minute), tagPack)
	}
	hot, canary := descendant(0), descendant(1)
	cold := make([]model.SHA, 0, coldFrontiers)
	for i := range coldFrontiers {
		cold = append(cold, descendant(2+i))
	}

	repo := open(t, dir)
	ctx := t.Context()
	for _, seed := range []model.SHA{hot, canary} {
		got, err := repo.IsAncestor(ctx, root, seed)
		if err != nil {
			t.Fatalf("warming IsAncestor(%s, %s): %v", root, seed, err)
		}
		if !got {
			t.Fatalf("warming IsAncestor(%s, %s) = false, want true", root, seed)
		}
	}
	unreadable := chain[depth/2]
	removeLooseObject(t, dir, string(unreadable))

	for i, oneShot := range cold {
		got, err := repo.IsAncestor(ctx, root, hot)
		if err != nil {
			t.Fatalf("IsAncestor(%s, hot %s) after %d one-shot frontiers: %v — the hot frontier was evicted, so the walk re-expanded into the removed commit %s", root, hot, i, err, unreadable)
		}
		if !got {
			t.Fatalf("IsAncestor(%s, hot %s) after %d one-shot frontiers = false, want true", root, hot, i)
		}
		if _, err := repo.IsAncestor(ctx, root, oneShot); !errors.Is(err, plumbing.ErrObjectNotFound) {
			t.Fatalf("IsAncestor(%s, cold[%d] %s) = %v, want plumbing.ErrObjectNotFound: a frontier the memo has never held has to walk into the removed commit %s", root, i, oneShot, err, unreadable)
		}
	}
	if _, err := repo.IsAncestor(ctx, root, canary); !errors.Is(err, plumbing.ErrObjectNotFound) {
		t.Fatalf("fixture invalid: the untouched canary frontier %s outlived %d insertions (%v), so nothing was ever evicted and the hot frontier proves nothing", canary, coldFrontiers, err)
	}
}

// TestIsAncestorMemoResumes drives the ancestry memo past both of its edges: a
// frontier resumed one commit at a time, drained by a negative verdict and then
// queried again, and more distinct descendants than reachCap so every round
// evicts a frontier and re-expands it from scratch. Every verdict stays git's.
func TestIsAncestorMemoResumes(t *testing.T) {
	const (
		depth       = 16
		descendants = 16
		rounds      = 3
	)
	dir := initRepo(t)
	repo := open(t, dir)
	chain := opsChain(t, repo, depth)
	head := chain[depth-1]
	offChain := write(t, repo, []model.SHA{chain[0]}, t3, tagPack)
	ctx := t.Context()

	for i := depth - 1; i >= 0; i-- {
		got, err := repo.IsAncestor(ctx, chain[i], head)
		if err != nil {
			t.Fatalf("IsAncestor(chain[%d], head): %v", i, err)
		}
		if !got {
			t.Fatalf("IsAncestor(chain[%d] %s, head %s) = false, want true after %d resumes", i, chain[i], head, depth-1-i)
		}
	}

	drained := []struct {
		name string
		a    model.SHA
		want bool
	}{
		{"off-chain commit drains the frontier", offChain, false},
		{"a drained frontier still answers true", chain[0], true},
		{"and still answers false", offChain, false},
		{"and still answers true mid-chain", chain[depth/2], true},
	}
	for _, tc := range drained {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.IsAncestor(ctx, tc.a, head)
			if err != nil {
				t.Fatalf("IsAncestor(%s, %s): %v", tc.a, head, err)
			}
			if got != tc.want {
				t.Errorf("IsAncestor(%s, %s) = %t, want %t", tc.a, head, got, tc.want)
			}
			if oracle := gitIsAncestor(t, dir, tc.a, head); oracle != tc.want {
				t.Errorf("fixture invalid: git merge-base --is-ancestor %s %s = %t, want %t", tc.a, head, oracle, tc.want)
			}
		})
	}

	heads := make([]model.SHA, 0, descendants)
	for i := range descendants {
		heads = append(heads, write(t, repo, []model.SHA{head}, t0.Add(time.Duration(depth+i)*time.Minute), tagPack))
	}
	probes := []struct {
		name string
		a    model.SHA
		want bool
	}{
		{"chain root", chain[0], true},
		{"chain head", head, true},
		{"off-chain commit", offChain, false},
	}
	for round := range rounds {
		for i, descendant := range heads {
			self, err := repo.IsAncestor(ctx, descendant, descendant)
			if err != nil {
				t.Fatalf("round %d, heads[%d]: IsAncestor against itself: %v", round, i, err)
			}
			if !self {
				t.Fatalf("round %d, heads[%d] %s: IsAncestor against itself = false, want true — the frontier belongs to another descendant", round, i, descendant)
			}
			for _, p := range probes {
				got, err := repo.IsAncestor(ctx, p.a, descendant)
				if err != nil {
					t.Fatalf("round %d, heads[%d], %s: IsAncestor: %v", round, i, p.name, err)
				}
				if got != p.want {
					t.Fatalf("round %d, heads[%d] %s, %s: IsAncestor(%s, %s) = %t, want %t", round, i, descendant, p.name, p.a, descendant, got, p.want)
				}
			}
		}
	}
}
