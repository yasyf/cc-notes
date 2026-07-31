package gitobj_test

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/model"
)

func gitAtEnv(when time.Time) []string {
	ts := when.UTC().Format("2006-01-02T15:04:05Z")
	return []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=" + testName,
		"GIT_AUTHOR_EMAIL=" + testEmail,
		"GIT_AUTHOR_DATE=" + ts,
		"GIT_COMMITTER_NAME=" + testName,
		"GIT_COMMITTER_EMAIL=" + testEmail,
		"GIT_COMMITTER_DATE=" + ts,
	}
}

func gitAt(t *testing.T, dir string, when time.Time, args ...string) string {
	t.Helper()
	//nolint:gosec // G204: test helper shells out to git with fixed argv[0] and test-controlled args.
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = gitAtEnv(when)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitAt(t *testing.T, dir string, when time.Time, msg string) model.SHA {
	t.Helper()
	gitAt(t, dir, when, "commit", "--allow-empty", "-q", "-m", msg)
	return model.SHA(git(t, dir, "rev-parse", "HEAD"))
}

func mergeAt(t *testing.T, dir string, when time.Time, branch, msg string) model.SHA {
	t.Helper()
	gitAt(t, dir, when, "merge", "--no-ff", "-q", "-m", msg, branch)
	return model.SHA(git(t, dir, "rev-parse", "HEAD"))
}

func revList(t *testing.T, dir string, tip model.SHA, args ...string) []model.SHA {
	t.Helper()
	out := git(t, dir, append(append([]string{"rev-list"}, args...), string(tip))...)
	lines := strings.Fields(out)
	shas := make([]model.SHA, 0, len(lines))
	for _, line := range lines {
		shas = append(shas, model.SHA(line))
	}
	return shas
}

func walked(commits []gitobj.CodeCommit) []model.SHA {
	shas := make([]model.SHA, 0, len(commits))
	for _, c := range commits {
		shas = append(shas, c.SHA)
	}
	return shas
}

// TestWalkCommits builds one diamond (c1 root; a and b both on c1; m merges b
// into a) with strictly increasing commit times and runs every ordering,
// dedup, limit, and since case against it.
func TestWalkCommits(t *testing.T) {
	dir := initRepo(t)
	c1 := commitAt(t, dir, t0, "c1")
	git(t, dir, "checkout", "-q", "-b", "left")
	a := commitAt(t, dir, t1, "a")
	git(t, dir, "checkout", "-q", "main")
	git(t, dir, "checkout", "-q", "-b", "right")
	b := commitAt(t, dir, t2, "b")
	git(t, dir, "checkout", "-q", "left")
	m := mergeAt(t, dir, t3, "right", "merge")

	cc := func(sha model.SHA, parents []model.SHA, when time.Time, msg string) gitobj.CodeCommit {
		return gitobj.CodeCommit{
			SHA:        sha,
			Parents:    parents,
			Author:     testActor,
			AuthorTime: when.Unix(),
			CommitTime: when.Unix(),
			Summary:    msg,
		}
	}
	ccC1 := cc(c1, nil, t0, "c1")
	ccA := cc(a, []model.SHA{c1}, t1, "a")
	ccB := cc(b, []model.SHA{c1}, t2, "b")
	ccM := cc(m, []model.SHA{a, b}, t3, "merge")

	repo := open(t, dir)
	cases := []struct {
		name      string
		tips      []model.SHA
		limit     int
		since     int64
		want      []gitobj.CodeCommit
		truncated bool
	}{
		{"linear", []model.SHA{a}, 0, 0, []gitobj.CodeCommit{ccA, ccC1}, false},
		{"merge both parents", []model.SHA{m}, 0, 0, []gitobj.CodeCommit{ccM, ccB, ccA, ccC1}, false},
		{"two tips deduped", []model.SHA{a, b}, 0, 0, []gitobj.CodeCommit{ccB, ccA, ccC1}, false},
		{"limit cutoff", []model.SHA{m}, 2, 0, []gitobj.CodeCommit{ccM, ccB}, true},
		{"since cutoff", []model.SHA{m}, 0, t2.Unix(), []gitobj.CodeCommit{ccM, ccB}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, truncated, err := repo.WalkCommits(t.Context(), tc.tips, tc.limit, tc.since)
			if err != nil {
				t.Fatalf("WalkCommits: %v", err)
			}
			if truncated != tc.truncated {
				t.Errorf("truncated = %t, want %t", truncated, tc.truncated)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("WalkCommits = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestWalkCommitsShallowTruncation clones one commit of a two-commit history
// via file:// --depth 1, so the tip's parent object is absent: the walk yields
// the tip alone and reports truncated without error.
func TestWalkCommitsShallowTruncation(t *testing.T) {
	fixture := initRepo(t)
	commitAt(t, fixture, t0, "c1")
	c2 := commitAt(t, fixture, t1, "c2")
	c1 := model.SHA(git(t, fixture, "rev-parse", "HEAD~1"))

	clone := filepath.Join(t.TempDir(), "clone")
	git(t, filepath.Dir(clone), "-c", "protocol.file.allow=always", "clone", "-q", "--depth", "1", "file://"+fixture, clone)

	repo := open(t, clone)
	got, truncated, err := repo.WalkCommits(t.Context(), []model.SHA{c2}, 0, 0)
	if err != nil {
		t.Fatalf("WalkCommits: %v", err)
	}
	if !truncated {
		t.Errorf("truncated = false, want true (parent %s absent in shallow clone)", c1)
	}
	want := []gitobj.CodeCommit{{
		SHA:        c2,
		Parents:    []model.SHA{c1},
		Author:     testActor,
		AuthorTime: t1.Unix(),
		CommitTime: t1.Unix(),
		Summary:    "c2",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("WalkCommits = %+v, want %+v", got, want)
	}
}

func TestWalkCommitsUnknownTip(t *testing.T) {
	dir := initRepo(t)
	commitAt(t, dir, t0, "c1")
	repo := open(t, dir)

	absent := model.SHA(strings.Repeat("beefcafe", 5))
	if _, _, err := repo.WalkCommits(t.Context(), []model.SHA{absent}, 0, 0); !errors.Is(err, gitobj.ErrCommitNotFound) {
		t.Errorf("WalkCommits with absent tip = %v, want ErrCommitNotFound", err)
	}
	if _, _, err := repo.WalkCommits(t.Context(), []model.SHA{"not-a-sha"}, 0, 0); err == nil || !strings.Contains(err.Error(), "not-a-sha") {
		t.Errorf("WalkCommits with malformed tip = %v, want error naming it", err)
	}
	if _, err := repo.FirstParentMerges(t.Context(), absent, 0, 0); !errors.Is(err, gitobj.ErrCommitNotFound) {
		t.Errorf("FirstParentMerges with absent tip = %v, want ErrCommitNotFound", err)
	}
}

// TestFirstParentMerges builds a top-level merge M (parents [c2, fm]) whose
// second-parent side carries its own merge fm: only M lies on M's first-parent
// path, so fm must be skipped.
func TestFirstParentMerges(t *testing.T) {
	t4 := t0.Add(4 * time.Minute)
	t5 := t0.Add(5 * time.Minute)

	dir := initRepo(t)
	c1 := commitAt(t, dir, t0, "c1")
	git(t, dir, "checkout", "-q", "-b", "feature")
	commitAt(t, dir, t1, "fa")
	git(t, dir, "checkout", "-q", "-b", "sub")
	commitAt(t, dir, t2, "s1")
	git(t, dir, "checkout", "-q", "feature")
	fm := mergeAt(t, dir, t3, "sub", "fm")
	git(t, dir, "checkout", "-q", "main")
	c2 := commitAt(t, dir, t4, "c2")
	m := mergeAt(t, dir, t5, "feature", "M")

	if len(strings.Fields(git(t, dir, "rev-list", "--parents", "-n", "1", string(fm)))) != 3 {
		t.Fatalf("fixture invalid: fm %s is not a merge commit", fm)
	}

	repo := open(t, dir)
	got, err := repo.FirstParentMerges(t.Context(), m, 0, 0)
	if err != nil {
		t.Fatalf("FirstParentMerges: %v", err)
	}
	want := []gitobj.CodeCommit{{
		SHA:        m,
		Parents:    []model.SHA{c2, fm},
		Author:     testActor,
		AuthorTime: t5.Unix(),
		CommitTime: t5.Unix(),
		Summary:    "M",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FirstParentMerges = %+v, want %+v (fm %s must be skipped, c1 %s is not a merge)", got, want, fm, c1)
	}
}

// TestFirstParentMergesNonMonotonicSince pins the window walk against
// backdated commits. A single ancient-dated commit between the tip and an
// in-window merge must not end the walk — sinceSlop tolerates the blip — while
// a merge buried behind sinceSlop consecutive old commits stays out of reach,
// and merges older than since are never collected.
func TestFirstParentMergesNonMonotonicSince(t *testing.T) {
	t3 := t0.Add(3 * time.Minute)
	ancient := t0.Add(-24 * time.Hour)

	dir := initRepo(t)
	commitAt(t, dir, t0, "base")
	git(t, dir, "checkout", "-q", "-b", "side")
	commitAt(t, dir, t1, "s1")
	git(t, dir, "checkout", "-q", "main")
	m := mergeAt(t, dir, t2, "side", "merge side")
	commitAt(t, dir, ancient, "backdated")
	tip := commitAt(t, dir, t3, "tip")

	repo := open(t, dir)
	got, err := repo.FirstParentMerges(t.Context(), tip, 0, t2.Unix())
	if err != nil {
		t.Fatalf("FirstParentMerges: %v", err)
	}
	if len(got) != 1 || got[0].SHA != m {
		t.Errorf("FirstParentMerges past a backdated blip = %+v, want just merge %s", got, m)
	}

	got, err = repo.FirstParentMerges(t.Context(), tip, 0, t3.Unix()+1)
	if err != nil {
		t.Fatalf("FirstParentMerges: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("FirstParentMerges with everything before since = %+v, want none", got)
	}
}

// TestFirstParentMergesSinceSlopStops proves the walk terminates: an in-window
// merge hidden behind sinceSlop (100) consecutive out-of-window commits is not
// reached — the run of old commits ends the walk before it.
func TestFirstParentMergesSinceSlopStops(t *testing.T) {
	t3 := t0.Add(3 * time.Minute)
	ancient := t0.Add(-24 * time.Hour)

	dir := initRepo(t)
	commitAt(t, dir, t0, "base")
	git(t, dir, "checkout", "-q", "-b", "side")
	commitAt(t, dir, t1, "s1")
	git(t, dir, "checkout", "-q", "main")
	m := mergeAt(t, dir, t2, "side", "merge side")
	for i := range 100 {
		commitAt(t, dir, ancient, fmt.Sprintf("old-%d", i))
	}
	tip := commitAt(t, dir, t3, "tip")

	repo := open(t, dir)
	got, err := repo.FirstParentMerges(t.Context(), tip, 0, t2.Unix())
	if err != nil {
		t.Fatalf("FirstParentMerges: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("FirstParentMerges behind %d old commits = %+v, want none (walk must stop at the slop, not reach %s)", 100, got, m)
	}
}

// TestWalkShallowGraft grafts a history that continues past the boundary with
// every object still present — a shallow clone that later fetched more — so
// only the graft, and not a missing object, can stop a walk. git rev-list is
// the oracle for what a grafted handle may still reach, and the handle that
// walked the whole history before the graft appeared has to reach exactly the
// same: the boundary belongs to the repository, not to whichever handle
// happened to open first.
func TestWalkShallowGraft(t *testing.T) {
	t4, t5, t6 := t0.Add(4*time.Minute), t0.Add(5*time.Minute), t0.Add(6*time.Minute)

	dir := initRepo(t)
	commitAt(t, dir, t0, "c1")
	git(t, dir, "checkout", "-q", "-b", "early")
	commitAt(t, dir, t1, "e1")
	git(t, dir, "checkout", "-q", "main")
	early := mergeAt(t, dir, t2, "early", "merge early")
	boundary := commitAt(t, dir, t3, "boundary")
	git(t, dir, "checkout", "-q", "-b", "late")
	commitAt(t, dir, t4, "l1")
	git(t, dir, "checkout", "-q", "main")
	late := mergeAt(t, dir, t5, "late", "merge late")
	tip := commitAt(t, dir, t6, "tip")

	whole := open(t, dir)
	full, truncated, err := whole.WalkCommits(t.Context(), []model.SHA{tip}, 0, 0)
	if err != nil {
		t.Fatalf("WalkCommits without the graft: %v", err)
	}
	if len(full) != 7 || truncated {
		t.Fatalf("fixture invalid: ungrafted walk = %d commits, truncated %t, want 7 and false", len(full), truncated)
	}
	fullMerges, err := whole.FirstParentMerges(t.Context(), tip, 0, 0)
	if err != nil {
		t.Fatalf("FirstParentMerges without the graft: %v", err)
	}
	if !reflect.DeepEqual(walked(fullMerges), []model.SHA{late, early}) {
		t.Fatalf("fixture invalid: ungrafted first-parent merges = %v, want [%s %s]", walked(fullMerges), late, early)
	}

	gittest.Shallow(t, dir, string(boundary))
	handles := []struct {
		name string
		repo *gitobj.Repo
	}{
		{"opened under the graft", open(t, dir)},
		{"opened before the graft", whole},
	}

	t.Run("WalkCommits", func(t *testing.T) {
		want := revList(t, dir, tip)
		slices.Sort(want)
		for _, handle := range handles {
			t.Run(handle.name, func(t *testing.T) {
				got, truncated, err := handle.repo.WalkCommits(t.Context(), []model.SHA{tip}, 0, 0)
				if err != nil {
					t.Fatalf("WalkCommits: %v", err)
				}
				if !truncated {
					t.Errorf("truncated = false, want true: history continues past the graft at %s", boundary)
				}
				reached := walked(got)
				slices.Sort(reached)
				if !slices.Equal(reached, want) {
					t.Errorf("WalkCommits reached %v, git rev-list reached %v (merge %s lies past the graft)", reached, want, early)
				}
			})
		}
	})

	t.Run("FirstParentMerges", func(t *testing.T) {
		want := revList(t, dir, tip, "--first-parent", "--merges")
		for _, handle := range handles {
			t.Run(handle.name, func(t *testing.T) {
				got, err := handle.repo.FirstParentMerges(t.Context(), tip, 0, 0)
				if err != nil {
					t.Fatalf("FirstParentMerges: %v", err)
				}
				if !reflect.DeepEqual(walked(got), want) {
					t.Errorf("FirstParentMerges = %v, git rev-list --first-parent --merges = %v (merge %s lies past the graft)", walked(got), want, early)
				}
			})
		}
	})
}
