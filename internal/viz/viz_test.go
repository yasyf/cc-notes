// White-box tests: living in package viz lets the topology tests inspect the
// unexported trunk sentinel and lane status constants, and lets sibling lanes
// reuse these git fixtures. Every fixture runs a real git repository in
// t.TempDir() over the real object database.
package viz

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

const (
	fxName  = "Test User"
	fxEmail = "test@example.com"
	// fxBase is the committer time of the first commit; each later commit adds
	// fxStep, so every fixture commit has a fixed, known time.
	fxBase = int64(1767225600) // 2026-01-01T00:00:00Z
	fxStep = int64(60)
)

// commitInfo is one fixture commit's sha and committer time in unix seconds.
type commitInfo struct {
	sha  model.SHA
	time int64
}

// gitRepo is a real git repository under a temp dir with a deterministic
// committer clock, driving fixtures for the topology tests.
type gitRepo struct {
	t     *testing.T
	dir   string
	clock int64
}

// newGitRepo initializes a repository on the main branch.
func newGitRepo(t *testing.T) *gitRepo { return newGitRepoOn(t, "main") }

// newRecentGitRepo initializes a repository whose fixture clock starts an hour
// ago instead of at fxBase, so its branches fall inside the default history
// window. Tests reaching an endpoint that is fixed at the default window need
// it, since fxBase long predates that window and its lanes would be filtered
// out; tests that assert exact commit times keep newGitRepo's fixed clock.
func newRecentGitRepo(t *testing.T) *gitRepo {
	t.Helper()
	r := newGitRepoOn(t, "main")
	r.clock = time.Now().Unix() - int64(time.Hour.Seconds())
	return r
}

// newGitRepoOn initializes a repository on the named initial branch.
func newGitRepoOn(t *testing.T, branch string) *gitRepo {
	t.Helper()
	gittest.ScrubEnv(t)
	r := &gitRepo{t: t, dir: t.TempDir(), clock: fxBase}
	r.git("init", "-q", "-b", branch)
	r.git("config", "user.name", fxName)
	r.git("config", "user.email", fxEmail)
	return r
}

// git runs a git plumbing command with no date override and returns stdout.
func (r *gitRepo) git(args ...string) string { return r.gitAt(0, args...) }

// gitAt runs a git command, pinning the author and committer date to when (unix
// seconds) when when > 0 so commit and merge times are deterministic.
func (r *gitRepo) gitAt(when int64, args ...string) string {
	r.t.Helper()
	full := append([]string{"--no-pager", "-C", r.dir}, args...)
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	cmd := exec.Command("git", full...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if when > 0 {
		date := fmt.Sprintf("@%d +0000", when)
		cmd.Env = append(cmd.Env, "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String())
}

// at repositions the fixture clock so the next commit lands exactly at when
// (unix seconds), letting a fixture straddle the default history window.
func (r *gitRepo) at(when int64) { r.clock = when - fxStep }

// commit writes a unique file and commits it with message on the current
// branch, advancing the clock.
func (r *gitRepo) commit(message string) commitInfo { return r.commitMsg(message) }

// commitMsg is commit with one or more -m message paragraphs, so a trailer
// block (a later paragraph of key: value lines) can be attached.
func (r *gitRepo) commitMsg(messages ...string) commitInfo {
	r.t.Helper()
	r.clock += fxStep
	name := fmt.Sprintf("f-%d.txt", r.clock)
	if err := os.WriteFile(filepath.Join(r.dir, name), []byte(messages[0]+"\n"), 0o600); err != nil {
		r.t.Fatalf("write %s: %v", name, err)
	}
	r.git("add", name)
	args := make([]string, 0, 2+2*len(messages))
	args = append(args, "commit", "-q")
	for _, m := range messages {
		args = append(args, "-m", m)
	}
	r.gitAt(r.clock, args...)
	return commitInfo{sha: r.head(), time: r.clock}
}

// commitChain fast-imports n empty commits onto main in one subprocess,
// advancing the clock per commit; the last commit's message paragraphs are
// tipMessages, so a trailer block can ride along. The repository must have no
// commits yet. Returns the tip commit.
func (r *gitRepo) commitChain(n int, tipMessages ...string) commitInfo {
	r.t.Helper()
	var stream bytes.Buffer
	for i := 1; i <= n; i++ {
		r.clock += fxStep
		msg := fmt.Sprintf("c%d\n", i)
		if i == n {
			msg = strings.Join(tipMessages, "\n\n") + "\n"
		}
		fmt.Fprintf(&stream, "commit refs/heads/main\ncommitter %s <%s> %d +0000\ndata %d\n%s\n", fxName, fxEmail, r.clock, len(msg), msg)
	}
	//nolint:gosec // G204: test helper shells out to git with fixed args.
	cmd := exec.Command("git", "-C", r.dir, "fast-import", "--quiet")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	cmd.Stdin = &stream
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		r.t.Fatalf("git fast-import: %v\n%s", err, stderr.String())
	}
	return commitInfo{sha: model.SHA(r.git("rev-parse", "main")), time: r.clock}
}

// mergeNoFF merges branch into the current branch with a merge commit at time
// when, returning the merge commit.
func (r *gitRepo) mergeNoFF(when int64, branch, message string) commitInfo {
	r.t.Helper()
	r.gitAt(when, "merge", "--no-ff", "-m", message, branch)
	return commitInfo{sha: r.head(), time: when}
}

// mergeOctopus merges two or more branches into the current branch in a single
// octopus merge commit at time when, returning the merge commit. Because each
// branch touches a distinct file, the current tip stays the first parent and the
// merged branches follow in the order given.
func (r *gitRepo) mergeOctopus(when int64, message string, branches ...string) commitInfo {
	r.t.Helper()
	r.gitAt(when, append([]string{"merge", "--no-ff", "-m", message}, branches...)...)
	return commitInfo{sha: r.head(), time: when}
}

// head returns the current HEAD commit sha.
func (r *gitRepo) head() model.SHA { return model.SHA(r.git("rev-parse", "HEAD")) }

// openStore opens a cc-notes store on the repository.
func (r *gitRepo) openStore() *store.Store {
	r.t.Helper()
	s, err := store.Open(r.dir)
	if err != nil {
		r.t.Fatalf("open store: %v", err)
	}
	return s
}

// doneTask creates a task on branch and marks it done, returning its id, so a
// squash-merge trailer can name a completed task folded onto a branch.
func (r *gitRepo) doneTask(s *store.Store, title string, branch model.Branch) model.EntityID {
	r.t.Helper()
	ctx := r.t.Context()
	snap, err := s.Create(ctx, []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: title, Type: model.TypeTask, Branch: branch}})
	if err != nil {
		r.t.Fatalf("create task: %v", err)
	}
	task := snap.(model.Task)
	if _, err := s.Append(ctx, refs.For(model.KindTask, task.ID), []model.Op{model.SetStatus{Status: model.StatusDone}}); err != nil {
		r.t.Fatalf("set task done: %v", err)
	}
	return task.ID
}

// laneByName returns the lane with the given name, failing if absent.
func laneByName(t *testing.T, g *Graph, name string) Lane {
	t.Helper()
	for _, l := range g.Lanes {
		if l.Name == name {
			return l
		}
	}
	var names []string
	for _, l := range g.Lanes {
		names = append(names, l.Name)
	}
	t.Fatalf("lane %q not found; lanes: %v", name, names)
	return Lane{}
}
