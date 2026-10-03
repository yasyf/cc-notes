package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/ccnhome"
	"github.com/yasyf/cc-notes/internal/gittest"
)

func refsUnder(t *testing.T, dir string) []string {
	t.Helper()
	out := gittest.Git(t, dir, "for-each-ref", "--format=%(refname)", "refs/cc-notes/")
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// TestSharedStorageCLIUsesThinIdentityAndBranch drives `cc-notes -R <clone>`
// from inside the source: the author, anchors, branch, and script cwd are the
// clone's, and the records land in the source.
func TestSharedStorageCLIUsesThinIdentityAndBranch(t *testing.T) {
	source := initRepo(t)
	if err := os.Unsetenv("CC_NOTES_ACTOR"); err != nil {
		t.Fatal(err)
	}
	gittest.Git(t, source, "config", "user.name", "Source User")
	gittest.Git(t, source, "config", "user.email", "source@example.com")
	commitFile(t, source, "a.go", "source\n")
	thin := gittest.ShallowClone(t, source, 1)
	gittest.Git(t, thin, "config", "user.name", "Thin User")
	gittest.Git(t, thin, "config", "user.email", "thin@example.com")
	gittest.Git(t, thin, "checkout", "-q", "-b", "feature")
	thinHead := commitFile(t, thin, "a.go", "thin\n")
	thinBlob := gittest.Git(t, thin, "rev-parse", "HEAD:a.go")
	mustRun(t, source, "-R", thin, "storage", "bind", "--source", source)
	inThin := func(args ...string) string {
		t.Helper()
		return mustRun(t, source, append([]string{"-R", thin}, args...)...)
	}

	note := mustJSON[noteJSON](t, inThin("show", jsonID(t, inThin("note", "add", "Thin fact", "--body", "from the clone", "--commit", "HEAD", "--path", "a.go", "--json")), "--json"))
	if want := "Thin User <thin@example.com>"; note.Author != want {
		t.Fatalf("note author = %q, want the clone's identity %q", note.Author, want)
	}
	if note.VerifiedCommit == nil || *note.VerifiedCommit != thinHead {
		t.Fatalf("verified_commit = %v, want the clone's HEAD %s", note.VerifiedCommit, thinHead)
	}
	if len(note.Anchors) != 2 || note.Anchors[0].Kind != "commit" || note.Anchors[0].Value != thinHead ||
		note.Anchors[1].Kind != "path" || note.Anchors[1].Value != "a.go" ||
		note.Anchors[1].Witness == nil || *note.Anchors[1].Witness != thinBlob {
		t.Fatalf("anchors = %+v, want commit %s and a.go witnessed at the clone's blob %s", note.Anchors, thinHead, thinBlob)
	}

	task := mustJSON[taskJSON](t, inThin("show", jsonID(t, inThin("task", "add", "Thin task", "--criterion", "runs in the clone", "--json")), "--json"))
	if task.Branch != "feature" {
		t.Fatalf("task branch = %q, want the clone's feature", task.Branch)
	}
	script := filepath.Join(t.TempDir(), "check.sh")
	if err := os.WriteFile(script, []byte("pwd > out"), 0o600); err != nil {
		t.Fatal(err)
	}
	inThin("task", "criterion", "script", task.ID, task.Criteria[0].ID[:7], script)
	inThin("task", "validate", task.ID, "--yes", "--json")
	validated := mustJSON[taskJSON](t, inThin("show", task.ID, "--json"))
	if got := validated.Criteria[0].Status; got != "met" {
		t.Fatalf("criterion status = %q, want met", got)
	}
	//nolint:gosec // G304: reads the file the test's own script wrote.
	raw, err := os.ReadFile(filepath.Join(thin, "out"))
	if err != nil {
		t.Fatalf("read the script's output in the clone: %v", err)
	}
	if got, want := canonicalPath(t, strings.TrimSpace(string(raw))), canonicalPath(t, thin); got != want {
		t.Fatalf("validation script ran in %s, want the clone %s", got, want)
	}
	if _, err := os.Stat(filepath.Join(source, "out")); !os.IsNotExist(err) {
		t.Fatalf("validation script wrote into the source (stat err %v)", err)
	}

	if got := refsUnder(t, thin); got != nil {
		t.Fatalf("clone refs/cc-notes/ = %v, want none", got)
	}
	want := []string{"refs/cc-notes/notes/" + note.ID, "refs/cc-notes/tasks/" + task.ID}
	if got := refsUnder(t, source); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("source refs/cc-notes/ = %v, want %v", got, want)
	}
}

// TestSharedStorageKGKeysByCheckout pins checkout registration and the graph
// index of a bound clone to the clone's own common directory, never the source's.
func TestSharedStorageKGKeysByCheckout(t *testing.T) {
	source := initRepo(t)
	commitFile(t, source, "a.go", "source\n")
	thin := gittest.ShallowClone(t, source, 1)
	mustRun(t, thin, "storage", "bind", "--source", source)
	mustRun(t, thin, "note", "add", "Fact", "--body", "graph me")
	mustRun(t, thin, "kg", "build")

	_, thinCommon := gittest.Dirs(t, thin)
	_, sourceCommon := gittest.Dirs(t, source)
	repo, err := ccnhome.ForRepo(thinCommon)
	if err != nil {
		t.Fatal(err)
	}
	info, err := repo.ReadInfo()
	if err != nil {
		t.Fatalf("read the clone's registration: %v", err)
	}
	if want := canonicalPath(t, thinCommon); info.CommonDir != want {
		t.Fatalf("registered common dir = %s, want the clone's %s", info.CommonDir, want)
	}
	if want := []string{canonicalPath(t, thin)}; !slices.Equal(info.Worktrees, want) {
		t.Fatalf("registered worktrees = %v, want %v", info.Worktrees, want)
	}
	if _, err := os.Stat(repo.Graph()); err != nil {
		t.Fatalf("graph index under the clone's key: %v", err)
	}
	sourceRepo, err := ccnhome.ForRepo(sourceCommon)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sourceRepo.Dir); !os.IsNotExist(err) {
		t.Fatalf("the source's derived-state dir %s exists (stat err %v), want nothing keyed by the records repository", sourceRepo.Dir, err)
	}
	_, stderr, err := runCLI(t, thin, "kg", "stat")
	if err != nil || stderr != "" {
		t.Fatalf("kg stat = %v (stderr %q), want the index kg build stored under the clone's key", err, stderr)
	}
}
