package gitcmd_test

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

var memoPatterns = []string{
	`^remote\..*\.fetch$`,
	`^remote\..*\.push$`,
	`^cc-notes\.local`,
	`^http\.(.+\.)?extraheader$`,
	`^nothing\.matches$`,
}

func writeMemoConfig(t *testing.T, commonDir string) {
	t.Helper()
	included := filepath.Join(t.TempDir(), "config")
	body := "[remote \"Mixed\"]\n\tfetch = +refs/a/*:refs/b/*\n\tfetch = +refs/c/*:refs/d/*\n" +
		"[cc-notes]\n\tlocalLabel = one\n\tlocalLabel = two\n" +
		"[http \"https://example.com\"]\n\textraHeader = local\n" +
		"[memo]\n\tbarekey\n" +
		"[include]\n\tpath = " + included + "\n"
	file, err := os.OpenFile(filepath.Join(commonDir, "config"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(body); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(included, []byte("[remote \"included\"]\n\tfetch = +refs/e/*:refs/f/*\n[cc-notes]\n\tlocalLabel = included\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertMemoConfigMatchesGit(t *testing.T, plain gitcmd.Git) {
	t.Helper()
	ctx := gitcmd.WithCommandMemo(t.Context())
	for _, pattern := range memoPatterns {
		want, err := plain.ConfigGetRegexp(t.Context(), pattern)
		if err != nil {
			t.Fatal(err)
		}
		got, err := plain.ConfigGetRegexp(ctx, pattern)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ConfigGetRegexp(%s) = %q, git says %q", pattern, got, want)
		}
	}
	want, err := plain.ConfigLocal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	got, err := plain.ConfigLocal(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ConfigLocal = %q, git says %q", got, want)
	}
}

func TestMemoizedConfigMatchesGit(t *testing.T) {
	t.Run("work tree", func(t *testing.T) {
		dir := gittest.InitRepo(t)
		writeMemoConfig(t, filepath.Join(dir, ".git"))
		assertMemoConfigMatchesGit(t, gitcmd.Git{Dir: dir})
	})
	t.Run("subdirectory", func(t *testing.T) {
		dir := gittest.InitRepo(t)
		writeMemoConfig(t, filepath.Join(dir, ".git"))
		sub := filepath.Join(dir, "a", "b")
		if err := os.MkdirAll(sub, 0o750); err != nil {
			t.Fatal(err)
		}
		assertMemoConfigMatchesGit(t, gitcmd.Git{Dir: sub})
	})
	t.Run("linked worktree", func(t *testing.T) {
		dir := gittest.InitRepo(t)
		gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
		writeMemoConfig(t, filepath.Join(dir, ".git"))
		linked := filepath.Join(t.TempDir(), "linked")
		gittest.Git(t, dir, "worktree", "add", "-q", "-b", "linked", linked)
		gittest.Git(t, linked, "config", "extensions.worktreeConfig", "true")
		gittest.Git(t, linked, "config", "--worktree", "cc-notes.localLabel", "worktree")
		assertMemoConfigMatchesGit(t, gitcmd.Git{Dir: linked})
	})
	t.Run("bare", func(t *testing.T) {
		dir := gittest.InitBare(t)
		writeMemoConfig(t, dir)
		assertMemoConfigMatchesGit(t, gitcmd.Git{Dir: dir})
	})
	t.Run("backend", func(t *testing.T) {
		dir := gittest.InitRepo(t)
		common := filepath.Join(dir, ".git")
		writeMemoConfig(t, common)
		assertMemoConfigMatchesGit(t, gitcmd.Backend(common, dir))
	})
	t.Run("GIT_CONFIG override", func(t *testing.T) {
		dir := gittest.InitRepo(t)
		writeMemoConfig(t, filepath.Join(dir, ".git"))
		override := filepath.Join(t.TempDir(), "override")
		if err := os.WriteFile(override, []byte("[cc-notes]\n\tlocalLabel = override\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("GIT_CONFIG", override)
		assertMemoConfigMatchesGit(t, gitcmd.Git{Dir: dir})
	})
}

func TestMemoizedConfigSeesItsOwnWrites(t *testing.T) {
	dir := gittest.InitRepo(t)
	g := gitcmd.Git{Dir: dir}
	ctx := gitcmd.WithCommandMemo(t.Context())
	const key = "remote.origin.push"
	read := func() []string {
		t.Helper()
		pairs, err := g.ConfigGetRegexp(ctx, `^remote\.origin\.push$`)
		if err != nil {
			t.Fatal(err)
		}
		values := make([]string, len(pairs))
		for i, pair := range pairs {
			values[i] = pair[1]
		}
		return values
	}
	if got := read(); len(got) != 0 {
		t.Fatalf("initial = %q", got)
	}
	if err := g.ConfigAdd(ctx, key, "one"); err != nil {
		t.Fatal(err)
	}
	if got := read(); !reflect.DeepEqual(got, []string{"one"}) {
		t.Fatalf("after add = %q", got)
	}
	if err := g.ConfigReplaceValue(ctx, key, "one", "two"); err != nil {
		t.Fatal(err)
	}
	if got := read(); !reflect.DeepEqual(got, []string{"two"}) {
		t.Fatalf("after replace = %q", got)
	}
	if err := g.ConfigUnsetValue(ctx, key, "two"); err != nil {
		t.Fatal(err)
	}
	if got := read(); len(got) != 0 {
		t.Fatalf("after unset = %q", got)
	}
	if err := g.ConfigSet(ctx, "cc-notes.localLabel", "set"); err != nil {
		t.Fatal(err)
	}
	pairs, err := g.ConfigGetRegexp(ctx, `^cc-notes\.local`)
	if err != nil || !reflect.DeepEqual(pairs, [][2]string{{"cc-notes.locallabel", "set"}}) {
		t.Fatalf("after set = %q, %v", pairs, err)
	}
}

func TestMemoizedAuthorIdentMatchesGit(t *testing.T) {
	tests := []struct {
		name   string
		config map[string]string
		env    map[string]string
		bare   string
	}{
		{name: "user config", config: map[string]string{"user.name": "Ann Lee", "user.email": "ann@example.com"}},
		{name: "author config wins", config: map[string]string{"user.name": "Ann", "user.email": "ann@example.com", "author.name": "Bo", "author.email": "bo@example.com"}},
		{name: "environment wins", config: map[string]string{"user.name": "Ann", "user.email": "ann@example.com", "author.name": "Bo"}, env: map[string]string{"GIT_AUTHOR_NAME": "Cy", "GIT_AUTHOR_EMAIL": "cy@example.com"}},
		{name: "trailing crud", config: map[string]string{"user.name": "Ann Lee Jr.", "user.email": "ann@example.com."}},
		{name: "leading crud", config: map[string]string{"user.name": "'Ann", "user.email": "<ann@example.com>"}},
		{name: "brackets inside", config: map[string]string{"user.name": "Ann <the> Lee", "user.email": "ann@example.com"}},
		{name: "spaces in environment", config: map[string]string{"user.email": "ann@example.com"}, env: map[string]string{"GIT_AUTHOR_NAME": "  Ann  "}},
		{name: "empty environment name", config: map[string]string{"user.name": "Ann", "user.email": "ann@example.com"}, env: map[string]string{"GIT_AUTHOR_NAME": ""}},
		{name: "EMAIL fallback", config: map[string]string{"user.name": "Ann", "user.useConfigOnly": "false"}, env: map[string]string{"EMAIL": "env@example.com"}},
		{name: "useConfigOnly with no email", config: map[string]string{"user.name": "Ann", "user.useConfigOnly": "true"}},
		{name: "bare author name", config: map[string]string{"user.name": "Ann", "user.email": "ann@example.com"}, bare: "[author]\n\tname\n"},
		{name: "invalid useConfigOnly", config: map[string]string{"user.name": "Ann", "user.email": "ann@example.com", "user.useConfigOnly": "maybe"}},
		{name: "author date set", config: map[string]string{"user.name": "Ann", "user.email": "ann@example.com"}, env: map[string]string{"GIT_AUTHOR_DATE": "not a date"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plain := initRepoNoIdentity(t)
			for key, value := range test.config {
				gittest.Git(t, plain.Dir, "config", key, value)
			}
			if test.bare != "" {
				file, err := os.OpenFile(filepath.Join(plain.Dir, ".git", "config"), os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := file.WriteString(test.bare); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			for key, value := range test.env {
				t.Setenv(key, value)
			}
			wantName, wantEmail, wantErr := plain.AuthorIdent(t.Context())
			gotName, gotEmail, gotErr := plain.AuthorIdent(gitcmd.WithCommandMemo(t.Context()))
			if (gotErr == nil) != (wantErr == nil) || gotName != wantName || gotEmail != wantEmail {
				t.Fatalf("got %q %q %v, git says %q %q %v", gotName, gotEmail, gotErr, wantName, wantEmail, wantErr)
			}
		})
	}
}

func TestMemoizedHeadMatchesGit(t *testing.T) {
	dir := gittest.InitRepo(t)
	memoHead := func(dir string) (model.Branch, error) {
		t.Helper()
		g := gitcmd.Git{Dir: dir}
		ctx := gitcmd.WithCommandMemo(t.Context())
		if _, _, _, err := g.Dirs(ctx); err != nil {
			t.Fatal(err)
		}
		return g.HeadBranch(ctx)
	}
	assertSame := func(dir string) {
		t.Helper()
		want, wantErr := gitcmd.Git{Dir: dir}.HeadBranch(t.Context())
		got, gotErr := memoHead(dir)
		if got != want || errors.Is(gotErr, gitcmd.ErrDetachedHead) != errors.Is(wantErr, gitcmd.ErrDetachedHead) || (gotErr == nil) != (wantErr == nil) {
			t.Fatalf("HeadBranch = %q %v, git says %q %v", got, gotErr, want, wantErr)
		}
	}
	assertSame(dir)
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
	gittest.Git(t, dir, "switch", "-q", "-c", "feature/with/slashes")
	assertSame(dir)
	linked := filepath.Join(t.TempDir(), "linked")
	gittest.Git(t, dir, "worktree", "add", "-q", "-b", "linked", linked)
	assertSame(linked)
	gittest.Git(t, dir, "branch", "-q", "real", "linked")
	gittest.Git(t, dir, "symbolic-ref", "refs/heads/alias", "refs/heads/real")
	gittest.Git(t, dir, "symbolic-ref", "HEAD", "refs/heads/alias")
	assertSame(dir)
	gittest.Git(t, dir, "switch", "-q", "--detach", "linked")
	assertSame(dir)
	if _, err := memoHead(dir); !errors.Is(err, gitcmd.ErrDetachedHead) {
		t.Fatalf("detached HEAD = %v", err)
	}
	gittest.Git(t, dir, "symbolic-ref", "HEAD", "refs/custom/head")
	assertSame(dir)
}
