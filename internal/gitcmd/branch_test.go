package gitcmd_test

import (
	"errors"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

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
