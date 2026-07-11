package viz

import (
	"reflect"
	"testing"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

// evShape is the deterministic projection of an Event the tests assert on: its
// type, attributed branch, and detail. Time and sha are wall-clock and content
// dependent (the store stamps commit time from the wall clock), so they are
// excluded from equality and observed only relatively.
type evShape struct {
	typ    string
	branch string
	detail map[string]string
}

// shapesFor projects, in graph order, every event belonging to the entity id.
func shapesFor(g *Graph, id model.EntityID) []evShape {
	var out []evShape
	for _, e := range g.Events {
		if e.Entity.ID == id {
			out = append(out, evShape{typ: e.Type, branch: e.Branch, detail: e.Detail})
		}
	}
	return out
}

// eventTime returns the time of the first event of the given type for id.
func eventTime(t *testing.T, g *Graph, id model.EntityID, typ string) int64 {
	t.Helper()
	for _, e := range g.Events {
		if e.Entity.ID == id && e.Type == typ {
			return e.Time
		}
	}
	t.Fatalf("no %q event for %s", typ, id)
	return 0
}

// summaryByID returns the legend summary for id, failing if absent.
func summaryByID(t *testing.T, g *Graph, id model.EntityID) EntitySummary {
	t.Helper()
	for _, s := range g.Entities {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no summary for %s", id)
	return EntitySummary{}
}

func buildGraph(t *testing.T, r *gitRepo) *Graph {
	t.Helper()
	g, err := NewBuilder(r.openStore()).Graph(t.Context(), fullWindow)
	if err != nil {
		t.Fatalf("Graph: %v", err)
	}
	return g
}

func createTask(t *testing.T, s *store.Store, title string, branch model.Branch) model.EntityID {
	t.Helper()
	snap, err := s.Create(t.Context(), []model.Op{model.CreateTask{Nonce: model.NewNonce(), Title: title, Type: model.TypeTask, Branch: branch}})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	return snap.(model.Task).ID
}

func createNote(t *testing.T, s *store.Store, title string) model.EntityID {
	t.Helper()
	snap, err := s.Create(t.Context(), []model.Op{model.CreateNote{Nonce: model.NewNonce(), Title: title}})
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	return snap.(model.Note).ID
}

func createDoc(t *testing.T, s *store.Store, title string) model.EntityID {
	t.Helper()
	snap, err := s.Create(t.Context(), []model.Op{model.CreateDoc{Nonce: model.NewNonce(), Title: title}})
	if err != nil {
		t.Fatalf("create doc: %v", err)
	}
	return snap.(model.Doc).ID
}

func createLog(t *testing.T, s *store.Store, title string) model.EntityID {
	t.Helper()
	snap, err := s.Create(t.Context(), []model.Op{model.CreateLog{Nonce: model.NewNonce(), Title: title}})
	if err != nil {
		t.Fatalf("create log: %v", err)
	}
	return snap.(model.Log).ID
}

// createRunbook creates a runbook whose initial steps ride in the create pack,
// returning the folded snapshot so callers can drive runs against its step ids.
func createRunbook(t *testing.T, s *store.Store, title string, steps ...string) model.Runbook {
	t.Helper()
	ops := make([]model.Op, 0, 1+len(steps))
	ops = append(ops, model.CreateRunbook{Nonce: model.NewNonce(), Title: title})
	prev := ""
	for _, text := range steps {
		pos := model.PositionBetween(prev, "")
		ops = append(ops, model.AddStep{ID: model.NewNonce(), Text: text, Position: pos})
		prev = pos
	}
	snap, err := s.Create(t.Context(), ops)
	if err != nil {
		t.Fatalf("create runbook: %v", err)
	}
	return snap.(model.Runbook)
}

func appendOps(t *testing.T, s *store.Store, ref string, ops ...model.Op) {
	t.Helper()
	if _, err := s.Append(t.Context(), ref, ops); err != nil {
		t.Fatalf("append %s: %v", ref, err)
	}
}

// TestEventsTaskLifecycle pins the full task lifecycle in trail order — create,
// claim, branch move, close, commit link — with the branch attribution stepping
// with the task's branch and the move and link carrying their detail.
func TestEventsTaskLifecycle(t *testing.T) {
	r := newGitRepo(t)
	c1 := r.commit("c1")
	s := r.openStore()
	id := createTask(t, s, "ship it", model.Branch("alpha"))
	ref := refs.For(model.KindTask, id)
	appendOps(t, s, ref, model.Claim{Assignee: model.Actor("alice <alice@example.com>")})
	appendOps(t, s, ref, model.SetBranch{Branch: model.Branch("beta")})
	appendOps(t, s, ref, model.SetStatus{Status: model.StatusDone})
	appendOps(t, s, ref, model.LinkCommit{SHA: c1.sha})

	g := buildGraph(t, r)
	got := shapesFor(g, id)
	want := []evShape{
		{typ: evCreated, branch: "alpha"},
		{typ: evClaimed, branch: "alpha"},
		{typ: evBranchMoved, branch: "beta", detail: map[string]string{"from": "alpha", "to": "beta"}},
		{typ: evClosed, branch: "beta"},
		{typ: evCommitLinked, branch: "beta", detail: map[string]string{"sha": string(c1.sha)}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events =\n%+v\nwant\n%+v", got, want)
	}
}

// TestEventsReclaimDistinctFromClaim covers that a claim (status→in_progress
// with an assignee) and a later assignee change while in_progress classify to
// distinct verbs.
func TestEventsReclaimDistinctFromClaim(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	s := r.openStore()
	id := createTask(t, s, "hand off", "")
	ref := refs.For(model.KindTask, id)
	appendOps(t, s, ref, model.Claim{Assignee: model.Actor("alice <alice@example.com>")})
	appendOps(t, s, ref, model.SetAssignee{Assignee: model.Actor("bob <bob@example.com>")})

	g := buildGraph(t, r)
	got := shapesFor(g, id)
	want := []evShape{
		{typ: evCreated},
		{typ: evClaimed},
		{typ: evReclaimed},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events =\n%+v\nwant\n%+v", got, want)
	}
}

// TestEventsNoteAndDoc covers the note/doc freshness lifecycle: a note created,
// verified, then superseded, and a doc created then flagged stale.
func TestEventsNoteAndDoc(t *testing.T) {
	r := newGitRepo(t)
	c1 := r.commit("c1")
	s := r.openStore()

	noteID := createNote(t, s, "durable fact")
	appendOps(t, s, refs.For(model.KindNote, noteID), model.VerifyNote{VerifiedCommit: c1.sha})
	appendOps(t, s, refs.For(model.KindNote, noteID), model.AddSupersededBy{ID: model.EntityID("0123456789abcdef0123456789abcdef01234567")})

	docID := createDoc(t, s, "design doc")
	appendOps(t, s, refs.For(model.KindDoc, docID), model.MarkStale{Reason: "rewritten"})

	g := buildGraph(t, r)
	gotNote := shapesFor(g, noteID)
	wantNote := []evShape{{typ: evCreated}, {typ: evVerified}, {typ: evSuperseded}}
	if !reflect.DeepEqual(gotNote, wantNote) {
		t.Errorf("note events =\n%+v\nwant\n%+v", gotNote, wantNote)
	}
	gotDoc := shapesFor(g, docID)
	wantDoc := []evShape{{typ: evCreated}, {typ: evStale}}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("doc events =\n%+v\nwant\n%+v", gotDoc, wantDoc)
	}
}

// TestEventsLogEntries covers a log whose two appended entries emit one ordered
// "entry" event each, carrying the entry text.
func TestEventsLogEntries(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	s := r.openStore()
	logID := createLog(t, s, "incident timeline")
	ref := refs.For(model.KindLog, logID)
	appendOps(t, s, ref, model.AppendEntry{Text: "first entry"})
	appendOps(t, s, ref, model.AppendEntry{Text: "second entry"})

	g := buildGraph(t, r)
	got := shapesFor(g, logID)
	want := []evShape{
		{typ: evCreated},
		{typ: evEntry, detail: map[string]string{"text": "first entry"}},
		{typ: evEntry, detail: map[string]string{"text": "second entry"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events =\n%+v\nwant\n%+v", got, want)
	}
}

// TestEventsRunbookLifecycle pins a runbook's lifecycle in trail order: create,
// a run started against a task (detail carrying the run and task short ids), a
// mid-run step-result update folding to a single edit, the run finished
// (detail carrying the terminal status), and the runbook archived.
func TestEventsRunbookLifecycle(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	s := r.openStore()

	taskID := createTask(t, s, "deploy task", "")
	rb := createRunbook(t, s, "deploy runbook", "build image", "roll out")
	ref := refs.For(model.KindRunbook, rb.ID)
	runID := model.NewNonce()
	appendOps(t, s, ref, model.StartRun{ID: runID, Task: taskID})
	appendOps(t, s, ref, model.SetRunStepStatus{RunID: runID, StepID: rb.Steps[0].ID, Status: model.StepDone})
	appendOps(t, s, ref, model.FinishRun{ID: runID, Status: model.RunSucceeded})
	appendOps(t, s, ref, model.SetRunbookStatus{Status: model.RunbookArchived})

	g := buildGraph(t, r)
	got := shapesFor(g, rb.ID)
	want := []evShape{
		{typ: evCreated},
		{typ: evRunStarted, detail: map[string]string{"run": runID[:7], "task": string(taskID)[:7]}},
		{typ: evEdited},
		{typ: evRunFinished, detail: map[string]string{"run": runID[:7], "status": string(model.RunSucceeded)}},
		{typ: evStatus},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events =\n%+v\nwant\n%+v", got, want)
	}
}

// TestEventsRunbookRunFinishStatuses covers that each terminal finish status
// surfaces as a run_finished carrying that status, and that a run started
// without a task omits the task detail.
func TestEventsRunbookRunFinishStatuses(t *testing.T) {
	for _, status := range []model.RunStatus{model.RunSucceeded, model.RunFailed, model.RunAbandoned} {
		t.Run(string(status), func(t *testing.T) {
			r := newGitRepo(t)
			r.commit("c1")
			s := r.openStore()
			rb := createRunbook(t, s, "procedure", "only step")
			ref := refs.For(model.KindRunbook, rb.ID)
			runID := model.NewNonce()
			appendOps(t, s, ref, model.StartRun{ID: runID})
			appendOps(t, s, ref, model.FinishRun{ID: runID, Status: status})

			g := buildGraph(t, r)
			got := shapesFor(g, rb.ID)
			want := []evShape{
				{typ: evCreated},
				{typ: evRunStarted, detail: map[string]string{"run": runID[:7]}},
				{typ: evRunFinished, detail: map[string]string{"run": runID[:7], "status": string(status)}},
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("events =\n%+v\nwant\n%+v", got, want)
			}
		})
	}
}

// TestEventsRunbookFinishedRunCorrection covers correcting a step result on an
// already-finished run: the run diffs as a terminal-on-both-sides pair, a
// content correction that folds to a single edit after the real run_finished —
// never a spurious second run_finished.
func TestEventsRunbookFinishedRunCorrection(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	s := r.openStore()
	rb := createRunbook(t, s, "procedure", "only step")
	ref := refs.For(model.KindRunbook, rb.ID)
	runID := model.NewNonce()
	appendOps(t, s, ref, model.StartRun{ID: runID})
	appendOps(t, s, ref, model.FinishRun{ID: runID, Status: model.RunSucceeded})
	appendOps(t, s, ref, model.SetRunStepStatus{RunID: runID, StepID: rb.Steps[0].ID, Status: model.StepDone})

	g := buildGraph(t, r)
	got := shapesFor(g, rb.ID)
	want := []evShape{
		{typ: evCreated},
		{typ: evRunStarted, detail: map[string]string{"run": runID[:7]}},
		{typ: evRunFinished, detail: map[string]string{"run": runID[:7], "status": string(model.RunSucceeded)}},
		{typ: evEdited},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events =\n%+v\nwant\n%+v", got, want)
	}
}

// TestEventsRunbookStatusAndRunInOnePack covers that a status change and run
// activity folded into one op-pack surface as distinct events — the accumulate
// pattern taskEvents uses across orthogonal axes — rather than the status
// swallowing the run finish.
func TestEventsRunbookStatusAndRunInOnePack(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	s := r.openStore()
	rb := createRunbook(t, s, "procedure", "only step")
	ref := refs.For(model.KindRunbook, rb.ID)
	runID := model.NewNonce()
	appendOps(t, s, ref, model.StartRun{ID: runID})
	appendOps(t, s, ref,
		model.SetRunbookStatus{Status: model.RunbookArchived},
		model.FinishRun{ID: runID, Status: model.RunSucceeded})

	g := buildGraph(t, r)
	got := shapesFor(g, rb.ID)
	want := []evShape{
		{typ: evCreated},
		{typ: evRunStarted, detail: map[string]string{"run": runID[:7]}},
		{typ: evStatus},
		{typ: evRunFinished, detail: map[string]string{"run": runID[:7], "status": string(model.RunSucceeded)}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events =\n%+v\nwant\n%+v", got, want)
	}
}

// TestEventsCheckpointSkipped covers that a checkpoint commit — a compaction
// marker — produces no event: a compacted note reports only its create and
// verify.
func TestEventsCheckpointSkipped(t *testing.T) {
	r := newGitRepo(t)
	c1 := r.commit("c1")
	s := r.openStore()
	noteID := createNote(t, s, "compact me")
	ref := refs.For(model.KindNote, noteID)
	appendOps(t, s, ref, model.VerifyNote{VerifiedCommit: c1.sha})
	if _, err := s.Compact(t.Context(), ref); err != nil {
		t.Fatalf("compact: %v", err)
	}

	g := buildGraph(t, r)
	got := shapesFor(g, noteID)
	want := []evShape{{typ: evCreated}, {typ: evVerified}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events =\n%+v\nwant\n%+v", got, want)
	}
}

// TestEventsDeletedBranchLane covers the reconstruction of a deleted-branch
// lane: a task created on a branch with no live ref, then relocated to the live
// trunk, yields an inferred deleted lane merged into the trunk, spanning the
// events that named the dead branch.
func TestEventsDeletedBranchLane(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	s := r.openStore()
	id := createTask(t, s, "stranded", model.Branch("feature/gone"))
	ref := refs.For(model.KindTask, id)
	appendOps(t, s, ref, model.SetBranch{Branch: model.Branch("main")})

	g := buildGraph(t, r)

	got := shapesFor(g, id)
	want := []evShape{
		{typ: evCreated, branch: "feature/gone"},
		{typ: evBranchMoved, branch: "main", detail: map[string]string{"from": "feature/gone", "to": "main"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events =\n%+v\nwant\n%+v", got, want)
	}

	lane := laneByName(t, g, "feature/gone")
	if !lane.Inferred || lane.Status != statusDeleted {
		t.Errorf("lane inferred/status = %t/%q, want true/%q", lane.Inferred, lane.Status, statusDeleted)
	}
	if lane.Fork != nil || lane.Tip != nil {
		t.Errorf("lane fork/tip = %+v/%+v, want nil/nil", lane.Fork, lane.Tip)
	}
	if lane.Merge == nil {
		t.Fatalf("lane merge = nil, want inferred merge into main")
	}
	if lane.Merge.Into != "main" || lane.Merge.Kind != kindInferred {
		t.Errorf("lane merge into/kind = %q/%q, want main/%q", lane.Merge.Into, lane.Merge.Kind, kindInferred)
	}
	createdAt := eventTime(t, g, id, evCreated)
	movedAt := eventTime(t, g, id, evBranchMoved)
	if lane.Start != createdAt {
		t.Errorf("lane start = %d, want first event %d", lane.Start, createdAt)
	}
	if lane.End != movedAt {
		t.Errorf("lane end = %d, want last event %d", lane.End, movedAt)
	}
	if lane.Merge.Time != movedAt {
		t.Errorf("lane merge time = %d, want move event %d", lane.Merge.Time, movedAt)
	}
	if live := laneByName(t, g, "main"); live.Status == statusDeleted {
		t.Errorf("live main lane shadowed by a deleted lane")
	}
}

// TestEventsBranchAttribution covers that events step with the task's branch: a
// create on one branch and every later event on the relocated branch, with the
// move itself carrying the new branch.
func TestEventsBranchAttribution(t *testing.T) {
	r := newGitRepo(t)
	r.commit("c1")
	s := r.openStore()
	id := createTask(t, s, "moves", model.Branch("wip"))
	ref := refs.For(model.KindTask, id)
	appendOps(t, s, ref, model.SetBranch{Branch: model.Branch("main")})
	appendOps(t, s, ref, model.SetStatus{Status: model.StatusInProgress})

	g := buildGraph(t, r)
	got := shapesFor(g, id)
	want := []evShape{
		{typ: evCreated, branch: "wip"},
		{typ: evBranchMoved, branch: "main", detail: map[string]string{"from": "wip", "to": "main"}},
		{typ: evStatus, branch: "main"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events =\n%+v\nwant\n%+v", got, want)
	}
}

// TestEntitiesSummary covers the legend: a task and a note fold into per-kind
// summaries carrying the lifecycle fields each surfaces.
func TestEntitiesSummary(t *testing.T) {
	r := newGitRepo(t)
	c1 := r.commit("c1")
	s := r.openStore()

	taskID := createTask(t, s, "summary task", model.Branch("feat"))
	appendOps(t, s, refs.For(model.KindTask, taskID), model.Claim{Assignee: model.Actor("alice <alice@example.com>")})
	appendOps(t, s, refs.For(model.KindTask, taskID), model.SetStatus{Status: model.StatusDone})

	noteID := createNote(t, s, "summary note")
	appendOps(t, s, refs.For(model.KindNote, noteID), model.VerifyNote{VerifiedCommit: c1.sha})

	g := buildGraph(t, r)

	ts := summaryByID(t, g, taskID)
	if ts.Kind != entityTask || ts.Title != "summary task" {
		t.Errorf("task summary kind/title = %q/%q", ts.Kind, ts.Title)
	}
	if ts.Status != string(model.StatusDone) || ts.Branch != "feat" || ts.Assignee != "alice <alice@example.com>" {
		t.Errorf("task summary status/branch/assignee = %q/%q/%q", ts.Status, ts.Branch, ts.Assignee)
	}
	if ts.ClosedAt == 0 || ts.StartedAt == 0 {
		t.Errorf("task summary started/closed = %d/%d, want both nonzero", ts.StartedAt, ts.ClosedAt)
	}

	ns := summaryByID(t, g, noteID)
	if ns.Kind != entityNote || ns.Title != "summary note" {
		t.Errorf("note summary kind/title = %q/%q", ns.Kind, ns.Title)
	}
	if ns.VerifiedAt == 0 || ns.Stale || ns.Superseded {
		t.Errorf("note summary verified/stale/superseded = %d/%t/%t", ns.VerifiedAt, ns.Stale, ns.Superseded)
	}
}
