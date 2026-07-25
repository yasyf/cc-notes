package gitcmd_test

import (
	"errors"
	"fmt"
	"slices"
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
