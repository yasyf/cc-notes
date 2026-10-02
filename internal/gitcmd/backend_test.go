package gitcmd_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitcmd"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

const lfsURL = "https://git-server.com/foo/bar.git/info/lfs"

// repoCensus is everything a misrouted command could change in a repository:
// its refs, its config bytes, and its object count.
type repoCensus struct {
	refs, config, objects string
}

func censusOf(t *testing.T, dir string) repoCensus {
	t.Helper()
	//nolint:gosec // G304: reads the test repository's own config.
	config, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return repoCensus{
		refs:    gittest.Git(t, dir, "for-each-ref", "--format=%(refname) %(objectname)"),
		config:  string(config),
		objects: gittest.Git(t, dir, "count-objects", "-v"),
	}
}

func canonicalPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("eval symlinks %q: %v", path, err)
	}
	return resolved
}

// TestBackendIgnoresInheritedRouting drives every records verb on a Backend
// handle while one inherited routing variable points at a second, "thin"
// repository. The source must answer or be mutated, the thin repository must
// be byte-for-byte untouched, and a checkout handle under the same variable
// must still follow it.
func TestBackendIgnoresInheritedRouting(t *testing.T) {
	source := gittest.InitRepo(t)
	gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "s1")
	sourceHead := resolve(t, source, "HEAD")
	upstream := t.TempDir()
	gittest.Git(t, upstream, "init", "-q", "-b", "main")
	gittest.Git(t, upstream, "config", "user.name", "Upstream User")
	gittest.Git(t, upstream, "config", "user.email", "upstream@example.com")
	gittest.Git(t, upstream, "commit", "-q", "--allow-empty", "-m", "u1")
	upstreamHead := resolve(t, upstream, "HEAD")
	gittest.Git(t, source, "remote", "add", "upstream", upstream)
	gittest.Git(t, source, "config", "cc-notes.probe", "source")
	sourceLog := verbLogHelper(t, gitcmd.Git{Dir: source}, "alice", "s3cret")

	thin := t.TempDir()
	gittest.Git(t, thin, "init", "-q", "-b", "main")
	gittest.Git(t, thin, "config", "user.name", "Thin User")
	gittest.Git(t, thin, "config", "user.email", "thin@example.com")
	gittest.Git(t, thin, "commit", "-q", "--allow-empty", "-m", "t1")
	thinHead := resolve(t, thin, "HEAD")
	gittest.Git(t, thin, "remote", "add", "thinremote", upstream)
	gittest.Git(t, thin, "config", "cc-notes.probe", "thin")
	gittest.Git(t, thin, "update-ref", "refs/cc-notes/notes/decoy", string(thinHead))
	thinLog := verbLogHelper(t, gitcmd.Git{Dir: thin}, "bob", "hunter2")
	thinGit := filepath.Join(thin, ".git")

	sourceGit := filepath.Join(source, ".git")
	wantCommon := canonicalPath(t, sourceGit)

	rows := []struct {
		key, value string
	}{
		{"GIT_DIR", thinGit},
		{"GIT_COMMON_DIR", thinGit},
		{"GIT_OBJECT_DIRECTORY", filepath.Join(thinGit, "objects")},
		{"GIT_ALTERNATE_OBJECT_DIRECTORIES", filepath.Join(thinGit, "objects")},
		{"GIT_WORK_TREE", thin},
		{"GIT_INDEX_FILE", filepath.Join(thinGit, "index")},
		{"GIT_CONFIG", filepath.Join(thinGit, "config")},
		{"GIT_REFERENCE_BACKEND", "reftable"},
		{"GIT_NAMESPACE", "elsewhere"},
		{"GIT_CEILING_DIRECTORIES", source},
	}
	for _, row := range rows {
		t.Run(row.key, func(t *testing.T) {
			before := censusOf(t, thin)
			t.Setenv(row.key, row.value)
			ctx := t.Context()
			g := gitcmd.Backend(sourceGit, filepath.Dir(sourceGit))
			ref := "refs/cc-notes/notes/" + strings.ToLower(row.key)

			_, commonDir, _, err := g.Dirs(ctx)
			if err != nil {
				t.Fatalf("Dirs: %v", err)
			}
			if got := canonicalPath(t, commonDir); got != wantCommon {
				t.Fatalf("Dirs common dir = %q, want the source %q", got, wantCommon)
			}
			if _, discovered, _, err := gitcmd.Discover(ctx, source); err != nil || canonicalPath(t, discovered) != wantCommon {
				t.Fatalf("Discover(source) = %q, %v; want the source %q", discovered, err, wantCommon)
			}
			if err := g.UpdateRefs(ctx, []gitcmd.RefUpdate{{Ref: ref, New: sourceHead}}); err != nil {
				t.Fatalf("UpdateRefs in the source: %v", err)
			}
			if err := g.UpdateRefs(ctx, []gitcmd.RefUpdate{{Ref: ref + "-thin-only", New: thinHead}}); err == nil {
				t.Fatalf("UpdateRefs accepted %s, an object only the thin repository holds", thinHead)
			}
			refs, err := g.Refs(ctx, "refs/cc-notes/")
			if err != nil {
				t.Fatalf("Refs: %v", err)
			}
			if refs[ref] != sourceHead {
				t.Fatalf("Refs[%s] = %s, want %s", ref, refs[ref], sourceHead)
			}
			if _, decoy := refs["refs/cc-notes/notes/decoy"]; decoy {
				t.Fatalf("Refs listed the thin repository's decoy: %v", refs)
			}
			first, err := g.FirstRef(ctx, "refs/cc-notes/")
			if err != nil {
				t.Fatalf("FirstRef: %v", err)
			}
			if !strings.HasPrefix(first, "refs/cc-notes/notes/git_") {
				t.Fatalf("FirstRef = %q, want a source ref", first)
			}
			probe, err := g.ConfigGet(ctx, "cc-notes.probe")
			if err != nil {
				t.Fatalf("ConfigGet: %v", err)
			}
			if probe != "source" {
				t.Fatalf("ConfigGet(cc-notes.probe) = %q, want %q", probe, "source")
			}
			if err := g.ConfigSet(ctx, "cc-notes.row", row.key); err != nil {
				t.Fatalf("ConfigSet: %v", err)
			}
			if got, err := g.ConfigGet(ctx, "cc-notes.row"); err != nil || got != row.key {
				t.Fatalf("ConfigGet(cc-notes.row) = %q, %v; want %q", got, err, row.key)
			}
			if err := g.Fetch(ctx, "upstream", "+refs/heads/main:refs/cc-notes-sync/upstream/main"); err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			remotes, err := g.Remotes(ctx)
			if err != nil {
				t.Fatalf("Remotes: %v", err)
			}
			if !slices.Equal(remotes, []string{"upstream"}) {
				t.Fatalf("Remotes = %q, want [upstream]", remotes)
			}
			cred, err := g.CredentialFill(ctx, lfsURL)
			if err != nil {
				t.Fatalf("CredentialFill: %v", err)
			}
			if cred != (gitcmd.Credential{Username: "alice", Password: "s3cret"}) {
				t.Fatalf("CredentialFill = %+v, want the source helper's alice/s3cret", cred)
			}

			if err := os.Unsetenv(row.key); err != nil {
				t.Fatalf("unset %s: %v", row.key, err)
			}
			if after := censusOf(t, thin); after != before {
				t.Fatalf("thin repository changed under %s=%s:\nbefore %+v\nafter  %+v", row.key, row.value, before, after)
			}
			if got := resolve(t, source, "refs/cc-notes-sync/upstream/main"); got != upstreamHead {
				t.Fatalf("source fetched %s, want %s", got, upstreamHead)
			}
			if got := resolve(t, source, ref); got != sourceHead {
				t.Fatalf("source ref %s = %s, want %s", ref, got, sourceHead)
			}
			if verbs := loggedVerbs(t, thinLog); verbs != nil {
				t.Fatalf("thin credential helper ran %q", verbs)
			}
		})
	}
	if verbs := loggedVerbs(t, sourceLog); len(verbs) != len(rows) {
		t.Fatalf("source credential helper ran %d times, want %d: %q", len(verbs), len(rows), verbs)
	}

	t.Run("control: a checkout handle still honours GIT_DIR", func(t *testing.T) {
		t.Setenv("GIT_DIR", filepath.Join(source, ".git"))
		_, commonDir, _, err := (gitcmd.Git{Dir: thin}).Dirs(t.Context())
		if err != nil {
			t.Fatalf("Dirs: %v", err)
		}
		if got := canonicalPath(t, commonDir); got != wantCommon {
			t.Fatalf("checkout handle common dir = %q, want GIT_DIR's %q", got, wantCommon)
		}
	})
}

// TestConfigFileSetExactFile proves the binding write lands in the named file
// under both handle kinds, even with GIT_CONFIG and GIT_DIR pointing elsewhere,
// and that a git-escaped value reads back byte-exact.
func TestConfigFileSetExactFile(t *testing.T) {
	repo := gittest.InitRepo(t)
	other := gittest.InitRepo(t)
	otherConfig := filepath.Join(other, ".git", "config")
	t.Setenv("GIT_CONFIG", otherConfig)
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	//nolint:gosec // G304: reads the test repository's own config.
	otherBefore, err := os.ReadFile(otherConfig)
	if err != nil {
		t.Fatalf("read other config: %v", err)
	}
	target := filepath.Join(repo, ".git", "config")
	value := `{"version":1,"commonDir":"/a b;#\"\\x","device":1,"inode":2}`
	ctx := t.Context()
	for _, tc := range []struct {
		name string
		g    gitcmd.Git
		key  string
	}{
		{"backend handle", gitcmd.Backend(filepath.Join(repo, ".git"), repo), "cc-notes.storage"},
		{"checkout handle", gitcmd.Git{Dir: repo}, "cc-notes.checkout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.g.ConfigFileSet(ctx, target, tc.key, value); err != nil {
				t.Fatalf("ConfigFileSet: %v", err)
			}
			if got := gittest.Git(t, repo, "config", "--file", target, "--get", tc.key); got != value {
				t.Fatalf("%s in %s = %q, want %q", tc.key, target, got, value)
			}
		})
	}
	//nolint:gosec // G304: reads the test repository's own config.
	otherAfter, err := os.ReadFile(otherConfig)
	if err != nil {
		t.Fatalf("read other config: %v", err)
	}
	if string(otherAfter) != string(otherBefore) {
		t.Fatalf("ConfigFileSet wrote into GIT_CONFIG's file:\n%s", otherAfter)
	}
}

// TestBackendNeverDiscovers pins S1: a Backend handle opens exactly the git
// directory it was given. A nested .git file inside that directory redirects
// discovery from there to another repository; it moves neither the ref
// transaction nor the credential helper off the source.
func TestBackendNeverDiscovers(t *testing.T) {
	source := gittest.InitRepo(t)
	gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "s1")
	sourceHead := resolve(t, source, "HEAD")
	sourceGit := filepath.Join(source, ".git")
	sourceLog := verbLogHelper(t, gitcmd.Git{Dir: source}, "alice", "s3cret")
	victim := gittest.InitRepo(t)
	gittest.Git(t, victim, "commit", "-q", "--allow-empty", "-m", "v1")
	victimGit := filepath.Join(victim, ".git")
	victimLog := verbLogHelper(t, gitcmd.Git{Dir: victim}, "mallory", "pwned")
	if err := os.WriteFile(filepath.Join(sourceGit, ".git"), []byte("gitdir: "+victimGit+"\n"), 0o600); err != nil {
		t.Fatalf("plant nested .git file: %v", err)
	}
	ctx := t.Context()
	if _, common, _, err := (gitcmd.Git{Dir: sourceGit}).Dirs(ctx); err != nil || canonicalPath(t, common) != canonicalPath(t, victimGit) {
		t.Skipf("discovery from inside %s is not redirected by the nested .git file (common %q, %v): the fixture is inert on this git", sourceGit, common, err)
	}
	before := censusOf(t, victim)

	g := gitcmd.Backend(sourceGit, filepath.Dir(sourceGit))
	_, common, _, err := g.Dirs(ctx)
	if err != nil {
		t.Fatalf("Dirs: %v", err)
	}
	if canonicalPath(t, common) != canonicalPath(t, sourceGit) {
		t.Fatalf("Dirs common dir = %q, want %q", common, sourceGit)
	}
	const ref = "refs/cc-notes/notes/pinned"
	if err := g.UpdateRef(ctx, ref, sourceHead, ""); err != nil {
		t.Fatalf("UpdateRef: %v", err)
	}
	if got := resolve(t, source, ref); got != sourceHead {
		t.Fatalf("source %s = %s, want %s", ref, got, sourceHead)
	}
	cred := gitcmd.Credential{Username: "alice", Password: "s3cret"}
	if err := g.CredentialApprove(ctx, lfsURL, cred); err != nil {
		t.Fatalf("CredentialApprove: %v", err)
	}
	if after := censusOf(t, victim); after != before {
		t.Fatalf("victim repository changed:\nbefore %+v\nafter  %+v", before, after)
	}
	if verbs := loggedVerbs(t, victimLog); verbs != nil {
		t.Fatalf("victim credential helper ran %q", verbs)
	}
	if verbs := loggedVerbs(t, sourceLog); !slices.Equal(verbs, []string{"store"}) {
		t.Fatalf("source credential helper ran %q, want [store]", verbs)
	}
}

func objectPresent(t *testing.T, dir string, sha model.SHA) bool {
	t.Helper()
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	cmd := exec.Command("git", "-C", dir, "cat-file", "-e", string(sha))
	cmd.Env = append(os.Environ(), "GIT_NO_LAZY_FETCH=1")
	return cmd.Run() == nil
}

func packCount(t *testing.T, dir string) int {
	t.Helper()
	packs, err := filepath.Glob(filepath.Join(dir, ".git", "objects", "pack", "*.pack"))
	if err != nil {
		t.Fatalf("glob packs: %v", err)
	}
	return len(packs)
}

// TestCommitProbesNeverLazyFetch pins D4: in a promisor-backed partial clone,
// the commit-existence, resolution, and ancestry probes report a commit the
// clone never fetched as missing instead of dialing the promisor remote, on
// both handle kinds, and the object stays absent afterwards.
func TestCommitProbesNeverLazyFetch(t *testing.T) {
	source := gittest.InitRepo(t)
	gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "one")
	gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "two")
	gittest.Git(t, source, "config", "uploadpack.allowFilter", "true")
	thin := t.TempDir()
	gittest.Git(t, filepath.Dir(thin), "clone", "-q", "--no-local", "--filter=blob:none", "file://"+source, thin)
	gittest.Git(t, source, "commit", "-q", "--allow-empty", "-m", "three")
	missing := resolve(t, source, "HEAD")
	head := resolve(t, thin, "HEAD")
	if gittest.Git(t, thin, "config", "--get", "remote.origin.promisor") != "true" {
		t.Fatal("clone is not promisor-backed; the lazy-fetch path is not under test")
	}
	if objectPresent(t, thin, missing) {
		t.Fatalf("%s is already present in the clone", missing)
	}
	packs := packCount(t, thin)

	ctx := t.Context()
	for _, tc := range []struct {
		name string
		g    gitcmd.Git
	}{
		{"checkout handle", gitcmd.Git{Dir: thin}},
		{"backend handle", gitcmd.Backend(filepath.Join(thin, ".git"), thin)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resolved, err := tc.g.ResolveRevs(ctx, []string{string(missing), string(head)})
			if err != nil {
				t.Fatalf("ResolveRevs: %v", err)
			}
			if resolved[string(missing)] != "" || resolved[string(head)] != head {
				t.Fatalf("ResolveRevs = %v, want %s missing and %s present", resolved, missing, head)
			}
			if _, err := tc.g.CommitSHA(ctx, string(missing)); !errors.Is(err, gitcmd.ErrRevNotFound) {
				t.Fatalf("CommitSHA(missing) = %v, want ErrRevNotFound", err)
			}
			if sha, err := tc.g.CommitSHA(ctx, string(head)); err != nil || sha != head {
				t.Fatalf("CommitSHA(head) = %s, %v; want %s", sha, err, head)
			}
			if _, err := tc.g.MergeBase(ctx, string(missing), string(head)); err == nil {
				t.Fatal("MergeBase(missing, head) succeeded; the walk fetched the commit")
			}
			if _, err := tc.g.AncestorSet(ctx, missing); err == nil {
				t.Fatal("AncestorSet(missing) succeeded; the walk fetched the commit")
			}
			if objectPresent(t, thin, missing) {
				t.Fatalf("%s was fetched into the clone by a probe", missing)
			}
			if got := packCount(t, thin); got != packs {
				t.Fatalf("pack count moved from %d to %d: a probe fetched", packs, got)
			}
		})
	}
}
