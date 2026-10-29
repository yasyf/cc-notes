package gitcmd_test

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func commitAt(t *testing.T, g gitcmd.Git, msg string, unix int64) model.SHA {
	t.Helper()
	date := fmt.Sprintf("@%d +0000", unix)
	t.Setenv("GIT_AUTHOR_DATE", date)
	t.Setenv("GIT_COMMITTER_DATE", date)
	gittest.Git(t, g.Dir, "commit", "-q", "--allow-empty", "-m", msg)
	return model.SHA(gittest.Git(t, g.Dir, "rev-parse", "HEAD"))
}

func TestTrunkBranch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(t *testing.T, g gitcmd.Git)
		want    model.Branch
		wantErr error
	}{
		{
			name: "origin/HEAD set wins over local main",
			setup: func(t *testing.T, g gitcmd.Git) {
				sha := commitEmpty(t, g, "c1")
				gittest.Git(t, g.Dir, "update-ref", "refs/remotes/origin/release", string(sha))
				gittest.Git(t, g.Dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/release")
			},
			want: "release",
		},
		{
			name:  "origin unset, main exists",
			setup: func(t *testing.T, g gitcmd.Git) { commitEmpty(t, g, "c1") },
			want:  "main",
		},
		{
			name: "only master exists",
			setup: func(t *testing.T, g gitcmd.Git) {
				gittest.Git(t, g.Dir, "checkout", "-q", "-b", "master")
				commitEmpty(t, g, "c1")
			},
			want: "master",
		},
		{
			name: "no origin, no main, no master",
			setup: func(t *testing.T, g gitcmd.Git) {
				gittest.Git(t, g.Dir, "checkout", "-q", "-b", "wip")
				commitEmpty(t, g, "c1")
			},
			wantErr: gitcmd.ErrNoTrunk,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := initRepo(t)
			tc.setup(t, g)
			got, err := g.TrunkBranch(t.Context())
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("TrunkBranch() err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("TrunkBranch() unexpected err = %v", err)
			}
			if got != tc.want {
				t.Fatalf("TrunkBranch() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCurrentBranch(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setup   func(t *testing.T, g gitcmd.Git)
		want    model.Branch
		wantErr error
	}{
		{
			name:  "attached HEAD returns its branch",
			setup: func(t *testing.T, g gitcmd.Git) { commitEmpty(t, g, "c1") },
			want:  "main",
		},
		{
			name: "detached at trunk tip returns trunk",
			setup: func(t *testing.T, g gitcmd.Git) {
				commitEmpty(t, g, "c1")
				gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "HEAD")
			},
			want: "main",
		},
		{
			name: "detached past a bookmark returns the nearest bookmark",
			setup: func(t *testing.T, g gitcmd.Git) {
				commitEmpty(t, g, "c1")
				gittest.Git(t, g.Dir, "checkout", "-q", "-b", "feat")
				commitEmpty(t, g, "c2")
				gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "HEAD")
				commitEmpty(t, g, "c3")
			},
			want: "feat",
		},
		{
			name: "branch merged into trunk is excluded, trunk wins",
			setup: func(t *testing.T, g gitcmd.Git) {
				commitEmpty(t, g, "c1")
				gittest.Git(t, g.Dir, "checkout", "-q", "-b", "old")
				commitEmpty(t, g, "c2")
				gittest.Git(t, g.Dir, "checkout", "-q", "main")
				gittest.Git(t, g.Dir, "merge", "-q", "--no-ff", "-m", "merge old", "old")
				gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "HEAD")
			},
			want: "main",
		},
		{
			name: "two divergent non-merged bookmarks are ambiguous, trunk wins",
			setup: func(t *testing.T, g gitcmd.Git) {
				commitEmpty(t, g, "c1")
				gittest.Git(t, g.Dir, "checkout", "-q", "-b", "branchA")
				commitEmpty(t, g, "a1")
				gittest.Git(t, g.Dir, "checkout", "-q", "main")
				gittest.Git(t, g.Dir, "checkout", "-q", "-b", "branchB")
				commitEmpty(t, g, "b1")
				gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "branchA")
				gittest.Git(t, g.Dir, "merge", "-q", "--no-ff", "-m", "octo", "branchB")
			},
			want: "main",
		},
		{
			name: "stacked bookmarks select the unique top despite name and timestamp order",
			setup: func(t *testing.T, g gitcmd.Git) {
				commitEmpty(t, g, "trunk")
				gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "HEAD")
				commitAt(t, g, "lower", 1700000300)
				gittest.Git(t, g.Dir, "branch", "z-lower")
				gittest.Git(t, g.Dir, "branch", "z-lower-alias")
				commitAt(t, g, "middle", 1700000200)
				gittest.Git(t, g.Dir, "branch", "a-middle")
				commitAt(t, g, "top", 1700000100)
				gittest.Git(t, g.Dir, "branch", "m-top")
				gittest.Git(t, g.Dir, "tag", "m-top", "main")
			},
			want: "m-top",
		},
		{
			name: "two names at the top of a stack remain ambiguous",
			setup: func(t *testing.T, g gitcmd.Git) {
				commitEmpty(t, g, "trunk")
				gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "HEAD")
				commitEmpty(t, g, "lower")
				gittest.Git(t, g.Dir, "branch", "lower")
				commitEmpty(t, g, "top")
				gittest.Git(t, g.Dir, "branch", "top")
				gittest.Git(t, g.Dir, "branch", "top-alias")
			},
			want: "main",
		},
		{
			name: "a trunk-excluded alias moved to the top is not a candidate",
			setup: func(t *testing.T, g gitcmd.Git) {
				commitEmpty(t, g, "trunk")
				gittest.Git(t, g.Dir, "branch", "excluded")
				gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "HEAD")
				commitEmpty(t, g, "lower")
				gittest.Git(t, g.Dir, "branch", "lower")
				top := commitEmpty(t, g, "top")
				gittest.Git(t, g.Dir, "branch", "top")
				gittest.GitShim{
					Pattern:    `*"for-each-ref --points-at"*`,
					Interleave: [][]string{{"-C", g.Dir, "update-ref", "refs/heads/excluded", string(top)}},
				}.Install(t)
			},
			want: "top",
		},
		{
			name: "a bookmarked merge contains both prior divergent bookmarks",
			setup: func(t *testing.T, g gitcmd.Git) {
				commitEmpty(t, g, "trunk")
				gittest.Git(t, g.Dir, "checkout", "-q", "-b", "left")
				commitEmpty(t, g, "left")
				gittest.Git(t, g.Dir, "checkout", "-q", "-b", "right", "main")
				commitEmpty(t, g, "right")
				gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "left")
				gittest.Git(t, g.Dir, "merge", "-q", "--no-ff", "-m", "merge right", "right")
				gittest.Git(t, g.Dir, "branch", "merged")
			},
			want: "merged",
		},
		{
			name: "no trunk, detached at a sole bookmark tip returns it",
			setup: func(t *testing.T, g gitcmd.Git) {
				gittest.Git(t, g.Dir, "checkout", "-q", "-b", "wip")
				commitEmpty(t, g, "c1")
				gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "HEAD")
			},
			want: "wip",
		},
		{
			name: "no trunk, detached past the sole bookmark errors",
			setup: func(t *testing.T, g gitcmd.Git) {
				gittest.Git(t, g.Dir, "checkout", "-q", "-b", "wip")
				commitEmpty(t, g, "c1")
				gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "HEAD")
				commitEmpty(t, g, "c2")
			},
			wantErr: gitcmd.ErrDetachedHead,
		},
		{
			name: "bookmark and same-named tag resolve to the branch, not heads/feat",
			setup: func(t *testing.T, g gitcmd.Git) {
				commitEmpty(t, g, "c1")
				gittest.Git(t, g.Dir, "checkout", "-q", "-b", "feat")
				commitEmpty(t, g, "c2")
				gittest.Git(t, g.Dir, "tag", "feat", "main")
				gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "HEAD")
				commitEmpty(t, g, "c3")
			},
			want: "feat",
		},
		{
			name: "no trunk, sole bookmark with a non-breaking space survives verbatim",
			setup: func(t *testing.T, g gitcmd.Git) {
				gittest.Git(t, g.Dir, "checkout", "-q", "-b", "feat\u00a0")
				commitEmpty(t, g, "c1")
				gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "HEAD")
			},
			want: "feat\u00a0",
		},
		{
			name: "remote-only trunk, detached past a local branch returns the branch",
			setup: func(t *testing.T, g gitcmd.Git) {
				sha := commitEmpty(t, g, "c1")
				gittest.Git(t, g.Dir, "update-ref", "refs/remotes/origin/release", string(sha))
				gittest.Git(t, g.Dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/release")
				gittest.Git(t, g.Dir, "checkout", "-q", "-b", "feat")
				commitEmpty(t, g, "c2")
				gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "HEAD")
				commitEmpty(t, g, "c3")
			},
			want: "feat",
		},
		{
			name: "remote-only trunk, no candidates returns the remote default",
			setup: func(t *testing.T, g gitcmd.Git) {
				sha := commitEmpty(t, g, "c1")
				gittest.Git(t, g.Dir, "update-ref", "refs/remotes/origin/release", string(sha))
				gittest.Git(t, g.Dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/release")
				gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "HEAD")
			},
			want: "release",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := initRepo(t)
			tc.setup(t, g)
			got, err := g.CurrentBranch(t.Context())
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("CurrentBranch() err = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("CurrentBranch() unexpected err = %v", err)
			}
			if got != tc.want {
				t.Fatalf("CurrentBranch() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCurrentBranchAncestryWorkBound(t *testing.T) {
	for _, count := range []int{0, 1, 4, 20} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			g := initRepo(t)
			commitEmpty(t, g, "trunk")
			gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "HEAD")
			want := model.Branch("main")
			for i := range count {
				name := fmt.Sprintf("bookmark-%02d", i)
				commitEmpty(t, g, name)
				gittest.Git(t, g.Dir, "branch", name)
				want = model.Branch(name)
			}
			calls := filepath.Join(t.TempDir(), "git-calls")
			t.Setenv("GIT_NO_LAZY_FETCH", "0")
			stubGit(t, fmt.Sprintf(`printf '%%s|%%s\n' "$GIT_NO_LAZY_FETCH" "$*" >> %q`, calls))
			got, err := g.CurrentBranch(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("CurrentBranch() = %q, want %q", got, want)
			}
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			var independent, pointsAt, merged int
			for _, line := range strings.Split(string(data), "\n") {
				switch {
				case strings.Contains(line, "merge-base --is-ancestor"):
					t.Fatalf("pairwise ancestry query: %s", line)
				case strings.Contains(line, "merge-base --independent"):
					independent++
					if !strings.HasPrefix(line, "1|") {
						t.Fatalf("ancestry query permits lazy fetch: %s", line)
					}
					for i := range count {
						if !slices.Contains(strings.Fields(line), fmt.Sprintf("refs/heads/bookmark-%02d", i)) {
							t.Fatalf("ancestry query omitted qualified bookmark %d: %s", i, line)
						}
					}
				case strings.Contains(line, "for-each-ref --points-at"):
					pointsAt++
				case strings.Contains(line, "for-each-ref --merged"):
					merged++
				}
			}
			wantQueries := 0
			if count > 1 {
				wantQueries = 1
			}
			if independent != wantQueries || pointsAt != wantQueries || merged != 2 {
				t.Fatalf("Git queries independent=%d points-at=%d merged=%d, want %d %d 2", independent, pointsAt, merged, wantQueries, wantQueries)
			}
		})
	}
}

func TestCurrentBranchAncestryErrors(t *testing.T) {
	for _, tc := range []struct {
		name       string
		pattern    string
		interleave []string
		want       string
	}{
		{
			name:       "candidate ref disappears before reduction",
			pattern:    `*"merge-base --independent"*`,
			interleave: []string{"update-ref", "-d", "refs/heads/top"},
			want:       "independent bookmarks:",
		},
		{
			name:       "reduction fails",
			pattern:    `*"merge-base --independent"*`,
			interleave: []string{"config", "core.repositoryformatversion", "999"},
			want:       "independent bookmarks:",
		},
		{
			name:       "points-at lookup fails",
			pattern:    `*"for-each-ref --points-at"*`,
			interleave: []string{"config", "core.repositoryformatversion", "999"},
			want:       "branches at ",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := initRepo(t)
			commitEmpty(t, g, "trunk")
			gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "HEAD")
			commitEmpty(t, g, "lower")
			gittest.Git(t, g.Dir, "branch", "lower")
			commitEmpty(t, g, "top")
			gittest.Git(t, g.Dir, "branch", "top")
			gittest.GitShim{
				Pattern:    tc.pattern,
				Interleave: [][]string{append([]string{"-C", g.Dir}, tc.interleave...)},
			}.Install(t)
			branch, err := g.CurrentBranch(t.Context())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("CurrentBranch() = %q, %v, want error containing %q", branch, err, tc.want)
			}
			if branch != "" {
				t.Fatalf("CurrentBranch() = %q after Git failure, want empty branch", branch)
			}
		})
	}
}

func TestCurrentBranchCancelled(t *testing.T) {
	g := initRepo(t)
	commitEmpty(t, g, "trunk")
	gittest.Git(t, g.Dir, "checkout", "-q", "--detach", "HEAD")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	branch, err := g.CurrentBranch(ctx)
	if branch != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("CurrentBranch() = %q, %v, want empty branch and context.Canceled", branch, err)
	}
}

func TestRefTips(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()

	c1 := commitAt(t, g, "c1", 1700000000)
	gittest.Git(t, g.Dir, "checkout", "-q", "-b", "feat")
	c2 := commitAt(t, g, "c2", 1700000100)
	gittest.Git(t, g.Dir, "update-ref", "refs/remotes/origin/release", string(c1))

	got, err := g.RefTips(ctx, "refs/heads/", "refs/remotes/origin/")
	if err != nil {
		t.Fatalf("RefTips: %v", err)
	}
	want := []gitcmd.RefTip{
		{Ref: "refs/heads/feat", Tip: c2, Time: 1700000100},
		{Ref: "refs/heads/main", Tip: c1, Time: 1700000000},
		{Ref: "refs/remotes/origin/release", Tip: c1, Time: 1700000000},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("RefTips() = %+v, want %+v", got, want)
	}

	got, err = g.RefTips(ctx, "refs/does-not-exist/")
	if err != nil {
		t.Fatalf("RefTips no match: %v", err)
	}
	if got != nil {
		t.Fatalf("RefTips no match = %+v, want nil", got)
	}
}

func TestRefs(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()

	c1 := commitAt(t, g, "c1", 1700000000)
	gittest.Git(t, g.Dir, "checkout", "-q", "-b", "feat")
	c2 := commitAt(t, g, "c2", 1700000100)
	gittest.Git(t, g.Dir, "update-ref", "refs/remotes/origin/release", string(c1))
	gittest.Git(t, g.Dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/release")

	got, err := g.Refs(ctx, "refs/heads/", "refs/remotes/origin/")
	if err != nil {
		t.Fatalf("Refs: %v", err)
	}
	want := map[string]model.SHA{
		"refs/heads/feat":             c2,
		"refs/heads/main":             c1,
		"refs/remotes/origin/release": c1,
	}
	if !maps.Equal(got, want) {
		t.Fatalf("Refs() = %+v, want %+v", got, want)
	}

	got, err = g.Refs(ctx, "refs/does-not-exist/")
	if err != nil {
		t.Fatalf("Refs no match: %v", err)
	}
	if got != nil {
		t.Fatalf("Refs no match = %+v, want nil", got)
	}
}

func TestFirstRef(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()
	c1 := commitEmpty(t, g, "c1")

	cases := []struct {
		name     string
		refs     []string
		prefixes []string
		want     string
	}{
		{name: "none", prefixes: []string{"refs/cc-notes/"}},
		{name: "one", refs: []string{"refs/cc-notes/notes/b"}, prefixes: []string{"refs/cc-notes/"}, want: "refs/cc-notes/notes/b"},
		{name: "many in refname order", refs: []string{"refs/cc-notes/tasks/a", "refs/cc-notes/notes/c", "refs/cc-notes/notes/a"}, prefixes: []string{"refs/cc-notes/"}, want: "refs/cc-notes/notes/a"},
		{name: "second prefix only", refs: []string{"refs/cc-notes-sync/origin/notes/a"}, prefixes: []string{"refs/cc-notes/", "refs/cc-notes-sync/"}, want: "refs/cc-notes-sync/origin/notes/a"},
		{name: "prefix is not a substring match", refs: []string{"refs/cc-notes-sync/origin/notes/a"}, prefixes: []string{"refs/cc-notes/"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, ref := range tc.refs {
				gittest.Git(t, g.Dir, "update-ref", ref, string(c1))
			}
			t.Cleanup(func() {
				for _, ref := range tc.refs {
					gittest.Git(t, g.Dir, "update-ref", "-d", ref)
				}
			})
			got, err := g.FirstRef(ctx, tc.prefixes...)
			if err != nil {
				t.Fatalf("FirstRef: %v", err)
			}
			if got != tc.want {
				t.Fatalf("FirstRef(%v) = %q, want %q", tc.prefixes, got, tc.want)
			}
		})
	}
}

func TestAncestorSet(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()
	c1 := commitAt(t, g, "c1", 1700000000)
	c2 := commitAt(t, g, "c2", 1700000100)
	gittest.Git(t, g.Dir, "checkout", "-q", "-b", "side", string(c1))
	c3 := commitAt(t, g, "c3", 1700000200)

	got, err := g.AncestorSet(ctx, c2)
	if err != nil {
		t.Fatalf("AncestorSet: %v", err)
	}
	want := map[model.SHA]struct{}{c1: {}, c2: {}}
	if !maps.Equal(got, want) {
		t.Fatalf("AncestorSet() = %+v, want %+v; side commit %s must be absent", got, want, c3)
	}
}

func TestMergedRefs(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()

	commitAt(t, g, "c1", 1700000000)
	gittest.Git(t, g.Dir, "checkout", "-q", "-b", "merged-branch")
	commitAt(t, g, "c2", 1700000100)
	gittest.Git(t, g.Dir, "checkout", "-q", "main")
	gittest.Git(t, g.Dir, "merge", "-q", "--no-ff", "-m", "merge merged-branch", "merged-branch")
	trunkTip := resolve(t, g.Dir, "HEAD")
	gittest.Git(t, g.Dir, "checkout", "-q", "-b", "equal-branch", string(trunkTip))
	gittest.Git(t, g.Dir, "checkout", "-q", "-b", "unmerged-branch", "merged-branch")
	commitAt(t, g, "c3", 1700000200)
	gittest.Git(t, g.Dir, "checkout", "-q", "main")

	got, err := g.MergedRefs(ctx, trunkTip, "refs/heads/")
	if err != nil {
		t.Fatalf("MergedRefs: %v", err)
	}
	want := []string{"refs/heads/equal-branch", "refs/heads/main", "refs/heads/merged-branch"}
	if !slices.Equal(got, want) {
		t.Fatalf("MergedRefs() = %v, want %v", got, want)
	}

	got, err = g.MergedRefs(ctx, trunkTip, "refs/does-not-exist/")
	if err != nil {
		t.Fatalf("MergedRefs no match: %v", err)
	}
	if got != nil {
		t.Fatalf("MergedRefs no match = %v, want nil", got)
	}
}
