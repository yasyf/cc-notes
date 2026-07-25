package viz

import (
	"fmt"
	"testing"
	"time"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// windowAnchors are fixture commit times either side of the default history
// window: inWindow is an hour ago, preWindow a month older than the window
// floor, so a branch tipped there is stale however long the test takes to run.
func windowAnchors() (inWindow, preWindow int64) {
	now := time.Now().Unix()
	return now - int64(time.Hour.Seconds()), now - int64(defaultWindow.Seconds()) - 30*24*3600
}

// TestWindowFilterHidesStaleLanes covers which branches survive the default
// window: a stale branch is dropped and never reappears as a task-inferred
// deleted lane, a stale branch the trunk absorbed inside the window is rescued
// at its real merge point, one absorbed before the window stays dropped, a live
// task pins its branch however stale, and an explicit since overrides the whole
// filter.
func TestWindowFilterHidesStaleLanes(t *testing.T) {
	inWindow, preWindow := windowAnchors()

	t.Run("stale branch dropped without a phantom deleted lane", func(t *testing.T) {
		r := newGitRepo(t)
		r.at(preWindow)
		r.commit("c1")
		r.git("checkout", "-q", "-b", "stale-feature")
		r.commit("s1")
		r.git("checkout", "-q", "main")
		r.at(inWindow)
		r.commit("c2")
		s := r.openStore()
		// A done task, so the branch is stale rather than task-rescued, yet its
		// events still name it: the hidden branch has to claim its own name or
		// the task trail reconstructs it as a deleted lane.
		r.doneTask(s, "shipped on stale-feature", model.Branch("stale-feature"))

		g := buildDefaultGraph(t, r)
		assertNoLane(t, g, "stale-feature")
		assertTrunk(t, g, "main")
		if g.Repo.LanesOmitted != 1 {
			t.Errorf("lanes_omitted = %d, want 1", g.Repo.LanesOmitted)
		}
	})

	t.Run("stale branch merged in the window is rescued", func(t *testing.T) {
		r := newGitRepo(t)
		r.at(preWindow)
		r.commit("c1")
		r.git("checkout", "-q", "-b", "old-merged")
		b1 := r.commit("b1")
		r.git("checkout", "-q", "main")
		r.at(inWindow)
		r.commit("c2")
		m := r.mergeNoFF(inWindow+120, "old-merged", "Merge branch 'old-merged'")

		g := buildDefaultGraph(t, r)
		lane := laneByName(t, g, "old-merged")
		if lane.Status != statusMerged {
			t.Errorf("old-merged status = %q, want merged", lane.Status)
		}
		if lane.Merge == nil || lane.Merge.SHA != m.sha || lane.Merge.Kind != kindMerge {
			t.Errorf("old-merged merge = %s, want {%s kind=%s}", mergeString(lane.Merge), m.sha, kindMerge)
		}
		if lane.Tip == nil || lane.Tip.SHA != b1.sha {
			t.Errorf("old-merged tip = %+v, want %s", lane.Tip, b1.sha)
		}
		if g.Repo.LanesOmitted != 0 {
			t.Errorf("lanes_omitted = %d, want 0", g.Repo.LanesOmitted)
		}
	})

	t.Run("stale branch merged before the window stays dropped", func(t *testing.T) {
		r := newGitRepo(t)
		r.at(preWindow)
		r.commit("c1")
		r.git("checkout", "-q", "-b", "old-merged")
		r.commit("b1")
		r.git("checkout", "-q", "main")
		r.mergeNoFF(preWindow+120, "old-merged", "Merge branch 'old-merged'")
		// A second branch merged inside the window, so the rescue probe has a
		// boundary to compare against and the drop is a real verdict rather
		// than the no-merge-in-window shortcut.
		r.at(inWindow)
		r.commit("c2")
		r.git("checkout", "-q", "-b", "fresh")
		r.commit("f1")
		r.git("checkout", "-q", "main")
		r.mergeNoFF(inWindow+180, "fresh", "Merge branch 'fresh'")

		g := buildDefaultGraph(t, r)
		assertNoLane(t, g, "old-merged")
		if lane := laneByName(t, g, "fresh"); lane.Status != statusMerged {
			t.Errorf("fresh status = %q, want merged", lane.Status)
		}
		if g.Repo.LanesOmitted != 1 {
			t.Errorf("lanes_omitted = %d, want 1", g.Repo.LanesOmitted)
		}
	})

	t.Run("a live task pins its stale branch", func(t *testing.T) {
		r := newGitRepo(t)
		r.at(preWindow)
		r.commit("c1")
		r.git("checkout", "-q", "-b", "wip")
		w1 := r.commit("w1")
		r.git("checkout", "-q", "main")
		r.at(inWindow)
		r.commit("c2")
		createTask(t, r.openStore(), "still working on wip", model.Branch("wip"))

		g := buildDefaultGraph(t, r)
		lane := laneByName(t, g, "wip")
		if lane.Status != statusActive {
			t.Errorf("wip status = %q, want active", lane.Status)
		}
		if lane.Tip == nil || lane.Tip.SHA != w1.sha {
			t.Errorf("wip tip = %+v, want %s", lane.Tip, w1.sha)
		}
		if g.Repo.LanesOmitted != 0 {
			t.Errorf("lanes_omitted = %d, want 0", g.Repo.LanesOmitted)
		}
	})

	t.Run("a cancelled task does not pin its branch", func(t *testing.T) {
		r := newGitRepo(t)
		r.at(preWindow)
		r.commit("c1")
		r.git("checkout", "-q", "-b", "abandoned")
		r.commit("a1")
		r.git("checkout", "-q", "main")
		r.at(inWindow)
		r.commit("c2")
		s := r.openStore()
		id := createTask(t, s, "gave up on abandoned", model.Branch("abandoned"))
		appendOps(t, s, refs.For(model.KindTask, id), model.SetStatus{Status: model.StatusCancelled})

		g := buildDefaultGraph(t, r)
		assertNoLane(t, g, "abandoned")
	})

	t.Run("a stale trunk keeps its lane", func(t *testing.T) {
		r := newGitRepo(t)
		r.at(preWindow)
		r.commit("c1")
		r.git("checkout", "-q", "-b", "stale-feature")
		r.commit("s1")
		r.git("checkout", "-q", "main")

		g := buildDefaultGraph(t, r)
		assertTrunk(t, g, "main")
		assertNoLane(t, g, "stale-feature")
	})

	t.Run("an explicit since overrides the filter", func(t *testing.T) {
		r := newGitRepo(t)
		r.at(preWindow)
		r.commit("c1")
		r.git("checkout", "-q", "-b", "stale-feature")
		s1 := r.commit("s1")
		r.git("checkout", "-q", "main")
		r.at(inWindow)
		r.commit("c2")

		g := buildGraph(t, r)
		lane := laneByName(t, g, "stale-feature")
		if lane.Tip == nil || lane.Tip.SHA != s1.sha {
			t.Errorf("stale-feature tip = %+v, want %s", lane.Tip, s1.sha)
		}
		if g.Repo.LanesOmitted != 0 {
			t.Errorf("lanes_omitted = %d, want 0 under an explicit since", g.Repo.LanesOmitted)
		}
	})
}

// TestLaneCap covers the cap that backstops the window filter: over the cap the
// most recently tipped lanes win, ties broken by name; the surplus is counted in
// LanesOmitted; an unset MaxLanes selects the generous default; and a lane a live
// task names is kept over the cap rather than counted against it.
func TestLaneCap(t *testing.T) {
	inWindow, preWindow := windowAnchors()

	// buildFive lays five in-window branches down with strictly increasing tip
	// times, so "the newest three" is unambiguous.
	buildFive := func(t *testing.T) *gitRepo {
		t.Helper()
		r := newGitRepo(t)
		r.at(inWindow)
		r.commit("c1")
		for i := range 5 {
			name := fmt.Sprintf("b-%d", i)
			r.git("checkout", "-q", "-b", name, "main")
			r.at(inWindow + int64(60*(i+1)))
			r.commit(name)
		}
		r.git("checkout", "-q", "main")
		return r
	}

	t.Run("over the cap keeps the newest tips", func(t *testing.T) {
		r := buildFive(t)
		g := buildCappedGraph(t, r, 3)
		if len(g.Lanes) != 4 {
			t.Fatalf("lanes = %v, want the trunk plus 3 branches", laneNames(g))
		}
		for _, name := range []string{"b-2", "b-3", "b-4"} {
			if lane := laneByName(t, g, name); lane.Status != statusActive {
				t.Errorf("%s status = %q, want active", name, lane.Status)
			}
		}
		assertNoLane(t, g, "b-0")
		assertNoLane(t, g, "b-1")
		if g.Repo.LanesOmitted != 2 {
			t.Errorf("lanes_omitted = %d, want 2", g.Repo.LanesOmitted)
		}
		assertTrunk(t, g, "main")
	})

	t.Run("an unset cap keeps every lane", func(t *testing.T) {
		r := buildFive(t)
		g := buildCappedGraph(t, r, 0)
		if len(g.Lanes) != 6 {
			t.Fatalf("lanes = %v, want the trunk plus 5 branches", laneNames(g))
		}
		if g.Repo.LanesOmitted != 0 {
			t.Errorf("lanes_omitted = %d, want 0", g.Repo.LanesOmitted)
		}
	})

	t.Run("a task-named lane is exempt from the cap", func(t *testing.T) {
		r := newGitRepo(t)
		r.at(preWindow)
		r.commit("c1")
		r.git("checkout", "-q", "-b", "claimed")
		r.commit("k1")
		r.git("checkout", "-q", "main")
		r.at(inWindow)
		r.commit("c2")
		r.git("checkout", "-q", "-b", "recent", "main")
		r.commit("r1")
		r.git("checkout", "-q", "main")
		createTask(t, r.openStore(), "working on claimed", model.Branch("claimed"))

		g := buildCappedGraph(t, r, 1)
		if len(g.Lanes) != 3 {
			t.Fatalf("lanes = %v, want the trunk plus claimed and recent", laneNames(g))
		}
		laneByName(t, g, "claimed")
		laneByName(t, g, "recent")
		if g.Repo.LanesOmitted != 0 {
			t.Errorf("lanes_omitted = %d, want 0 (the cap admits one lane and the task lane is exempt)", g.Repo.LanesOmitted)
		}
	})
}

// TestMiningWindow pins that deleted-branch mining is window-bounded: a branch
// merged and deleted before the window is not reconstructed, while one merged
// inside it still is.
func TestMiningWindow(t *testing.T) {
	inWindow, preWindow := windowAnchors()
	cases := []struct {
		name      string
		mergeTime int64
		wantLane  bool
	}{
		{"merged before the window is not mined", preWindow + 120, false},
		{"merged inside the window is mined", inWindow + 120, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newGitRepo(t)
			r.at(preWindow)
			r.commit("c1")
			r.git("checkout", "-q", "-b", "gone")
			gone := r.commit("g1")
			r.git("checkout", "-q", "main")
			r.at(inWindow)
			r.commit("c2")
			m := r.mergeNoFF(tc.mergeTime, "gone", "Merge branch 'gone'")
			r.git("branch", "-D", "gone")

			g := buildDefaultGraph(t, r)
			if !tc.wantLane {
				assertNoLane(t, g, "gone")
				return
			}
			lane := laneByName(t, g, "gone")
			if lane.Status != statusDeleted || lane.Inferred {
				t.Errorf("gone status/inferred = %q/%t, want deleted/false", lane.Status, lane.Inferred)
			}
			if lane.Tip == nil || lane.Tip.SHA != gone.sha {
				t.Errorf("gone tip = %+v, want %s", lane.Tip, gone.sha)
			}
			if lane.Merge == nil || lane.Merge.SHA != m.sha {
				t.Errorf("gone merge = %s, want %s", mergeString(lane.Merge), m.sha)
			}
		})
	}
}

// buildDefaultGraph builds the graph over the default history window, the
// setting the window filter and the lane cap apply to.
func buildDefaultGraph(t *testing.T, r *gitRepo) *Graph {
	t.Helper()
	return buildCappedGraph(t, r, 0)
}

// buildCappedGraph builds the graph over the default history window with the
// given lane cap.
func buildCappedGraph(t *testing.T, r *gitRepo, maxLanes int) *Graph {
	t.Helper()
	b := NewBuilder(r.openStore())
	b.MaxLanes = maxLanes
	g, err := b.Graph(t.Context(), 0)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	return g
}

// assertNoLane fails when a lane of the given name is present.
func assertNoLane(t *testing.T, g *Graph, name string) {
	t.Helper()
	for _, l := range g.Lanes {
		if l.Name == name {
			t.Fatalf("lane %q present, want it filtered out: %s", name, laneString(l))
		}
	}
}

// laneNames lists the graph's lane names for failure messages.
func laneNames(g *Graph) []string {
	names := make([]string, 0, len(g.Lanes))
	for _, l := range g.Lanes {
		names = append(names, l.Name)
	}
	return names
}
