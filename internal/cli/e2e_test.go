// End-to-end contract suite: every test drives the real built binary via
// os/exec against a real git repository, pinning the exact stdout bytes,
// stderr lines, and exit codes agents script against. TestMain builds the
// binary once per test-binary run; each subprocess gets a scrubbed git
// environment and an explicit CC_NOTES_ACTOR.
package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gittest"
)

const matrixActor = "Matrix Worker <worker@example.com>"

// binTimeout bounds a single binary invocation. Real commands finish in
// milliseconds, so the ceiling is generous enough never to fire on a healthy
// run; it exists only so a subprocess that genuinely stalls fails the test
// fast instead of hanging the whole package to its global timeout.
const binTimeout = 2 * time.Minute

var testBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cc-notes-bin-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	testBinary = filepath.Join(dir, "cc-notes")
	//nolint:gosec // G204: fixed go-build of this repo's own binary in the e2e test setup.
	build := exec.Command("go", "build", "-o", testBinary, "github.com/yasyf/cc-notes/cmd/cc-notes")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build cc-notes: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// binResult captures one subprocess invocation of the built binary.
type binResult struct {
	Stdout string
	Stderr string
	Code   int
}

// binEnv builds a subprocess environment with every git knob scrubbed,
// global/system config pinned to /dev/null, and the actor frozen.
func binEnv(actor string) []string {
	scrub := make(map[string]bool, len(gitEnvKeys))
	for _, key := range gitEnvKeys {
		scrub[key] = true
	}
	host := os.Environ()
	env := make([]string, 0, len(host)+4)
	for _, kv := range host {
		if key, _, _ := strings.Cut(kv, "="); !scrub[key] {
			env = append(env, kv)
		}
	}
	return append(
		env,
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"CC_NOTES_ACTOR="+actor,
	)
}

// execBin runs the built binary in dir as actor, bounded by binTimeout so a
// stalled subprocess fails the test fast instead of hanging the package to its
// global timeout. It is goroutine-safe: the returned error reports a launch
// failure or a timeout, never a non-zero exit.
func execBin(dir, actor string, args ...string) (binResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), binTimeout)
	defer cancel()
	//nolint:gosec // G204: testBinary is the e2e-built cc-notes binary; args are test-controlled.
	cmd := exec.CommandContext(ctx, testBinary, args...)
	cmd.Dir = dir
	cmd.Env = binEnv(actor)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	res := binResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() == context.DeadlineExceeded {
		return res, fmt.Errorf("cc-notes %s: timed out after %s", strings.Join(args, " "), binTimeout)
	}
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exit):
		res.Code = exit.ExitCode()
	default:
		return res, err
	}
	return res, nil
}

// mustBin runs the built binary expecting the success contract: exit 0 and an
// empty stderr.
func mustBin(t *testing.T, dir, actor string, args ...string) string {
	t.Helper()
	res, err := execBin(dir, actor, args...)
	if err != nil {
		t.Fatalf("cc-notes %s: %v", strings.Join(args, " "), err)
	}
	if res.Code != 0 || res.Stderr != "" {
		t.Fatalf("cc-notes %s: exit %d, stderr %q, stdout %q", strings.Join(args, " "), res.Code, res.Stderr, res.Stdout)
	}
	return res.Stdout
}

// addTaskBin creates a task through the binary as actorA and returns its
// parsed JSON document.
func addTaskBin(t *testing.T, dir, title string, extra ...string) taskJSON {
	t.Helper()
	args := append([]string{"task", "add", title, "--no-validation-criteria", "--json"}, extra...)
	return mustJSON[taskJSON](t, mustBin(t, dir, actorA, args...))
}

// TestExitCodeMatrix pins the lifecycle x exit-code contract: every case runs
// exactly one command as matrixActor and asserts the exit code, the exact
// stdout bytes on success (with stderr empty), and the exact single stderr
// line on failure (with stdout empty).
func TestExitCodeMatrix(t *testing.T) {
	cases := []struct {
		name   string
		noRepo bool
		// setup prepares the repository and returns the command under test
		// plus the expected exact stdout (success) or stderr (failure); an
		// empty want defers to wantPrefix.
		setup      func(t *testing.T, dir string) (args []string, want string)
		wantCode   int
		wantPrefix string
		// multiline exempts a case from the single-line stderr invariant: the
		// self-healing usage errors span an accepted-flags block.
		multiline bool
		after     func(t *testing.T, dir string)
	}{
		{
			name: "claim on open task exits 0 with lean line",
			setup: func(t *testing.T, dir string) ([]string, string) {
				task := addTaskBin(t, dir, "Work")
				return []string{"task", "claim", task.ID},
					task.ID[:7] + "\tin_progress\tP2\t" + matrixActor + "\tWork\n"
			},
			wantCode: 0,
		},
		{
			name: "claim on task claimed by another actor exits 4",
			setup: func(t *testing.T, dir string) ([]string, string) {
				task := addTaskBin(t, dir, "Work")
				mustBin(t, dir, actorB, "task", "claim", task.ID)
				return []string{"task", "claim", task.ID},
					fmt.Sprintf("conflict: %s already claimed by %s (in_progress)\n", task.ID[:7], actorB)
			},
			wantCode: 4,
		},
		{
			name: "claim on done task exits 4",
			setup: func(t *testing.T, dir string) ([]string, string) {
				task := addTaskBin(t, dir, "Work")
				mustBin(t, dir, actorA, "task", "done", task.ID)
				return []string{"task", "claim", task.ID},
					fmt.Sprintf("conflict: %s not open (done)\n", task.ID[:7])
			},
			wantCode: 4,
		},
		{
			name: "done on in_progress task exits 0 with lean line",
			setup: func(t *testing.T, dir string) ([]string, string) {
				task := addTaskBin(t, dir, "Work")
				mustBin(t, dir, actorB, "task", "claim", task.ID)
				return []string{"task", "done", task.ID},
					task.ID[:7] + "\tdone\tP2\t" + actorB + "\tWork\n"
			},
			wantCode: 0,
		},
		{
			name: "done on done task exits 4",
			setup: func(t *testing.T, dir string) ([]string, string) {
				task := addTaskBin(t, dir, "Work")
				mustBin(t, dir, actorA, "task", "done", task.ID)
				return []string{"task", "done", task.ID},
					fmt.Sprintf("conflict: %s already done\n", task.ID[:7])
			},
			wantCode: 4,
		},
		{
			name: "cancel on open task exits 0 with lean line",
			setup: func(t *testing.T, dir string) ([]string, string) {
				task := addTaskBin(t, dir, "Work")
				return []string{"task", "cancel", task.ID},
					task.ID[:7] + "\tcancelled\tP2\t-\tWork\n"
			},
			wantCode: 0,
		},
		{
			name: "edit --status open reopens a done task",
			setup: func(t *testing.T, dir string) ([]string, string) {
				task := addTaskBin(t, dir, "Work")
				mustBin(t, dir, actorA, "task", "done", task.ID)
				return []string{"task", "edit", task.ID, "--status", "open"},
					task.ID[:7] + "\topen\tP2\t-\tWork\n"
			},
			wantCode: 0,
			after: func(t *testing.T, dir string) {
				tasks := mustJSON[[]taskJSON](t, mustBin(t, dir, actorA, "task", "list", "--json"))
				if len(tasks) != 1 || tasks[0].Status != "open" || tasks[0].ClosedAt != nil {
					t.Errorf("reopened task = %+v, want status open with closed_at null", tasks)
				}
			},
		},
		{
			name: "ready excludes blocked, assigned, and non-open tasks",
			setup: func(t *testing.T, dir string) ([]string, string) {
				urgent := addTaskBin(t, dir, "Urgent", "--priority", "0")
				blocker := addTaskBin(t, dir, "Blocker", "--priority", "1")
				addTaskBin(t, dir, "Blocked", "--blocked-by", blocker.ID)
				assigned := addTaskBin(t, dir, "Assigned")
				mustBin(t, dir, actorB, "task", "claim", assigned.ID)
				closed := addTaskBin(t, dir, "Closed")
				mustBin(t, dir, actorA, "task", "done", closed.ID)
				return []string{"task", "ready"},
					urgent.ID[:7] + "\topen\tP0\t-\tUrgent\n" + blocker.ID[:7] + "\topen\tP1\t-\tBlocker\n"
			},
			wantCode: 0,
		},
		{
			name: "dependency cycle exits 1",
			setup: func(t *testing.T, dir string) ([]string, string) {
				first := addTaskBin(t, dir, "First")
				second := addTaskBin(t, dir, "Second")
				mustBin(t, dir, actorA, "task", "dep", first.ID, second.ID)
				return []string{"task", "dep", second.ID, first.ID},
					fmt.Sprintf("error: dependency cycle: %s already blocks %s\n", second.ID[:7], first.ID[:7])
			},
			wantCode: 1,
		},
		{
			name: "unknown id exits 3",
			setup: func(_ *testing.T, _ string) ([]string, string) {
				return []string{"task", "show", "feedfac"},
					fmt.Sprintf("not-found: entity not found: no task matches %q\n", "feedfac")
			},
			wantCode: 3,
		},
		{
			name: "ambiguous prefix exits 5 listing candidates",
			setup: func(t *testing.T, dir string) ([]string, string) {
				type entity struct{ id, title string }
				seen := map[string]entity{}
				for i := 0; ; i++ {
					title := fmt.Sprintf("Amb %d", i)
					created := entity{addTaskBin(t, dir, title).ID, title}
					prefix := created.id[:1]
					prev, collided := seen[prefix]
					if !collided {
						seen[prefix] = created
						continue
					}
					lo, hi := prev, created
					if hi.id < lo.id {
						lo, hi = hi, lo
					}
					return []string{"task", "show", prefix},
						fmt.Sprintf("ambiguous: ambiguous task prefix %q: %s %q, %s %q\n",
							prefix, lo.id[:7], lo.title, hi.id[:7], hi.title)
				}
			},
			wantCode: 5,
		},
		{
			name: "unknown flag exits 2 with an accepted-flags block",
			setup: func(_ *testing.T, _ string) ([]string, string) {
				return []string{"task", "list", "--bogus"}, ""
			},
			wantCode:   2,
			wantPrefix: "usage: unknown flag: --bogus\ntask list takes: ",
			multiline:  true,
		},
		{
			name: "unknown command exits 2 with the version hint",
			setup: func(_ *testing.T, _ string) ([]string, string) {
				return []string{"frobnicate"}, ""
			},
			wantCode:   2,
			wantPrefix: "usage: unknown command \"frobnicate\" for \"cc-notes\"; this binary is cc-notes ",
		},
		{
			name: "arity shape stops at the --by flag token",
			setup: func(_ *testing.T, _ string) ([]string, string) {
				return []string{"doc", "supersede"}, "usage: cc-notes doc supersede accepts 1 arg(s) (OLD), received 0\n"
			},
			wantCode: 2,
		},
		{
			name: "arity shape caps to the arity the mode allows",
			setup: func(_ *testing.T, _ string) ([]string, string) {
				return []string{"doc", "add", "one", "two", "three", "--checkout"}, "usage: cc-notes doc add accepts at most 1 arg(s) (TITLE), received 3\n"
			},
			wantCode: 2,
		},
		{
			name: "root unknown flag before a mistyped command surfaces the typo suggestion",
			setup: func(_ *testing.T, _ string) ([]string, string) {
				return []string{"--bogus", "task_list"}, "usage: unknown flag: --bogus\ncc-notes takes no flags\ndid you mean \"task list\"?\n"
			},
			wantCode:  2,
			multiline: true,
		},
		{
			name: "root unknown flag after a bad command token surfaces the version hint",
			setup: func(_ *testing.T, _ string) ([]string, string) {
				return []string{"frobnicate", "--bogus"}, ""
			},
			wantCode:   2,
			wantPrefix: "usage: unknown flag: --bogus\ncc-notes takes no flags\nthis binary is cc-notes ",
			multiline:  true,
		},
		{
			name: "root unknown flag alone stays quiet — no command or version hint",
			setup: func(_ *testing.T, _ string) ([]string, string) {
				return []string{"--bogus"}, "usage: unknown flag: --bogus\ncc-notes takes no flags\n"
			},
			wantCode:  2,
			multiline: true,
		},
		{
			name: "invalid --branch exits 2",
			setup: func(_ *testing.T, _ string) ([]string, string) {
				return []string{"task", "list", "--branch", "../evil"}, ""
			},
			wantCode:   2,
			wantPrefix: "usage: invalid branch \"../evil\": ",
		},
		{
			name:   "outside a git repository exits 1",
			noRepo: true,
			setup: func(_ *testing.T, _ string) ([]string, string) {
				return []string{"note", "list"}, ""
			},
			wantCode:   1,
			wantPrefix: "error: ",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var dir string
			if tc.noRepo {
				gittest.ScrubEnv(t)
				dir = t.TempDir()
			} else {
				dir = initRepo(t)
			}
			args, want := tc.setup(t, dir)
			res, err := execBin(dir, matrixActor, args...)
			if err != nil {
				t.Fatalf("cc-notes %s: %v", strings.Join(args, " "), err)
			}
			if res.Code != tc.wantCode {
				t.Fatalf("exit = %d, want %d (stdout %q, stderr %q)", res.Code, tc.wantCode, res.Stdout, res.Stderr)
			}
			if tc.wantCode == 0 {
				if res.Stderr != "" {
					t.Errorf("stderr = %q, want empty on success", res.Stderr)
				}
				if res.Stdout != want {
					t.Errorf("stdout = %q, want %q", res.Stdout, want)
				}
			} else {
				if res.Stdout != "" {
					t.Errorf("stdout = %q, want empty on failure", res.Stdout)
				}
				if want != "" && res.Stderr != want {
					t.Errorf("stderr = %q, want %q", res.Stderr, want)
				}
				if tc.wantPrefix != "" && !strings.HasPrefix(res.Stderr, tc.wantPrefix) {
					t.Errorf("stderr = %q, want prefix %q", res.Stderr, tc.wantPrefix)
				}
				if tc.multiline {
					if !strings.HasSuffix(res.Stderr, "\n") {
						t.Errorf("stderr = %q, want a trailing newline", res.Stderr)
					}
				} else if strings.Count(res.Stderr, "\n") != 1 || !strings.HasSuffix(res.Stderr, "\n") {
					t.Errorf("stderr = %q, want exactly one line", res.Stderr)
				}
			}
			if tc.after != nil {
				tc.after(t, dir)
			}
		})
	}
}

// TestClaimRaceConcurrentActors races six true subprocesses with distinct
// actors over one claim: exactly one wins with exit 0, five lose with exit 4,
// and the folded assignee is the winner's actor.
func TestClaimRaceConcurrentActors(t *testing.T) {
	dir := initRepo(t)
	task := addTaskBin(t, dir, "Contested")

	const racers = 6
	actors := make([]string, racers)
	results := make([]binResult, racers)
	errs := make([]error, racers)
	var wg sync.WaitGroup
	for i := range racers {
		actors[i] = fmt.Sprintf("Racer %d <racer%d@example.com>", i, i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = execBin(dir, actors[i], "task", "claim", task.ID)
		}()
	}
	wg.Wait()

	var winners, losers []int
	for i := range racers {
		if errs[i] != nil {
			t.Fatalf("racer %d: %v", i, errs[i])
		}
		switch res := results[i]; res.Code {
		case 0:
			winners = append(winners, i)
			if res.Stderr != "" {
				t.Errorf("racer %d stderr = %q, want empty on success", i, res.Stderr)
			}
		case 4:
			losers = append(losers, i)
			if res.Stdout != "" {
				t.Errorf("racer %d stdout = %q, want empty on conflict", i, res.Stdout)
			}
			if !strings.HasPrefix(res.Stderr, "conflict: ") || strings.Count(res.Stderr, "\n") != 1 {
				t.Errorf("racer %d stderr = %q, want one conflict: line", i, res.Stderr)
			}
		default:
			t.Errorf("racer %d exit = %d (stderr %q), want 0 or 4", i, res.Code, res.Stderr)
		}
	}
	if len(winners) != 1 || len(losers) != racers-1 {
		t.Fatalf("winners = %v, losers = %v, want exactly 1 and %d (results %+v)", winners, losers, racers-1, results)
	}

	winner := winners[0]
	fields := strings.Split(strings.TrimSuffix(results[winner].Stdout, "\n"), "\t")
	if len(fields) != 5 || fields[1] != "in_progress" || fields[3] != actors[winner] {
		t.Fatalf("winner stdout = %q, want lean line claimed by %q", results[winner].Stdout, actors[winner])
	}
	shown := mustJSON[taskJSON](t, mustBin(t, dir, actorA, "task", "show", task.ID, "--json"))
	if shown.Assignee == nil || *shown.Assignee != actors[winner] {
		t.Fatalf("folded assignee = %v, want winner %q", shown.Assignee, actors[winner])
	}
}

// TestBranchScopingAndEdit pins the branch attribute: list scopes to a
// branch by default and via --branch, edit --branch reassigns a task (printing
// the post-edit lean line), and slashed branch names round-trip.
func TestBranchScopingAndEdit(t *testing.T) {
	dir := initRepo(t)
	feature := addTaskBin(t, dir, "Feature work", "--branch", "feature/x", "--priority", "1")
	featureLine := feature.ID[:7] + "\topen\tP1\t-\tFeature work\n"

	if out := mustBin(t, dir, actorA, "task", "list"); out != "" {
		t.Fatalf("main list = %q, want empty before move", out)
	}
	if out := mustBin(t, dir, actorA, "task", "list", "--branch", "feature/x"); out != featureLine {
		t.Fatalf("feature/x list = %q, want %q", out, featureLine)
	}
	if out := mustBin(t, dir, actorA, "task", "edit", feature.ID, "--branch", "main"); out != featureLine {
		t.Fatalf("edit --branch output = %q, want lean line %q", out, featureLine)
	}
	if out := mustBin(t, dir, actorA, "task", "list"); out != featureLine {
		t.Fatalf("main list after edit = %q, want %q", out, featureLine)
	}
	if out := mustBin(t, dir, actorA, "task", "list", "--branch", "feature/x"); out != "" {
		t.Fatalf("feature/x list after move = %q, want empty", out)
	}

	slashed := addTaskBin(t, dir, "Login fix", "--branch", "feature/login/x")
	slashedLine := slashed.ID[:7] + "\topen\tP2\t-\tLogin fix\n"
	if out := mustBin(t, dir, actorA, "task", "edit", slashed.ID, "--branch", "main"); out != slashedLine {
		t.Fatalf("slashed edit --branch output = %q, want %q", out, slashedLine)
	}
	if out := mustBin(t, dir, actorA, "task", "list"); out != featureLine+slashedLine {
		t.Fatalf("main list = %q, want %q", out, featureLine+slashedLine)
	}
	if out := mustBin(t, dir, actorA, "task", "list", "--branch", "feature/login/x"); out != "" {
		t.Fatalf("feature/login/x list after move = %q, want empty", out)
	}
}

// TestEditBranchInvalidDestUsage pins the CLI boundary for edit --branch: an
// invalid branch is a usage error (exit 2) raised before any write, so the task
// is untouched and sync still converges.
func TestEditBranchInvalidDestUsage(t *testing.T) {
	gittest.ScrubEnv(t)
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	gittest.Git(t, root, "init", "-q", "--bare", "-b", "main", "remote.git")
	dir := filepath.Join(root, "work")
	gittest.Git(t, root, "clone", "-q", bare, "work")
	gittest.Git(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
	mustBin(t, dir, actorA, "init")
	task := addTaskBin(t, dir, "Survivor")
	line := task.ID[:7] + "\topen\tP2\t-\tSurvivor\n"

	res, err := execBin(dir, actorA, "task", "edit", task.ID, "--branch", "../evil")
	if err != nil {
		t.Fatalf("cc-notes task edit --branch: %v", err)
	}
	if res.Code != 2 || res.Stdout != "" {
		t.Fatalf("edit --branch ../evil: exit %d stdout %q, want exit 2 with empty stdout (stderr %q)", res.Code, res.Stdout, res.Stderr)
	}
	if want := "usage: invalid branch \"../evil\": "; !strings.HasPrefix(res.Stderr, want) {
		t.Errorf("stderr = %q, want prefix %q", res.Stderr, want)
	}
	if out := mustBin(t, dir, actorA, "task", "list"); out != line {
		t.Errorf("task list after failed edit = %q, want %q", out, line)
	}
	if out := mustBin(t, dir, actorA, "sync"); out != "pushed: 1\nrounds: 1\n" {
		t.Errorf("sync after failed edit = %q, want pushed: 1 / rounds: 1", out)
	}
}

// TestClaimDetachedHead pins global id resolution for single-task commands:
// claim finds a task by id regardless of HEAD, so it succeeds from a detached
// HEAD with no --branch flag.
func TestClaimDetachedHead(t *testing.T) {
	dir := initRepo(t)
	task := addTaskBin(t, dir, "On main")
	gittest.Git(t, dir, "commit", "-q", "--allow-empty", "-m", "c")
	gittest.Git(t, dir, "checkout", "-q", "--detach")

	out := mustBin(t, dir, matrixActor, "task", "claim", task.ID)
	if want := task.ID[:7] + "\tin_progress\tP2\t" + matrixActor + "\tOn main\n"; out != want {
		t.Fatalf("claim from detached HEAD = %q, want %q", out, want)
	}
}

// TestNoteLifecycleViaBinary drives the full note lifecycle through the
// binary: add with anchors, search, edit, tag filters, and rm with --all
// visibility.
func TestNoteLifecycleViaBinary(t *testing.T) {
	dir := initRepo(t)
	short := commitFile(t, dir, "seed.go", "package main")[:8]
	note := mustJSON[noteJSON](t, mustBin(t, dir, actorA, "note", "add", "Anchored note",
		"--body", "First body", "--label", "design", "--label", "api",
		"--commit", short, "--path", "internal/cli", "--json"))
	mustBin(t, dir, actorA, "note", "add", "Plain", "--label", "misc")

	noteLine := note.ID[:7] + "\t" + dateOf(t, note.UpdatedAt) + "\tapi,design\tAnchored note\n"
	if out := mustBin(t, dir, actorA, "note", "search", "anchored"); out != noteLine {
		t.Fatalf("search = %q, want %q", out, noteLine)
	}

	edited := mustBin(t, dir, actorA, "note", "edit", note.ID, "--title", "Anchored note v2", "--add-label", "temp", "--rm-label", "api")
	shown := mustJSON[noteJSON](t, mustBin(t, dir, actorA, "note", "show", note.ID, "--json"))
	editedLine := note.ID[:7] + "\t" + dateOf(t, shown.UpdatedAt) + "\tdesign,temp\tAnchored note v2\n"
	if edited != editedLine {
		t.Fatalf("edit output = %q, want %q", edited, editedLine)
	}
	if out := mustBin(t, dir, actorA, "note", "list", "--label", "design"); out != editedLine {
		t.Fatalf("list --label design = %q, want %q", out, editedLine)
	}
	if out := mustBin(t, dir, actorA, "note", "list", "--label", "api"); out != "" {
		t.Fatalf("list --label api = %q, want empty after rm-label", out)
	}

	removed := mustBin(t, dir, actorA, "note", "rm", note.ID)
	tombstoned := mustJSON[noteJSON](t, mustBin(t, dir, actorA, "note", "show", note.ID, "--json"))
	if !tombstoned.Deleted {
		t.Fatalf("note after rm = %+v, want deleted", tombstoned)
	}
	removedLine := note.ID[:7] + "\t" + dateOf(t, tombstoned.UpdatedAt) + "\tdesign,temp\tAnchored note v2\n"
	if removed != removedLine {
		t.Fatalf("rm output = %q, want %q", removed, removedLine)
	}
	if out := mustBin(t, dir, actorA, "note", "search", "anchored"); out != "" {
		t.Fatalf("search after rm = %q, want empty", out)
	}
	if out := mustBin(t, dir, actorA, "note", "list"); strings.Contains(out, note.ID[:7]) || strings.Count(out, "\n") != 1 {
		t.Fatalf("list after rm = %q, want only the Plain note", out)
	}
	if out := mustBin(t, dir, actorA, "note", "list", "--all"); !strings.Contains(out, removedLine) || strings.Count(out, "\n") != 2 {
		t.Fatalf("list --all = %q, want both notes including %q", out, removedLine)
	}
}

// TestNoteExpireViaBinary drives the explicit out-of-date flag through the
// binary: expire stamps stale_at/stale_by/stale_reason and surfaces an EXPIRED
// verdict in note review (kept by --expired, never hidden from note list);
// note verify clears the flag; an explicit --clear clears it; and combining
// --clear with --reason is a usage error.
func TestNoteExpireViaBinary(t *testing.T) {
	dir := initRepo(t)
	note := mustJSON[noteJSON](t, mustBin(t, dir, actorA, "note", "add", "API key rotation",
		"--body", "rotate quarterly", "--json"))
	short := note.ID[:7]

	const reason = "policy moved to monthly"
	mustBin(t, dir, actorA, "note", "expire", note.ID, "--reason", reason)

	flagged := mustJSON[noteJSON](t, mustBin(t, dir, actorA, "note", "show", note.ID, "--json"))
	if flagged.StaleAt == nil || *flagged.StaleAt == "" {
		t.Fatalf("stale_at = %v, want non-empty after expire", flagged.StaleAt)
	}
	if flagged.StaleBy == nil || *flagged.StaleBy != actorA {
		t.Fatalf("stale_by = %v, want %q", flagged.StaleBy, actorA)
	}
	if flagged.StaleReason == nil || *flagged.StaleReason != reason {
		t.Fatalf("stale_reason = %v, want %q", flagged.StaleReason, reason)
	}

	const wantExpired = "EXPIRED"
	review := mustBin(t, dir, actorA, "note", "review")
	if !strings.Contains(review, short) || !strings.Contains(review, wantExpired) {
		t.Fatalf("note review = %q, want it to contain %q and %q", review, short, wantExpired)
	}
	if expiredOnly := mustBin(t, dir, actorA, "note", "review", "--expired"); !strings.Contains(expiredOnly, short) {
		t.Fatalf("note review --expired = %q, want it to contain %q", expiredOnly, short)
	}
	if listed := mustBin(t, dir, actorA, "note", "list"); !strings.Contains(listed, short) {
		t.Fatalf("note list = %q, want the expired note %q to remain visible", listed, short)
	}

	mustBin(t, dir, actorA, "note", "verify", note.ID)
	verified := mustJSON[noteJSON](t, mustBin(t, dir, actorA, "note", "show", note.ID, "--json"))
	if verified.StaleAt != nil {
		t.Fatalf("stale_at = %v, want cleared after verify", verified.StaleAt)
	}
	if afterVerify := mustBin(t, dir, actorA, "note", "review"); strings.Contains(afterVerify, wantExpired) {
		t.Fatalf("note review after verify = %q, want no %q verdict", afterVerify, wantExpired)
	}

	mustBin(t, dir, actorA, "note", "expire", note.ID)
	reflagged := mustJSON[noteJSON](t, mustBin(t, dir, actorA, "note", "show", note.ID, "--json"))
	if reflagged.StaleAt == nil || *reflagged.StaleAt == "" {
		t.Fatalf("stale_at = %v, want non-empty after re-expire", reflagged.StaleAt)
	}

	mustBin(t, dir, actorA, "note", "expire", note.ID, "--clear")
	cleared := mustJSON[noteJSON](t, mustBin(t, dir, actorA, "note", "show", note.ID, "--json"))
	if cleared.StaleAt != nil {
		t.Fatalf("stale_at = %v, want cleared after --clear", cleared.StaleAt)
	}

	res, err := execBin(dir, actorA, "note", "expire", note.ID, "--clear", "--reason", "x")
	if err != nil {
		t.Fatalf("cc-notes note expire --clear --reason: %v", err)
	}
	if res.Code == 0 {
		t.Fatalf("note expire --clear --reason: exit %d, want non-zero usage error (stderr %q)", res.Code, res.Stderr)
	}
}

// TestTaskJSONContract asserts the full JSON document for one rich task:
// every field present in DTO order (byte round-trip through the mirror
// struct), RFC3339 UTC "Z" timestamps, sorted sets, derived blocks on the
// blocker's side, and null-vs-empty-string semantics.
func TestTaskJSONContract(t *testing.T) {
	dir := initRepo(t)
	blocker := addTaskBin(t, dir, "Blocker task")
	rich := addTaskBin(t, dir, "Rich task", "--body", "Full description", "--type", "bug",
		"--priority", "1", "--label", "zeta", "--label", "alpha", "--blocked-by", blocker.ID)
	mustBin(t, dir, actorB, "task", "comment", rich.ID, "observed from B")
	const winner = "Winner W <w@example.com>"
	mustBin(t, dir, winner, "task", "claim", rich.ID)

	raw := mustBin(t, dir, actorA, "task", "show", rich.ID, "--json")
	if !strings.HasSuffix(raw, "\n") || strings.Count(raw, "\n") != 1 {
		t.Fatalf("raw JSON = %q, want one compact document with one trailing newline", raw)
	}
	if !strings.HasPrefix(raw, `{"id":"`) {
		t.Fatalf("raw JSON = %q, want id first", raw)
	}
	shown := mustJSON[taskJSON](t, raw)
	remarshaled, err := json.Marshal(shown)
	if err != nil {
		t.Fatalf("remarshal: %v", err)
	}
	if string(remarshaled)+"\n" != raw {
		t.Fatalf("JSON shape drifted from the DTO contract:\n got  %q\n want %q", raw, string(remarshaled)+"\n")
	}

	if len(shown.ID) != 40 || strings.ToLower(shown.ID) != shown.ID {
		t.Errorf("id = %q, want 40 lowercase hex chars", shown.ID)
	}
	if shown.Branch != "main" || shown.Title != "Rich task" || shown.Description != "Full description" {
		t.Errorf("branch/title/description = %q/%q/%q", shown.Branch, shown.Title, shown.Description)
	}
	if shown.Type != "bug" || shown.Status != "in_progress" || shown.Priority != 1 {
		t.Errorf("type/status/priority = %q/%q/%d", shown.Type, shown.Status, shown.Priority)
	}
	if shown.Assignee == nil || *shown.Assignee != winner {
		t.Errorf("assignee = %v, want %q", shown.Assignee, winner)
	}
	if strings.Join(shown.Labels, ",") != "alpha,zeta" {
		t.Errorf("labels = %v, want sorted [alpha zeta]", shown.Labels)
	}
	if len(shown.BlockedBy) != 1 || shown.BlockedBy[0] != blocker.ID {
		t.Errorf("blocked_by = %v, want [%s]", shown.BlockedBy, blocker.ID)
	}
	if len(shown.Blocks) != 0 || !strings.Contains(raw, `"blocks":[]`) {
		t.Errorf("blocks = %v (raw %q), want empty non-null array", shown.Blocks, raw)
	}
	if shown.Parent != nil || !strings.Contains(raw, `"parent":null`) {
		t.Errorf("parent = %v, want null", shown.Parent)
	}
	if len(shown.Comments) != 1 || shown.Comments[0].Author != actorB || shown.Comments[0].Body != "observed from B" {
		t.Errorf("comments = %+v, want one comment by %q", shown.Comments, actorB)
	}
	for name, value := range map[string]string{
		"created_at": shown.CreatedAt,
		"updated_at": shown.UpdatedAt,
		"comment ts": shown.Comments[0].TS,
	} {
		if _, err := time.Parse(time.RFC3339, value); err != nil || !strings.HasSuffix(value, "Z") {
			t.Errorf("%s = %q, want RFC3339 UTC Z (%v)", name, value, err)
		}
	}
	if shown.StartedAt == nil || !strings.HasSuffix(*shown.StartedAt, "Z") {
		t.Errorf("started_at = %v, want RFC3339Z after claim", shown.StartedAt)
	}
	if shown.ClosedAt != nil || !strings.Contains(raw, `"closed_at":null`) {
		t.Errorf("closed_at = %v, want null", shown.ClosedAt)
	}
	if len(shown.Commits) != 0 || !strings.Contains(raw, `"commits":[]`) {
		t.Errorf("commits = %v (raw %q), want empty non-null array", shown.Commits, raw)
	}
	if shown.Lease.Holder == nil || *shown.Lease.Holder != winner {
		t.Errorf("lease.holder = %v, want %q", shown.Lease.Holder, winner)
	}
	if shown.Lease.Heartbeat == nil || !strings.HasSuffix(*shown.Lease.Heartbeat, "Z") {
		t.Errorf("lease.heartbeat = %v, want RFC3339Z after claim", shown.Lease.Heartbeat)
	}

	rawBlocker := mustBin(t, dir, actorA, "task", "show", blocker.ID, "--json")
	blockerShown := mustJSON[taskJSON](t, rawBlocker)
	if len(blockerShown.Blocks) != 1 || blockerShown.Blocks[0] != rich.ID {
		t.Errorf("blocker blocks = %v, want derived [%s]", blockerShown.Blocks, rich.ID)
	}
	for _, fragment := range []string{`"description":""`, `"assignee":null`, `"parent":null`, `"started_at":null`, `"closed_at":null`, `"commits":[]`, `"lease":{"holder":null,"heartbeat":null}`} {
		if !strings.Contains(rawBlocker, fragment) {
			t.Errorf("blocker JSON %q missing %q", rawBlocker, fragment)
		}
	}
}

// TestAutoInstallAnnouncesRefspecs pins the stderr disclosure contract: the
// first mutating command in a wired clone announces the exact config lines
// auto-install added — including the push.default override note — and every
// later command stays silent.
func TestAutoInstallAnnouncesRefspecs(t *testing.T) {
	gittest.ScrubEnv(t)
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	gittest.Git(t, root, "init", "-q", "--bare", "-b", "main", "remote.git")
	dir := filepath.Join(root, "work")
	gittest.Git(t, root, "clone", "-q", bare, "work")
	gittest.Git(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")

	res, err := execBin(dir, actorA, "task", "add", "First write", "--no-validation-criteria")
	if err != nil {
		t.Fatalf("cc-notes task add: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("task add exit = %d (stderr %q)", res.Code, res.Stderr)
	}
	want := "cc-notes: installed refspecs in .git/config for \"origin\": " +
		"remote.origin.fetch=+refs/cc-notes/*:refs/cc-notes-sync/origin/*; " +
		"remote.origin.push=HEAD; " +
		"remote.origin.push=refs/cc-notes/*:refs/cc-notes/*; " +
		"core.logAllRefUpdates=always\n" +
		"cc-notes: note: \"git push\" now pushes the current branch to its same-named remote branch (remote.origin.push overrides push.default)\n"
	if res.Stderr != want {
		t.Fatalf("first mutating command stderr = %q, want %q", res.Stderr, want)
	}
	mustBin(t, dir, actorA, "task", "add", "Second write", "--no-validation-criteria")
}

// TestAutoInstallAnnouncesDroppedRefspec pins the drop disclosure: a repo wired
// with BOTH the pre-fix killer refspec and the new tracking one (an old binary
// re-ran after a new one) still prunes unsynced refs through the old line, so
// the first mutating command drops it and says so on stderr — a data-loss fix,
// not a silent cosmetic tidy. Push and the reflog are pre-wired so the drop is
// the only change autoInstall makes, and the next command is a silent no-op.
func TestAutoInstallAnnouncesDroppedRefspec(t *testing.T) {
	gittest.ScrubEnv(t)
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	gittest.Git(t, root, "init", "-q", "--bare", "-b", "main", "remote.git")
	dir := filepath.Join(root, "work")
	gittest.Git(t, root, "clone", "-q", bare, "work")
	gittest.Git(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
	gittest.Git(t, dir, "config", "--add", "remote.origin.fetch", "+refs/cc-notes/*:refs/cc-notes/*")
	gittest.Git(t, dir, "config", "--add", "remote.origin.fetch", "+refs/cc-notes/*:refs/cc-notes-sync/origin/*")
	gittest.Git(t, dir, "config", "--add", "remote.origin.push", "HEAD")
	gittest.Git(t, dir, "config", "--add", "remote.origin.push", "refs/cc-notes/*:refs/cc-notes/*")
	gittest.Git(t, dir, "config", "core.logAllRefUpdates", "always")

	res, err := execBin(dir, actorA, "task", "add", "First write", "--no-validation-criteria")
	if err != nil {
		t.Fatalf("cc-notes task add: %v", err)
	}
	if res.Code != 0 {
		t.Fatalf("task add exit = %d (stderr %q)", res.Code, res.Stderr)
	}
	want := "cc-notes: removed pre-fix fetch refspecs a plain \"git fetch --prune\" would use to delete unsynced refs: remote.origin.fetch=+refs/cc-notes/*:refs/cc-notes/*\n"
	if res.Stderr != want {
		t.Fatalf("first mutating command stderr = %q, want %q", res.Stderr, want)
	}
	res2, err := execBin(dir, actorA, "task", "add", "Second write", "--no-validation-criteria")
	if err != nil {
		t.Fatalf("second task add: %v", err)
	}
	if res2.Code != 0 || res2.Stderr != "" {
		t.Fatalf("second mutating command: exit %d stderr %q, want silent success", res2.Code, res2.Stderr)
	}
}

// TestTwoCloneSyncRoundTrip round-trips a task through a bare remote: clone A
// adds and syncs, clone B syncs and sees a byte-identical task list.
func TestTwoCloneSyncRoundTrip(t *testing.T) {
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
	task := mustJSON[taskJSON](t, mustBin(t, cloneA, actorA, "task", "add", "Shared task", "--no-validation-criteria", "--json"))
	if out := mustBin(t, cloneA, actorA, "sync"); out != "pushed: 1\nrounds: 1\n" {
		t.Fatalf("clone A sync = %q, want pushed: 1 / rounds: 1", out)
	}

	cloneB := clone("b")
	if out := mustBin(t, cloneB, actorB, "sync"); out != "created: 1\nrounds: 1\n" {
		t.Fatalf("clone B sync = %q, want created: 1 / rounds: 1", out)
	}

	listA := mustBin(t, cloneA, actorA, "task", "list", "--json")
	listB := mustBin(t, cloneB, actorB, "task", "list", "--json")
	if listB != listA {
		t.Fatalf("clone B list = %q, want byte-equal to clone A %q", listB, listA)
	}
	if !strings.Contains(listB, `"id":"`+task.ID+`"`) {
		t.Fatalf("clone B list = %q, missing task %s", listB, task.ID)
	}
}
