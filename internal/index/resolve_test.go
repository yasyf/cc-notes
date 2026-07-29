package index

import (
	"errors"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/ccnhome"
	"github.com/yasyf/cc-notes/internal/gittest"
)

// layouts is the set of repository shapes one working directory can sit in,
// plus the registry entries `cc-notes init` would have written for them. The
// nested repository is deliberately left unregistered: it is the case that
// proves the walk stops at a boundary instead of charging its parent.
type layouts struct {
	main      string
	linked    string
	submodule string
	bare      string
	alias     string
	nested    string

	mainRepo      ccnhome.Repo
	submoduleRepo ccnhome.Repo
	bareRepo      ccnhome.Repo
}

func initLayouts(t *testing.T) layouts {
	t.Helper()
	gittest.ScrubEnv(t)
	root := t.TempDir()

	main := filepath.Join(root, "main")
	initLayoutRepo(t, main)
	gittest.Git(t, main, "commit", "-q", "--allow-empty", "-m", "base")

	linked := filepath.Join(root, "linked")
	gittest.Git(t, main, "worktree", "add", "-q", "-b", "resolve-linked", linked)

	bare := filepath.Join(root, "bare.git")
	if err := os.Mkdir(bare, 0o750); err != nil {
		t.Fatalf("mkdir bare repo: %v", err)
	}
	gittest.Git(t, bare, "init", "-q", "--bare")

	source := filepath.Join(root, "source")
	initLayoutRepo(t, source)
	gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "base")

	super := filepath.Join(root, "super")
	initLayoutRepo(t, super)
	gittest.Git(t, super, "-c", "protocol.file.allow=always", "submodule", "add", "-q", source, "module")
	submodule := filepath.Join(super, "module")

	alias := filepath.Join(root, "main-alias")
	if err := os.Symlink(main, alias); err != nil {
		t.Fatalf("symlink repo: %v", err)
	}

	nested := filepath.Join(main, "nested")
	initLayoutRepo(t, nested)

	l := layouts{main: main, linked: linked, submodule: submodule, bare: bare, alias: alias, nested: nested}
	_, mainCommon := gittest.Dirs(t, main)
	_, submoduleCommon := gittest.Dirs(t, submodule)
	_, bareCommon := gittest.Dirs(t, bare)
	l.mainRepo = record(t, mainCommon, main)
	if linkedRepo := record(t, mainCommon, linked); linkedRepo != l.mainRepo {
		t.Fatalf("linked worktree registered as %+v, want %+v", linkedRepo, l.mainRepo)
	}
	l.submoduleRepo = record(t, submoduleCommon, submodule)
	l.bareRepo = record(t, bareCommon, "")
	return l
}

func initLayoutRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	gittest.Git(t, dir, "config", "user.name", "Test User")
	gittest.Git(t, dir, "config", "user.email", "test@example.com")
}

func resolved(t *testing.T, path string) string {
	t.Helper()
	out, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %q: %v", path, err)
	}
	return out
}

func record(t *testing.T, commonDir, worktree string) ccnhome.Repo {
	t.Helper()
	repo, err := ccnhome.RecordWorktree(commonDir, worktree)
	if err != nil {
		t.Fatalf("RecordWorktree(%q, %q): %v", commonDir, worktree, err)
	}
	return repo
}

// TestResolveRepositoryLayouts is the six-layout table. Every directory it
// feeds Resolve is the lexical path t.TempDir handed out, which on darwin is
// the /var spelling of a /private/var directory — so every row also exercises
// the alias the registry never records, on top of the explicit symlink row.
func TestResolveRepositoryLayouts(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	l := initLayouts(t)
	r := NewResolver()

	for _, tc := range []struct {
		name    string
		dir     string
		want    ccnhome.Repo
		wantErr error
	}{
		{name: "main worktree", dir: l.main, want: l.mainRepo},
		{name: "linked worktree shares the main repository's key", dir: l.linked, want: l.mainRepo},
		{name: "submodule checkout", dir: l.submodule, want: l.submoduleRepo},
		{name: "bare repository", dir: l.bare, want: l.bareRepo},
		{name: "alias path", dir: l.alias, want: l.mainRepo},
		{name: "nested unregistered repository", dir: l.nested, wantErr: ErrUnknownRepo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Resolve(tc.dir)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Resolve(%q) error = %v, want %v", tc.dir, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("Resolve(%q) = %+v, want %+v", tc.dir, got, tc.want)
			}
		})
	}
}

func TestResolveWalksUpwardToTheOwningRepository(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	l := initLayouts(t)
	deep := filepath.Join(l.main, "a", "b")
	if err := os.MkdirAll(deep, 0o750); err != nil {
		t.Fatalf("mkdir %q: %v", deep, err)
	}
	_, mainCommon := gittest.Dirs(t, l.main)
	linkedGitDir, _ := gittest.Dirs(t, l.linked)
	if resolved(t, linkedGitDir) == resolved(t, mainCommon) {
		t.Fatalf("linked worktree git dir = %q, want its own directory rather than the common one", linkedGitDir)
	}
	r := NewResolver()

	for _, tc := range []struct {
		name    string
		dir     string
		want    ccnhome.Repo
		wantErr error
	}{
		{name: "directory inside a registered worktree", dir: deep, want: l.mainRepo},
		{name: "directory inside the git directory", dir: filepath.Join(mainCommon, "refs"), want: l.mainRepo},
		// .git/worktrees/<name> carries HEAD and refs but no objects, so it is
		// not a boundary and the walk has to climb into the common directory.
		{name: "a linked worktree's own git directory", dir: linkedGitDir, want: l.mainRepo},
		{name: "directory in no repository at all", dir: t.TempDir(), wantErr: ErrUnknownRepo},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := r.Resolve(tc.dir)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Resolve(%q) error = %v, want %v", tc.dir, err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("Resolve(%q) = %+v, want %+v", tc.dir, got, tc.want)
			}
		})
	}
}

func TestResolveRejectsRelativeDirectory(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	if _, err := NewResolver().Resolve("relative/dir"); err == nil {
		t.Fatal("Resolve on a relative working directory returned no error")
	}
}

// TestResolveCachesUnknownUntilTheTTLExpires pins both halves of the negative
// cache: an unknown answer is served from memory rather than re-reading the
// registry, and it stops being served once it ages out.
func TestResolveCachesUnknownUntilTheTTLExpires(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	gittest.ScrubEnv(t)
	checkout := t.TempDir()
	initLayoutRepo(t, checkout)
	_, commonDir := gittest.Dirs(t, checkout)

	clock := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	r := newResolver(func() time.Time { return clock })

	if _, err := r.Resolve(checkout); !errors.Is(err, ErrUnknownRepo) {
		t.Fatalf("Resolve before registration = %v, want %v", err, ErrUnknownRepo)
	}
	repo := record(t, commonDir, checkout)
	if _, err := r.Resolve(checkout); !errors.Is(err, ErrUnknownRepo) {
		t.Fatalf("Resolve inside the TTL = %v, want the cached %v", err, ErrUnknownRepo)
	}

	clock = clock.Add(unknownTTL)
	got, err := r.Resolve(checkout)
	if err != nil {
		t.Fatalf("Resolve after the TTL: %v", err)
	}
	if got != repo {
		t.Fatalf("Resolve after the TTL = %+v, want %+v", got, repo)
	}

	r.cacheUnknown(t.TempDir())
	resolvedCheckout, err := filepath.EvalSymlinks(checkout)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := r.unknown[resolvedCheckout]; ok {
		t.Fatalf("expired entry for %q survived a later insert", resolvedCheckout)
	}
}

// TestResolveStopsServingAReapedRepository pins the two halves of this build
// step against each other: ccnhome.Reap unregisters a clone and deletes its
// derived state, and a resolver that never revalidates a snapshot hit would go
// on handing out that directory for the life of the daemon.
func TestResolveStopsServingAReapedRepository(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	gittest.ScrubEnv(t)
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	initLayoutRepo(t, checkout)
	_, commonDir := gittest.Dirs(t, checkout)
	repo := record(t, commonDir, checkout)
	r := NewResolver()

	got, err := r.Resolve(checkout)
	if err != nil {
		t.Fatalf("Resolve before the reap: %v", err)
	}
	if got != repo {
		t.Fatalf("Resolve before the reap = %+v, want %+v", got, repo)
	}

	if err := os.RemoveAll(commonDir); err != nil {
		t.Fatalf("remove the git directory: %v", err)
	}
	removed, err := ccnhome.Reap()
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if !slices.Equal(removed, []string{repo.Key}) {
		t.Fatalf("Reap removed %v, want %v", removed, []string{repo.Key})
	}

	got, err = r.Resolve(checkout)
	if !errors.Is(err, ErrUnknownRepo) {
		t.Fatalf("Resolve after the reap error = %v, want %v", err, ErrUnknownRepo)
	}
	if got != (ccnhome.Repo{}) {
		t.Fatalf("Resolve after the reap = %+v, want the zero repository", got)
	}
}

// TestResolveFollowsAReassignedWorktree pins the half of staleness a live
// repository hides: RecordWorktree releases a path its previous owner claimed,
// but that owner still exists and its descriptor is still on disk, so a
// revalidation that only asked whether the hit repository is still registered
// would answer yes and serve the wrong repository's index for the life of the
// daemon. Nothing else heals it — the reload runs only on a walk miss.
func TestResolveFollowsAReassignedWorktree(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	gittest.ScrubEnv(t)
	root := t.TempDir()
	moved := filepath.Join(root, "moved")
	initLayoutRepo(t, moved)
	other := filepath.Join(root, "other")
	initLayoutRepo(t, other)
	_, movedCommon := gittest.Dirs(t, moved)
	_, otherCommon := gittest.Dirs(t, other)
	first := record(t, movedCommon, moved)
	second := record(t, otherCommon, other)
	r := NewResolver()

	got, err := r.Resolve(moved)
	if err != nil {
		t.Fatalf("Resolve before the reassignment: %v", err)
	}
	if got != first {
		t.Fatalf("Resolve before the reassignment = %+v, want %+v", got, first)
	}

	if reclaimed := record(t, otherCommon, moved); reclaimed != second {
		t.Fatalf("reassignment registered %+v, want %+v", reclaimed, second)
	}
	if _, err := os.Stat(first.InfoPath()); err != nil {
		t.Fatalf("the previous owner must still be registered for this case to bite: %v", err)
	}

	got, err = r.Resolve(moved)
	if err != nil {
		t.Fatalf("Resolve after the reassignment: %v", err)
	}
	if got != second {
		t.Fatalf("Resolve after the reassignment = %+v, want %+v", got, second)
	}
}

func TestResolveIsSafeUnderConcurrentQueries(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	l := initLayouts(t)
	r := NewResolver()

	queries := []struct {
		dir     string
		want    ccnhome.Repo
		wantErr error
	}{
		{dir: l.main, want: l.mainRepo},
		{dir: l.linked, want: l.mainRepo},
		{dir: l.bare, want: l.bareRepo},
		{dir: l.submodule, want: l.submoduleRepo},
		{dir: l.nested, wantErr: ErrUnknownRepo},
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				for _, q := range queries {
					got, err := r.Resolve(q.dir)
					if !errors.Is(err, q.wantErr) {
						t.Errorf("Resolve(%q) error = %v, want %v", q.dir, err, q.wantErr)
						return
					}
					if got != q.want {
						t.Errorf("Resolve(%q) = %+v, want %+v", q.dir, got, q.want)
						return
					}
				}
			}
		}()
	}
	wg.Wait()
}

// TestResolveRunsNoSubprocess is the zero-exec proof, and it proves exactly one
// thing: resolution spawns no command looked up on PATH. One git rev-parse would
// spend the whole hook budget, which is why resolution lives daemon-side at all.
// PATH is replaced by a directory of recording shims, so a spawn of git, sh,
// bash or env lands in the log, and a spawn of anything else fails to launch —
// which surfaces because every row's repository is asserted, so an error a
// resolution swallowed would still show up as a wrong answer.
//
// What it does not cover: an exec by absolute path, which reaches no shim and
// touches no PATH lookup. TestResolveSourceReachesNoSubprocessPackage narrows
// that gap to resolve.go's own imports and no further — a helper in a sibling
// file of package index could still exec, and nothing here would see it. The
// package builds graphs, and building a graph runs git, so a package-wide
// import ban is not available; internal/gitcmd has no injectable runner to
// intercept either.
func TestResolveRunsNoSubprocess(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	l := initLayouts(t)
	r := NewResolver()

	shim := t.TempDir()
	spawned := filepath.Join(t.TempDir(), "spawned.log")
	if err := os.WriteFile(spawned, nil, 0o600); err != nil {
		t.Fatalf("create spawn log: %v", err)
	}
	script := "#!/bin/sh\necho \"$0 $*\" >> '" + spawned + "'\n"
	for _, name := range []string{"git", "sh", "bash", "env"} {
		if err := os.WriteFile(filepath.Join(shim, name), []byte(script), 0o700); err != nil {
			t.Fatalf("write %s shim: %v", name, err)
		}
	}
	t.Setenv("PATH", shim)

	if err := exec.Command("git", "rev-parse", "--git-dir").Run(); err != nil {
		t.Fatalf("shim control: %v", err)
	}
	if lines := spawnedCommands(t, spawned); len(lines) != 1 {
		t.Fatalf("shim control recorded %v, want exactly one spawn", lines)
	}
	if err := os.Truncate(spawned, 0); err != nil {
		t.Fatalf("truncate spawn log: %v", err)
	}

	for _, q := range []struct {
		dir     string
		want    ccnhome.Repo
		wantErr error
	}{
		{dir: l.main, want: l.mainRepo},
		{dir: l.linked, want: l.mainRepo},
		{dir: l.submodule, want: l.submoduleRepo},
		{dir: l.bare, want: l.bareRepo},
		{dir: l.alias, want: l.mainRepo},
		{dir: l.nested, wantErr: ErrUnknownRepo},
	} {
		got, err := r.Resolve(q.dir)
		if !errors.Is(err, q.wantErr) {
			t.Fatalf("Resolve(%q) error = %v, want %v", q.dir, err, q.wantErr)
		}
		if got != q.want {
			t.Fatalf("Resolve(%q) = %+v, want %+v", q.dir, got, q.want)
		}
	}
	if lines := spawnedCommands(t, spawned); len(lines) != 0 {
		t.Fatalf("resolution spawned %v, want no subprocess at all", lines)
	}
}

func spawnedCommands(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatalf("read spawn log: %v", err)
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return nil
	}
	return strings.Split(text, "\n")
}

// TestResolveSourceReachesNoSubprocessPackage pins the structural half of the
// zero-exec rule against the branch a runtime probe can miss: resolve.go cannot
// reach a subprocess at all. It is scoped to this one file because the rest of
// internal/index builds graphs, and building a graph does run git.
func TestResolveSourceReachesNoSubprocessPackage(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "resolve.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse resolve.go: %v", err)
	}
	forbidden := map[string]bool{
		"os/exec": true,
		"github.com/yasyf/cc-notes/internal/gitcmd": true,
	}
	for _, imported := range file.Imports {
		path, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			t.Fatalf("unquote import %s: %v", imported.Path.Value, err)
		}
		if forbidden[path] {
			t.Errorf("resolve.go imports %s; resolution must not be able to run a subprocess", path)
		}
	}
}
