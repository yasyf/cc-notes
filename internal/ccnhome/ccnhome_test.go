package ccnhome_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/ccnhome"
	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
)

func TestRootHonorsOverride(t *testing.T) {
	root := t.TempDir()
	t.Setenv(ccnhome.Env, root)
	got, err := ccnhome.Root()
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if got != root {
		t.Fatalf("Root = %q, want %q", got, root)
	}
}

func TestRootDefaultsUnderHome(t *testing.T) {
	_ = os.Unsetenv(ccnhome.Env)
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := ccnhome.Root()
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if want := filepath.Join(home, ".cc-notes"); got != want {
		t.Fatalf("Root = %q, want %q", got, want)
	}
}

func TestRootRejectsInexactOverride(t *testing.T) {
	for _, value := range []string{"", "relative/state", "/tmp/state/", "/tmp/./state"} {
		t.Setenv(ccnhome.Env, value)
		if _, err := ccnhome.Root(); err == nil {
			t.Fatalf("Root with %s=%q returned no error", ccnhome.Env, value)
		}
	}
}

func TestRepoPaths(t *testing.T) {
	root := t.TempDir()
	t.Setenv(ccnhome.Env, root)

	for _, tc := range []struct {
		name string
		fn   func() (string, error)
		want string
	}{
		{"socket", ccnhome.SocketPath, filepath.Join(root, "daemon.sock")},
		{"log", ccnhome.LogPath, filepath.Join(root, "daemon.log")},
		{"service", ccnhome.ServiceDir, filepath.Join(root, "service")},
		{"models", ccnhome.ModelsDir, filepath.Join(root, "models")},
	} {
		got, err := tc.fn()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got != tc.want {
			t.Errorf("%s = %q, want %q", tc.name, got, tc.want)
		}
	}

	dir := gittest.InitRepo(t)
	_, commonDir := gittest.Dirs(t, dir)
	repo, err := ccnhome.ForRepo(commonDir)
	if err != nil {
		t.Fatalf("ForRepo: %v", err)
	}
	if len(repo.Key) != 32 {
		t.Fatalf("key %q has %d characters, want 32", repo.Key, len(repo.Key))
	}
	if want := filepath.Join(root, "repos", repo.Key); repo.Dir != want {
		t.Fatalf("Dir = %q, want %q", repo.Dir, want)
	}
	for _, tc := range []struct{ got, want string }{
		{repo.Graph(), filepath.Join(repo.Dir, "graph-v1")},
		{repo.Embed(), filepath.Join(repo.Dir, "embed-v1")},
		{repo.QueuePending(), filepath.Join(repo.Dir, "queue-v1", "pending")},
		{repo.QueueFailed(), filepath.Join(repo.Dir, "queue-v1", "failed")},
		{repo.InfoPath(), filepath.Join(repo.Dir, "repo.json")},
	} {
		if tc.got != tc.want {
			t.Errorf("path = %q, want %q", tc.got, tc.want)
		}
	}
}

// TestRepoKeyRepositoryLayouts reproduces the five repository layouts the S3
// spike measured — a normal checkout, its linked worktree, a bare repository,
// a submodule checkout, and a symlinked alias of the normal checkout — and
// pins the two properties the key exists to provide: every view of one
// repository shares a key, and distinct repositories do not.
func TestRepoKeyRepositoryLayouts(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	layouts := initLayouts(t)

	keys := make(map[string]string, len(layouts))
	raw := make(map[string]string, len(layouts))
	for name, dir := range layouts {
		_, commonDir := gittest.Dirs(t, dir)
		key, err := ccnhome.RepoKey(commonDir)
		if err != nil {
			t.Fatalf("RepoKey(%s): %v", name, err)
		}
		keys[name] = key
		raw[name] = unresolvedKey(t, commonDir)
	}

	for _, pair := range [][2]string{{"normal", "linked"}, {"normal", "alias"}} {
		if keys[pair[0]] != keys[pair[1]] {
			t.Errorf("%s and %s keys differ: %s != %s", pair[0], pair[1], keys[pair[0]], keys[pair[1]])
		}
	}
	if raw["normal"] == raw["alias"] {
		t.Errorf("unresolved keys for normal and its symlink alias match: %s", raw["normal"])
	}
	for _, pair := range [][2]string{{"normal", "bare"}, {"normal", "submodule"}, {"bare", "submodule"}} {
		if keys[pair[0]] == keys[pair[1]] {
			t.Errorf("%s and %s keys collide: %s", pair[0], pair[1], keys[pair[0]])
		}
	}
}

func TestRepoKeyIsStableAcrossRelativePaths(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	dir := gittest.InitRepo(t)
	_, commonDir := gittest.Dirs(t, dir)

	absolute, err := ccnhome.RepoKey(commonDir)
	if err != nil {
		t.Fatalf("RepoKey: %v", err)
	}
	nested, err := ccnhome.RepoKey(filepath.Join(commonDir, "objects", ".."))
	if err != nil {
		t.Fatalf("RepoKey nested: %v", err)
	}
	if absolute != nested {
		t.Fatalf("keys differ across path spellings: %s != %s", absolute, nested)
	}
}

func TestRepoKeyRejectsMissingDirectory(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	if _, err := ccnhome.RepoKey(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Fatal("RepoKey on a missing directory returned no error")
	}
}

func TestRecordWorktreeUnionsWorktrees(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	layouts := initLayouts(t)
	_, commonDir := gittest.Dirs(t, layouts["normal"])

	repo, err := ccnhome.RecordWorktree(commonDir, layouts["normal"])
	if err != nil {
		t.Fatalf("RecordWorktree: %v", err)
	}
	linked, err := ccnhome.RecordWorktree(commonDir, layouts["linked"])
	if err != nil {
		t.Fatalf("RecordWorktree linked: %v", err)
	}
	if linked.Key != repo.Key {
		t.Fatalf("linked worktree key %s, want %s", linked.Key, repo.Key)
	}

	info, err := repo.ReadInfo()
	if err != nil {
		t.Fatalf("ReadInfo: %v", err)
	}
	if want := resolve(t, commonDir); info.CommonDir != want {
		t.Errorf("CommonDir = %q, want %q", info.CommonDir, want)
	}
	want := []string{resolve(t, layouts["linked"]), resolve(t, layouts["normal"])}
	slices.Sort(want)
	if !slices.Equal(info.Worktrees, want) {
		t.Fatalf("Worktrees = %v, want %v", info.Worktrees, want)
	}

	if _, err := ccnhome.RecordWorktree(commonDir, layouts["normal"]); err != nil {
		t.Fatalf("RecordWorktree repeat: %v", err)
	}
	repeated, err := repo.ReadInfo()
	if err != nil {
		t.Fatalf("ReadInfo repeat: %v", err)
	}
	if !slices.Equal(repeated.Worktrees, want) {
		t.Fatalf("Worktrees after repeat = %v, want %v", repeated.Worktrees, want)
	}
}

func TestRecordWorktreeBareRepository(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	bare := gittest.InitBare(t)
	_, commonDir := gittest.Dirs(t, bare)

	repo, err := ccnhome.RecordWorktree(commonDir, "")
	if err != nil {
		t.Fatalf("RecordWorktree: %v", err)
	}
	info, err := repo.ReadInfo()
	if err != nil {
		t.Fatalf("ReadInfo: %v", err)
	}
	if len(info.Worktrees) != 0 {
		t.Fatalf("Worktrees = %v, want none", info.Worktrees)
	}
	if want := resolve(t, commonDir); info.CommonDir != want {
		t.Fatalf("CommonDir = %q, want %q", info.CommonDir, want)
	}
}

// TestDirsFeedsRepoKey pins the composition the daemon actually runs: the
// common directory gitcmd reports for a linked worktree keys the same index as
// the one it reports for the main checkout.
func TestDirsFeedsRepoKey(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	layouts := initLayouts(t)
	ctx := t.Context()

	_, mainCommon, err := (gitcmd.Git{Dir: layouts["normal"]}).Dirs(ctx)
	if err != nil {
		t.Fatalf("Dirs(normal): %v", err)
	}
	_, linkedCommon, err := (gitcmd.Git{Dir: layouts["linked"]}).Dirs(ctx)
	if err != nil {
		t.Fatalf("Dirs(linked): %v", err)
	}
	mainKey, err := ccnhome.RepoKey(mainCommon)
	if err != nil {
		t.Fatalf("RepoKey(normal): %v", err)
	}
	linkedKey, err := ccnhome.RepoKey(linkedCommon)
	if err != nil {
		t.Fatalf("RepoKey(linked): %v", err)
	}
	if mainKey != linkedKey {
		t.Fatalf("keys differ: %s (%s) != %s (%s)", mainKey, mainCommon, linkedKey, linkedCommon)
	}
}

// initLayouts builds the five repository layouts under one temporary root and
// returns their working directories by name.
func initLayouts(t *testing.T) map[string]string {
	t.Helper()
	gittest.ScrubEnv(t)
	root := t.TempDir()

	normal := filepath.Join(root, "normal")
	initLayoutRepo(t, normal)
	gittest.Git(t, normal, "commit", "-q", "--allow-empty", "-m", "base")

	linked := filepath.Join(root, "linked")
	gittest.Git(t, normal, "worktree", "add", "-q", "-b", "layout-linked", linked)

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

	alias := filepath.Join(root, "normal-alias")
	if err := os.Symlink(normal, alias); err != nil {
		t.Fatalf("symlink repo: %v", err)
	}

	return map[string]string{
		"normal":    normal,
		"linked":    linked,
		"bare":      bare,
		"submodule": filepath.Join(super, "module"),
		"alias":     alias,
	}
}

func initLayoutRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatalf("mkdir repo: %v", err)
	}
	gittest.Git(t, dir, "init", "-q", "-b", "main")
	gittest.Git(t, dir, "config", "user.name", "Test User")
	gittest.Git(t, dir, "config", "user.email", "test@example.com")
}

func resolve(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %q: %v", path, err)
	}
	return resolved
}

// unresolvedKey is RepoKey without the symlink resolution: the key the layout
// would carry if filepath.Abs alone were trusted.
func unresolvedKey(t *testing.T, commonDir string) string {
	t.Helper()
	abs, err := filepath.Abs(commonDir)
	if err != nil {
		t.Fatalf("absolute %q: %v", commonDir, err)
	}
	sum := sha256.Sum256([]byte("cc-notes.index.v1\x00" + abs))
	return hex.EncodeToString(sum[:])[:32]
}

func TestKeyDomainIsPinned(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	dir := t.TempDir()
	got, err := ccnhome.RepoKey(dir)
	if err != nil {
		t.Fatalf("RepoKey: %v", err)
	}
	sum := sha256.Sum256([]byte("cc-notes.index.v1\x00" + resolve(t, dir)))
	if want := hex.EncodeToString(sum[:])[:32]; got != want {
		t.Fatalf("RepoKey = %s, want %s", got, want)
	}
	if strings.ToLower(got) != got {
		t.Fatalf("RepoKey %q is not lower hex", got)
	}
}
