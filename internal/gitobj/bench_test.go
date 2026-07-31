package gitobj_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/osfs"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

const (
	benchChainDepth = 512
	benchGraftDepth = 20
	// benchFrontiers must exceed the ancestry memo's capacity, so every
	// iteration is a memo miss and the benchmark measures the walk itself.
	benchFrontiers = 32
	benchAncestors = 256
	benchPackDepth = 64
)

var benchAnchorCounts = []int{8, 64}

func benchOpen(b *testing.B, dir string) *gitobj.Repo {
	b.Helper()
	repo, err := gitobj.Open(gittest.Dirs(b, dir))
	if err != nil {
		b.Fatalf("Open(%s): %v", dir, err)
	}
	return repo
}

func benchWrite(b *testing.B, repo *gitobj.Repo, parents []model.SHA, n int) model.SHA {
	b.Helper()
	pack := model.Pack{Lamport: model.Lamport(n + 1), Ops: []model.Op{model.SetTitle{Title: strconv.Itoa(n)}}}
	sig := gitobj.Signature{Name: testName, Email: testEmail, When: t0.Add(time.Duration(n) * time.Minute)}
	sha, err := repo.WriteOpsCommit(b.Context(), parents, sig, "cc-notes: bench", pack)
	if err != nil {
		b.Fatalf("WriteOpsCommit: %v", err)
	}
	return sha
}

func benchChain(b *testing.B, repo *gitobj.Repo, depth int) []model.SHA {
	b.Helper()
	chain := make([]model.SHA, 0, depth)
	var parents []model.SHA
	for i := range depth {
		sha := benchWrite(b, repo, parents, i)
		chain = append(chain, sha)
		parents = []model.SHA{sha}
	}
	return chain
}

// BenchmarkIsAncestorShallowMiss measures the verdict a shallow monorepo pays
// most for: an ancestor unreachable from the descendant, so the walk cannot
// early-terminate and runs the history out. Honoring the graft bounds it at
// benchGraftDepth commits instead of benchChainDepth.
func BenchmarkIsAncestorShallowMiss(b *testing.B) {
	dir := gittest.InitRepo(b)
	writer := benchOpen(b, dir)
	chain := benchChain(b, writer, benchChainDepth)
	tip := chain[len(chain)-1]

	frontiers := make([]model.SHA, 0, benchFrontiers)
	for i := range benchFrontiers {
		frontiers = append(frontiers, benchWrite(b, writer, []model.SHA{tip}, benchChainDepth+i))
	}
	unreachable := benchWrite(b, writer, nil, benchChainDepth+benchFrontiers)
	gittest.Shallow(b, dir, string(chain[len(chain)-1-benchGraftDepth]))

	repo := benchOpen(b, dir)
	ctx := b.Context()
	for i := 0; b.Loop(); i++ {
		descendant := frontiers[i%benchFrontiers]
		ok, err := repo.IsAncestor(ctx, unreachable, descendant)
		if err != nil {
			b.Fatalf("IsAncestor: %v", err)
		}
		if ok {
			b.Fatalf("IsAncestor(%s, %s) = true, want false", unreachable, descendant)
		}
	}
}

// BenchmarkIsAncestorManyAncestorsOneHead measures the drift sweep's shape:
// many distinct ancestors checked against one head. Every verdict is true, so
// the cost is how deep the ancestor sits — until a per-descendant memo makes
// the walk a lookup. The gogit sub-benchmark is the baseline the memo replaced:
// object.Commit.IsAncestor, which re-walks the descendant's history from
// scratch on every query, so one run reports both sides of the change.
func BenchmarkIsAncestorManyAncestorsOneHead(b *testing.B) {
	dir := gittest.InitRepo(b)
	chain := benchChain(b, benchOpen(b, dir), benchAncestors)
	head := chain[len(chain)-1]

	b.Run("gitobj", func(b *testing.B) {
		repo := benchOpen(b, dir)
		ctx := b.Context()
		for i := 0; b.Loop(); i++ {
			ancestor := chain[i%benchAncestors]
			ok, err := repo.IsAncestor(ctx, ancestor, head)
			if err != nil {
				b.Fatalf("IsAncestor: %v", err)
			}
			if !ok {
				b.Fatalf("IsAncestor(%s, %s) = false, want true", ancestor, head)
			}
		}
	})
	b.Run("gogit", func(b *testing.B) {
		gitDir, commonDir := gittest.Dirs(b, dir)
		storage := filesystem.NewStorage(dotgit.NewRepositoryFilesystem(osfs.New(gitDir), osfs.New(commonDir)), cache.NewObjectLRUDefault())
		commit := func(sha model.SHA) *object.Commit {
			c, err := object.GetCommit(storage, plumbing.NewHash(string(sha)))
			if err != nil {
				b.Fatalf("GetCommit(%s): %v", sha, err)
			}
			return c
		}
		for i := 0; b.Loop(); i++ {
			ancestor := chain[i%benchAncestors]
			ok, err := commit(ancestor).IsAncestor(commit(head))
			if err != nil {
				b.Fatalf("object.Commit.IsAncestor: %v", err)
			}
			if !ok {
				b.Fatalf("object.Commit.IsAncestor(%s, %s) = false, want true", ancestor, head)
			}
		}
	})
}

func benchAnchorRepo(b *testing.B, count int) (dir string, rev model.SHA, anchors []string) {
	b.Helper()
	dir = gittest.InitRepo(b)
	anchors = make([]string, 0, count)
	for i := 0; len(anchors) < count; i++ {
		pkg := fmt.Sprintf("pkg/p%03d", i)
		if err := os.MkdirAll(filepath.Join(dir, pkg), 0o750); err != nil {
			b.Fatalf("mkdir %s: %v", pkg, err)
		}
		file := pkg + "/unit.go"
		if err := os.WriteFile(filepath.Join(dir, file), fmt.Appendf(nil, "package p%03d\n", i), 0o600); err != nil {
			b.Fatalf("write %s: %v", file, err)
		}
		anchors = append(anchors, pkg)
		if len(anchors) < count {
			anchors = append(anchors, file)
		}
	}
	gittest.Git(b, dir, "add", "-A")
	gittest.Git(b, dir, "commit", "-q", "-m", "bench fixture")
	return dir, model.SHA(gittest.Git(b, dir, "rev-parse", "HEAD")), anchors
}

// benchGitPathOID resolves one anchor the way internal/gitcmd did before this
// package took the read path in-process: one git rev-parse fork per anchor.
func benchGitPathOID(b *testing.B, dir string, rev model.SHA, path string) {
	b.Helper()
	//nolint:gosec // G204: benchmark helper shells out to git with fixed argv[0] and test-controlled args.
	cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", string(rev)+":"+path)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		b.Fatalf("git rev-parse %s:%s: %v", rev, path, err)
	}
	if len(bytes.TrimSpace(out)) != 40 {
		b.Fatalf("git rev-parse %s:%s printed %q, want one oid", rev, path, out)
	}
}

// BenchmarkPathOIDManyAnchors measures one status run: every path and dir
// anchor resolved at one rev. The git sub-benchmark is the baseline this
// package replaced — internal/gitcmd resolved every anchor by forking git
// rev-parse — so one run reports both sides of the change.
func BenchmarkPathOIDManyAnchors(b *testing.B) {
	for _, count := range benchAnchorCounts {
		b.Run(fmt.Sprintf("anchors=%d", count), func(b *testing.B) {
			dir, rev, anchors := benchAnchorRepo(b, count)
			b.Run("gitobj", func(b *testing.B) {
				repo := benchOpen(b, dir)
				ctx := b.Context()
				for b.Loop() {
					for _, anchor := range anchors {
						oid, err := repo.PathOID(ctx, rev, anchor)
						if err != nil {
							b.Fatalf("PathOID(%s, %s): %v", rev, anchor, err)
						}
						if len(oid) != 40 {
							b.Fatalf("PathOID(%s, %s) = %q, want a 40-hex oid", rev, anchor, oid)
						}
					}
				}
			})
			b.Run("git", func(b *testing.B) {
				for b.Loop() {
					for _, anchor := range anchors {
						benchGitPathOID(b, dir, rev, anchor)
					}
				}
			})
		})
	}
}

// BenchmarkReadChainPacked measures a chain read against a packed object
// database, where every commit, tree, and ops blob is a packfile lookup.
func BenchmarkReadChainPacked(b *testing.B) {
	dir := gittest.InitRepo(b)
	chain := benchChain(b, benchOpen(b, dir), benchPackDepth)
	tip := chain[len(chain)-1]
	gittest.Git(b, dir, "update-ref", "refs/cc-notes/notes/bench", string(tip))
	gittest.Git(b, dir, "repack", "-a", "-d", "-q")

	repo := benchOpen(b, dir)
	ctx := b.Context()
	for b.Loop() {
		commits, err := repo.ReadChain(ctx, tip)
		if err != nil {
			b.Fatalf("ReadChain: %v", err)
		}
		if len(commits) != benchPackDepth {
			b.Fatalf("ReadChain = %d commits, want %d", len(commits), benchPackDepth)
		}
	}
}
