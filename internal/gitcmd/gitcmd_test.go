package gitcmd_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/model"
)

// scrubGitEnv clears every git environment knob that could leak host state
// into a test and pins global/system config to /dev/null. t.Setenv with the
// original value registers the restore before os.Unsetenv removes the key.
func scrubGitEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_NAMESPACE", "GIT_CEILING_DIRECTORIES",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_AUTHOR_DATE",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "GIT_COMMITTER_DATE",
		"GIT_EDITOR", "EMAIL", "GIT_ASKPASS", "SSH_ASKPASS",
	} {
		if value, ok := os.LookupEnv(key); ok {
			t.Setenv(key, value)
			_ = os.Unsetenv(key)
		}
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_SYSTEM", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func initRepoNoIdentity(t *testing.T) gitcmd.Git {
	t.Helper()
	scrubGitEnv(t)
	g := gitcmd.Git{Dir: t.TempDir()}
	mustGit(t, g.Dir, "init", "-q", "-b", "main")
	return g
}

func initRepo(t *testing.T) gitcmd.Git {
	t.Helper()
	g := initRepoNoIdentity(t)
	mustGit(t, g.Dir, "config", "user.name", "Test User")
	mustGit(t, g.Dir, "config", "user.email", "test@example.com")
	return g
}

func initBare(t *testing.T) string {
	t.Helper()
	scrubGitEnv(t)
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q", "--bare")
	return dir
}

func commitEmpty(t *testing.T, g gitcmd.Git, msg string) model.SHA {
	t.Helper()
	mustGit(t, g.Dir, "commit", "-q", "--allow-empty", "-m", msg)
	return model.SHA(mustGit(t, g.Dir, "rev-parse", "HEAD"))
}

func resolve(t *testing.T, dir, ref string) model.SHA {
	t.Helper()
	return model.SHA(mustGit(t, dir, "rev-parse", "--verify", ref))
}

func TestUpdateRefCreateAndCAS(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()
	c1 := commitEmpty(t, g, "c1")
	c2 := commitEmpty(t, g, "c2")
	ref := "refs/cc-notes/notes/" + string(c1)

	if err := g.UpdateRef(ctx, ref, c1, ""); err != nil {
		t.Fatalf("create: %v", err)
	}
	if got := resolve(t, g.Dir, ref); got != c1 {
		t.Fatalf("after create: ref at %s, want %s", got, c1)
	}

	if err := g.UpdateRef(ctx, ref, c2, ""); !errors.Is(err, gitcmd.ErrCASMismatch) {
		t.Fatalf("create when exists: got %v, want ErrCASMismatch", err)
	}
	if got := resolve(t, g.Dir, ref); got != c1 {
		t.Fatalf("failed create moved ref to %s, want %s", got, c1)
	}

	if err := g.UpdateRef(ctx, ref, c2, c1); err != nil {
		t.Fatalf("cas update: %v", err)
	}
	if got := resolve(t, g.Dir, ref); got != c2 {
		t.Fatalf("after cas update: ref at %s, want %s", got, c2)
	}

	if err := g.UpdateRef(ctx, ref, c1, c1); !errors.Is(err, gitcmd.ErrCASMismatch) {
		t.Fatalf("stale old: got %v, want ErrCASMismatch", err)
	}
	if got := resolve(t, g.Dir, ref); got != c2 {
		t.Fatalf("failed cas moved ref to %s, want %s", got, c2)
	}

	missing := "refs/cc-notes/notes/" + string(c2)
	if err := g.UpdateRef(ctx, missing, c2, c1); !errors.Is(err, gitcmd.ErrCASMismatch) {
		t.Fatalf("expected-old on missing ref: got %v, want ErrCASMismatch", err)
	}
}

func TestUpdateRefBranchPaths(t *testing.T) {
	for _, tc := range []struct {
		name   string
		branch string
	}{
		{name: "slashed branch", branch: "feature/sub/x"},
		{name: "exotic but valid branch", branch: "feat{x}/y"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := initRepo(t)
			ctx := t.Context()
			c1 := commitEmpty(t, g, "c1")
			ref := "refs/cc-notes/tasks/" + tc.branch + "/" + string(c1)
			if err := g.UpdateRef(ctx, ref, c1, ""); err != nil {
				t.Fatalf("create %q: %v", ref, err)
			}
			if got := resolve(t, g.Dir, ref); got != c1 {
				t.Fatalf("ref %q at %s, want %s", ref, got, c1)
			}
		})
	}
}

func TestUpdateRefEmptyNew(t *testing.T) {
	g := initRepo(t)
	err := g.UpdateRef(t.Context(), "refs/cc-notes/x", "", "")
	if err == nil || errors.Is(err, gitcmd.ErrCASMismatch) {
		t.Fatalf("empty new: got %v, want plain error", err)
	}
}

func TestCheckRefFormat(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()
	for _, branch := range []string{"main", "feature/sub/x", "feat{x}/y", "v1.0.0"} {
		if err := g.CheckRefFormat(ctx, branch); err != nil {
			t.Errorf("CheckRefFormat(%q) = %v, want nil", branch, err)
		}
	}
	// "@" is absent: check-ref-format --branch reads it as the HEAD
	// shorthand and accepts it; the model's refNameValid still rejects it.
	for _, branch := range []string{"../evil", "feat ure", ".hidden", "a//b", "feature/", "x.lock", "a..b", "ref~1", "HEAD^"} {
		if err := g.CheckRefFormat(ctx, branch); err == nil {
			t.Errorf("CheckRefFormat(%q) = nil, want error", branch)
		}
	}
}

func TestUpdateRefConcurrentRace(t *testing.T) {
	g := initRepo(t)
	mustGit(t, g.Dir, "config", "core.filesRefLockTimeout", "3000")
	ctx := t.Context()
	base := commitEmpty(t, g, "base")
	ref := "refs/cc-notes/notes/" + string(base)
	if err := g.UpdateRef(ctx, ref, base, ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	const n = 8
	shas := make([]model.SHA, n)
	for i := range n {
		shas[i] = commitEmpty(t, g, "contender")
	}

	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = g.UpdateRef(ctx, ref, shas[i], base)
		}()
	}
	wg.Wait()

	var winner model.SHA
	winners := 0
	for i, err := range errs {
		switch {
		case err == nil:
			winners++
			winner = shas[i]
		case !errors.Is(err, gitcmd.ErrCASMismatch):
			t.Fatalf("goroutine %d: got %v, want nil or ErrCASMismatch", i, err)
		}
	}
	if winners != 1 {
		t.Fatalf("got %d winners, want exactly 1 (errs: %v)", winners, errs)
	}
	if got := resolve(t, g.Dir, ref); got != winner {
		t.Fatalf("ref at %s, want winner %s", got, winner)
	}
}

func TestFetchPushRoundTrip(t *testing.T) {
	bare := initBare(t)
	a := initRepo(t)
	mustGit(t, a.Dir, "remote", "add", "origin", bare)
	ctx := t.Context()
	c1 := commitEmpty(t, a, "c1")
	ref := "refs/cc-notes/notes/" + string(c1)
	if err := a.UpdateRef(ctx, ref, c1, ""); err != nil {
		t.Fatalf("create: %v", err)
	}

	if err := a.Push(ctx, "origin", "refs/cc-notes/*:refs/cc-notes/*"); err != nil {
		t.Fatalf("push: %v", err)
	}
	if got := resolve(t, bare, ref); got != c1 {
		t.Fatalf("remote ref at %s, want %s", got, c1)
	}

	b := gitcmd.Git{Dir: t.TempDir()}
	mustGit(t, b.Dir, "init", "-q", "-b", "main")
	mustGit(t, b.Dir, "remote", "add", "origin", bare)
	mustGit(t, b.Dir, "config", "user.name", "Other User")
	mustGit(t, b.Dir, "config", "user.email", "other@example.com")
	if err := b.Fetch(ctx, "origin", "refs/cc-notes/*:refs/cc-notes/*"); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if got := resolve(t, b.Dir, ref); got != c1 {
		t.Fatalf("fetched ref at %s, want %s", got, c1)
	}

	divergent := commitEmpty(t, b, "divergent")
	if err := b.UpdateRef(ctx, ref, divergent, c1); err != nil {
		t.Fatalf("diverge local ref: %v", err)
	}
	if err := b.Push(ctx, "origin", "refs/cc-notes/*:refs/cc-notes/*"); !errors.Is(err, gitcmd.ErrNonFastForward) {
		t.Fatalf("diverged push: got %v, want ErrNonFastForward", err)
	}
	if got := resolve(t, bare, ref); got != c1 {
		t.Fatalf("rejected push moved remote ref to %s, want %s", got, c1)
	}
}

func TestConfig(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()

	got, err := g.ConfigGetAll(ctx, "ccnotes.missing")
	if err != nil {
		t.Fatalf("get-all missing: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("get-all missing: got %q, want empty", got)
	}

	if err := g.ConfigAdd(ctx, "ccnotes.fetch", "one"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := g.ConfigAdd(ctx, "ccnotes.fetch", "two"); err != nil {
		t.Fatalf("add: %v", err)
	}
	got, err = g.ConfigGetAll(ctx, "ccnotes.fetch")
	if err != nil {
		t.Fatalf("get-all: %v", err)
	}
	if want := []string{"one", "two"}; !slices.Equal(got, want) {
		t.Fatalf("get-all: got %q, want %q", got, want)
	}

	if err := g.ConfigSet(ctx, "ccnotes.single", "a"); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := g.ConfigSet(ctx, "ccnotes.single", "b"); err != nil {
		t.Fatalf("set again: %v", err)
	}
	got, err = g.ConfigGetAll(ctx, "ccnotes.single")
	if err != nil {
		t.Fatalf("get-all single: %v", err)
	}
	if want := []string{"b"}; !slices.Equal(got, want) {
		t.Fatalf("get-all single: got %q, want %q", got, want)
	}

	if err := g.ConfigAdd(ctx, "ccnotes.newline", "a\nb"); err != nil {
		t.Fatalf("add newline value: %v", err)
	}
	got, err = g.ConfigGetAll(ctx, "ccnotes.newline")
	if err != nil {
		t.Fatalf("get-all newline: %v", err)
	}
	if want := []string{"a\nb"}; !slices.Equal(got, want) {
		t.Fatalf("get-all newline: got %q, want %q", got, want)
	}
}

func TestConfigGet(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()

	got, err := g.ConfigGet(ctx, "ccnotes.missing")
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if got != "" {
		t.Fatalf("get missing: got %q, want empty", got)
	}

	mustGit(t, g.Dir, "config", "ccnotes.single", "local-value")
	got, err = g.ConfigGet(ctx, "ccnotes.single")
	if err != nil {
		t.Fatalf("get local: %v", err)
	}
	if got != "local-value" {
		t.Fatalf("get local: got %q, want local-value", got)
	}

	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[lfs]\n\turl = https://global.example/lfs\n[ccnotes]\n\tsingle = global-value\n"), 0o600); err != nil {
		t.Fatalf("write global config: %v", err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)

	got, err = g.ConfigGet(ctx, "lfs.url")
	if err != nil {
		t.Fatalf("get global-only: %v", err)
	}
	if got != "https://global.example/lfs" {
		t.Fatalf("full-scope get missed global value: got %q", got)
	}

	got, err = g.ConfigGet(ctx, "ccnotes.single")
	if err != nil {
		t.Fatalf("get layered: %v", err)
	}
	if got != "local-value" {
		t.Fatalf("local must win over global: got %q", got)
	}

	locals, err := g.ConfigGetAll(ctx, "lfs.url")
	if err != nil {
		t.Fatalf("get-all local scope: %v", err)
	}
	if len(locals) != 0 {
		t.Fatalf("ConfigGetAll is local-scope, must not see global: got %q", locals)
	}
}

func TestRemoteURL(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()

	if _, err := g.RemoteURL(ctx, "origin"); err == nil {
		t.Fatal("missing remote: want error")
	}

	mustGit(t, g.Dir, "remote", "add", "origin", "https://git-server.com/foo/bar.git")
	got, err := g.RemoteURL(ctx, "origin")
	if err != nil {
		t.Fatalf("remote url: %v", err)
	}
	if got != "https://git-server.com/foo/bar.git" {
		t.Fatalf("remote url: got %q, want https://git-server.com/foo/bar.git", got)
	}

	mustGit(t, g.Dir, "config", "url.https://mirror.example/.insteadOf", "https://git-server.com/")
	got, err = g.RemoteURL(ctx, "origin")
	if err != nil {
		t.Fatalf("remote url with insteadOf: %v", err)
	}
	if got != "https://mirror.example/foo/bar.git" {
		t.Fatalf("insteadOf not applied: got %q, want https://mirror.example/foo/bar.git", got)
	}
}

func TestHeadBranch(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()

	got, err := g.HeadBranch(ctx)
	if err != nil {
		t.Fatalf("unborn: %v", err)
	}
	if got != "main" {
		t.Fatalf("unborn: got %q, want main", got)
	}

	mustGit(t, g.Dir, "checkout", "-q", "-b", "feature/x")
	got, err = g.HeadBranch(ctx)
	if err != nil {
		t.Fatalf("feature branch: %v", err)
	}
	if got != "feature/x" {
		t.Fatalf("feature branch: got %q, want feature/x", got)
	}

	commitEmpty(t, g, "c1")
	mustGit(t, g.Dir, "checkout", "-q", "--detach")
	if _, err = g.HeadBranch(ctx); !errors.Is(err, gitcmd.ErrDetachedHead) {
		t.Fatalf("detached: got %v, want ErrDetachedHead", err)
	}
}

func TestAuthorIdent(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()

	name, email, err := g.AuthorIdent(ctx)
	if err != nil {
		t.Fatalf("local identity: %v", err)
	}
	if name != "Test User" || email != "test@example.com" {
		t.Fatalf("local identity: got %q <%s>, want Test User <test@example.com>", name, email)
	}

	t.Setenv("GIT_AUTHOR_NAME", "Env Author")
	t.Setenv("GIT_AUTHOR_EMAIL", "env@example.com")
	name, email, err = g.AuthorIdent(ctx)
	if err != nil {
		t.Fatalf("env identity: %v", err)
	}
	if name != "Env Author" || email != "env@example.com" {
		t.Fatalf("env identity: got %q <%s>, want Env Author <env@example.com>", name, email)
	}
}

func TestAuthorIdentMissing(t *testing.T) {
	g := initRepoNoIdentity(t)
	mustGit(t, g.Dir, "config", "user.useConfigOnly", "true")
	_, _, err := g.AuthorIdent(t.Context())
	if err == nil {
		t.Fatal("missing identity: want error")
	}
	if msg := err.Error(); !strings.Contains(msg, "user.name") || !strings.Contains(msg, "user.email") {
		t.Fatalf("missing identity: error %q lacks user.name/user.email hint", msg)
	}
}

func TestCommitSHA(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()
	full := commitEmpty(t, g, "c1")

	got, err := g.CommitSHA(ctx, "HEAD")
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	if got != full {
		t.Fatalf("HEAD: got %q, want %q", got, full)
	}

	short := string(full)[:8]
	got, err = g.CommitSHA(ctx, short)
	if err != nil {
		t.Fatalf("short %s: %v", short, err)
	}
	if got != full {
		t.Fatalf("short %s: got %q, want full %q", short, got, full)
	}

	if _, err := g.CommitSHA(ctx, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"); !errors.Is(err, gitcmd.ErrRevNotFound) {
		t.Fatalf("unknown rev: got %v, want ErrRevNotFound", err)
	}
}

func TestTaskTrailers(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()

	none := commitEmpty(t, g, "no trailer")
	got, err := g.TaskTrailers(ctx, string(none))
	if err != nil {
		t.Fatalf("none: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("none: got %q, want empty", got)
	}

	mustGit(t, g.Dir, "commit", "-q", "--allow-empty", "-m", "one\n\ncc-task: d82c087")
	single := model.SHA(mustGit(t, g.Dir, "rev-parse", "HEAD"))
	got, err = g.TaskTrailers(ctx, string(single))
	if err != nil {
		t.Fatalf("single: %v", err)
	}
	if want := []string{"d82c087"}; !slices.Equal(got, want) {
		t.Fatalf("single: got %q, want %q", got, want)
	}

	mustGit(t, g.Dir, "commit", "-q", "--allow-empty", "-m", "two\n\ncc-task: aaa1111\ncc-task: bbb2222")
	multi := model.SHA(mustGit(t, g.Dir, "rev-parse", "HEAD"))
	got, err = g.TaskTrailers(ctx, string(multi))
	if err != nil {
		t.Fatalf("multi: %v", err)
	}
	if want := []string{"aaa1111", "bbb2222"}; !slices.Equal(got, want) {
		t.Fatalf("multi: got %q, want %q", got, want)
	}
}

func TestRemotes(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()

	got, err := g.Remotes(ctx)
	if err != nil {
		t.Fatalf("no remotes: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("no remotes: got %q, want empty", got)
	}

	mustGit(t, g.Dir, "remote", "add", "origin", t.TempDir())
	mustGit(t, g.Dir, "remote", "add", "upstream", t.TempDir())
	got, err = g.Remotes(ctx)
	if err != nil {
		t.Fatalf("remotes: %v", err)
	}
	slices.Sort(got)
	if want := []string{"origin", "upstream"}; !slices.Equal(got, want) {
		t.Fatalf("remotes: got %q, want %q", got, want)
	}
}

func TestCommonDir(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()
	commitEmpty(t, g, "c1")

	root, err := filepath.EvalSymlinks(g.Dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	want := filepath.Join(root, ".git")

	got, err := g.CommonDir(ctx)
	if err != nil {
		t.Fatalf("common dir: %v", err)
	}
	if gotEval, err := filepath.EvalSymlinks(got); err == nil {
		got = gotEval
	}
	if got != want {
		t.Fatalf("common dir: got %q, want %q", got, want)
	}

	linked := t.TempDir()
	mustGit(t, g.Dir, "worktree", "add", "-q", linked)
	gotLinked, err := (gitcmd.Git{Dir: linked}).CommonDir(ctx)
	if err != nil {
		t.Fatalf("common dir from linked worktree: %v", err)
	}
	if gotEval, err := filepath.EvalSymlinks(gotLinked); err == nil {
		gotLinked = gotEval
	}
	if gotLinked != want {
		t.Fatalf("linked worktree common dir: got %q, want %q", gotLinked, want)
	}
}

func TestRoot(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()
	want, err := filepath.EvalSymlinks(g.Dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	got, err := g.Root(ctx)
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	if got != want {
		t.Fatalf("root: got %q, want %q", got, want)
	}

	sub := filepath.Join(g.Dir, "nested", "dir")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err = gitcmd.Git{Dir: sub}.Root(ctx)
	if err != nil {
		t.Fatalf("root from subdir: %v", err)
	}
	if got != want {
		t.Fatalf("root from subdir: got %q, want %q", got, want)
	}

	if _, err := (gitcmd.Git{Dir: t.TempDir()}).Root(ctx); err == nil {
		t.Fatal("root outside a repo: want error")
	}
}

func TestMergeBase(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()

	base := commitEmpty(t, g, "base")
	mustGit(t, g.Dir, "checkout", "-q", "-b", "feature")
	commitEmpty(t, g, "feature tip")
	feature := resolve(t, g.Dir, "HEAD")
	mustGit(t, g.Dir, "checkout", "-q", "main")
	commitEmpty(t, g, "main tip")
	main := resolve(t, g.Dir, "HEAD")

	got, err := g.MergeBase(ctx, string(main), string(feature))
	if err != nil {
		t.Fatalf("MergeBase across fork: %v", err)
	}
	if got != base {
		t.Fatalf("MergeBase = %q, want fork point %q", got, base)
	}

	mustGit(t, g.Dir, "checkout", "-q", "--orphan", "unrelated")
	orphan := commitEmpty(t, g, "orphan root")
	if _, err := g.MergeBase(ctx, string(main), string(orphan)); !errors.Is(err, gitcmd.ErrRevNotFound) {
		t.Fatalf("MergeBase on unrelated histories = %v, want ErrRevNotFound", err)
	}
}

func TestDefaultBranch(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()
	commitEmpty(t, g, "c1")

	if _, err := g.DefaultBranch(ctx); !errors.Is(err, gitcmd.ErrNoDefaultBranch) {
		t.Fatalf("unset origin/HEAD = %v, want ErrNoDefaultBranch", err)
	}

	mustGit(t, g.Dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	got, err := g.DefaultBranch(ctx)
	if err != nil {
		t.Fatalf("set origin/HEAD: %v", err)
	}
	if got != "main" {
		t.Fatalf("DefaultBranch = %q, want main", got)
	}
}

func TestRevRangeFileAuthors(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()

	commitFile := func(name, email, path, content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(g.Dir, path), []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		mustGit(t, g.Dir, "add", path)
		mustGit(t, g.Dir,
			"-c", "user.name="+name, "-c", "user.email="+email,
			"commit", "-q", "-m", "edit "+path)
	}

	commitFile("Base", "base@x.com", "shared.txt", "a\n")
	base := resolve(t, g.Dir, "HEAD")

	// An empty range yields an empty, non-nil map.
	empty, err := g.RevRangeFileAuthors(ctx, string(base), string(base))
	if err != nil {
		t.Fatalf("empty range: %v", err)
	}
	if empty == nil {
		t.Fatal("empty range: got nil map, want empty non-nil")
	}
	if len(empty) != 0 {
		t.Fatalf("empty range: got %v, want empty", empty)
	}

	// A merge-only-or-empty commit contributes no paths.
	mustGit(t, g.Dir,
		"-c", "user.name=Alice", "-c", "user.email=alice@x.com",
		"commit", "-q", "--allow-empty", "-m", "empty by alice")
	commitFile("Alice", "alice@x.com", "shared.txt", "a\nb\n") // alice touches shared + onlysecond
	commitFile("Alice", "alice@x.com", "onlysecond.txt", "x\n")
	commitFile("Bob", "bob@x.com", "shared.txt", "a\nb\nc\n") // bob touches shared + onlyfirst
	commitFile("Bob", "bob@x.com", "onlyfirst.txt", "y\n")
	head := resolve(t, g.Dir, "HEAD")

	got, err := g.RevRangeFileAuthors(ctx, string(base), string(head))
	if err != nil {
		t.Fatalf("RevRangeFileAuthors: %v", err)
	}
	want := map[string][]string{
		"shared.txt":     {"alice@x.com", "bob@x.com"},
		"onlyfirst.txt":  {"bob@x.com"},
		"onlysecond.txt": {"alice@x.com"},
	}
	if len(got) != len(want) {
		t.Fatalf("RevRangeFileAuthors: got %v, want %v", got, want)
	}
	for path, wantEmails := range want {
		if !slices.Equal(got[path], wantEmails) {
			t.Fatalf("RevRangeFileAuthors[%q] = %v, want %v", path, got[path], wantEmails)
		}
	}
}

func TestWorktreeBlobOID(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()

	path := "dir/file.txt"
	if err := os.MkdirAll(filepath.Join(g.Dir, "dir"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(g.Dir, path), []byte("first\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	oid, err := g.WorktreeBlobOID(ctx, path)
	if err != nil {
		t.Fatalf("WorktreeBlobOID: %v", err)
	}
	if want := mustGit(t, g.Dir, "hash-object", "--", path); oid != want {
		t.Fatalf("WorktreeBlobOID = %q, want %q", oid, want)
	}

	if err := os.WriteFile(filepath.Join(g.Dir, path), []byte("second\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	edited, err := g.WorktreeBlobOID(ctx, path)
	if err != nil {
		t.Fatalf("WorktreeBlobOID after edit: %v", err)
	}
	if edited == oid {
		t.Fatalf("WorktreeBlobOID after edit = %q, want a different oid than %q", edited, oid)
	}

	if _, err := g.WorktreeBlobOID(ctx, "missing.txt"); !errors.Is(err, gitcmd.ErrPathNotFound) {
		t.Fatalf("WorktreeBlobOID on absent path = %v, want ErrPathNotFound", err)
	}
}

func TestPathOID(t *testing.T) {
	g := initRepo(t)
	ctx := t.Context()

	path := "dir/file.txt"
	if err := os.MkdirAll(filepath.Join(g.Dir, "dir"), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(g.Dir, path), []byte("first\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustGit(t, g.Dir, "add", path)
	mustGit(t, g.Dir, "commit", "-q", "-m", "add file")

	oid, err := g.PathOID(ctx, "HEAD", path)
	if err != nil {
		t.Fatalf("PathOID: %v", err)
	}
	if want := mustGit(t, g.Dir, "rev-parse", "HEAD:"+path); oid != want {
		t.Fatalf("PathOID = %q, want %q", oid, want)
	}

	if _, err := g.PathOID(ctx, "HEAD", "missing.txt"); !errors.Is(err, gitcmd.ErrPathNotFound) {
		t.Fatalf("PathOID on absent path = %v, want ErrPathNotFound", err)
	}

	if err := os.WriteFile(filepath.Join(g.Dir, path), []byte("second\n"), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	mustGit(t, g.Dir, "commit", "-q", "-am", "edit file")
	edited, err := g.PathOID(ctx, "HEAD", path)
	if err != nil {
		t.Fatalf("PathOID after edit: %v", err)
	}
	if edited == oid {
		t.Fatalf("PathOID after edit = %q, want a different oid than %q", edited, oid)
	}
}
