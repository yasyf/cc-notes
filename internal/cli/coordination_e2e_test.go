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
	mustGit(t, dir, "checkout", "-q", "--detach")

	res, err := execBin(dir, actorA, "task", "start", task.ID)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if res.Code != 1 || !strings.Contains(res.Stderr, "detached HEAD") {
		t.Fatalf("detached start = exit %d stderr %q, want exit 1 with detached-HEAD message", res.Code, res.Stderr)
	}
	shown := showTaskBin(t, dir, actorA, task.ID)
	if shown.Status != "open" || shown.Assignee != nil {
		t.Fatalf("detached start mutated task: status %q assignee %v, want open/null", shown.Status, shown.Assignee)
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
	mustGit(t, dir, "config", "cc-notes.leaseTTL", "8760h")
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
	mustGit(t, dir, "config", "cc-notes.leaseTTL", "0s")
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
	if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	mustGit(t, dir, "add", path)
	mustGit(t, dir, "commit", "-q", "-m", "implement\n\ncc-task: "+id)
	return mustGit(t, dir, "rev-parse", "HEAD")
}

func TestTaskClaimSyncYield(t *testing.T) {
	scrubGitEnv(t)
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	mustGit(t, root, "init", "-q", "--bare", "-b", "main", "remote.git")
	clone := func(name string) string {
		dir := filepath.Join(root, name)
		mustGit(t, root, "clone", "-q", bare, name)
		mustGit(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
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
	scrubGitEnv(t)
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	mustGit(t, root, "init", "-q", "--bare", "-b", "main", "remote.git")
	clone := func(name string) string {
		dir := filepath.Join(root, name)
		mustGit(t, root, "clone", "-q", bare, name)
		mustGit(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
		mustGit(t, dir, "config", "cc-notes.leaseTTL", "0s")
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
