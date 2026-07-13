package viz

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/model"
)

// fullWindow selects every fixture commit regardless of the wall clock, so the
// deterministic fixed commit dates drive attribution instead of a 90-day floor.
const fullWindow = int64(1)

// TestTopologyMergedBranches pins the three merge classifications a single
// branch off the trunk can resolve to — a merge commit, a fast-forward, and an
// inferred squash — asserting the whole "B" lane by value.
func TestTopologyMergedBranches(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, r *gitRepo) Lane
	}{
		{
			name: "merge commit",
			build: func(_ *testing.T, r *gitRepo) Lane {
				r.commit("c1")
				c2 := r.commit("c2")
				r.git("checkout", "-q", "-b", "B")
				r.commit("b1")
				b2 := r.commit("b2")
				r.git("checkout", "-q", "main")
				m := r.mergeNoFF(c2.time+1000, "B", "merge B")
				return Lane{
					Name:    "B",
					Parent:  "main",
					Fork:    &Point{SHA: c2.sha, Time: c2.time},
					Merge:   &MergePoint{SHA: m.sha, Time: m.time, Into: "main", Kind: kindMerge},
					Status:  statusMerged,
					Tip:     &Point{SHA: b2.sha, Time: b2.time},
					Start:   c2.time,
					End:     m.time,
					Commits: 2,
				}
			},
		},
		{
			name: "fast forward",
			build: func(_ *testing.T, r *gitRepo) Lane {
				r.commit("c1")
				r.commit("c2")
				r.git("checkout", "-q", "-b", "B")
				r.commit("b1")
				b2 := r.commit("b2")
				r.git("checkout", "-q", "main")
				r.git("merge", "--ff-only", "B")
				r.commit("c3")
				return Lane{
					Name:    "B",
					Parent:  "main",
					Fork:    &Point{SHA: b2.sha, Time: b2.time},
					Merge:   &MergePoint{SHA: b2.sha, Time: b2.time, Into: "main", Kind: kindFastForward},
					Status:  statusMerged,
					Tip:     &Point{SHA: b2.sha, Time: b2.time},
					Start:   b2.time,
					End:     b2.time,
					Commits: 0,
				}
			},
		},
		{
			name: "squash inferred",
			build: func(_ *testing.T, r *gitRepo) Lane {
				r.commit("c1")
				c2 := r.commit("c2")
				r.git("checkout", "-q", "-b", "B")
				r.commit("b1")
				b2 := r.commit("b2")
				r.git("checkout", "-q", "main")
				taskID := r.doneTask(r.openStore(), "ship B", model.Branch("B"))
				c3 := r.commitMsg("squash B", "cc-task: "+taskID.Short())
				return Lane{
					Name:    "B",
					Parent:  "main",
					Fork:    &Point{SHA: c2.sha, Time: c2.time},
					Merge:   &MergePoint{SHA: c3.sha, Time: c3.time, Into: "main", Kind: kindInferred},
					Status:  statusMerged,
					Tip:     &Point{SHA: b2.sha, Time: b2.time},
					Start:   c2.time,
					End:     c3.time,
					Commits: 2,
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newGitRepo(t)
			wantB := tc.build(t, r)
			g, err := NewBuilder(r.openStore()).Graph(t.Context(), fullWindow)
			if err != nil {
				t.Fatalf("Graph: %v", err)
			}
			if gotB := laneByName(t, g, "B"); !reflect.DeepEqual(gotB, wantB) {
				t.Errorf("B lane =\n%+v\nwant\n%+v", laneString(gotB), laneString(wantB))
			}
			assertTrunk(t, g, "main")
		})
	}
}

// TestTopologySequentialMerges covers two branches merged one after the other:
// once feature/one lands at M1, the later merge M2's second parent contains
// feature/one's tip transitively, so each lane must pin the oldest containing
// merge commit — feature/one gets M1, not M2 — along with the fork, extent, and
// commit count that follow from it.
func TestTopologySequentialMerges(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	c2 := r.commit("c2")
	r.git("checkout", "-q", "-b", "feature/one")
	o1 := r.commit("o1")
	r.git("checkout", "-q", "main")
	r.clock += fxStep
	m1 := r.mergeNoFF(r.clock, "feature/one", "merge feature/one")
	r.git("checkout", "-q", "-b", "feature/two")
	t1 := r.commit("t1")
	r.git("checkout", "-q", "main")
	r.clock += fxStep
	m2 := r.mergeNoFF(r.clock, "feature/two", "merge feature/two")

	g, err := NewBuilder(r.openStore()).Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	wantOne := Lane{
		Name:    "feature/one",
		Parent:  "main",
		Fork:    &Point{SHA: c2.sha, Time: c2.time},
		Merge:   &MergePoint{SHA: m1.sha, Time: m1.time, Into: "main", Kind: kindMerge},
		Status:  statusMerged,
		Tip:     &Point{SHA: o1.sha, Time: o1.time},
		Start:   c2.time,
		End:     m1.time,
		Commits: 1,
	}
	wantTwo := Lane{
		Name:    "feature/two",
		Parent:  "main",
		Fork:    &Point{SHA: m1.sha, Time: m1.time},
		Merge:   &MergePoint{SHA: m2.sha, Time: m2.time, Into: "main", Kind: kindMerge},
		Status:  statusMerged,
		Tip:     &Point{SHA: t1.sha, Time: t1.time},
		Start:   m1.time,
		End:     m2.time,
		Commits: 1,
	}
	if got := laneByName(t, g, "feature/one"); !reflect.DeepEqual(got, wantOne) {
		t.Errorf("feature/one lane =\n%+v\nwant\n%+v", laneString(got), laneString(wantOne))
	}
	if got := laneByName(t, g, "feature/two"); !reflect.DeepEqual(got, wantTwo) {
		t.Errorf("feature/two lane =\n%+v\nwant\n%+v", laneString(got), laneString(wantTwo))
	}
	assertTrunk(t, g, "main")
}

// TestTopologyOctopusMerge pins FIX B: an octopus merge with three parents lands
// both feature branches, one under parent index 1 and the other under index 2, so
// both lanes must report a "merge" at that commit. Before the fix only the second
// parent was checked, so the branch carried by a later parent fell through to
// "fast-forward".
func TestTopologyOctopusMerge(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	c2 := r.commit("c2")

	r.git("checkout", "-q", "-b", "b1")
	b1 := r.commit("b1-a")
	r.git("checkout", "-q", "main")
	r.git("checkout", "-q", "-b", "b2")
	b2 := r.commit("b2-a")
	r.git("checkout", "-q", "main")
	r.clock += fxStep
	m := r.mergeOctopus(r.clock, "octopus merge", "b1", "b2")

	g, err := NewBuilder(r.openStore()).Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	for _, tc := range []struct {
		lane string
		tip  model.SHA
	}{
		{"b1", b1.sha},
		{"b2", b2.sha},
	} {
		t.Run(tc.lane, func(t *testing.T) {
			l := laneByName(t, g, tc.lane)
			if l.Status != statusMerged {
				t.Errorf("%s status = %q, want merged", tc.lane, l.Status)
			}
			if l.Merge == nil || l.Merge.Kind != kindMerge || l.Merge.SHA != m.sha {
				t.Errorf("%s merge = %s, want {%s kind=%s}", tc.lane, mergeString(l.Merge), m.sha, kindMerge)
			}
			if l.Tip == nil || l.Tip.SHA != tc.tip {
				t.Errorf("%s tip = %+v, want %s", tc.lane, l.Tip, tc.tip)
			}
			if l.Fork == nil || l.Fork.SHA != c2.sha {
				t.Errorf("%s fork = %+v, want %s", tc.lane, l.Fork, c2.sha)
			}
		})
	}
	assertTrunk(t, g, "main")
}

// TestTopologySquashIgnoresMergedSideBranchTrailer pins FIX C: a cc-task trailer
// on feature/a — merged into the trunk with a merge commit — that names a task
// folded onto the still-active feature/b must not mark feature/b squash-merged.
// Squash inference only walks the trunk's first-parent line, so the trailer, which
// reaches the trunk through the merge's second parent, is invisible to it.
func TestTopologySquashIgnoresMergedSideBranchTrailer(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	c2 := r.commit("c2")

	r.git("checkout", "-q", "-b", "feature/b")
	r.commit("b1")
	r.git("checkout", "-q", "main")
	taskB := r.doneTask(r.openStore(), "ship feature/b", model.Branch("feature/b"))

	r.git("checkout", "-q", "-b", "feature/a")
	r.commitMsg("work a", "cc-task: "+taskB.Short())
	r.git("checkout", "-q", "main")
	r.clock += fxStep
	r.mergeNoFF(r.clock, "feature/a", "merge feature/a")

	g, err := NewBuilder(r.openStore()).Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	b := laneByName(t, g, "feature/b")
	if b.Status != statusActive {
		t.Errorf("feature/b status = %q, want active (a merged side branch's trailer must not squash-merge it)", b.Status)
	}
	if b.Merge != nil {
		t.Errorf("feature/b merge = %s, want nil", mergeString(b.Merge))
	}
	if b.Fork == nil || b.Fork.SHA != c2.sha {
		t.Errorf("feature/b fork = %+v, want %s", b.Fork, c2.sha)
	}
}

// mergeString renders a MergePoint for failure messages, or "nil".
func mergeString(m *MergePoint) string {
	if m == nil {
		return "nil"
	}
	return fmt.Sprintf("{%s@%d into=%s kind=%s}", m.SHA, m.Time, m.Into, m.Kind)
}

// TestTopologyNesting covers a branch off a branch: B2 forks from B1, so its
// parent is B1, not the trunk, while both stay active.
func TestTopologyNesting(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	c2 := r.commit("c2")
	r.git("checkout", "-q", "-b", "B1")
	r.commit("b1a")
	r.git("checkout", "-q", "-b", "B2")
	r.commit("b2a")
	r.git("checkout", "-q", "main")

	g, err := NewBuilder(r.openStore()).Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	b2 := laneByName(t, g, "B2")
	if b2.Parent != "B1" {
		t.Errorf("parent(B2) = %q, want B1", b2.Parent)
	}
	if b2.Status != statusActive {
		t.Errorf("B2 status = %q, want active", b2.Status)
	}
	if b2.Fork == nil || b2.Fork.SHA != c2.sha {
		t.Errorf("B2 fork = %+v, want %s", b2.Fork, c2.sha)
	}
	b1 := laneByName(t, g, "B1")
	if b1.Parent != "main" {
		t.Errorf("parent(B1) = %q, want main (a parent must never adopt its own child)", b1.Parent)
	}
	if b1.Status != statusActive {
		t.Errorf("B1 status = %q, want active", b1.Status)
	}
}

// TestTopologyFlatParentage covers the parentage cap: above maxParentageBranches
// the quadratic scan is skipped and every branch parents to the trunk.
func TestTopologyFlatParentage(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	r.commit("c2")
	count := maxParentageBranches + 1
	for i := range count {
		name := fmt.Sprintf("b-%02d", i)
		r.git("checkout", "-q", "-b", name, "main")
		r.commit(name)
	}
	r.git("checkout", "-q", "main")

	g, err := NewBuilder(r.openStore()).Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if want := count + 1; len(g.Lanes) != want {
		t.Fatalf("lanes = %d, want %d (trunk + %d branches)", len(g.Lanes), want, count)
	}
	if sample := laneByName(t, g, "b-07"); sample.Parent != "main" {
		t.Errorf("parent(b-07) = %q, want main (flat above cap)", sample.Parent)
	}
}

// TestTopologyTrunkResolution covers the trunk-probe fallback: a detached HEAD
// resolves via the local main branch, and a repo with no remote default, no
// HEAD branch, and no main/master fails loudly.
func TestTopologyTrunkResolution(t *testing.T) {
	t.Run("detached resolves via main probe", func(t *testing.T) {
		r := newGitRepo(t)
		r.commit("c1")
		r.git("checkout", "-q", "--detach", "HEAD")

		g, err := NewBuilder(r.openStore()).Graph(t.Context(), fullWindow)
		if err != nil {
			t.Fatalf("Graph: %v", err)
		}
		if g.Repo.Trunk != "main" {
			t.Errorf("trunk = %q, want main", g.Repo.Trunk)
		}
		// Detached at main's tip — the jj colocation norm. CurrentBranch resolves
		// main, so head reports it rather than degrading to empty.
		if g.Repo.Head != "main" {
			t.Errorf("head = %q, want main on a detached-at-tip HEAD", g.Repo.Head)
		}
	})

	t.Run("no default no head no main errors", func(t *testing.T) {
		r := newGitRepoOn(t, "feature")
		r.commit("c1")
		r.git("checkout", "-q", "--detach", "HEAD")

		if _, err := NewBuilder(r.openStore()).Graph(t.Context(), fullWindow); !errors.Is(err, errNoTrunk) {
			t.Fatalf("Graph err = %v, want errNoTrunk", err)
		}
	})
}

// TestTopologyRemoteOnlyBranch covers a branch that exists only as
// refs/remotes/origin/*: it still gets a lane.
func TestTopologyRemoteOnlyBranch(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	c2 := r.commit("c2")
	r.git("checkout", "-q", "-b", "featX")
	fx := r.commit("fx1")
	r.git("checkout", "-q", "main")
	r.git("update-ref", "refs/remotes/origin/featX", string(fx.sha))
	r.git("branch", "-D", "featX")

	g, err := NewBuilder(r.openStore()).Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	lane := laneByName(t, g, "featX")
	if lane.Status != statusActive {
		t.Errorf("featX status = %q, want active", lane.Status)
	}
	if lane.Tip == nil || lane.Tip.SHA != fx.sha {
		t.Errorf("featX tip = %+v, want %s", lane.Tip, fx.sha)
	}
	if lane.Fork == nil || lane.Fork.SHA != c2.sha {
		t.Errorf("featX fork = %+v, want %s", lane.Fork, c2.sha)
	}
}

// TestWindowSince pins the default-window lower bound: never earlier than
// defaultWindow before now, and never earlier than the oldest active fork.
func TestWindowSince(t *testing.T) {
	day := int64(24 * 3600)
	ninety := int64(defaultWindow.Seconds())
	now := int64(1_000_000_000)
	cases := []struct {
		name       string
		oldestFork int64
		want       int64
	}{
		{"no active lanes floors at window", 0, now - ninety},
		{"old fork clamped to window floor", now - 200*day, now - ninety},
		{"recent fork tightens window", now - 30*day, now - 30*day},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := windowSince(now, tc.oldestFork); got != tc.want {
				t.Errorf("windowSince(%d, %d) = %d, want %d", now, tc.oldestFork, got, tc.want)
			}
		})
	}
}

// TestOldestRefBackedFork pins that the default-window floor considers every
// ref-backed lane's fork, merged branches included: a merged branch that forked
// before every open branch must set the floor, or its fork and merge connectors
// dangle left of the trunk rail.
func TestOldestRefBackedFork(t *testing.T) {
	day := int64(24 * 3600)
	now := int64(1_000_000_000)
	cases := []struct {
		name   string
		others []*branchState
		want   int64
	}{
		{"no forks", []*branchState{{name: "a", status: statusActive}}, 0},
		{
			"merged fork predates the open fork",
			[]*branchState{
				{name: "open", status: statusActive, hasFork: true, forkTime: now - 10*day},
				{name: "merged", status: statusMerged, hasFork: true, forkTime: now - 50*day},
			},
			now - 50*day,
		},
		{
			"open fork is oldest",
			[]*branchState{
				{name: "open", status: statusActive, hasFork: true, forkTime: now - 80*day},
				{name: "merged", status: statusMerged, hasFork: true, forkTime: now - 20*day},
			},
			now - 80*day,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := oldestRefBackedFork(tc.others); got != tc.want {
				t.Errorf("oldestRefBackedFork = %d, want %d", got, tc.want)
			}
		})
	}
}

// assertTrunk checks the trunk lane's invariants: it forks from nothing, merges
// into nothing, has no parent, and stays active with an open end.
func assertTrunk(t *testing.T, g *Graph, name string) {
	t.Helper()
	if g.Repo.Trunk != name {
		t.Errorf("repo trunk = %q, want %q", g.Repo.Trunk, name)
	}
	trunk := laneByName(t, g, name)
	if trunk.Parent != "" || trunk.Fork != nil || trunk.Merge != nil {
		t.Errorf("trunk lane = %+v, want no parent/fork/merge", laneString(trunk))
	}
	if trunk.Status != statusActive || trunk.End != 0 {
		t.Errorf("trunk status/end = %q/%d, want active/0", trunk.Status, trunk.End)
	}
}

// laneString renders a lane with its pointer fields dereferenced for readable
// failure messages.
func laneString(l Lane) string {
	fork, merge, tip := "nil", "nil", "nil"
	if l.Fork != nil {
		fork = fmt.Sprintf("{%s@%d}", l.Fork.SHA, l.Fork.Time)
	}
	if l.Merge != nil {
		merge = fmt.Sprintf("{%s@%d into=%s kind=%s}", l.Merge.SHA, l.Merge.Time, l.Merge.Into, l.Merge.Kind)
	}
	if l.Tip != nil {
		tip = fmt.Sprintf("{%s@%d}", l.Tip.SHA, l.Tip.Time)
	}
	return fmt.Sprintf("Lane{name=%s parent=%s status=%s inferred=%t fork=%s merge=%s tip=%s start=%d end=%d commits=%d}",
		l.Name, l.Parent, l.Status, l.Inferred, fork, merge, tip, l.Start, l.End, l.Commits)
}
