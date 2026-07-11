package viz

import (
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/model"
)

// TestParseMergeSummary pins the merge-subject forms mining recognizes — git's
// default and "into" variants, remote-tracking (remote stripped), and a GitHub
// pull request (owner stripped, nested branch kept) — plus an unparseable
// subject.
func TestParseMergeSummary(t *testing.T) {
	cases := []struct {
		name    string
		summary string
		want    string
		into    string
		ok      bool
	}{
		{"git default", "Merge branch 'feature/foo'", "feature/foo", "", true},
		{"into form", "Merge branch 'topic' into release", "topic", "release", true},
		{"remote tracking", "Merge remote-tracking branch 'origin/feature/bar'", "feature/bar", "", true},
		{"remote tracking into", "Merge remote-tracking branch 'origin/hotfix' into main", "hotfix", "main", true},
		{"github pr", "Merge pull request #42 from octocat/feature-x", "feature-x", "", true},
		{"github pr nested", "Merge pull request #7 from octocat/feat/nested", "feat/nested", "", true},
		{"unparseable subject", "fix: correct the thing", "", "", false},
		{"plain commit", "Add the widget", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			name, into, ok := parseMergeSummary(tc.summary)
			if name != tc.want || into != tc.into || ok != tc.ok {
				t.Errorf("parseMergeSummary(%q) = (%q, %q, %t), want (%q, %q, %t)",
					tc.summary, name, into, ok, tc.want, tc.into, tc.ok)
			}
		})
	}
}

// TestMineDeletedBranches covers the four DAG-mining outcomes: a merged branch
// whose ref was deleted becomes a full deleted lane; a branch merged into a
// branch that was then also deleted is reconstructed recursively with the right
// parentage; a squash-then-delete with no surviving merge commit stays a
// task-inferred lane and is not mined; and a merged branch whose ref is kept is
// not duplicated.
func TestMineDeletedBranches(t *testing.T) {
	t.Run("merged then deleted", func(t *testing.T) {
		r := newGitRepo(t)
		r.commit("c1")
		c2 := r.commit("c2")
		r.git("checkout", "-q", "-b", "B")
		r.commit("b1")
		b2 := r.commit("b2")
		r.git("checkout", "-q", "main")
		m := r.mergeNoFF(c2.time+1000, "B", "Merge branch 'B'")
		r.git("branch", "-D", "B")

		g, err := NewBuilder(r.openStore()).Graph(t.Context(), fullWindow)
		if err != nil {
			t.Fatalf("Graph: %v", err)
		}
		want := Lane{
			Name:     "B",
			Parent:   "main",
			Fork:     &Point{SHA: c2.sha, Time: c2.time},
			Merge:    &MergePoint{SHA: m.sha, Time: m.time, Into: "main", Kind: kindMerge},
			Status:   statusDeleted,
			Inferred: false,
			Tip:      &Point{SHA: b2.sha, Time: b2.time},
			Start:    c2.time,
			End:      m.time,
			Commits:  2,
		}
		if got := laneByName(t, g, "B"); !reflect.DeepEqual(got, want) {
			t.Errorf("mined B lane =\n%s\nwant\n%s", laneString(got), laneString(want))
		}
		assertTrunk(t, g, "main")
	})

	t.Run("nested both deleted", func(t *testing.T) {
		r := newGitRepo(t)
		r.commit("c1")
		c2 := r.commit("c2")
		r.git("checkout", "-q", "-b", "B1")
		b1a := r.commit("b1a")
		r.git("checkout", "-q", "-b", "B2")
		b2a := r.commit("b2a")
		r.git("checkout", "-q", "B1")
		m2 := r.mergeNoFF(b2a.time+100, "B2", "Merge branch 'B2'")
		r.git("checkout", "-q", "main")
		m1 := r.mergeNoFF(m2.time+100, "B1", "Merge branch 'B1'")
		r.git("branch", "-D", "B1")
		r.git("branch", "-D", "B2")

		g, err := NewBuilder(r.openStore()).Graph(t.Context(), fullWindow)
		if err != nil {
			t.Fatalf("Graph: %v", err)
		}
		wantB1 := Lane{
			Name:     "B1",
			Parent:   "main",
			Fork:     &Point{SHA: c2.sha, Time: c2.time},
			Merge:    &MergePoint{SHA: m1.sha, Time: m1.time, Into: "main", Kind: kindMerge},
			Status:   statusDeleted,
			Inferred: false,
			Tip:      &Point{SHA: m2.sha, Time: m2.time},
			Start:    c2.time,
			End:      m1.time,
			Commits:  3,
		}
		wantB2 := Lane{
			Name:     "B2",
			Parent:   "B1",
			Fork:     &Point{SHA: b1a.sha, Time: b1a.time},
			Merge:    &MergePoint{SHA: m2.sha, Time: m2.time, Into: "B1", Kind: kindMerge},
			Status:   statusDeleted,
			Inferred: false,
			Tip:      &Point{SHA: b2a.sha, Time: b2a.time},
			Start:    b1a.time,
			End:      m2.time,
			Commits:  1,
		}
		if got := laneByName(t, g, "B1"); !reflect.DeepEqual(got, wantB1) {
			t.Errorf("mined B1 lane =\n%s\nwant\n%s", laneString(got), laneString(wantB1))
		}
		if got := laneByName(t, g, "B2"); !reflect.DeepEqual(got, wantB2) {
			t.Errorf("mined B2 lane =\n%s\nwant\n%s", laneString(got), laneString(wantB2))
		}
		assertTrunk(t, g, "main")
	})

	t.Run("squash then delete stays task-inferred", func(t *testing.T) {
		r := newGitRepo(t)
		r.commit("c1")
		r.commit("c2")
		r.git("checkout", "-q", "-b", "B")
		r.commit("b1")
		r.commit("b2")
		r.git("checkout", "-q", "main")
		s := r.openStore()
		taskID := createTask(t, s, "ship B", model.Branch("B"))
		ref := refs.For(model.KindTask, taskID)
		appendOps(t, s, ref, model.SetStatus{Status: model.StatusDone})
		r.commitMsg("squash B", "cc-task: "+taskID.Short())
		appendOps(t, s, ref, model.SetBranch{Branch: model.Branch("main")})
		r.git("branch", "-D", "B")

		g, err := NewBuilder(s).Graph(t.Context(), fullWindow)
		if err != nil {
			t.Fatalf("Graph: %v", err)
		}
		lane := laneByName(t, g, "B")
		if lane.Status != statusDeleted {
			t.Errorf("B status = %q, want deleted", lane.Status)
		}
		if !lane.Inferred {
			t.Errorf("B inferred = false, want true (a squash+delete has no merge commit to mine)")
		}
		if lane.Fork != nil || lane.Tip != nil {
			t.Errorf("B fork/tip = %v/%v, want nil/nil (a task-inferred lane is ref-less)", lane.Fork, lane.Tip)
		}
		if lane.Merge == nil || lane.Merge.Kind != kindInferred {
			t.Errorf("B merge = %s, want an inferred merge", mergeString(lane.Merge))
		}
		if n := countLanes(g, "B"); n != 1 {
			t.Errorf("lanes named B = %d, want 1 (mining must not duplicate the task-inferred lane)", n)
		}
	})

	t.Run("live merged branch not duplicated", func(t *testing.T) {
		r := newGitRepo(t)
		r.commit("c1")
		c2 := r.commit("c2")
		r.git("checkout", "-q", "-b", "B")
		r.commit("b1")
		r.commit("b2")
		r.git("checkout", "-q", "main")
		r.mergeNoFF(c2.time+1000, "B", "Merge branch 'B'")

		g, err := NewBuilder(r.openStore()).Graph(t.Context(), fullWindow)
		if err != nil {
			t.Fatalf("Graph: %v", err)
		}
		if n := countLanes(g, "B"); n != 1 {
			t.Fatalf("lanes named B = %d, want 1 (a merged branch whose ref is kept must not be mined again)", n)
		}
		lane := laneByName(t, g, "B")
		if lane.Status != statusMerged {
			t.Errorf("B status = %q, want merged", lane.Status)
		}
		if lane.Inferred {
			t.Errorf("B inferred = true, want false (the lane is ref-backed)")
		}
	})
}

// TestMinedLanesMemoSurvivesEntityChurn pins the mined-lane memo key: an entity
// ref append between two Graph calls drops the whole-graph cache but must not
// re-mine, because the mined cache keys on (trunk tip, live tips, since) alone.
// A branch-ref invalidation, by contrast, clears it.
func TestMinedLanesMemoSurvivesEntityChurn(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	c2 := r.commit("c2")
	r.git("checkout", "-q", "-b", "B")
	r.commit("b1")
	r.git("checkout", "-q", "main")
	r.mergeNoFF(c2.time+1000, "B", "Merge branch 'B'")
	r.git("branch", "-D", "B")

	s := r.openStore()
	b := NewBuilder(s)

	g1, err := b.Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	if laneByName(t, g1, "B").Status != statusDeleted {
		t.Fatalf("B not mined on first Graph")
	}
	if n := minedCacheLen(b); n != 1 {
		t.Fatalf("minedCache entries after first Graph = %d, want 1", n)
	}

	id := createTask(t, s, "churn task", model.Branch("main"))
	ref := refs.For(model.KindTask, id)
	appendOps(t, s, ref, model.SetStatus{Status: model.StatusDone})
	b.InvalidateRefs([]string{ref})

	g2, err := b.Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph after entity churn: %v", err)
	}
	if g2 == g1 {
		t.Fatalf("entity churn returned the stale cached graph, want a rebuild")
	}
	if laneByName(t, g2, "B").Status != statusDeleted {
		t.Errorf("B lane lost after entity churn")
	}
	if n := minedCacheLen(b); n != 1 {
		t.Errorf("minedCache entries after entity churn = %d, want 1 (entity churn must not re-mine)", n)
	}

	b.InvalidateRefs([]string{"refs/heads/some-branch"})
	if n := minedCacheLen(b); n != 0 {
		t.Errorf("minedCache entries after branch-ref invalidation = %d, want 0", n)
	}
}

// countLanes counts the lanes named name.
func countLanes(g *Graph, name string) int {
	n := 0
	for _, l := range g.Lanes {
		if l.Name == name {
			n++
		}
	}
	return n
}

// minedCacheLen returns the number of memoized mined-lane sets the builder holds.
func minedCacheLen(b *Builder) int {
	b.minedMu.Lock()
	defer b.minedMu.Unlock()
	return len(b.minedCache)
}
