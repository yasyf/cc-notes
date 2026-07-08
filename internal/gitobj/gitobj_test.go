package gitobj_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-git/go-git/v5/storage/filesystem/dotgit"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

const (
	testName  = "Test User"
	testEmail = "test@example.com"
	testActor = model.Actor("Test User <test@example.com>")
)

var (
	t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	t1 = t0.Add(time.Minute)
	t2 = t0.Add(2 * time.Minute)
	t3 = t0.Add(3 * time.Minute)

	createPack = model.Pack{Lamport: 1, Ops: []model.Op{model.CreateNote{
		Nonce: "0123456789abcdef0123456789abcdef",
		Title: "hello",
		Body:  "world",
		Tags:  []string{"a", "b"},
	}}}
	retitlePack = model.Pack{Lamport: 2, Ops: []model.Op{model.SetTitle{Title: "v2"}}}
	tagPack     = model.Pack{Lamport: 2, Ops: []model.Op{model.AddTag{Tag: "c"}}}
	bodyPack    = model.Pack{Lamport: 3, Ops: []model.Op{model.SetBody{Body: "v3"}}}
)

// createPackJSON pins the canonical wire bytes of createPack: the blob
// content is part of the storage format, so a marshal-layout change must
// fail here.
const createPackJSON = `{"v":1,"lamport":1,"ops":[{"kind":"create_note","nonce":"0123456789abcdef0123456789abcdef","title":"hello","body":"world","tags":["a","b"],"anchors":null}]}`

func gitEnv() []string {
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=" + testName,
		"GIT_AUTHOR_EMAIL=" + testEmail,
		"GIT_AUTHOR_DATE=2026-01-02T03:04:05Z",
		"GIT_COMMITTER_NAME=" + testName,
		"GIT_COMMITTER_EMAIL=" + testEmail,
		"GIT_COMMITTER_DATE=2026-01-02T03:04:05Z",
	}
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	return gitStdin(t, dir, "", args...)
}

func gitStdin(t *testing.T, dir, stdin string, args ...string) string {
	t.Helper()
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	cmd.Stdin = strings.NewReader(stdin)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func gitRaw(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return out
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	git(t, dir, "config", "user.name", testName)
	git(t, dir, "config", "user.email", testEmail)
	return dir
}

func open(t *testing.T, dir string) *gitobj.Repo {
	t.Helper()
	repo, err := gitobj.Open(dir)
	if err != nil {
		t.Fatalf("Open(%s): %v", dir, err)
	}
	return repo
}

func sigAt(when time.Time) gitobj.Signature {
	return gitobj.Signature{Name: testName, Email: testEmail, When: when}
}

func write(t *testing.T, repo *gitobj.Repo, parents []model.SHA, when time.Time, pack model.Pack) model.SHA {
	t.Helper()
	sha, err := repo.WriteOpsCommit(t.Context(), parents, sigAt(when), "cc-notes: test", pack)
	if err != nil {
		t.Fatalf("WriteOpsCommit: %v", err)
	}
	return sha
}

func TestWriteOpsCommitGitOracle(t *testing.T) {
	dir := initRepo(t)
	repo := open(t, dir)

	sha := write(t, repo, nil, t0, createPack)

	if typ := git(t, dir, "cat-file", "-t", string(sha)); typ != "commit" {
		t.Fatalf("cat-file -t %s = %q, want commit", sha, typ)
	}
	commitBody := git(t, dir, "cat-file", "-p", string(sha))
	lines := strings.Split(commitBody, "\n")
	treeSHA, ok := strings.CutPrefix(lines[0], "tree ")
	if !ok {
		t.Fatalf("first commit line = %q, want tree header", lines[0])
	}
	ident := fmt.Sprintf("%s <%s> %d +0000", testName, testEmail, t0.Unix())
	if want := "author " + ident; lines[1] != want {
		t.Errorf("author line = %q, want %q", lines[1], want)
	}
	if want := "committer " + ident; lines[2] != want {
		t.Errorf("committer line = %q, want %q", lines[2], want)
	}
	if !strings.Contains(commitBody, "cc-notes: test") {
		t.Errorf("commit body %q missing message", commitBody)
	}

	treeBody := git(t, dir, "cat-file", "-p", treeSHA)
	fields := strings.Fields(treeBody)
	if len(fields) != 4 || fields[0] != "100644" || fields[1] != "blob" || fields[3] != "ops.json" {
		t.Fatalf("tree entry = %q, want '100644 blob <sha>\tops.json'", treeBody)
	}
	blob := gitRaw(t, dir, "cat-file", "-p", fields[2])
	if string(blob) != createPackJSON {
		t.Errorf("ops.json blob = %q, want %q", blob, createPackJSON)
	}
	wire, err := createPack.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	if string(blob) != string(wire) {
		t.Errorf("ops.json blob = %q, want MarshalJSON output %q", blob, wire)
	}
}

func TestWriteOpsCommitDeterministic(t *testing.T) {
	dir := initRepo(t)
	repo := open(t, dir)

	root := write(t, repo, nil, t0, createPack)
	child := write(t, repo, []model.SHA{root}, t1, retitlePack)

	if again := write(t, repo, nil, t0, createPack); again != root {
		t.Errorf("rewrite of root = %s, want %s", again, root)
	}
	if again := write(t, repo, []model.SHA{root}, t1, retitlePack); again != child {
		t.Errorf("rewrite of child = %s, want %s", again, child)
	}

	other := open(t, initRepo(t))
	if elsewhere := write(t, other, nil, t0, createPack); elsewhere != root {
		t.Errorf("root in fresh repo = %s, want %s", elsewhere, root)
	}
}

func TestWriteOpsCommitInvalidParent(t *testing.T) {
	repo := open(t, initRepo(t))
	_, err := repo.WriteOpsCommit(t.Context(), []model.SHA{"not-a-sha"}, sigAt(t0), "m", createPack)
	if err == nil || !strings.Contains(err.Error(), "not-a-sha") {
		t.Fatalf("WriteOpsCommit with bad parent = %v, want error naming it", err)
	}
}

func TestReadChainLinear(t *testing.T) {
	dir := initRepo(t)
	repo := open(t, dir)

	c1 := write(t, repo, nil, t0, createPack)
	c2 := write(t, repo, []model.SHA{c1}, t1, retitlePack)
	c3 := write(t, repo, []model.SHA{c2}, t2, bodyPack)

	got, err := repo.ReadChain(t.Context(), c3)
	if err != nil {
		t.Fatalf("ReadChain: %v", err)
	}
	want := []model.PackCommit{
		{SHA: c3, Parents: []model.SHA{c2}, Author: testActor, AuthorTime: t2.Unix(), Pack: bodyPack},
		{SHA: c2, Parents: []model.SHA{c1}, Author: testActor, AuthorTime: t1.Unix(), Pack: retitlePack},
		{SHA: c1, Parents: nil, Author: testActor, AuthorTime: t0.Unix(), Pack: createPack},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadChain = %+v, want %+v", got, want)
	}
}

func TestReadChainMergeDiamond(t *testing.T) {
	dir := initRepo(t)
	repo := open(t, dir)

	root := write(t, repo, nil, t0, createPack)
	left := write(t, repo, []model.SHA{root}, t1, retitlePack)
	right := write(t, repo, []model.SHA{root}, t2, tagPack)
	mergePack := model.Pack{Lamport: 3}
	merge := write(t, repo, []model.SHA{left, right}, t3, mergePack)

	got, err := repo.ReadChain(t.Context(), merge)
	if err != nil {
		t.Fatalf("ReadChain: %v", err)
	}
	// The empty-ops merge pack round-trips as "ops":[], so the decoded
	// slice is empty but non-nil.
	want := []model.PackCommit{
		{SHA: merge, Parents: []model.SHA{left, right}, Author: testActor, AuthorTime: t3.Unix(), Pack: model.Pack{Lamport: 3, Ops: []model.Op{}}},
		{SHA: left, Parents: []model.SHA{root}, Author: testActor, AuthorTime: t1.Unix(), Pack: retitlePack},
		{SHA: right, Parents: []model.SHA{root}, Author: testActor, AuthorTime: t2.Unix(), Pack: tagPack},
		{SHA: root, Parents: nil, Author: testActor, AuthorTime: t0.Unix(), Pack: createPack},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadChain = %+v, want %+v", got, want)
	}
}

func TestReadChainIncompleteFabricatedParent(t *testing.T) {
	repo := open(t, initRepo(t))
	fake := model.SHA(strings.Repeat("beefcafe", 5))
	tip := write(t, repo, []model.SHA{fake}, t0, createPack)

	_, err := repo.ReadChain(t.Context(), tip)
	if !errors.Is(err, gitobj.ErrIncompleteChain) {
		t.Fatalf("ReadChain = %v, want ErrIncompleteChain", err)
	}
	if !strings.Contains(err.Error(), string(fake)) {
		t.Errorf("error %q does not name the missing sha %s", err, fake)
	}
	if !strings.Contains(err.Error(), "missing from object database") {
		t.Errorf("error %q does not report a non-shallow miss", err)
	}
}

func TestReadChainIncompleteDeletedObject(t *testing.T) {
	dir := initRepo(t)
	repo := open(t, dir)
	c1 := write(t, repo, nil, t0, createPack)
	c2 := write(t, repo, []model.SHA{c1}, t1, retitlePack)

	loose := filepath.Join(dir, ".git", "objects", string(c1)[:2], string(c1)[2:])
	if err := os.Remove(loose); err != nil {
		t.Fatalf("remove %s: %v", loose, err)
	}

	_, err := open(t, dir).ReadChain(t.Context(), c2)
	if !errors.Is(err, gitobj.ErrIncompleteChain) {
		t.Fatalf("ReadChain = %v, want ErrIncompleteChain", err)
	}
	if !strings.Contains(err.Error(), string(c1)) {
		t.Errorf("error %q does not name the missing sha %s", err, c1)
	}
	if !strings.Contains(err.Error(), "missing from object database") {
		t.Errorf("error %q does not report a non-shallow miss", err)
	}
}

// TestReadChainIncompleteShallow points refs/heads/main at an ops commit, then
// clones it --depth 1 over file://: the tip's parent is beyond the shallow
// boundary, so ReadChain fails ErrIncompleteChain naming the absent parent and
// reports a shallow clone (the .git/shallow file exists).
func TestReadChainIncompleteShallow(t *testing.T) {
	origin := initRepo(t)
	repo := open(t, origin)
	c1 := write(t, repo, nil, t0, createPack)
	c2 := write(t, repo, []model.SHA{c1}, t1, retitlePack)
	git(t, origin, "update-ref", "refs/heads/main", string(c2))

	clone := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(clone), "-c", "protocol.file.allow=always", "clone", "-q", "--depth", "1", "file://"+origin, clone)

	_, err := open(t, clone).ReadChain(t.Context(), c2)
	if !errors.Is(err, gitobj.ErrIncompleteChain) {
		t.Fatalf("ReadChain = %v, want ErrIncompleteChain", err)
	}
	if !strings.Contains(err.Error(), "shallow clone") {
		t.Errorf("error %q does not report a shallow clone", err)
	}
	if !strings.Contains(err.Error(), string(c1)) {
		t.Errorf("error %q does not name the missing sha %s", err, c1)
	}
}

// stalePack reproduces the go-git pack-index staleness the reindex fix heals:
// origin packs an ops chain c1<-c2, a handle seeds its index with a successful
// packed ReadChain(c2), then a peer's c3 is fetched into a *second* pack
// invisible to that seeded index. It returns the pre-seeded handle plus the
// three commit shas; every reader must reindex on the c3 miss to see it.
func stalePack(t *testing.T) (repo *gitobj.Repo, c1, c2, c3 model.SHA) {
	t.Helper()
	origin := initRepo(t)
	repo = open(t, origin)
	c1 = write(t, repo, nil, t0, createPack)
	c2 = write(t, repo, []model.SHA{c1}, t1, retitlePack)
	ref := "refs/cc-notes/notes/n1"
	git(t, origin, "update-ref", ref, string(c2))
	git(t, origin, "repack", "-a", "-d", "-q")

	if _, err := repo.ReadChain(t.Context(), c2); err != nil {
		t.Fatalf("seed ReadChain(c2): %v", err)
	}

	peer := initRepo(t)
	peerRepo := open(t, peer)
	write(t, peerRepo, nil, t0, createPack)
	write(t, peerRepo, []model.SHA{c1}, t1, retitlePack)
	c3 = write(t, peerRepo, []model.SHA{c2}, t2, bodyPack)
	git(t, peer, "update-ref", ref, string(c3))

	git(t, origin, "-c", "transfer.unpackLimit=1", "-c", "protocol.file.allow=always",
		"fetch", "-q", "file://"+peer, ref+":"+ref)

	packs, err := filepath.Glob(filepath.Join(origin, ".git", "objects", "pack", "*.pack"))
	if err != nil {
		t.Fatalf("glob packs: %v", err)
	}
	if len(packs) != 2 {
		t.Fatalf("want exactly 2 packs after unpackLimit=1 fetch, got %d: %v", len(packs), packs)
	}
	loose := filepath.Join(origin, ".git", "objects", string(c3)[:2], string(c3)[2:])
	if _, err := os.Stat(loose); !os.IsNotExist(err) {
		t.Fatalf("c3 %s must stay packed, but stat of loose object gave %v", c3, err)
	}
	return repo, c1, c2, c3
}

// TestReindexOnStalePack proves every public reader reindexes on a miss caused
// by a pack landed after the index was seeded (the sync incident). Each case
// gets its own stalePack fixture so its own wrap site is exercised: reverting
// the reindex retry fails every case with ErrIncompleteChain/ErrCommitNotFound.
func TestReindexOnStalePack(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, repo *gitobj.Repo, c1, c2, c3 model.SHA)
	}{
		{"ReadChain", func(t *testing.T, repo *gitobj.Repo, c1, c2, c3 model.SHA) {
			got, err := repo.ReadChain(t.Context(), c3)
			if err != nil {
				t.Fatalf("ReadChain(c3): %v", err)
			}
			want := []model.PackCommit{
				{SHA: c3, Parents: []model.SHA{c2}, Author: testActor, AuthorTime: t2.Unix(), Pack: bodyPack},
				{SHA: c2, Parents: []model.SHA{c1}, Author: testActor, AuthorTime: t1.Unix(), Pack: retitlePack},
				{SHA: c1, Parents: nil, Author: testActor, AuthorTime: t0.Unix(), Pack: createPack},
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("ReadChain = %+v, want %+v", got, want)
			}
		}},
		{"WalkCommits", func(t *testing.T, repo *gitobj.Repo, c1, c2, c3 model.SHA) {
			got, truncated, err := repo.WalkCommits(t.Context(), []model.SHA{c3}, 0, 0)
			if err != nil {
				t.Fatalf("WalkCommits([c3]): %v", err)
			}
			if truncated {
				t.Errorf("truncated = true, want false (c3 reachable after reindex)")
			}
			if shas := []model.SHA{got[0].SHA, got[1].SHA, got[2].SHA}; len(got) != 3 || shas[0] != c3 || shas[1] != c2 || shas[2] != c1 {
				t.Errorf("WalkCommits = %v, want [%s %s %s]", got, c3, c2, c1)
			}
		}},
		{"FirstParentMerges", func(t *testing.T, repo *gitobj.Repo, _, _, c3 model.SHA) {
			got, err := repo.FirstParentMerges(t.Context(), c3, 0, 0)
			if err != nil {
				t.Fatalf("FirstParentMerges(c3): %v", err)
			}
			if got != nil {
				t.Errorf("FirstParentMerges = %v, want nil (linear chain has no merges)", got)
			}
		}},
		{"IsAncestor", func(t *testing.T, repo *gitobj.Repo, c1, _, c3 model.SHA) {
			got, err := repo.IsAncestor(t.Context(), c1, c3)
			if err != nil {
				t.Fatalf("IsAncestor(c1, c3): %v", err)
			}
			if !got {
				t.Errorf("IsAncestor(c1, c3) = false, want true")
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo, c1, c2, c3 := stalePack(t)
			tc.run(t, repo, c1, c2, c3)
		})
	}
}

func TestReadChainCorruptCommit(t *testing.T) {
	dir := initRepo(t)
	emptyTree := gitStdin(t, dir, "", "mktree")
	bare := git(t, dir, "commit-tree", emptyTree, "-m", "no ops")

	_, err := open(t, dir).ReadChain(t.Context(), model.SHA(bare))
	if !errors.Is(err, gitobj.ErrCorruptCommit) {
		t.Fatalf("ReadChain = %v, want ErrCorruptCommit", err)
	}
	if !strings.Contains(err.Error(), bare) {
		t.Errorf("error %q does not name the corrupt commit %s", err, bare)
	}
}

func TestReadChainUndecodablePack(t *testing.T) {
	dir := initRepo(t)
	blob := gitStdin(t, dir, "junk", "hash-object", "-w", "--stdin")
	tree := gitStdin(t, dir, fmt.Sprintf("100644 blob %s\tops.json\n", blob), "mktree")
	sha := git(t, dir, "commit-tree", tree, "-m", "junk ops")

	_, err := open(t, dir).ReadChain(t.Context(), model.SHA(sha))
	if err == nil || !strings.Contains(err.Error(), sha) {
		t.Fatalf("ReadChain = %v, want decode error naming commit %s", err, sha)
	}
}

func TestTip(t *testing.T) {
	dir := initRepo(t)
	repo := open(t, dir)
	sha := write(t, repo, nil, t0, createPack)
	ref := "refs/cc-notes/notes/" + string(sha)
	git(t, dir, "update-ref", ref, string(sha))

	got, err := repo.Tip(t.Context(), ref)
	if err != nil {
		t.Fatalf("Tip: %v", err)
	}
	if got != sha {
		t.Errorf("Tip = %s, want %s", got, sha)
	}

	_, err = repo.Tip(t.Context(), "refs/cc-notes/notes/"+strings.Repeat("ab", 20))
	if !errors.Is(err, gitobj.ErrRefNotFound) {
		t.Errorf("Tip on missing ref = %v, want ErrRefNotFound", err)
	}
}

func TestListPrefix(t *testing.T) {
	dir := initRepo(t)
	repo := open(t, dir)
	git(t, dir, "commit", "--allow-empty", "-q", "-m", "base")

	note1 := write(t, repo, nil, t0, createPack)
	note2 := write(t, repo, nil, t1, createPack)
	task := write(t, repo, nil, t2, createPack)
	refs := map[string]model.SHA{
		"refs/cc-notes/notes/" + string(note1):     note1,
		"refs/cc-notes/notes/" + string(note2):     note2,
		"refs/cc-notes/tasks/main/" + string(task): task,
	}
	for ref, sha := range refs {
		git(t, dir, "update-ref", ref, string(sha))
	}

	assertPrefix := func(repo *gitobj.Repo, prefix string, want map[string]model.SHA) {
		t.Helper()
		got, err := repo.ListPrefix(t.Context(), prefix)
		if err != nil {
			t.Fatalf("ListPrefix(%s): %v", prefix, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("ListPrefix(%s) = %v, want %v", prefix, got, want)
		}
	}
	assertPrefix(repo, "refs/cc-notes/", refs)
	assertPrefix(repo, "refs/cc-notes/notes/", map[string]model.SHA{
		"refs/cc-notes/notes/" + string(note1): note1,
		"refs/cc-notes/notes/" + string(note2): note2,
	})
	assertPrefix(repo, "refs/cc-notes/tasks/", map[string]model.SHA{
		"refs/cc-notes/tasks/main/" + string(task): task,
	})

	git(t, dir, "pack-refs", "--all")
	assertPrefix(open(t, dir), "refs/cc-notes/", refs)
}

// TestListPrefixSkipsLockFiles pins that a git ref-write lock file — created
// by a concurrent update-ref and surfaced as a ref by go-git's refs walk —
// never appears in a listing: git forbids the .lock suffix in refnames.
func TestListPrefixSkipsLockFiles(t *testing.T) {
	dir := initRepo(t)
	repo := open(t, dir)
	note := write(t, repo, nil, t0, createPack)
	ref := "refs/cc-notes/notes/" + string(note)
	git(t, dir, "update-ref", ref, string(note))

	lock := filepath.Join(dir, ".git", "refs", "cc-notes", "notes", "other.lock")
	if err := os.WriteFile(lock, []byte(note+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := repo.ListPrefix(t.Context(), "refs/cc-notes/")
	if err != nil {
		t.Fatalf("ListPrefix: %v", err)
	}
	want := map[string]model.SHA{ref: note}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ListPrefix = %v, want %v", got, want)
	}
}

// TestListPrefixPersistentEmptyRefFile pins the fail-loud bound of the
// empty-ref retry: a file under refs/ that stays empty — a crashed writer,
// not a transient lock window — must surface, not be swallowed.
func TestListPrefixPersistentEmptyRefFile(t *testing.T) {
	cases := []struct {
		name string
		file string
	}{
		{"empty lock file", "stuck.lock"},
		{"empty ref file", "stuck"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := initRepo(t)
			repo := open(t, dir)
			note := write(t, repo, nil, t0, createPack)
			git(t, dir, "update-ref", "refs/cc-notes/notes/"+string(note), string(note))

			empty := filepath.Join(dir, ".git", "refs", "cc-notes", "notes", tc.file)
			if err := os.WriteFile(empty, nil, 0o644); err != nil {
				t.Fatal(err)
			}

			_, err := repo.ListPrefix(t.Context(), "refs/cc-notes/")
			if !errors.Is(err, dotgit.ErrEmptyRefFile) {
				t.Errorf("ListPrefix = %v, want dotgit.ErrEmptyRefFile", err)
			}
		})
	}
}

// TestListPrefixDuringRefWrites reproduces the claim-contest race: real git
// update-ref processes rewrite a ref — each holding a briefly-empty
// <ref>.lock — while this process lists the prefix through go-git. Without
// the empty-ref retry the listing fails mid-contest with "ref file is
// empty"; without the .lock skip it surfaces phantom lock-file refs.
func TestListPrefixDuringRefWrites(t *testing.T) {
	dir := initRepo(t)
	repo := open(t, dir)
	c1 := write(t, repo, nil, t0, createPack)
	c2 := write(t, repo, []model.SHA{c1}, t1, retitlePack)
	ref := "refs/cc-notes/notes/contested"
	git(t, dir, "update-ref", ref, string(c1))

	const updates = 150
	shas := [2]model.SHA{c1, c2}
	writerErr := make(chan error, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range updates {
			//nolint:gosec // G204: fixed argv[0], test-controlled args.
			cmd := exec.Command("git", "update-ref", ref, string(shas[i%2]))
			cmd.Dir = dir
			cmd.Env = gitEnv()
			if out, err := cmd.CombinedOutput(); err != nil {
				writerErr <- fmt.Errorf("update-ref %d: %v\n%s", i, err, out)
				return
			}
		}
	}()

	for listing := true; listing; {
		select {
		case <-done:
			listing = false
		default:
		}
		tips, err := repo.ListPrefix(t.Context(), "refs/cc-notes/")
		if err != nil {
			t.Fatalf("ListPrefix during ref writes: %v", err)
		}
		for name := range tips {
			if strings.HasSuffix(name, ".lock") {
				t.Fatalf("ListPrefix surfaced lock file %s", name)
			}
		}
		// Directory enumeration during a same-directory rename may
		// transiently see neither name (unspecified by POSIX), so the ref
		// may be absent from one listing; when present it must hold a
		// value some update-ref actually wrote.
		if got, ok := tips[ref]; ok && got != c1 && got != c2 {
			t.Fatalf("ListPrefix[%s] = %q, want %s or %s", ref, got, c1, c2)
		}
	}
	select {
	case err := <-writerErr:
		t.Fatal(err)
	default:
	}
}

func TestIsAncestor(t *testing.T) {
	repo := open(t, initRepo(t))
	root := write(t, repo, nil, t0, createPack)
	mid := write(t, repo, []model.SHA{root}, t1, retitlePack)
	tip := write(t, repo, []model.SHA{mid}, t2, bodyPack)
	sibling := write(t, repo, []model.SHA{root}, t3, tagPack)

	cases := []struct {
		name string
		a, b model.SHA
		want bool
	}{
		{"root ancestor of tip", root, tip, true},
		{"tip not ancestor of root", tip, root, false},
		{"commit is its own ancestor", mid, mid, true},
		{"mid not ancestor of sibling", mid, sibling, false},
		{"sibling not ancestor of tip", sibling, tip, false},
		{"root ancestor of sibling", root, sibling, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := repo.IsAncestor(t.Context(), tc.a, tc.b)
			if err != nil {
				t.Fatalf("IsAncestor: %v", err)
			}
			if got != tc.want {
				t.Errorf("IsAncestor(%s, %s) = %t, want %t", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestOpenFromSubdirectory(t *testing.T) {
	dir := initRepo(t)
	sha := write(t, open(t, dir), nil, t0, createPack)
	ref := "refs/cc-notes/notes/" + string(sha)
	git(t, dir, "update-ref", ref, string(sha))

	sub := filepath.Join(dir, "deep", "nested")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", sub, err)
	}
	got, err := open(t, sub).Tip(t.Context(), ref)
	if err != nil {
		t.Fatalf("Tip from subdirectory: %v", err)
	}
	if got != sha {
		t.Errorf("Tip = %s, want %s", got, sha)
	}
}

func TestOpenLinkedWorktree(t *testing.T) {
	dir := initRepo(t)
	git(t, dir, "commit", "--allow-empty", "-q", "-m", "base")
	main := open(t, dir)
	sha := write(t, main, nil, t0, createPack)
	ref := "refs/cc-notes/notes/" + string(sha)
	git(t, dir, "update-ref", ref, string(sha))

	wt := filepath.Join(t.TempDir(), "wt")
	git(t, dir, "worktree", "add", "-q", "-b", "scratch", wt)

	repo := open(t, wt)
	got, err := repo.Tip(t.Context(), ref)
	if err != nil {
		t.Fatalf("Tip from worktree: %v", err)
	}
	if got != sha {
		t.Errorf("Tip = %s, want %s", got, sha)
	}
	listed, err := repo.ListPrefix(t.Context(), "refs/cc-notes/")
	if err != nil {
		t.Fatalf("ListPrefix from worktree: %v", err)
	}
	if want := map[string]model.SHA{ref: sha}; !reflect.DeepEqual(listed, want) {
		t.Errorf("ListPrefix = %v, want %v", listed, want)
	}

	child := write(t, repo, []model.SHA{sha}, t1, retitlePack)
	if typ := git(t, dir, "cat-file", "-t", string(child)); typ != "commit" {
		t.Fatalf("worktree-written object not in shared ODB: cat-file -t = %q", typ)
	}
	chain, err := open(t, dir).ReadChain(t.Context(), child)
	if err != nil {
		t.Fatalf("ReadChain from main repo: %v", err)
	}
	if len(chain) != 2 || chain[0].SHA != child || chain[1].SHA != sha {
		t.Errorf("chain = %+v, want [%s %s]", chain, child, sha)
	}

	sub := filepath.Join(wt, "inner")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatalf("mkdir %s: %v", sub, err)
	}
	if got, err := open(t, sub).Tip(t.Context(), ref); err != nil || got != sha {
		t.Errorf("Tip from worktree subdirectory = %s, %v; want %s, nil", got, err, sha)
	}
}

// TestConcurrentAccess pins Repo's synchronization story: go-git's
// filesystem storage caches are not thread-safe, so concurrent writes and
// reads through one Repo must serialize internally (fails under -race
// without the Repo mutex).
func TestConcurrentAccess(t *testing.T) {
	repo := open(t, initRepo(t))
	root := write(t, repo, nil, t0, createPack)

	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pack := model.Pack{Lamport: 2, Ops: []model.Op{model.AddTag{Tag: fmt.Sprintf("t%d", i)}}}
			sha, err := repo.WriteOpsCommit(t.Context(), []model.SHA{root}, sigAt(t1), "m", pack)
			if err != nil {
				t.Errorf("WriteOpsCommit %d: %v", i, err)
				return
			}
			chain, err := repo.ReadChain(t.Context(), sha)
			if err != nil {
				t.Errorf("ReadChain %d: %v", i, err)
				return
			}
			if len(chain) != 2 || chain[0].SHA != sha || chain[1].SHA != root {
				t.Errorf("chain %d = %+v, want [%s %s]", i, chain, sha, root)
			}
		}()
	}
	wg.Wait()
}

func TestContextCancelled(t *testing.T) {
	repo := open(t, initRepo(t))
	sha := write(t, repo, nil, t0, createPack)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := repo.WriteOpsCommit(ctx, nil, sigAt(t0), "m", createPack); !errors.Is(err, context.Canceled) {
		t.Errorf("WriteOpsCommit with cancelled ctx = %v, want context.Canceled", err)
	}
	if _, err := repo.ReadChain(ctx, sha); !errors.Is(err, context.Canceled) {
		t.Errorf("ReadChain with cancelled ctx = %v, want context.Canceled", err)
	}
}
