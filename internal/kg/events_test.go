package kg_test

import (
	"slices"
	"testing"

	"github.com/yasyf/cc-notes/internal/kg"
	"github.com/yasyf/cc-notes/internal/lifecycle"
	"github.com/yasyf/cc-notes/model"
)

func eventTypes(g *kg.Graph, entity kg.NodeID) []string {
	var types []string
	for _, ev := range g.Events {
		if ev.Entity == entity {
			types = append(types, ev.Type)
		}
	}
	return types
}

// TestBuildIngestsLifecycleEvents pins the temporal ingest: the graph replays
// each chain through the same trail and verb vocabulary the visualization
// reads, so a claim, a close, and a commit link are named events, not
// undifferentiated edits.
func TestBuildIngestsLifecycleEvents(t *testing.T) {
	const session = "9a1b2c3d-4e5f-4a6b-8c7d-1e2f3a4b5c6d"
	f := newFixture(t)
	t.Setenv("CC_NOTES_SESSION_ID", session)
	sha := f.commit(t, "base", "internal/kg/build.go")
	task := f.create(t, model.CreateTask{
		Nonce: model.NewNonce(), Title: "task", Type: model.TypeTask, Branch: "main",
	}).EntityID()
	f.append(t, model.KindTask, task, model.Claim{Assignee: model.Actor("alice <alice@example.com>")})
	f.append(t, model.KindTask, task, model.LinkCommit{SHA: sha})
	f.append(t, model.KindTask, task, model.SetStatus{Status: model.StatusDone})

	g := f.build(t)
	node := kg.EntityNode(model.KindTask, task)
	want := []string{
		lifecycle.TypeCreated, lifecycle.TypeClaimed,
		lifecycle.TypeCommitLinked, lifecycle.TypeClosed,
	}
	if got := eventTypes(g, node); !slices.Equal(got, want) {
		t.Fatalf("event types = %v, want %v", got, want)
	}
	for _, ev := range g.Events {
		if ev.Session != session {
			t.Errorf("event %s carries session %q, want %q", ev.Type, ev.Session, session)
		}
		if ev.At == 0 || ev.SHA == "" || ev.Actor == "" {
			t.Errorf("event %s is missing its time, sha, or actor: %+v", ev.Type, ev)
		}
	}
}

func TestBuildEventsAreTimeOrdered(t *testing.T) {
	f := newFixture(t)
	first := f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "first", Body: "a"}).EntityID()
	f.append(t, model.KindNote, first, model.SetBody{Body: "b"})
	f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "second", Body: "c"})

	g := f.build(t)
	if len(g.Events) < 3 {
		t.Fatalf("built %d events, want at least 3", len(g.Events))
	}
	if !slices.IsSortedFunc(g.Events, func(a, z kg.Event) int { return int(a.At - z.At) }) {
		t.Fatal("events are not in time order")
	}
}

// TestBuildSessionEdgeWeightsCountEvents pins the session edge as first class:
// its weight is how much of the entity that session actually wrote, which is
// what makes a forty-op session outrank a drive-by touch.
func TestBuildSessionEdgeWeightsCountEvents(t *testing.T) {
	const busy = "11111111-2222-4333-8444-555555555555"
	const brief = "66666666-7777-4888-8999-aaaaaaaaaaaa"
	f := newFixture(t)

	t.Setenv("CC_NOTES_SESSION_ID", busy)
	note := f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "note", Body: "a"}).EntityID()
	f.append(t, model.KindNote, note, model.SetBody{Body: "b"})
	f.append(t, model.KindNote, note, model.SetBody{Body: "c"})

	t.Setenv("CC_NOTES_SESSION_ID", brief)
	f.append(t, model.KindNote, note, model.SetBody{Body: "d"})

	g := f.build(t)
	self := kg.EntityNode(model.KindNote, note)
	busyEdge := requireEdge(t, g, self, kg.SessionNode(busy), kg.EdgeSession)
	briefEdge := requireEdge(t, g, self, kg.SessionNode(brief), kg.EdgeSession)
	if busyEdge.Weight != 3 || briefEdge.Weight != 1 {
		t.Fatalf("session weights = %v (busy) and %v (brief), want 3 and 1", busyEdge.Weight, briefEdge.Weight)
	}
	if node := requireNode(t, g, kg.SessionNode(busy)); node.UpdatedAt == 0 {
		t.Error("session node carries no last-event time")
	}
}

// TestBuildCochangeOverRealHistory drives the co-change scan against a real
// git history rather than synthetic numstat text, so the flags, the NUL record
// framing, and the churn accounting are all checked against what git actually
// prints.
func TestBuildCochangeOverRealHistory(t *testing.T) {
	f := newFixture(t)
	const impl, test, solo = "internal/kg/build.go", "internal/kg/build_test.go", "internal/kg/lonely.go"
	f.commit(t, "first", impl, test)
	f.commit(t, "second", impl, test)
	f.commit(t, "third", solo)
	for _, p := range []string{impl, test, solo} {
		f.create(t, model.CreateNote{
			Nonce: model.NewNonce(), Title: "anchors " + p, Body: "body",
			Anchors: []model.Anchor{pathAnchor(p)},
		})
	}

	g := f.build(t)
	e := requireEdge(t, g, kg.PathNode(impl), kg.PathNode(test), kg.EdgeCochange)
	if !e.Advisory || !e.Derived || e.Weight != 1 {
		t.Fatalf("cochange edge = %+v, want advisory, derived, weight 1", e)
	}
	if node := requireNode(t, g, kg.PathNode(impl)); node.Revisions != 2 || node.Churn == 0 {
		t.Errorf("%s node = %+v, want 2 revisions and non-zero churn", impl, node)
	}
	if _, ok := findEdge(g, kg.PathNode(impl), kg.PathNode(solo), kg.EdgeCochange); ok {
		t.Error("coupled two paths that never changed together")
	}
}

func TestBuildTagAndConceptEdges(t *testing.T) {
	f := newFixture(t)
	shared := "the SourceDigest call in `internal/kg/build.go`"
	f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "first", Body: shared, Tags: []string{"kg"}})
	second := f.create(t, model.CreateNote{
		Nonce: model.NewNonce(), Title: "second", Body: shared + " again", Tags: []string{"kg"},
	}).EntityID()

	g := f.build(t)
	self := kg.EntityNode(model.KindNote, second)
	if node := requireNode(t, g, kg.TagNode("kg")); node.Kind != kg.NodeTag {
		t.Fatalf("tag node = %+v", node)
	}
	requireEdge(t, g, self, kg.TagNode("kg"), kg.EdgeTag)
	requireEdge(t, g, self, kg.ConceptNode("sourcedigest"), kg.EdgeConcept)

	// A term only these two mention is discriminating; a term one mentions is not.
	if _, ok := findEdge(g, self, kg.ConceptNode("again"), kg.EdgeConcept); ok {
		t.Error("kept a singleton concept, which can create no edge")
	}
}
