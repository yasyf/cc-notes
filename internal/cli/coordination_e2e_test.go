// Coordination contract suite: drives the real built binary against real git
// repositories to pin task start/renew/claim --steal/--sync, task done commit
// linkage, and blame across the cc-task trailer and the folded commit set.
package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
)

// showTaskBin folds a task through `task show --json` as actor.
func showTaskBin(t *testing.T, dir, actor, id string) taskJSON {
	t.Helper()
	return mustJSON[taskJSON](t, mustBin(t, dir, actor, "task", "show", id, "--json"))
}

func TestTaskStartMovesOntoBranch(t *testing.T) {
	dir := initRepo(t)
	task := addTaskBin(t, dir, "Rotate keys", "--backlog")
	if task.Branch != "" {
		t.Fatalf("backlog task branch = %q, want empty", task.Branch)
	}
	out := mustBin(t, dir, actorA, "task", "start", task.ID)
	if want := task.ID[:7] + "\tin_progress\tP2\t" + actorA + "\tRotate keys\n"; out != want {
		t.Fatalf("start lean line = %q, want %q", out, want)
	}
	shown := showTaskBin(t, dir, actorA, task.ID)
	if shown.Branch != "main" || shown.Status != "in_progress" {
		t.Fatalf("after start branch/status = %q/%q, want main/in_progress", shown.Branch, shown.Status)
	}
	if shown.Assignee == nil || *shown.Assignee != actorA {
		t.Fatalf("after start assignee = %v, want %q", shown.Assignee, actorA)
	}
}

func TestTaskStartLostRaceDoesNotMove(t *testing.T) {
	dir := initRepo(t)
	task := addTaskBin(t, dir, "Contended", "--backlog")
	mustBin(t, dir, actorB, "task", "claim", task.ID)

	res, err := execBin(dir, actorA, "task", "start", task.ID)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if res.Code != 4 {
		t.Fatalf("start lost race exit = %d, want 4 (stderr %q)", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "already claimed by "+actorB) {
		t.Fatalf("start lost race stderr = %q, want already-claimed-by-B", res.Stderr)
	}
	shown := showTaskBin(t, dir, actorA, task.ID)
	if shown.Branch != "" {
		t.Fatalf("lost-race task branch = %q, want empty (SetBranch must not fire)", shown.Branch)
	}
	if shown.Assignee == nil || *shown.Assignee != actorB {
		t.Fatalf("lost-race assignee = %v, want %q", shown.Assignee, actorB)
	}
}

func TestTaskStartDetachedHead(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "a.txt", "one")
	task := addTaskBin(t, dir, "Detached", "--backlog")
	gittest.Git(t, dir, "checkout", "-q", "--detach")

	// Detached at main's tip — the jj colocation norm. CurrentBranch resolves
	// main, so start claims the task and moves it onto main.
	out := mustBin(t, dir, actorA, "task", "start", task.ID)
	if want := task.ID[:7] + "\tin_progress\tP2\t" + actorA + "\tDetached\n"; out != want {
		t.Fatalf("detached-at-tip start lean line = %q, want %q", out, want)
	}
	shown := showTaskBin(t, dir, actorA, task.ID)
	if shown.Branch != "main" || shown.Status != "in_progress" {
		t.Fatalf("detached-at-tip start branch/status = %q/%q, want main/in_progress", shown.Branch, shown.Status)
	}
	if shown.Assignee == nil || *shown.Assignee != actorA {
		t.Fatalf("detached-at-tip start assignee = %v, want %q", shown.Assignee, actorA)
	}
}

// TestTaskStartAmbiguousHead pins the graceful-degrade path: on a genuinely
// unresolvable HEAD (no trunk, advanced past the sole bookmark) start claims the
// task without a branch and warns, while --branch still overrides.
func TestTaskStartAmbiguousHead(t *testing.T) {
	dir := initRepo(t)
	task := addTaskBin(t, dir, "Ambiguous", "--backlog")
	// No trunk: rename the unborn main to wip so main never exists, then detach
	// and advance past wip's tip. CurrentBranch cannot resolve a branch.
	gittest.Git(t, dir, "checkout", "-q", "-b", "wip")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c1")
	gittest.Git(t, dir, "checkout", "-q", "--detach")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c2")

	res, err := execBin(dir, actorA, "task", "start", task.ID)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("ambiguous start exit = %d, stderr %q", res.Code, res.Stderr)
	}
	if want := task.ID[:7] + "\tin_progress\tP2\t" + actorA + "\tAmbiguous\n"; res.Stdout != want {
		t.Fatalf("ambiguous start stdout = %q, want %q", res.Stdout, want)
	}
	if !strings.Contains(res.Stderr, "detached HEAD with no resolvable branch") ||
		!strings.Contains(res.Stderr, "claimed "+task.ID[:7]) {
		t.Fatalf("ambiguous start stderr = %q, want detached-HEAD warning naming the task", res.Stderr)
	}
	shown := showTaskBin(t, dir, actorA, task.ID)
	if shown.Branch != "" || shown.Status != "in_progress" {
		t.Fatalf("ambiguous start branch/status = %q/%q, want empty/in_progress", shown.Branch, shown.Status)
	}
	if shown.Assignee == nil || *shown.Assignee != actorA {
		t.Fatalf("ambiguous start assignee = %v, want %q", shown.Assignee, actorA)
	}

	other := addTaskBin(t, dir, "With flag", "--backlog")
	out := mustBin(t, dir, actorA, "task", "start", other.ID, "--branch", "feat")
	if want := other.ID[:7] + "\tin_progress\tP2\t" + actorA + "\tWith flag\n"; out != want {
		t.Fatalf("--branch start lean line = %q, want %q", out, want)
	}
	if shown := showTaskBin(t, dir, actorA, other.ID); shown.Branch != "feat" {
		t.Fatalf("--branch start branch = %q, want feat", shown.Branch)
	}
}

// TestTaskStartRejectsBadBranch pins that a bad --branch is a UsageError raised
// at the resolve gate before the Claim append. The empty case is the regression:
// an explicitly-passed --branch= must NOT fall through to the current branch the
// way an omitted flag does. Both an empty value and an invalid ref format exit 2
// and leave the task open and unassigned on an attached-main repo, proving
// validation precedes any mutation.
func TestTaskStartRejectsBadBranch(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"invalid-format", "bad..name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := initRepo(t)
			task := addTaskBin(t, dir, "Guarded", "--backlog")

			res, err := execBin(dir, actorA, "task", "start", task.ID, "--branch="+tc.value)
			if err != nil {
				t.Fatalf("start: %v", err)
			}
			if res.Code != 2 {
				t.Fatalf("start --branch=%q exit = %d, want UsageError exit 2 (stderr %q)", tc.value, res.Code, res.Stderr)
			}
			if !strings.Contains(res.Stderr, "invalid branch") {
				t.Errorf("start --branch=%q stderr = %q, want it to name the invalid branch", tc.value, res.Stderr)
			}
			shown := showTaskBin(t, dir, actorA, task.ID)
			if shown.Status != "open" {
				t.Errorf("after rejected start status = %q, want open (Claim must not fire)", shown.Status)
			}
			if shown.Assignee != nil {
				t.Errorf("after rejected start assignee = %q, want nil (Claim must not fire)", *shown.Assignee)
			}
			if shown.Branch != "" {
				t.Errorf("after rejected start branch = %q, want empty", shown.Branch)
			}
		})
	}
}

// TestTaskAddEmptyBranch pins that an explicitly empty --branch= on add is a
// UsageError, not a fall-through to the current branch, and creates no task.
func TestTaskAddEmptyBranch(t *testing.T) {
	dir := initRepo(t)
	res, err := execBin(dir, actorA, "task", "add", "Ghost", "--no-validation-criteria", "--branch=")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if res.Code != 2 {
		t.Fatalf("task add --branch= exit = %d, want UsageError exit 2 (stderr %q)", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "invalid branch") {
		t.Errorf("task add --branch= stderr = %q, want it to name the invalid branch", res.Stderr)
	}
	listed := mustJSON[[]taskJSON](t, mustBin(t, dir, actorA, "task", "list", "--all-branches", "--json"))
	if len(listed) != 0 {
		t.Fatalf("after rejected add, task list --all-branches = %d tasks, want 0", len(listed))
	}
}

// TestTaskAddAmbiguousHead pins add's graceful-degrade path: on a genuinely
// unresolvable HEAD (no trunk, advanced past the sole bookmark) an omitted
// --branch creates the task on the backlog and warns, and task ready lists it
// via the same backlog degrade.
func TestTaskAddAmbiguousHead(t *testing.T) {
	dir := initRepo(t)
	// No trunk: rename the unborn main to wip so main never exists, then detach
	// and advance past wip's tip. CurrentBranch cannot resolve a branch.
	gittest.Git(t, dir, "checkout", "-q", "-b", "wip")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c1")
	gittest.Git(t, dir, "checkout", "-q", "--detach")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c2")

	res, err := execBin(dir, actorA, "task", "add", "Homeless", "--no-validation-criteria", "--json")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("ambiguous add exit = %d, stderr %q", res.Code, res.Stderr)
	}
	if !strings.Contains(res.Stderr, "created on the backlog") {
		t.Fatalf("ambiguous add stderr = %q, want the backlog-degrade warning", res.Stderr)
	}
	task := mustJSON[taskJSON](t, res.Stdout)
	if task.Branch != "" {
		t.Fatalf("ambiguous add branch = %q, want empty (backlog)", task.Branch)
	}
	ready := mustBin(t, dir, actorA, "task", "ready")
	if !strings.Contains(ready, task.ID[:7]) {
		t.Fatalf("task ready = %q, want the backlog task %s via backlog degrade", ready, task.ID[:7])
	}
}

func TestTaskRenew(t *testing.T) {
	dir := initRepo(t)
	task := addTaskBin(t, dir, "Long job")
	claimed := mustJSON[taskJSON](t, mustBin(t, dir, actorA, "task", "claim", task.ID, "--json"))
	if claimed.Lease.Heartbeat == nil {
		t.Fatal("claim left lease.heartbeat null")
	}
	before := mustTime(t, *claimed.Lease.Heartbeat)

	res, err := execBin(dir, actorB, "task", "renew", task.ID)
	if err != nil {
		t.Fatalf("renew: %v", err)
	}
	if res.Code != 4 || !strings.Contains(res.Stderr, "not you") {
		t.Fatalf("non-assignee renew = exit %d stderr %q, want exit 4 not-you", res.Code, res.Stderr)
	}

	// The heartbeat is the assignee op's AuthorTime at one-second granularity, so
	// advance the wall clock past the claim's second before renewing.
	time.Sleep(1100 * time.Millisecond)
	renewed := mustJSON[taskJSON](t, mustBin(t, dir, actorA, "task", "renew", task.ID, "--json"))
	if renewed.Lease.Heartbeat == nil {
		t.Fatal("renew left lease.heartbeat null")
	}
	after := mustTime(t, *renewed.Lease.Heartbeat)
	if !after.After(before) {
		t.Fatalf("renew heartbeat %v not after claim heartbeat %v", after, before)
	}
}

func mustTime(t *testing.T, rfc string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		t.Fatalf("parse time %q: %v", rfc, err)
	}
	return ts
}

func TestTaskClaimSteal(t *testing.T) {
	dir := initRepo(t)
	task := addTaskBin(t, dir, "Crashed agent's task")
	mustBin(t, dir, actorA, "task", "claim", task.ID)

	// Fresh lease: a steal is refused with the remaining time, exit 4.
	gittest.Git(t, dir, "config", "cc-notes.leaseTTL", "8760h")
	res, err := execBin(dir, actorB, "task", "claim", task.ID, "--steal")
	if err != nil {
		t.Fatalf("steal fresh: %v", err)
	}
	if res.Code != 4 || !strings.Contains(res.Stderr, "lease held by "+actorA) {
		t.Fatalf("steal of fresh lease = exit %d stderr %q, want exit 4 lease-held", res.Code, res.Stderr)
	}
	if shown := showTaskBin(t, dir, actorB, task.ID); shown.Assignee == nil || *shown.Assignee != actorA {
		t.Fatalf("fresh-lease steal moved holder to %v, want %q", shown.Assignee, actorA)
	}

	// Stale lease: the steal reclaims it.
	gittest.Git(t, dir, "config", "cc-notes.leaseTTL", "0s")
	out := mustBin(t, dir, actorB, "task", "claim", task.ID, "--steal")
	if want := task.ID[:7] + "\tin_progress\tP2\t" + actorB + "\tCrashed agent's task\n"; out != want {
		t.Fatalf("steal stale lean line = %q, want %q", out, want)
	}
	shown := showTaskBin(t, dir, actorB, task.ID)
	if shown.Assignee == nil || *shown.Assignee != actorB || shown.Status != "in_progress" {
		t.Fatalf("after steal assignee/status = %v/%q, want %q/in_progress", shown.Assignee, shown.Status, actorB)
	}
}

func TestTaskDoneLinksCommit(t *testing.T) {
	dir := initRepo(t)
	head := commitFile(t, dir, "impl.go", "package main")
	task := addTaskBin(t, dir, "Implement it")
	mustBin(t, dir, actorA, "task", "claim", task.ID)
	mustBin(t, dir, actorA, "task", "done", task.ID)

	shown := showTaskBin(t, dir, actorA, task.ID)
	if len(shown.Commits) != 1 || shown.Commits[0] != head {
		t.Fatalf("done commits = %v, want [%s]", shown.Commits, head)
	}
	text := mustBin(t, dir, actorA, "task", "show", task.ID)
	if !strings.Contains(text, "commits: "+head[:7]) {
		t.Fatalf("task show = %q, want commits header listing %s", text, head[:7])
	}
}

func TestBlame(t *testing.T) {
	dir := initRepo(t)

	// Trailer-only: an open task named by a commit's cc-task trailer.
	trailerTask := addTaskBin(t, dir, "Trailer task", "--backlog")
	trailerSHA := commitWithTrailer(t, dir, "feat.go", "package main", trailerTask.ID)

	// LinkCommit-only: a done task whose HEAD commit was anchored, no trailer.
	linkTask := addTaskBin(t, dir, "Linked task")
	linkSHA := commitFile(t, dir, "link.go", "package main")
	mustBin(t, dir, actorA, "task", "claim", linkTask.ID)
	mustBin(t, dir, actorA, "task", "done", linkTask.ID)

	// Both: a commit named by a trailer AND recorded as the task's done anchor.
	bothTask := addTaskBin(t, dir, "Both task")
	bothSHA := commitWithTrailer(t, dir, "both.go", "package main", bothTask.ID)
	mustBin(t, dir, actorA, "task", "claim", bothTask.ID)
	mustBin(t, dir, actorA, "task", "done", bothTask.ID)

	cases := []struct {
		name string
		sha  string
		want string
	}{
		{"trailer", trailerSHA, trailerTask.ID},
		{"link_commit", linkSHA, linkTask.ID},
		{"union dedup", bothSHA, bothTask.ID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := mustBin(t, dir, actorA, "blame", tc.sha)
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			if len(lines) != 1 {
				t.Fatalf("blame %s = %q, want exactly one task line", tc.name, out)
			}
			if !strings.HasPrefix(lines[0], tc.want[:7]+"\t") {
				t.Fatalf("blame %s = %q, want task %s", tc.name, out, tc.want[:7])
			}
		})
	}
}

// commitWithTrailer commits path under dir with a cc-task trailer naming id and
// returns the new HEAD sha.
func commitWithTrailer(t *testing.T, dir, path, content, id string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	gittest.Git(t, dir, "add", path)
	gittest.Git(t, dir, "commit", "-q", "-m", "implement\n\ncc-task: "+id)
	return gittest.Git(t, dir, "rev-parse", "HEAD")
}

func TestTaskClaimSyncYield(t *testing.T) {
	gittest.ScrubEnv(t)
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	gittest.Git(t, root, "init", "-q", "--bare", "-b", "main", "remote.git")
	clone := func(name string) string {
		dir := filepath.Join(root, name)
		gittest.Git(t, root, "clone", "-q", bare, name)
		gittest.Git(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
		return dir
	}

	cloneA := clone("a")
	mustBin(t, cloneA, actorA, "init")
	task := addTaskBin(t, cloneA, "Shared work")
	mustBin(t, cloneA, actorA, "sync")

	cloneB := clone("b")
	mustBin(t, cloneB, actorB, "sync")

	// A claims and pushes first; its claim carries the earlier author-time, so it
	// wins linearization deterministically over B's later same-lamport claim.
	mustBin(t, cloneA, actorA, "task", "claim", task.ID)
	mustBin(t, cloneA, actorA, "sync")
	time.Sleep(1100 * time.Millisecond)

	res, err := execBin(cloneB, actorB, "task", "claim", task.ID, "--sync")
	if err != nil {
		t.Fatalf("claim --sync: %v", err)
	}
	if res.Code != 4 || !strings.Contains(res.Stderr, "claimed by "+actorA) {
		t.Fatalf("claim --sync yield = exit %d stderr %q, want exit 4 claimed-by-A", res.Code, res.Stderr)
	}

	mustBin(t, cloneA, actorA, "sync")
	a := showTaskBin(t, cloneA, actorA, task.ID)
	b := showTaskBin(t, cloneB, actorB, task.ID)
	if a.Assignee == nil || *a.Assignee != actorA || b.Assignee == nil || *b.Assignee != actorA {
		t.Fatalf("post-sync assignees A=%v B=%v, want both %q", a.Assignee, b.Assignee, actorA)
	}
}

func TestTwoCloneStealConverges(t *testing.T) {
	gittest.ScrubEnv(t)
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	gittest.Git(t, root, "init", "-q", "--bare", "-b", "main", "remote.git")
	clone := func(name string) string {
		dir := filepath.Join(root, name)
		gittest.Git(t, root, "clone", "-q", bare, name)
		gittest.Git(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
		gittest.Git(t, dir, "config", "cc-notes.leaseTTL", "0s")
		return dir
	}

	cloneA := clone("a")
	mustBin(t, cloneA, actorA, "init")
	task := addTaskBin(t, cloneA, "Abandoned")
	mustBin(t, cloneA, actorA, "task", "claim", task.ID)
	mustBin(t, cloneA, actorA, "sync")

	cloneB := clone("b")
	mustBin(t, cloneB, actorB, "init")
	mustBin(t, cloneB, actorB, "sync")
	if held := showTaskBin(t, cloneB, actorB, task.ID); held.Assignee == nil || *held.Assignee != actorA {
		t.Fatalf("clone B sees holder %v, want %q", held.Assignee, actorA)
	}

	// B's view of A's lease is stale (ttl 0s); B steals and pushes the reclaim.
	mustBin(t, cloneB, actorB, "task", "claim", task.ID, "--steal")
	mustBin(t, cloneB, actorB, "sync")
	mustBin(t, cloneA, actorA, "sync")

	a := showTaskBin(t, cloneA, actorA, task.ID)
	b := showTaskBin(t, cloneB, actorB, task.ID)
	if a.Assignee == nil || *a.Assignee != actorB || b.Assignee == nil || *b.Assignee != actorB {
		t.Fatalf("post-steal assignees A=%v B=%v, want both %q", a.Assignee, b.Assignee, actorB)
	}
	if a.Status != "in_progress" || b.Status != "in_progress" {
		t.Fatalf("post-steal status A=%q B=%q, want both in_progress", a.Status, b.Status)
	}
}
