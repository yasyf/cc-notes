package gitobj_test

import (
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

type gitVerdict int

const (
	gitResolves gitVerdict = iota
	gitMisses
	gitFatals
)

func (v gitVerdict) String() string {
	return [...]string{"resolves", "misses", "fatals"}[v]
}

// gitPathOID classifies rev:path against git's own path resolver. The oracle is
// cat-file --batch-check, not rev-parse: rev-parse scans the whole rev:path
// token for ".." before it splits at the colon, so every path ending in ".."
// is parsed as a commit range and the resolver never runs (TestPathOIDRangeTrap).
// --batch-check answers a clean miss with a "missing" record and exit 0, a
// relative path climbing above the root with git's fatal and exit 128, and -z
// frames the request with a NUL, which a path carrying a newline needs.
func gitPathOID(t *testing.T, dir string, rev model.SHA, path string) (model.SHA, gitVerdict) {
	t.Helper()
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	cmd := exec.Command("git", "cat-file", "--batch-check", "-z")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(string(rev) + ":" + path + "\x00")
	out, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 128 {
			return "", gitFatals
		}
		t.Fatalf("git cat-file --batch-check %s:%s: %v", rev, path, err)
	}
	record := strings.TrimSuffix(string(out), "\n")
	if strings.HasSuffix(record, " missing") {
		return "", gitMisses
	}
	oid, _, ok := strings.Cut(record, " ")
	if !ok || len(oid) != 40 {
		t.Fatalf("git cat-file --batch-check %s:%s printed %q, want one oid", rev, path, out)
	}
	return model.SHA(oid), gitResolves
}

func commitFiles(t *testing.T, dir, message string, files map[string]string) model.SHA {
	t.Helper()
	for path, content := range files {
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir for %s: %v", path, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", message)
	return model.SHA(git(t, dir, "rev-parse", "HEAD"))
}

const (
	modeFile    = "100644"
	modeDir     = "40000"
	modeSymlink = "120000"
	modeGitlink = "160000"

	// unicodeNFC and unicodeNFD are the composed and decomposed spellings of
	// the same rendered name. Only the stored spelling resolves: nothing on
	// either side of the lookup normalizes.
	unicodeNFC = "\u00fcn\u00efcod\u00e9.go"
	unicodeNFD = "u\u0308ni\u0308code\u0301.go"
)

// treeEntry is one raw git tree record. The parity fixture is assembled from
// raw tree objects rather than through `git add`, because the index refuses
// ".git/config" and "git~1/f.go" outright (core.protectNTFS defaults on
// everywhere) while the read path git resolves them on has no such filter.
type treeEntry struct {
	mode string
	name string
	oid  string
}

func writeBlob(t *testing.T, dir, content string) string {
	t.Helper()
	return gitStdin(t, dir, content, "hash-object", "-w", "--stdin")
}

// writeTree writes a raw tree object. Entries sort by name with a directory's
// name carrying a trailing slash, the order git's own tree walk relies on to
// stop early.
func writeTree(t *testing.T, dir string, entries ...treeEntry) string {
	t.Helper()
	slices.SortFunc(entries, func(a, b treeEntry) int {
		return strings.Compare(treeSortName(a), treeSortName(b))
	})
	var raw strings.Builder
	for _, e := range entries {
		oid, err := hex.DecodeString(e.oid)
		if err != nil {
			t.Fatalf("entry %q oid %q: %v", e.name, e.oid, err)
		}
		if len(oid) != 20 {
			t.Fatalf("entry %q oid %q is %d bytes, want 20", e.name, e.oid, len(oid))
		}
		raw.WriteString(e.mode + " " + e.name + "\x00")
		raw.Write(oid)
	}
	return gitStdin(t, dir, raw.String(), "hash-object", "-w", "-t", "tree", "--stdin", "--literally")
}

func treeSortName(e treeEntry) string {
	if e.mode == modeDir {
		return e.name + "/"
	}
	return e.name
}

// craftFixture builds the tree the parity table is measured against: files,
// nested directories, symlinks (one to a directory, one to a file, one
// dangling), a gitlink whose commit is not in this object database, ".git" and
// "git~1" directories, and names carrying a trailing space, a backslash, "..",
// and non-ASCII bytes.
func craftFixture(t *testing.T) (string, model.SHA) {
	t.Helper()
	dir := initRepo(t)
	blob := func(content string) string { return writeBlob(t, dir, content) }
	tree := func(entries ...treeEntry) string { return writeTree(t, dir, entries...) }

	d := tree(
		treeEntry{modeFile, "f.go", blob("package d\n")},
		treeEntry{modeFile, "x..y.go", blob("package d // dots\n")},
		treeEntry{modeDir, "n", tree(treeEntry{modeFile, "g.go", blob("package n\n")})},
	)
	deep := tree(treeEntry{modeFile, "f.go", blob("package e\n")})
	for _, name := range []string{"e", "d", "c", "b"} {
		deep = tree(treeEntry{modeDir, name, deep})
	}
	root := tree(
		treeEntry{modeFile, "f.go", blob("package root\n")},
		treeEntry{modeDir, "d", d},
		treeEntry{modeDir, "a", deep},
		treeEntry{modeDir, "real", tree(treeEntry{modeDir, "src", tree(treeEntry{modeFile, "x.go", blob("package src\n")})})},
		treeEntry{modeDir, "vendor", tree(treeEntry{modeSymlink, "lib", blob("../real")})},
		treeEntry{modeSymlink, "link.go", blob("f.go")},
		treeEntry{modeSymlink, "dangle", blob("nowhere")},
		treeEntry{modeGitlink, "sub", strings.Repeat("11", 20)},
		treeEntry{modeDir, ".git", tree(treeEntry{modeFile, "config", blob("[core]\n")})},
		treeEntry{modeDir, "git~1", tree(treeEntry{modeFile, "f.go", blob("package git1\n")})},
		treeEntry{modeDir, "日本語", tree(treeEntry{modeFile, "テスト.go", blob("package jp\n")})},
		treeEntry{modeDir, "dot..dir", tree(treeEntry{modeFile, "f.go", blob("package dotdir\n")})},
		treeEntry{modeFile, "a..b.go", blob("package dots\n")},
		treeEntry{modeFile, "emoji🎉.go", blob("package emoji\n")},
		treeEntry{modeFile, "sp ace.go", blob("package space\n")},
		treeEntry{modeFile, "trailing ", blob("package trailing\n")},
		treeEntry{modeFile, `b\s.go`, blob("package backslash\n")},
		treeEntry{modeFile, unicodeNFC, blob("package unicode\n")},
	)
	commit := gitStdin(t, dir, "", "commit-tree", root, "-m", "fixture")
	git(t, dir, "update-ref", "refs/heads/main", commit)
	return dir, model.SHA(commit)
}

// TestPathOIDGitParity pins PathOID against real git, shape for shape. Anchor
// values are free-form strings nothing normalizes on the way in
// (notes/anchor.go), so every shape here is reachable from a stored anchor,
// and git's verdict on it is the contract: the want column asserts the fixture
// really carries the shape, then PathOID has to agree with git.
func TestPathOIDGitParity(t *testing.T) {
	dir, rev := craftFixture(t)
	repo := open(t, dir)

	cases := []struct {
		path string
		want gitVerdict
	}{
		{"", gitResolves},
		{".", gitMisses},
		{"..", gitMisses},
		{"...", gitMisses},
		{"/", gitMisses},
		{"//", gitMisses},
		{"///", gitMisses},
		{"./", gitResolves},
		{"../", gitFatals},
		{" ", gitMisses},
		{"  ", gitMisses},
		{"\t", gitMisses},

		{"f.go", gitResolves},
		{"./f.go", gitResolves},
		{"f.go/", gitMisses},
		{"f.go//", gitMisses},
		{"/f.go", gitMisses},
		{".//f.go", gitResolves},
		{"./././f.go", gitResolves},
		{"f.go/.", gitMisses},
		{"f.go/..", gitMisses},
		{"f.go..", gitMisses},
		{"f.go/x.go", gitMisses},
		{"f.go ", gitMisses},
		{" f.go", gitMisses},
		{"F.GO", gitMisses},
		{"f.go\n", gitMisses},
		{"./f.go/", gitMisses},
		{"./f.go/.", gitMisses},
		{"./f.go/./.", gitMisses},
		{"./f.go/git~1/..", gitMisses},
		{"./f.go/..", gitResolves},
		{"./f.go/..//", gitResolves},
		{"./a/b/c/d/e/f.go/.", gitMisses},
		{"./trailing /.", gitMisses},
		{"./a..b.go/.", gitMisses},
		{"./d/x..y.go/.", gitMisses},
		{"./nonexistent/.", gitMisses},
		{"./nonexistent/..", gitResolves},

		{"d", gitResolves},
		{"d/", gitResolves},
		{"d//", gitMisses},
		{"/d", gitMisses},
		{"/d/f.go", gitMisses},
		{"d//f.go", gitMisses},
		{"d/./f.go", gitMisses},
		{"d/../d/f.go", gitMisses},
		{"./d/../d/f.go", gitResolves},
		{"d/f.go", gitResolves},
		{"./d/f.go", gitResolves},
		{"d/f.go/", gitMisses},
		{"d/f.go//", gitMisses},
		{"d/f.go ", gitMisses},
		{"./d", gitResolves},
		{"./d/", gitResolves},
		{"d/./", gitMisses},
		{"d/..", gitMisses},
		{"d/../", gitMisses},
		{"d/n", gitResolves},
		{"d/n/", gitResolves},
		{"d/n/g.go", gitResolves},
		{"d/n/g.go/", gitMisses},
		{"d/n/g.go/.", gitMisses},
		{"d/n/..", gitMisses},
		{"d/n/../f.go", gitMisses},
		{"./d/n/../f.go", gitResolves},
		{"./d//f.go", gitResolves},
		{"./d/./f.go", gitResolves},
		{"../d/f.go", gitFatals},
		{"//d//f.go//", gitMisses},
		{"./.", gitResolves},
		{"././", gitResolves},
		{"./ ", gitMisses},
		{"./..", gitFatals},
		{"./d/.", gitResolves},
		{"./d/..", gitResolves},
		{"./d/n/.", gitResolves},
		{"./d/n/..", gitResolves},
		{"./d/n/g.go/.", gitMisses},
		{"./d/n/g.go/git~1/..", gitMisses},
		{"./dot..dir/.", gitResolves},
		{"./real/src/.", gitResolves},
		{"./git~1/.", gitResolves},
		{"./.git/config/.", gitMisses},

		{"a/b/c/d/e/f.go", gitResolves},
		{"./a/b/c/d/e/f.go", gitResolves},
		{"a/b/../b/c/d/e/f.go", gitMisses},
		{"./a/b/../b/c/d/e/f.go", gitResolves},

		{"nonexistent", gitMisses},
		{"./nonexistent", gitMisses},
		{"nonexistent/", gitMisses},
		{"d/nonexistent", gitMisses},
		{"nonexistent/f.go", gitMisses},
		{"d/n/nonexistent/g.go", gitMisses},

		{"vendor", gitResolves},
		{"vendor/", gitResolves},
		{"vendor/lib", gitResolves},
		{"vendor/lib/", gitMisses},
		{"./vendor/lib/", gitMisses},
		{"./vendor/lib/.", gitMisses},
		{"./vendor/lib/git~1/..", gitMisses},
		{"vendor/lib/src/x.go", gitMisses},
		{"./vendor/lib/src/x.go", gitMisses},
		{"vendor/lib/..", gitMisses},
		{"./vendor/lib/..", gitResolves},
		{"real", gitResolves},
		{"real/src/x.go", gitResolves},
		{"link.go", gitResolves},
		{"link.go/", gitMisses},
		{"./link.go/.", gitMisses},
		{"dangle", gitResolves},
		{"dangle/f.go", gitMisses},
		{"./dangle/.", gitMisses},

		{"sub", gitResolves},
		{"sub/", gitMisses},
		{"./sub", gitResolves},
		{"./sub/", gitMisses},
		{"sub/.", gitMisses},
		{"sub/..", gitMisses},
		{"./sub/.", gitMisses},
		{"./sub/git~1/..", gitMisses},
		{"./sub/..", gitResolves},
		{"sub/anything.go", gitMisses},

		{".git", gitResolves},
		{".git/", gitResolves},
		{".git/config", gitResolves},
		{"./.git/config", gitResolves},
		{".git/config/", gitMisses},
		{".GIT", gitMisses},
		{".Git", gitMisses},
		{".git.", gitMisses},
		{".git ", gitMisses},
		{"git~1", gitResolves},
		{"git~1/", gitResolves},
		{"git~1/f.go", gitResolves},
		{"GIT~1", gitMisses},
		{"GIT~1/f.go", gitMisses},
		{"git~2", gitMisses},

		{unicodeNFC, gitResolves},
		{unicodeNFD, gitMisses},
		{"日本語", gitResolves},
		{"日本語/", gitResolves},
		{"日本語/テスト.go", gitResolves},
		{"emoji🎉.go", gitResolves},
		{"sp ace.go", gitResolves},
		{"trailing ", gitResolves},
		{"./trailing ", gitResolves},
		{"trailing", gitMisses},
		{"trailing  ", gitMisses},
		{`b\s.go`, gitResolves},
		{`d\f.go`, gitMisses},

		{"a..b.go", gitResolves},
		{"./a..b.go", gitResolves},
		{"a..b.go/", gitMisses},
		{"d/x..y.go", gitResolves},
		{"./d/x..y.go", gitResolves},
		{"d/x..y.go ", gitMisses},
		{"dot..dir", gitResolves},
		{"dot..dir/", gitResolves},
		{"dot..dir/f.go", gitResolves},
		{"..f.go", gitMisses},
		{"..d", gitMisses},

		{"a:b.go", gitMisses},
		{"-", gitMisses},
		{"@", gitMisses},
		{":", gitMisses},
		{"f.go:", gitMisses},
		{"*", gitMisses},
		{"d/*", gitMisses},
		{"d/*.go", gitMisses},
		{"?.go", gitMisses},
		{"[df].go", gitMisses},
	}
	for _, tc := range cases {
		t.Run(strconv.QuoteToASCII(tc.path), func(t *testing.T) {
			want, verdict := gitPathOID(t, dir, rev, tc.path)
			if verdict != tc.want {
				t.Fatalf("fixture invalid: git cat-file %s:%s %s, want %s", rev, tc.path, verdict, tc.want)
			}
			got, err := repo.PathOID(t.Context(), rev, tc.path)
			switch tc.want {
			case gitResolves:
				if err != nil {
					t.Fatalf("PathOID(%s, %q): %v", rev, tc.path, err)
				}
				if got != want {
					t.Errorf("PathOID(%s, %q) = %s, git cat-file = %s", rev, tc.path, got, want)
				}
			case gitMisses:
				if !errors.Is(err, model.ErrPathNotFound) {
					t.Errorf("PathOID(%s, %q) = %q, %v, want model.ErrPathNotFound", rev, tc.path, got, err)
				}
			case gitFatals:
				if !errors.Is(err, gitobj.ErrPathEscapesRoot) {
					t.Errorf("PathOID(%s, %q) = %q, %v, want gitobj.ErrPathEscapesRoot", rev, tc.path, got, err)
				}
			}
		})
	}
}

// TestPathOIDRangeTrap pins why the parity table is oracled against cat-file
// and not rev-parse: rev-parse scans the whole rev:path token for ".." before
// it splits at the colon, so a path ending in ".." is parsed as a commit range
// and exits 1 with both endpoints on stdout, the path resolver never running.
// Read as a verdict that is a miss, and every "./x/.." row above would assert
// the opposite of what git resolves. PathOID takes rev and path as separate
// arguments, so it has no such token.
func TestPathOIDRangeTrap(t *testing.T) {
	dir, rev := craftFixture(t)
	for _, path := range []string{"./..", "./d/.."} {
		//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
		cmd := exec.Command("git", "rev-parse", "--verify", "--quiet", string(rev)+":"+path)
		cmd.Dir = dir
		out, err := cmd.Output()
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.ExitCode() != 1 {
			t.Fatalf("git rev-parse %s:%s = %q, %v, want the range-trap exit 1", rev, path, out, err)
		}
		if len(out) == 0 {
			t.Errorf("git rev-parse %s:%s printed nothing; the range trap prints both endpoints", rev, path)
		}
	}
}

// TestPathOIDUnbornHead pins an unborn HEAD as a miss. A repository with no
// commits still reaches PathOID through a directory anchor's drift check
// (internal/cli/note_review.go), which reads the miss as DRIFTED; anything
// else fails the whole status run.
func TestPathOIDUnbornHead(t *testing.T) {
	repo := open(t, initRepo(t))
	for _, path := range []string{"", "internal", "internal/fold/fold.go"} {
		if _, err := repo.PathOID(t.Context(), "", path); !errors.Is(err, model.ErrPathNotFound) {
			t.Errorf("PathOID(%q, %q) = %v, want model.ErrPathNotFound", "", path, err)
		}
	}
}

// TestPathOIDMemoOrderIndependence pins the root-tree memo against answers
// that depend on call order. go-git's Tree.FindEntry keys its own subtree
// cache with filepath.Join, which collapses empty components, so a handle
// warmed on "internal/fold/fold.go" resolved "/internal/fold/fold.go" and
// "internal//fold/fold.go" that a cold handle rejected.
func TestPathOIDMemoOrderIndependence(t *testing.T) {
	dir := initRepo(t)
	rev := commitFiles(t, dir, "fixture", map[string]string{
		"internal/fold/fold.go": "package fold\n",
		"internal/refs/refs.go": "package refs\n",
	})
	warm := []string{"internal", "internal/fold", "internal/fold/fold.go", "internal/refs/refs.go"}
	for _, path := range []string{
		"/internal/fold/fold.go",
		"internal//fold/fold.go",
		"internal/fold//fold.go",
		"/internal",
		"internal//fold",
		"internal/./fold/fold.go",
		"internal/fold/../fold/fold.go",
	} {
		t.Run(strconv.QuoteToASCII(path), func(t *testing.T) {
			if _, verdict := gitPathOID(t, dir, rev, path); verdict != gitMisses {
				t.Fatalf("fixture invalid: git cat-file %s:%s %s, want misses", rev, path, verdict)
			}
			cold := open(t, dir)
			if _, err := cold.PathOID(t.Context(), rev, path); !errors.Is(err, model.ErrPathNotFound) {
				t.Errorf("cold PathOID(%s, %q) = %v, want model.ErrPathNotFound", rev, path, err)
			}
			hot := open(t, dir)
			for _, seed := range warm {
				if _, err := hot.PathOID(t.Context(), rev, seed); err != nil {
					t.Fatalf("warm PathOID(%s, %q): %v", rev, seed, err)
				}
			}
			if _, err := hot.PathOID(t.Context(), rev, path); !errors.Is(err, model.ErrPathNotFound) {
				t.Errorf("warm PathOID(%s, %q) = %v, want model.ErrPathNotFound", rev, path, err)
			}
		})
	}
}

// TestPathOIDSharedSubtreeDecode pins the root-tree and subtree memos against
// a second rev: the memo has to evict, and repeated and interleaved lookups
// must all still be git's oids.
func TestPathOIDSharedSubtreeDecode(t *testing.T) {
	dir := initRepo(t)
	first := commitFiles(t, dir, "first", map[string]string{
		"a/b/c/one.go":  "one\n",
		"a/b/d/two.go":  "two\n",
		"a/e/three.go":  "three\n",
		"a/b/c/four.go": "four\n",
	})
	second := commitFiles(t, dir, "second", map[string]string{"a/b/c/one.go": "one, revised\n"})
	repo := open(t, dir)

	paths := []string{"a", "a/b", "a/b/c", "a/b/c/one.go", "a/b/c/four.go", "a/b/d/two.go", "a/e/three.go"}
	revs := []model.SHA{first, second, first}
	want := map[model.SHA]map[string]model.SHA{}
	for _, rev := range []model.SHA{first, second} {
		want[rev] = map[string]model.SHA{}
		for _, path := range paths {
			oid, verdict := gitPathOID(t, dir, rev, path)
			if verdict != gitResolves {
				t.Fatalf("fixture invalid: git cat-file %s:%s %s, want resolves", rev, path, verdict)
			}
			want[rev][path] = oid
		}
	}
	if want[first]["a/b/c/one.go"] == want[second]["a/b/c/one.go"] {
		t.Fatalf("fixture invalid: a/b/c/one.go has the same oid at both revs, so a stale memo would go unnoticed")
	}
	if want[first]["a/e/three.go"] != want[second]["a/e/three.go"] {
		t.Fatalf("fixture invalid: a/e/three.go changed between revs, so a shared subtree is not shared")
	}

	for round := range 3 {
		for _, rev := range revs {
			for _, path := range paths {
				got, err := repo.PathOID(t.Context(), rev, path)
				if err != nil {
					t.Fatalf("round %d: PathOID(%s, %s): %v", round, rev, path, err)
				}
				if got != want[rev][path] {
					t.Fatalf("round %d: PathOID(%s, %s) = %s, want %s", round, rev, path, got, want[rev][path])
				}
			}
			if _, err := repo.PathOID(t.Context(), rev, "a/b/c/absent.go"); !errors.Is(err, model.ErrPathNotFound) {
				t.Fatalf("round %d: PathOID(%s, a/b/c/absent.go) = %v, want model.ErrPathNotFound", round, rev, err)
			}
		}
	}
}

// TestPathOIDMemoServesRepeatedAnchors pins the cost profile a status run rests
// on, and not as a duration: the first pass decodes each tree once, and every
// later pass at the same rev answers from the memo without touching the object
// database. The object database is deleted between the passes, so an
// implementation that re-reads per anchor — the git rev-parse fork per anchor
// this package replaced, or a subtree cache the walk cannot hit — fails here
// rather than quietly costing three orders of magnitude more. The rev the memo
// never warmed is the control: it has to fail, or the deletion proved nothing.
func TestPathOIDMemoServesRepeatedAnchors(t *testing.T) {
	dir := initRepo(t)
	warmed := commitFiles(t, dir, "first", map[string]string{
		"internal/fold/fold.go": "package fold\n",
		"internal/refs/refs.go": "package refs\n",
		"notes/anchor.go":       "package notes\n",
		"README.md":             "# cc-notes\n",
	})
	cold := commitFiles(t, dir, "second", map[string]string{"README.md": "# cc-notes, revised\n"})

	anchors := []string{"internal", "internal/fold", "internal/fold/fold.go", "internal/refs/refs.go", "notes/anchor.go", "README.md"}
	want := make(map[string]model.SHA, len(anchors))
	for _, anchor := range anchors {
		oid, verdict := gitPathOID(t, dir, warmed, anchor)
		if verdict != gitResolves {
			t.Fatalf("fixture invalid: git cat-file %s:%s %s, want resolves", warmed, anchor, verdict)
		}
		want[anchor] = oid
	}

	repo := open(t, dir)
	for _, anchor := range anchors {
		got, err := repo.PathOID(t.Context(), warmed, anchor)
		if err != nil {
			t.Fatalf("warming PathOID(%s, %s): %v", warmed, anchor, err)
		}
		if got != want[anchor] {
			t.Fatalf("warming PathOID(%s, %s) = %s, git cat-file = %s", warmed, anchor, got, want[anchor])
		}
	}
	if err := os.RemoveAll(filepath.Join(dir, ".git", "objects")); err != nil {
		t.Fatalf("remove object database: %v", err)
	}

	for _, anchor := range anchors {
		got, err := repo.PathOID(t.Context(), warmed, anchor)
		if err != nil {
			t.Errorf("PathOID(%s, %s) with the object database gone: %v, want the memoized %s", warmed, anchor, err, want[anchor])
			continue
		}
		if got != want[anchor] {
			t.Errorf("PathOID(%s, %s) = %s, want the memoized %s", warmed, anchor, got, want[anchor])
		}
	}
	if _, err := repo.PathOID(t.Context(), cold, "README.md"); !errors.Is(err, gitobj.ErrCommitNotFound) {
		t.Errorf("PathOID(%s, README.md) with the object database gone = %v, want gitobj.ErrCommitNotFound: the deletion left the unwarmed rev readable", cold, err)
	}
}

// TestPathOIDReindexOnStalePack drives the subtree walk against a pack index
// built before the object arrived, the state a long-lived `cc-notes mcp` or
// FUSE helper reaches after a `git gc`. Every other object read in the package
// repairs it through retry, and the walk has to too.
func TestPathOIDReindexOnStalePack(t *testing.T) {
	dir := initRepo(t)
	one := writeBlob(t, dir, "package b\n")
	bTree := writeTree(t, dir, treeEntry{modeFile, "one.go", one})
	aTree := writeTree(t, dir, treeEntry{modeDir, "b", bTree})
	two := writeBlob(t, dir, "package q\n")
	qTree := writeTree(t, dir, treeEntry{modeFile, "two.go", two})
	pTree := writeTree(t, dir, treeEntry{modeDir, "q", qTree})
	root := writeTree(t, dir, treeEntry{modeDir, "a", aTree}, treeEntry{modeDir, "p", pTree})
	commit := gitStdin(t, dir, "", "commit-tree", root, "-m", "fixture")
	git(t, dir, "update-ref", "refs/heads/main", commit)
	rev := model.SHA(commit)

	packObjects(t, dir, commit, root, aTree, bTree, one)
	for _, oid := range []string{pTree, qTree, two} {
		removeLooseObject(t, dir, oid)
	}

	repo := open(t, dir)
	if _, err := repo.PathOID(t.Context(), rev, "a/b/one.go"); err != nil {
		t.Fatalf("seed PathOID(%s, a/b/one.go): %v", rev, err)
	}

	writeTree(t, dir, treeEntry{modeDir, "q", writeTree(t, dir, treeEntry{modeFile, "two.go", writeBlob(t, dir, "package q\n")})})
	git(t, dir, "repack", "-a", "-d", "-q")

	want, verdict := gitPathOID(t, dir, rev, "p/q/two.go")
	if verdict != gitResolves {
		t.Fatalf("fixture invalid: git cat-file %s:p/q/two.go %s, want resolves", rev, verdict)
	}
	got, err := repo.PathOID(t.Context(), rev, "p/q/two.go")
	if err != nil {
		t.Fatalf("PathOID(%s, p/q/two.go) after repack: %v", rev, err)
	}
	if got != want {
		t.Errorf("PathOID(%s, p/q/two.go) = %s, git cat-file = %s", rev, got, want)
	}
}

// TestPathOIDMissingSubtreeObject pins the other side of the mode
// discrimination: a component that really is a directory whose tree object is
// gone is object-database corruption, and stays a loud error rather than
// joining git's clean misses.
func TestPathOIDMissingSubtreeObject(t *testing.T) {
	dir := initRepo(t)
	rev := commitFiles(t, dir, "fixture", map[string]string{"a/b/one.go": "package b\n"})
	aTree := git(t, dir, "rev-parse", string(rev)+":a")
	removeLooseObject(t, dir, aTree)

	got, err := open(t, dir).PathOID(t.Context(), rev, "a/b/one.go")
	if err == nil {
		t.Fatalf("PathOID(%s, a/b/one.go) = %s with tree %s absent, want an error", rev, got, aTree)
	}
	if errors.Is(err, model.ErrPathNotFound) {
		t.Errorf("PathOID(%s, a/b/one.go) = %v, want a loud error rather than a miss", rev, err)
	}
}

// packObjects packs exactly the named objects and drops their loose copies, so
// the packs a Repo handle indexes carry these and nothing else.
func packObjects(t *testing.T, dir string, oids ...string) {
	t.Helper()
	base := filepath.Join(".git", "objects", "pack", "pack")
	gitStdin(t, dir, strings.Join(oids, "\n")+"\n", "pack-objects", "-q", base)
	git(t, dir, "prune-packed", "-q")
}

func looseObjectPath(dir, oid string) string {
	return filepath.Join(dir, ".git", "objects", oid[:2], oid[2:])
}

func removeLooseObject(t *testing.T, dir, oid string) {
	t.Helper()
	if err := os.Remove(looseObjectPath(dir, oid)); err != nil {
		t.Fatalf("remove loose object %s: %v", oid, err)
	}
}

// stashLooseObject moves a loose object out of the object database and returns
// the closure that puts it back, so a test can fail one object read and then
// let the same handle succeed at the same read.
func stashLooseObject(t *testing.T, dir, oid string) func() {
	t.Helper()
	path, stashed := looseObjectPath(dir, oid), filepath.Join(t.TempDir(), oid)
	if err := os.Rename(path, stashed); err != nil {
		t.Fatalf("stash loose object %s: %v", oid, err)
	}
	return func() {
		if err := os.Rename(stashed, path); err != nil {
			t.Fatalf("restore loose object %s: %v", oid, err)
		}
	}
}
