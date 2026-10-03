package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func TestRetainedReopensOnlyWhenItsFenceMoves(t *testing.T) {
	thin := func(f *sharedFixture) string { return f.thin }
	for _, tc := range []struct {
		name    string
		dir     func(f *sharedFixture) string
		move    func(t *testing.T, f *sharedFixture)
		reopens bool
	}{
		{"nothing moved", thin, func(*testing.T, *sharedFixture) {}, false},
		{"a working-tree write", thin, func(t *testing.T, f *sharedFixture) {
			if err := os.WriteFile(filepath.Join(f.thin, "scratch.txt"), []byte("x"), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
		}, false},
		{"an unrelated ref write", thin, func(t *testing.T, f *sharedFixture) {
			gittest.Git(t, f.thin, "update-ref", "refs/heads/unrelated", "HEAD")
		}, false},
		{"a commit moves HEAD's branch", thin, func(t *testing.T, f *sharedFixture) {
			gittest.Git(t, f.thin, "commit", "-q", "--allow-empty", "-m", "next")
		}, true},
		{"a checkout moves HEAD", thin, func(t *testing.T, f *sharedFixture) {
			gittest.Git(t, f.thin, "checkout", "-q", "-b", "other")
		}, true},
		{"a config edit", thin, func(t *testing.T, f *sharedFixture) {
			gittest.Git(t, f.thin, "config", "cc-notes.localLabel", "mine")
		}, true},
		{"a binding", thin, func(t *testing.T, f *sharedFixture) { f.bind(t) }, true},
		{"a worktree recreated at its path", func(f *sharedFixture) string { return f.worktreeA }, func(t *testing.T, f *sharedFixture) {
			gittest.Git(t, f.thin, "worktree", "remove", "--force", f.worktreeA)
			gittest.Git(t, f.thin, "worktree", "add", "-q", f.worktreeA, "wt-a")
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSharedFixture(t)
			r, err := Retain(t.Context(), tc.dir(f))
			if err != nil {
				t.Fatalf("Retain: %v", err)
			}
			first, err := r.Store(t.Context())
			if err != nil {
				t.Fatalf("Store: %v", err)
			}
			tc.move(t, f)
			second, err := r.Store(t.Context())
			if err != nil {
				t.Fatalf("Store after the move: %v", err)
			}
			if reopened := second.Repo != first.Repo; reopened != tc.reopens {
				t.Fatalf("reopened = %t, want %t", reopened, tc.reopens)
			}
			if second.root == first.root || second.policy == first.policy {
				t.Fatal("two operations share a Root or LocalPolicy memo")
			}
		})
	}
}

func TestRetainedRefusesABackendRedirectedAfterRetain(t *testing.T) {
	f := newSharedFixture(t)
	f.bind(t)
	r, err := Retain(t.Context(), f.thin)
	if err != nil {
		t.Fatalf("Retain: %v", err)
	}
	if _, err := r.Store(t.Context()); err != nil {
		t.Fatalf("Store: %v", err)
	}
	redirect(t, filepath.Join(f.sourceCommon, "refs"))
	if _, err := r.Store(t.Context()); !errors.Is(err, ErrBackendRedirects) {
		t.Fatalf("Store after the backend's refs became a symlink = %v, want ErrBackendRedirects", err)
	}
}

func TestRetainRefusesABackendRedirectRacingItsCapture(t *testing.T) {
	f := newSharedFixture(t)
	f.bind(t)
	redirectRefs := func() { redirect(t, filepath.Join(f.sourceCommon, "refs")) }
	if _, err := retain(t.Context(), f.thin, afterFirstOpen(redirectRefs)); !errors.Is(err, ErrBackendRedirects) {
		t.Fatalf("Retain with the backend's refs symlinked between validation and capture = %v, want ErrBackendRedirects", err)
	}
	if _, err := Retain(t.Context(), f.thin); !errors.Is(err, ErrBackendRedirects) {
		t.Fatalf("Retain after the redirect = %v, want ErrBackendRedirects", err)
	}
}

func TestRetainedRefusesABackendRedirectRacingAReopen(t *testing.T) {
	f := newSharedFixture(t)
	f.bind(t)
	r, err := Retain(t.Context(), f.thin)
	if err != nil {
		t.Fatalf("Retain: %v", err)
	}
	r.open = afterFirstOpen(func() { redirect(t, filepath.Join(f.sourceCommon, "refs")) })
	gittest.Git(t, f.thin, "commit", "-q", "--allow-empty", "-m", "move HEAD")
	for i := range 2 {
		if _, err := r.Store(t.Context()); !errors.Is(err, ErrBackendRedirects) {
			t.Fatalf("Store #%d with the backend's refs symlinked between validation and capture = %v, want ErrBackendRedirects", i+1, err)
		}
	}
}

func afterFirstOpen(mutate func()) func(context.Context, string) (*Store, error) {
	mutated := false
	return func(ctx context.Context, dir string) (*Store, error) {
		s, err := OpenContext(ctx, dir)
		if err == nil && !mutated {
			mutated = true
			mutate()
		}
		return s, err
	}
}

func redirect(t *testing.T, entry string) {
	t.Helper()
	if err := os.Rename(entry, entry+".real"); err != nil {
		t.Fatalf("move %s: %v", entry, err)
	}
	if err := os.Symlink(entry+".real", entry); err != nil {
		t.Fatalf("symlink %s: %v", entry, err)
	}
}

func TestRetainedFollowsTheCheckoutGitFindsAtItsPath(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replace func(t *testing.T, f *sharedFixture)
	}{
		{"moved, with another worktree of its repository created at its path", func(t *testing.T, f *sharedFixture) {
			gittest.Git(t, f.thin, "worktree", "move", f.worktreeA, filepath.Join(t.TempDir(), "moved"))
			gittest.Git(t, f.thin, "worktree", "add", "-q", "-b", "replacement", f.worktreeA)
		}},
		{"moved, with a worktree of another repository created at its path", func(t *testing.T, f *sharedFixture) {
			gittest.Git(t, f.thin, "worktree", "move", f.worktreeA, filepath.Join(t.TempDir(), "moved"))
			gittest.Git(t, f.source, "worktree", "add", "-q", "-b", "replacement", f.worktreeA)
		}},
		{".git repointed at another worktree of its repository", func(t *testing.T, f *sharedFixture) {
			admin, _ := gittest.Dirs(t, f.worktreeB)
			repoint(t, filepath.Join(f.worktreeA, ".git"), "gitdir: "+admin)
		}},
		{".git repointed at a worktree of another repository", func(t *testing.T, f *sharedFixture) {
			admin, _ := gittest.Dirs(t, gittest.AddWorktree(t, f.source, "elsewhere"))
			repoint(t, filepath.Join(f.worktreeA, ".git"), "gitdir: "+admin)
		}},
		{"commondir repointed at another repository", func(t *testing.T, f *sharedFixture) {
			admin, _ := gittest.Dirs(t, f.worktreeA)
			gittest.Git(t, f.source, "branch", "wt-a")
			repoint(t, filepath.Join(admin, "commondir"), f.sourceCommon)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newSharedFixture(t)
			commitAnchor(t, f.worktreeA)
			r, err := Retain(t.Context(), f.worktreeA)
			if err != nil {
				t.Fatalf("Retain: %v", err)
			}
			first, err := r.Store(t.Context())
			if err != nil {
				t.Fatalf("Store: %v", err)
			}
			tc.replace(t, f)
			second, err := r.Store(t.Context())
			if err != nil {
				t.Fatalf("Store after the replacement: %v", err)
			}
			if second.Repo == first.Repo {
				t.Fatalf("kept the store opened at %s for the checkout git now finds there", first.GitDir())
			}
			assertMatchesFreshOpen(t, f.worktreeA, second)
		})
	}
}

func TestRetainedRejectsADiscoveryThatMovesDuringItsOpen(t *testing.T) {
	for _, tc := range []struct {
		name  string
		moves bool
		race  func(t *testing.T, f *sharedFixture) func()
	}{
		{"commondir repointed at another repository", true, func(t *testing.T, f *sharedFixture) func() {
			admin, _ := gittest.Dirs(t, f.worktreeA)
			gittest.Git(t, f.source, "branch", "wt-a")
			return func() { repoint(t, filepath.Join(admin, "commondir"), f.sourceCommon) }
		}},
		{"commondir rewritten to its own repository by another path", false, func(t *testing.T, f *sharedFixture) func() {
			admin, _ := gittest.Dirs(t, f.worktreeA)
			return func() { repoint(t, filepath.Join(admin, "commondir"), f.thinCommon) }
		}},
		{".git repointed at another worktree of its repository", true, func(t *testing.T, f *sharedFixture) func() {
			admin, _ := gittest.Dirs(t, f.worktreeB)
			return func() { repoint(t, filepath.Join(f.worktreeA, ".git"), "gitdir: "+admin) }
		}},
		{".git repointed at a worktree of another repository", true, func(t *testing.T, f *sharedFixture) func() {
			admin, _ := gittest.Dirs(t, gittest.AddWorktree(t, f.source, "elsewhere"))
			return func() { repoint(t, filepath.Join(f.worktreeA, ".git"), "gitdir: "+admin) }
		}},
		{".git rewritten to its own admin directory by a relative path", false, func(t *testing.T, f *sharedFixture) func() {
			admin, _ := gittest.Dirs(t, f.worktreeA)
			rel, err := filepath.Rel(canonical(t, f.worktreeA), canonical(t, admin))
			if err != nil {
				t.Fatalf("relative admin path: %v", err)
			}
			return func() { repoint(t, filepath.Join(f.worktreeA, ".git"), "gitdir: "+rel) }
		}},
	} {
		t.Run(tc.name+", during retention", func(t *testing.T) {
			f := newSharedFixture(t)
			commitAnchor(t, f.worktreeA)
			r, err := retain(t.Context(), f.worktreeA, afterFirstOpen(tc.race(t, f)))
			if tc.moves {
				if !errors.Is(err, errDiscoveryMoved) {
					t.Fatalf("Retain racing the change = %v, want errDiscoveryMoved", err)
				}
				r, err = Retain(t.Context(), f.worktreeA)
			}
			if err != nil {
				t.Fatalf("Retain: %v", err)
			}
			for i := range 2 {
				s, err := r.Store(t.Context())
				if err != nil {
					t.Fatalf("Store #%d: %v", i+1, err)
				}
				assertMatchesFreshOpen(t, f.worktreeA, s)
			}
		})
		t.Run(tc.name+", during a reopen", func(t *testing.T) {
			f := newSharedFixture(t)
			commitAnchor(t, f.worktreeA)
			mutate := tc.race(t, f)
			r, err := Retain(t.Context(), f.worktreeA)
			if err != nil {
				t.Fatalf("Retain: %v", err)
			}
			r.open = afterFirstOpen(mutate)
			gittest.Git(t, f.worktreeA, "commit", "-q", "--allow-empty", "-m", "move HEAD")
			first, err := r.Store(t.Context())
			switch {
			case tc.moves && !errors.Is(err, errDiscoveryMoved):
				t.Fatalf("Store racing the change = %v, want errDiscoveryMoved", err)
			case !tc.moves && err != nil:
				t.Fatalf("Store racing the change: %v", err)
			case !tc.moves:
				assertMatchesFreshOpen(t, f.worktreeA, first)
			}
			second, err := r.Store(t.Context())
			if err != nil {
				t.Fatalf("Store after the race: %v", err)
			}
			assertMatchesFreshOpen(t, f.worktreeA, second)
		})
	}
}

func commitAnchor(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "anchor.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatalf("write anchor: %v", err)
	}
	gittest.Git(t, dir, "add", "anchor.txt")
	gittest.Git(t, dir, "commit", "-q", "-m", "anchor")
}

func repoint(t *testing.T, file, target string) {
	t.Helper()
	if err := os.WriteFile(file, []byte(target+"\n"), 0o600); err != nil {
		t.Fatalf("rewrite %s: %v", file, err)
	}
}

func assertMatchesFreshOpen(t *testing.T, dir string, retained *Store) {
	t.Helper()
	ctx := t.Context()
	fresh, err := OpenContext(ctx, dir)
	if err != nil {
		t.Fatalf("OpenContext(%s): %v", dir, err)
	}
	head, err := fresh.ContextRepo.Tip(ctx, "HEAD")
	if err != nil {
		t.Fatalf("fresh HEAD: %v", err)
	}
	if got, err := retained.ContextRepo.Tip(ctx, "HEAD"); err != nil || got != head {
		t.Fatalf("retained HEAD = %s, %v; a fresh open reads %s", got, err, head)
	}
	if got, want := headRef(t, retained.GitDir()), headRef(t, fresh.GitDir()); got != want {
		t.Fatalf("retained HEAD names %q; a fresh open reads %q", got, want)
	}
	branch, err := retained.Git.HeadBranch(ctx)
	if err != nil {
		t.Fatalf("retained branch: %v", err)
	}
	if got, want := headRef(t, retained.GitDir()), "ref: refs/heads/"+string(branch); got != want {
		t.Fatalf("retained HEAD names %q while its git runner is on %s", got, branch)
	}
	if !sameDir(t, retained.RecordsCommonDir(), fresh.RecordsCommonDir()) {
		t.Fatalf("retained records go to %s; a fresh open writes %s", retained.RecordsCommonDir(), fresh.RecordsCommonDir())
	}
	want, wantErr := fresh.ContextRepo.PathOID(ctx, head, "anchor.txt")
	got, gotErr := retained.ContextRepo.PathOID(ctx, head, "anchor.txt")
	if got != want || errors.Is(gotErr, model.ErrPathNotFound) != errors.Is(wantErr, model.ErrPathNotFound) {
		t.Fatalf("retained anchor.txt at HEAD = %s, %v; a fresh open reads %s, %v", got, gotErr, want, wantErr)
	}
}

func headRef(t *testing.T, gitDir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		t.Fatalf("read HEAD: %v", err)
	}
	return strings.TrimSpace(string(data))
}

func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ai, err := os.Stat(a)
	if err != nil {
		t.Fatalf("stat %s: %v", a, err)
	}
	bi, err := os.Stat(b)
	if err != nil {
		t.Fatalf("stat %s: %v", b, err)
	}
	return os.SameFile(ai, bi)
}
