package viz

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// updateGolden regenerates the golden files instead of asserting against them:
// go test ./internal/viz -run Golden -update.
var updateGolden = flag.Bool("update", false, "regenerate viz golden files")

// goldenStartDate and goldenEndDate are literal sprint dates — 2023 unix seconds
// disjoint from the fixture's git and op-commit times — so they survive
// normalization as raw anchors.
const (
	goldenStartDate = int64(1690000000) // 2023-07-22
	goldenEndDate   = int64(1698000000) // 2023-10-22
)

// TestGraphGolden pins one end-to-end Builder.Graph over a fixture spanning every
// merge classification, a deleted-branch lane, nested branches, and every entity
// kind. The output is normalized (see normalizeGraph) because op-commit shas,
// ids, and times are random per run. A second Graph call proves cache-hit
// equivalence: it returns the identical cached pointer, whose normalized JSON is
// byte-identical.
//
// The default window (Graph called with since 0) spans the last ninety days from
// now, which is later than the fixed January-2026 fixture commits, so the
// window-bounded attribution — every lane's Commits and the trunk's window start
// — is empty; the golden pins that empty attribution alongside the topology,
// events, and entity summaries, which do not depend on the window.
func TestGraphGolden(t *testing.T) {
	r := buildGoldenRepo(t)
	b := NewBuilder(r.openStore())

	g1, err := b.Graph(t.Context(), 0)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	got := normalizeGraph(t, g1)

	golden := filepath.Join("testdata", "graph_full.json")
	if *updateGolden {
		if err := os.WriteFile(golden, []byte(got), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read golden (regenerate with -update): %v", err)
	}
	if got != string(want) {
		t.Errorf("normalized graph mismatch (regenerate with -update):\n%s", got)
	}

	g2, err := b.Graph(t.Context(), 0)
	if err != nil {
		t.Fatalf("Graph second call: %v", err)
	}
	if g2 != g1 {
		t.Errorf("second Graph call returned a fresh graph, want the cached pointer")
	}
	if got2 := normalizeGraph(t, g2); got2 != got {
		t.Errorf("cached graph normalized differently:\n%s", got2)
	}
}

// buildGoldenRepo constructs the golden fixture: trunk main; feature/ff
// fast-forwarded; feature/merge merged via a merge commit; feature/squash
// squash-inferred from a cc-task trailer; nested feature/parent and
// feature/child; a deleted feature/gone reconstructed from a task trail; and one
// entity of every kind exercising the lifecycle, freshness, log-entry, checkpoint,
// and sprint/project-membership paths.
func buildGoldenRepo(t *testing.T) *gitRepo {
	t.Helper()
	r := newGitRepo(t)
	c1 := r.commit("c1")
	c2 := r.commit("c2")

	r.git("checkout", "-q", "-b", "feature/ff")
	r.commit("ff-a")
	r.git("checkout", "-q", "main")
	r.git("merge", "--ff-only", "feature/ff")
	r.commit("c3")

	r.git("checkout", "-q", "-b", "feature/merge")
	r.commit("fm-a")
	r.commit("fm-b")
	r.git("checkout", "-q", "main")
	r.clock += fxStep
	r.mergeNoFF(r.clock, "feature/merge", "merge feature/merge")

	r.git("checkout", "-q", "-b", "feature/squash")
	r.commit("sq-a")
	r.commit("sq-b")
	r.git("checkout", "-q", "main")
	s := r.openStore()
	squashID := r.doneTask(s, "squash task on feature-squash", model.Branch("feature/squash"))
	c4 := r.commitMsg("squash feature/squash", "cc-task: "+squashID.Short())

	r.git("checkout", "-q", "-b", "feature/parent")
	r.commit("pp-a")
	r.git("checkout", "-q", "-b", "feature/child")
	r.commit("cc-a")
	r.git("checkout", "-q", "main")

	buildGoldenEntities(t, s, c1, c2, c4)
	return r
}

// buildGoldenEntities writes one entity of every kind onto the store: a verified
// then superseded note, a compacted note, a doc marked stale, a log with two
// entries, an archived project, an active sprint carrying literal dates, a task in
// both, a full-lifecycle task linking a real commit, and a task stranded on the
// deleted feature/gone branch.
func buildGoldenEntities(t *testing.T, s *store.Store, c1, c2, c4 commitInfo) {
	t.Helper()
	ctx := t.Context()

	noteID := createNote(t, s, "verified superseded note")
	appendOps(t, s, refs.Note(noteID), model.VerifyNote{VerifiedCommit: c2.sha})
	appendOps(t, s, refs.Note(noteID), model.AddSupersededBy{ID: model.EntityID("0123456789abcdef0123456789abcdef01234567")})

	compactID := createNote(t, s, "compacted note")
	appendOps(t, s, refs.Note(compactID), model.VerifyNote{VerifiedCommit: c1.sha})
	if _, err := s.Compact(ctx, refs.Note(compactID)); err != nil {
		t.Fatalf("compact note: %v", err)
	}

	docID := createDocWhen(t, s, "stale design doc", "before the auth rewrite")
	appendOps(t, s, refs.Doc(docID), model.MarkStale{Reason: "rewritten"})

	logID := createLog(t, s, "rollout log")
	appendOps(t, s, refs.Log(logID), model.AppendEntry{Text: "flipped to 5%"})
	appendOps(t, s, refs.Log(logID), model.AppendEntry{Text: "flipped to 100%"})

	projID := createProject(t, s, "platform project")
	appendOps(t, s, refs.Project(projID), model.SetProjectStatus{Status: model.ProjectArchived})

	sprintID := createSprint(t, s, "q3 hardening sprint", projID)
	appendOps(t, s, refs.Sprint(sprintID),
		model.SetSprintStatus{Status: model.SprintActive},
		model.SetStartDate{Date: goldenStartDate},
		model.SetEndDate{Date: goldenEndDate})

	memberID := createTask(t, s, "member task in sprint", "")
	appendOps(t, s, refs.Task(memberID), model.SetProject{Project: projID}, model.SetSprint{Sprint: sprintID})

	lifeID := createTask(t, s, "lifecycle task across branches", model.Branch("feature/parent"))
	lref := refs.Task(lifeID)
	appendOps(t, s, lref, model.Claim{Assignee: model.Actor("alice <alice@example.com>")})
	appendOps(t, s, lref, model.SetBranch{Branch: model.Branch("main")})
	appendOps(t, s, lref, model.SetStatus{Status: model.StatusDone})
	appendOps(t, s, lref, model.LinkCommit{SHA: c4.sha})

	goneID := createTask(t, s, "gone branch task", model.Branch("feature/gone"))
	appendOps(t, s, refs.Task(goneID), model.SetBranch{Branch: model.Branch("main")})
}

// createDocWhen creates a doc carrying a --when qualifier, returning its id.
func createDocWhen(t *testing.T, s *store.Store, title, when string) model.EntityID {
	t.Helper()
	snap, err := s.Create(t.Context(), []model.Op{model.CreateDoc{Nonce: model.NewNonce(), Title: title, When: when}})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	return snap.(model.Doc).ID
}

// createProject creates a project, returning its id.
func createProject(t *testing.T, s *store.Store, title string) model.EntityID {
	t.Helper()
	snap, err := s.Create(t.Context(), []model.Op{model.CreateProject{Nonce: model.NewNonce(), Title: title}})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	return snap.(model.Project).ID
}

// createSprint creates a sprint in the given project, returning its id.
func createSprint(t *testing.T, s *store.Store, title string, project model.EntityID) model.EntityID {
	t.Helper()
	snap, err := s.Create(t.Context(), []model.Op{model.CreateSprint{Nonce: model.NewNonce(), Title: title, Project: project}})
	if err != nil {
		t.Fatalf("create sprint: %v", err)
	}
	return snap.(model.Sprint).ID
}
