package ccnhome_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
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

	before := stat(t, repo.InfoPath())
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
	if !os.SameFile(before, stat(t, repo.InfoPath())) {
		t.Fatal("RecordWorktree rewrote an unchanged descriptor; it must write nothing")
	}
	if _, err := ccnhome.RecordWorktree(commonDir, layouts["submodule"]); err != nil {
		t.Fatalf("RecordWorktree third worktree: %v", err)
	}
	if os.SameFile(before, stat(t, repo.InfoPath())) {
		t.Fatal("RecordWorktree left the descriptor in place after adding a worktree")
	}
}

func TestListReportsRegisteredRepositories(t *testing.T) {
	root := t.TempDir()
	t.Setenv(ccnhome.Env, root)
	checkout := gittest.InitRepo(t)
	bareDir := gittest.InitBare(t)
	_, normalCommon := gittest.Dirs(t, checkout)
	_, bareCommon := gittest.Dirs(t, bareDir)

	normal, err := ccnhome.RecordWorktree(normalCommon, checkout)
	if err != nil {
		t.Fatalf("RecordWorktree normal: %v", err)
	}
	bare, err := ccnhome.RecordWorktree(bareCommon, "")
	if err != nil {
		t.Fatalf("RecordWorktree bare: %v", err)
	}
	// A RecordWorktree between MkdirAll and the descriptor rename looks exactly
	// like this, and must not surface as a registered repository.
	if err := os.MkdirAll(filepath.Join(root, "repos", "0000000000000000"), 0o750); err != nil {
		t.Fatalf("mkdir descriptor-less entry: %v", err)
	}

	entries, err := ccnhome.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []ccnhome.Entry{
		{Repo: normal, Info: ccnhome.Info{CommonDir: resolve(t, normalCommon), Worktrees: []string{resolve(t, checkout)}}},
		{Repo: bare, Info: ccnhome.Info{CommonDir: resolve(t, bareCommon)}},
	}
	slices.SortFunc(want, func(a, b ccnhome.Entry) int { return strings.Compare(a.Repo.Key, b.Repo.Key) })
	if len(entries) != len(want) {
		t.Fatalf("List returned %d entries, want %d: %+v", len(entries), len(want), entries)
	}
	for i, got := range entries {
		if got.Repo != want[i].Repo || got.Info.CommonDir != want[i].Info.CommonDir || !slices.Equal(got.Info.Worktrees, want[i].Info.Worktrees) {
			t.Errorf("entry %d = %+v, want %+v", i, got, want[i])
		}
	}
}

func TestListOnEmptyRoot(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	entries, err := ccnhome.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("List on a root with no repos/ returned %+v, want none", entries)
	}
}

// TestListIgnoresNonDirectoryEntries pins enumeration against a stray file in
// repos/ — a .DS_Store, a leftover repo.json.* temp. Reading a descriptor
// through it fails with ENOTDIR, which is not fs.ErrNotExist, so treating every
// entry as a directory would make List, and with it Reap and the resolver's
// reload, return an error for good.
func TestListIgnoresNonDirectoryEntries(t *testing.T) {
	root := t.TempDir()
	t.Setenv(ccnhome.Env, root)
	checkout := gittest.InitRepo(t)
	_, commonDir := gittest.Dirs(t, checkout)
	repo, err := ccnhome.RecordWorktree(commonDir, checkout)
	if err != nil {
		t.Fatalf("RecordWorktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "repos", ".DS_Store"), []byte("stray"), 0o600); err != nil {
		t.Fatalf("plant stray file: %v", err)
	}

	entries, err := ccnhome.List()
	if err != nil {
		t.Fatalf("List with a stray file in repos/: %v", err)
	}
	if len(entries) != 1 || entries[0].Repo != repo {
		t.Fatalf("List = %+v, want exactly %+v", entries, repo)
	}
	removed, err := ccnhome.Reap()
	if err != nil {
		t.Fatalf("Reap with a stray file in repos/: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Reap removed %v, want nothing", removed)
	}
}

// TestListSkipsAndReapCollectsACorruptDescriptor pins the registry against a
// descriptor that does not decode. writeInfo syncs before it renames, so this
// package's own crashes cannot leave one, but the file outlives every process
// that touches it: an older binary that published it without that sync, a
// restore or filesystem repair that truncated it, and a hand edit each leave
// short or meaningless bytes in repo.json — a durable artifact of the
// environment, not a state the types could have prevented. Failing the scan on
// one would disable List, Reap and the resolver's reload for every repository
// at once, and Reap, the only cleanup path there is, is built on that same
// scan: the garbage would collect itself never.
func TestListSkipsAndReapCollectsACorruptDescriptor(t *testing.T) {
	const key = "ffffffffffffffff"
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "zero length", body: ""},
		{name: "truncated object", body: `{"common_dir":"/tmp/cl`},
		{name: "not json at all", body: "\x00\x00\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv(ccnhome.Env, root)
			checkout := gittest.InitRepo(t)
			_, commonDir := gittest.Dirs(t, checkout)
			live, err := ccnhome.RecordWorktree(commonDir, checkout)
			if err != nil {
				t.Fatalf("RecordWorktree: %v", err)
			}
			corrupt := filepath.Join(root, "repos", key)
			if err := os.MkdirAll(corrupt, 0o750); err != nil {
				t.Fatalf("mkdir corrupt entry: %v", err)
			}
			if err := os.WriteFile(filepath.Join(corrupt, "repo.json"), []byte(tc.body), 0o600); err != nil {
				t.Fatalf("plant corrupt descriptor: %v", err)
			}

			entries, err := ccnhome.List()
			if err != nil {
				t.Fatalf("List with a corrupt descriptor: %v", err)
			}
			if len(entries) != 1 || entries[0].Repo != live {
				t.Fatalf("List = %+v, want exactly %+v", entries, live)
			}
			removed, err := ccnhome.Reap()
			if err != nil {
				t.Fatalf("Reap with a corrupt descriptor: %v", err)
			}
			if !slices.Equal(removed, []string{key}) {
				t.Fatalf("Reap removed %v, want %v", removed, []string{key})
			}
			if _, err := os.Stat(corrupt); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("stat collected %s = %v, want not-exist", corrupt, err)
			}
			if _, err := os.Stat(live.InfoPath()); err != nil {
				t.Fatalf("live repository was reaped: %v", err)
			}
		})
	}
}

// TestRecordWorktreeRepublishesOverACorruptDescriptor pins the write path to
// the same reading of an undecodable descriptor the scan takes. List skipping
// one only hides the repository; the write is the one path that can put a good
// descriptor back, so refusing there would wedge the repository until Reap
// deleted the directory — and Reap runs on a schedule this build does not have
// yet.
func TestRecordWorktreeRepublishesOverACorruptDescriptor(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "zero length", body: ""},
		{name: "truncated object", body: `{"common_dir":"/tmp/cl`},
		{name: "not json at all", body: "\x00\x00\x00"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(ccnhome.Env, t.TempDir())
			checkout := gittest.InitRepo(t)
			_, commonDir := gittest.Dirs(t, checkout)
			repo, err := ccnhome.RecordWorktree(commonDir, checkout)
			if err != nil {
				t.Fatalf("RecordWorktree: %v", err)
			}
			if err := os.WriteFile(repo.InfoPath(), []byte(tc.body), 0o600); err != nil {
				t.Fatalf("plant corrupt descriptor: %v", err)
			}
			if _, err := repo.ReadInfo(); err == nil {
				t.Fatalf("planted descriptor %q still decodes", tc.body)
			}

			again, err := ccnhome.RecordWorktree(commonDir, checkout)
			if err != nil {
				t.Fatalf("RecordWorktree over a corrupt descriptor: %v", err)
			}
			if again != repo {
				t.Fatalf("RecordWorktree = %+v, want %+v", again, repo)
			}
			info, err := repo.ReadInfo()
			if err != nil {
				t.Fatalf("ReadInfo after republish: %v", err)
			}
			if want := resolve(t, commonDir); info.CommonDir != want {
				t.Errorf("CommonDir = %q, want %q", info.CommonDir, want)
			}
			if want := []string{resolve(t, checkout)}; !slices.Equal(info.Worktrees, want) {
				t.Errorf("Worktrees = %v, want %v", info.Worktrees, want)
			}
			entries, err := ccnhome.List()
			if err != nil {
				t.Fatalf("List after republish: %v", err)
			}
			if len(entries) != 1 || entries[0].Repo != repo {
				t.Fatalf("List = %+v, want exactly %+v", entries, repo)
			}
		})
	}
}

// TestRecordWorktreeReleasesAStaleClaim pins the one-claim-per-path rule: a
// checkout re-created as a worktree of a different repository leaves the old
// entry claiming it, and load() would award the path to whichever key sorts
// higher.
func TestRecordWorktreeReleasesAStaleClaim(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	first := gittest.InitRepo(t)
	second := gittest.InitRepo(t)
	_, firstCommon := gittest.Dirs(t, first)
	_, secondCommon := gittest.Dirs(t, second)
	shared := resolve(t, first)

	firstRepo, err := ccnhome.RecordWorktree(firstCommon, first)
	if err != nil {
		t.Fatalf("RecordWorktree first: %v", err)
	}
	secondRepo, err := ccnhome.RecordWorktree(secondCommon, second)
	if err != nil {
		t.Fatalf("RecordWorktree second: %v", err)
	}
	if _, err := ccnhome.RecordWorktree(secondCommon, first); err != nil {
		t.Fatalf("RecordWorktree second claiming the first checkout: %v", err)
	}

	firstInfo, err := firstRepo.ReadInfo()
	if err != nil {
		t.Fatalf("ReadInfo first: %v", err)
	}
	if len(firstInfo.Worktrees) != 0 {
		t.Errorf("first repository still claims %v, want the claim released", firstInfo.Worktrees)
	}
	secondInfo, err := secondRepo.ReadInfo()
	if err != nil {
		t.Fatalf("ReadInfo second: %v", err)
	}
	want := []string{resolve(t, second), shared}
	slices.Sort(want)
	if !slices.Equal(secondInfo.Worktrees, want) {
		t.Errorf("second repository claims %v, want %v", secondInfo.Worktrees, want)
	}
}

// TestReapRemovesRepositoriesThatNoLongerExist pins the sweep nothing else
// owns: GCLocal only ever sees the repository it runs in, so a deleted clone's
// derived state is permanent until Reap collects it.
func TestReapRemovesRepositoriesThatNoLongerExist(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	checkout := gittest.InitRepo(t)
	bareDir := gittest.InitBare(t)
	_, normalCommon := gittest.Dirs(t, checkout)
	_, bareCommon := gittest.Dirs(t, bareDir)

	normal, err := ccnhome.RecordWorktree(normalCommon, checkout)
	if err != nil {
		t.Fatalf("RecordWorktree normal: %v", err)
	}
	bare, err := ccnhome.RecordWorktree(bareCommon, "")
	if err != nil {
		t.Fatalf("RecordWorktree bare: %v", err)
	}
	if err := os.RemoveAll(bareDir); err != nil {
		t.Fatalf("remove bare repo: %v", err)
	}

	removed, err := ccnhome.Reap()
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if !slices.Equal(removed, []string{bare.Key}) {
		t.Fatalf("Reap removed %v, want %v", removed, []string{bare.Key})
	}
	if _, err := os.Stat(bare.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat reaped %s = %v, want not-exist", bare.Dir, err)
	}
	if _, err := os.Stat(normal.InfoPath()); err != nil {
		t.Fatalf("live repository was reaped: %v", err)
	}

	again, err := ccnhome.Reap()
	if err != nil {
		t.Fatalf("Reap again: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second Reap removed %v, want nothing", again)
	}
}

// TestReapCollectsACommonDirBehindAFile pins the one absence os does not report
// as fs.ErrNotExist: a clone whose directory was replaced by a regular file —
// unpacked over, or restored from a backup as an archive — leaves the recorded
// common directory behind a non-directory path component, which stats as
// ENOTDIR. Reading that as an absence it cannot prove would abort the whole
// sweep at the first such repository, so every other deleted clone's derived
// state would survive alongside it.
func TestReapCollectsACommonDirBehindAFile(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	live := gittest.InitRepo(t)
	replaced := gittest.InitRepo(t)
	_, liveCommon := gittest.Dirs(t, live)
	_, replacedCommon := gittest.Dirs(t, replaced)

	liveRepo, err := ccnhome.RecordWorktree(liveCommon, live)
	if err != nil {
		t.Fatalf("RecordWorktree live: %v", err)
	}
	replacedRepo, err := ccnhome.RecordWorktree(replacedCommon, replaced)
	if err != nil {
		t.Fatalf("RecordWorktree replaced: %v", err)
	}
	if err := os.RemoveAll(replaced); err != nil {
		t.Fatalf("remove the clone: %v", err)
	}
	if err := os.WriteFile(replaced, []byte("an archive where the clone was"), 0o600); err != nil {
		t.Fatalf("plant a file where the clone was: %v", err)
	}
	if _, err := os.Lstat(replacedCommon); !errors.Is(err, syscall.ENOTDIR) {
		t.Fatalf("lstat %s = %v, want ENOTDIR", replacedCommon, err)
	}

	removed, err := ccnhome.Reap()
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if !slices.Equal(removed, []string{replacedRepo.Key}) {
		t.Fatalf("Reap removed %v, want %v", removed, []string{replacedRepo.Key})
	}
	if _, err := os.Stat(replacedRepo.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stat reaped %s = %v, want not-exist", replacedRepo.Dir, err)
	}
	if _, err := os.Stat(liveRepo.InfoPath()); err != nil {
		t.Fatalf("live repository was reaped: %v", err)
	}
}

// TestReapKeepsRepositoriesItCannotProveGone pins the bar for a destructive
// sweep: Reap deletes an index that costs a full rebuild, so only a common
// directory that is definitively absent earns deletion. A checkout that is
// present but damaged, and one behind a directory the process cannot traverse,
// both survive.
func TestReapKeepsRepositoriesItCannotProveGone(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	damaged := gittest.InitRepo(t)
	_, damagedCommon := gittest.Dirs(t, damaged)
	damagedRepo, err := ccnhome.RecordWorktree(damagedCommon, damaged)
	if err != nil {
		t.Fatalf("RecordWorktree damaged: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(damagedCommon, "objects")); err != nil {
		t.Fatalf("strip objects: %v", err)
	}

	removed, err := ccnhome.Reap()
	if err != nil {
		t.Fatalf("Reap: %v", err)
	}
	if len(removed) != 0 {
		t.Fatalf("Reap removed %v, want nothing: the checkout is still there", removed)
	}
	if _, err := os.Stat(damagedRepo.InfoPath()); err != nil {
		t.Fatalf("damaged-but-present repository was reaped: %v", err)
	}

	if os.Getuid() == 0 {
		t.Skip("root traverses an unreadable directory, so the permission wall cannot be built")
	}
	walled := t.TempDir()
	checkout := filepath.Join(walled, "repo")
	if err := os.Mkdir(checkout, 0o750); err != nil {
		t.Fatalf("mkdir walled checkout: %v", err)
	}
	gittest.Git(t, checkout, "init", "-q", "-b", "main")
	_, walledCommon := gittest.Dirs(t, checkout)
	walledRepo, err := ccnhome.RecordWorktree(walledCommon, checkout)
	if err != nil {
		t.Fatalf("RecordWorktree walled: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(walled, 0o750) })
	if err := os.Chmod(walled, 0); err != nil {
		t.Fatalf("seal the directory: %v", err)
	}

	removed, err = ccnhome.Reap()
	if err == nil {
		t.Fatalf("Reap over an unreadable common directory returned no error (removed %v)", removed)
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("Reap error = %v, want a permission error", err)
	}
	if err := os.Chmod(walled, 0o750); err != nil {
		t.Fatalf("unseal the directory: %v", err)
	}
	if _, err := os.Stat(walledRepo.InfoPath()); err != nil {
		t.Fatalf("unreachable repository was reaped: %v", err)
	}
}

func TestIsGitDir(t *testing.T) {
	t.Setenv(ccnhome.Env, t.TempDir())
	layouts := initLayouts(t)
	_, normalCommon := gittest.Dirs(t, layouts["normal"])
	_, submoduleCommon := gittest.Dirs(t, layouts["submodule"])
	empty := t.TempDir()

	for _, tc := range []struct {
		name string
		dir  string
		want bool
	}{
		{"non-bare repository's git directory", normalCommon, true},
		{"bare repository", layouts["bare"], true},
		{"submodule's git directory", submoduleCommon, true},
		{"worktree root", layouts["normal"], false},
		{"directory that is not a repository", empty, false},
		{"directory that does not exist", filepath.Join(empty, "absent"), false},
	} {
		if got := ccnhome.IsGitDir(tc.dir); got != tc.want {
			t.Errorf("%s: IsGitDir(%q) = %t, want %t", tc.name, tc.dir, got, tc.want)
		}
	}
}

func stat(t *testing.T, path string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %q: %v", path, err)
	}
	return info
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
