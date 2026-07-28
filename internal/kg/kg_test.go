package kg_test

import (
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yasyf/cc-notes/internal/ccnhome"
	"github.com/yasyf/cc-notes/internal/gittest"
	"github.com/yasyf/cc-notes/internal/kg"
	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

type fixture struct {
	store *store.Store
	dir   string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Setenv(ccnhome.Env, t.TempDir())
	dir := gittest.InitRepo(t)
	s, err := store.Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return &fixture{store: s, dir: dir}
}

// commit writes path with content and commits it, returning the commit sha.
func (f *fixture) commit(t *testing.T, message string, paths ...string) model.SHA {
	t.Helper()
	for _, p := range paths {
		full := filepath.Join(f.dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
			t.Fatalf("mkdir %s: %v", p, err)
		}
		if err := os.WriteFile(full, []byte(message+"\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	gittest.Git(t, f.dir, "add", "-A")
	gittest.Git(t, f.dir, "commit", "-q", "-m", message)
	return model.SHA(gittest.Git(t, f.dir, "rev-parse", "HEAD"))
}

func (f *fixture) create(t *testing.T, ops ...model.Op) model.Snapshot {
	t.Helper()
	snap, err := f.store.CreateExact(t.Context(), ops)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return snap
}

func (f *fixture) append(t *testing.T, kind model.Kind, id model.EntityID, ops ...model.Op) {
	t.Helper()
	if _, err := f.store.Append(t.Context(), refs.For(kind, id), ops); err != nil {
		t.Fatalf("append %s: %v", id, err)
	}
}

func (f *fixture) build(t *testing.T) *kg.Graph {
	t.Helper()
	g, err := kg.Build(t.Context(), f.store)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return g
}

func pathAnchor(p string) model.Anchor {
	return model.Anchor{Kind: model.AnchorPath, Value: p}
}

func findEdge(g *kg.Graph, from, to kg.NodeID, kind kg.EdgeKind) (kg.Edge, bool) {
	for _, e := range g.Edges {
		if e.From == from && e.To == to && e.Kind == kind {
			return e, true
		}
	}
	return kg.Edge{}, false
}

func requireEdge(t *testing.T, g *kg.Graph, from, to kg.NodeID, kind kg.EdgeKind) kg.Edge {
	t.Helper()
	e, ok := findEdge(g, from, to, kind)
	if !ok {
		t.Fatalf("missing %s edge %s -> %s", kind, from, to)
	}
	return e
}

// requireCooccur returns the single co-occurrence edge between two notes,
// which the builder emits once with the lower node id first.
func requireCooccur(t *testing.T, g *kg.Graph, a, z model.EntityID) kg.Edge {
	t.Helper()
	nodes := []kg.NodeID{kg.EntityNode(model.KindNote, a), kg.EntityNode(model.KindNote, z)}
	slices.Sort(nodes)
	return requireEdge(t, g, nodes[0], nodes[1], kg.EdgeCooccur)
}

func requireNode(t *testing.T, g *kg.Graph, id kg.NodeID) kg.Node {
	t.Helper()
	i := slices.IndexFunc(g.Nodes, func(n kg.Node) bool { return n.ID == id })
	if i < 0 {
		t.Fatalf("missing node %s", id)
	}
	return g.Nodes[i]
}

func TestBuildDeclaredEdges(t *testing.T) {
	f := newFixture(t)
	sha := f.commit(t, "base", "internal/kg/build.go")

	project := f.create(t, model.CreateProject{Nonce: model.NewNonce(), Title: "indexing"}).EntityID()
	sprint := f.create(t, model.CreateSprint{Nonce: model.NewNonce(), Title: "week 1", Project: project}).EntityID()
	epic := f.create(t, model.CreateTask{Nonce: model.NewNonce(), Title: "epic", Type: model.TypeEpic, Branch: "main"}).EntityID()
	blocker := f.create(t, model.CreateTask{Nonce: model.NewNonce(), Title: "blocker", Type: model.TypeTask, Branch: "main"}).EntityID()
	task := f.create(t, model.CreateTask{Nonce: model.NewNonce(), Title: "task", Type: model.TypeTask, Branch: "feat/graph", Parent: epic}).EntityID()
	f.append(t, model.KindTask, task,
		model.AddDep{ID: blocker},
		model.SetSprint{Sprint: sprint},
		model.SetProject{Project: project},
		model.LinkCommit{SHA: sha},
	)
	investigation := f.create(t, model.CreateInvestigation{
		Nonce: model.NewNonce(), Title: "slow index", Premise: "the index rebuilds too often",
		Anchors: []model.Anchor{pathAnchor("internal/kg/build.go")},
	}).EntityID()
	f.append(t, model.KindInvestigation, investigation,
		model.AddFollowUp{ID: task},
		model.AddFixCommit{SHA: sha},
	)

	g := f.build(t)
	taskNode := kg.EntityNode(model.KindTask, task)
	investigationNode := kg.EntityNode(model.KindInvestigation, investigation)

	for _, tc := range []struct {
		from kg.NodeID
		to   kg.NodeID
		kind kg.EdgeKind
	}{
		{taskNode, kg.EntityNode(model.KindTask, blocker), kg.EdgeDep},
		{taskNode, kg.EntityNode(model.KindTask, epic), kg.EdgeParent},
		{taskNode, kg.EntityNode(model.KindSprint, sprint), kg.EdgeSprint},
		{taskNode, kg.EntityNode(model.KindProject, project), kg.EdgeProject},
		{taskNode, kg.CommitNode(sha), kg.EdgeCommit},
		{taskNode, kg.BranchNode("feat/graph"), kg.EdgeBranch},
		{kg.EntityNode(model.KindSprint, sprint), kg.EntityNode(model.KindProject, project), kg.EdgeProject},
		{investigationNode, taskNode, kg.EdgeFollowUp},
		{investigationNode, kg.CommitNode(sha), kg.EdgeFixCommit},
		{investigationNode, kg.PathNode("internal/kg/build.go"), kg.EdgeAnchor},
	} {
		if e := requireEdge(t, g, tc.from, tc.to, tc.kind); e.Weight != 1 {
			t.Errorf("%s edge weight = %v, want 1", tc.kind, e.Weight)
		}
	}

	node := requireNode(t, g, taskNode)
	if node.Kind != kg.NodeTask || node.Title != "task" || node.Value != string(task) {
		t.Fatalf("task node = %+v", node)
	}
}

func TestBuildAnchorCarriesWitnessOID(t *testing.T) {
	f := newFixture(t)
	f.commit(t, "base", "internal/kg/build.go")
	blob := gittest.Git(t, f.dir, "rev-parse", "HEAD:internal/kg/build.go")

	note := f.create(t, model.CreateNote{
		Nonce: model.NewNonce(), Title: "anchored", Body: "body",
		Anchors: []model.Anchor{pathAnchor("internal/kg/build.go")},
	}).EntityID()
	f.append(t, model.KindNote, note, model.VerifyNote{
		VerifiedCommit: model.SHA(gittest.Git(t, f.dir, "rev-parse", "HEAD")),
		Witness:        []model.AnchorWitness{{Anchor: pathAnchor("internal/kg/build.go"), OID: model.SHA(blob)}},
	})

	g := f.build(t)
	e := requireEdge(t, g, kg.EntityNode(model.KindNote, note), kg.PathNode("internal/kg/build.go"), kg.EdgeAnchor)
	if e.OID != model.SHA(blob) {
		t.Fatalf("witness oid = %q, want %q", e.OID, blob)
	}
	if e.Derived {
		t.Fatal("a written anchor is marked derived")
	}
}

func TestBuildDerivesTaskPathAnchorsFromCommits(t *testing.T) {
	f := newFixture(t)
	sha := f.commit(t, "touch two files", "internal/kg/build.go", "internal/kg/index.go")
	f.commit(t, "unlinked", "internal/other.go")

	task := f.create(t, model.CreateTask{Nonce: model.NewNonce(), Title: "wire the graph", Type: model.TypeTask, Branch: "main"}).EntityID()
	f.append(t, model.KindTask, task, model.LinkCommit{SHA: sha})

	g := f.build(t)
	taskNode := kg.EntityNode(model.KindTask, task)
	for _, p := range []string{"internal/kg/build.go", "internal/kg/index.go"} {
		e := requireEdge(t, g, taskNode, kg.PathNode(p), kg.EdgeAnchor)
		if !e.Derived {
			t.Errorf("derived anchor for %s is not marked derived", p)
		}
	}
	if _, ok := findEdge(g, taskNode, kg.PathNode("internal/other.go"), kg.EdgeAnchor); ok {
		t.Fatal("a path from an unlinked commit became an anchor")
	}
}

func TestBuildContainmentChain(t *testing.T) {
	f := newFixture(t)
	f.commit(t, "base", "internal/kg/build.go")
	f.create(t, model.CreateNote{
		Nonce: model.NewNonce(), Title: "anchored", Body: "body",
		Anchors: []model.Anchor{pathAnchor("internal/kg/build.go")},
	})

	g := f.build(t)
	for _, tc := range [][2]kg.NodeID{
		{kg.PathNode("internal/kg/build.go"), kg.DirNode("internal/kg")},
		{kg.DirNode("internal/kg"), kg.DirNode("internal")},
		{kg.DirNode("internal"), kg.DirNode(".")},
	} {
		requireEdge(t, g, tc[0], tc[1], kg.EdgeWithin)
	}
	if _, ok := findEdge(g, kg.DirNode("."), kg.DirNode("."), kg.EdgeWithin); ok {
		t.Fatal("the repository root contains itself")
	}
}

func TestBuildCooccurrenceOverSharedAnchor(t *testing.T) {
	f := newFixture(t)
	f.commit(t, "base", "internal/kg/build.go")
	anchors := []model.Anchor{pathAnchor("internal/kg/build.go")}
	first := f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "first", Body: "a", Anchors: anchors}).EntityID()
	second := f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "second", Body: "b", Anchors: anchors}).EntityID()

	g := f.build(t)
	nodes := []kg.NodeID{kg.EntityNode(model.KindNote, first), kg.EntityNode(model.KindNote, second)}
	slices.Sort(nodes)
	e := requireEdge(t, g, nodes[0], nodes[1], kg.EdgeCooccur)
	if want := anchorPrior / math.Log2(3); math.Abs(e.Weight-want) > 1e-9 {
		t.Fatalf("cooccur weight = %v, want %v", e.Weight, want)
	}
	if _, ok := findEdge(g, nodes[1], nodes[0], kg.EdgeCooccur); ok {
		t.Fatal("cooccurrence emitted both directions; the reverse index covers the other end")
	}
}

// The hub priors the co-occurrence weight scales by, mirrored from build.go's
// hubPrior so an external test can name what it is asserting.
const (
	anchorPrior  = 0.02
	sessionPrior = 0.48
)

// TestBuildSessionOutweighsAnchor pins the phase's central finding: the
// per-op session is the strongest free signal in the corpus, so two entities
// sharing a session must outrank two sharing only a file anchor.
func TestBuildSessionOutweighsAnchor(t *testing.T) {
	const session = "5f1c0f2e-8a4b-4d3c-9e1f-6b7a8c9d0e1f"
	f := newFixture(t)
	f.commit(t, "base", "internal/kg/build.go")
	anchors := []model.Anchor{pathAnchor("internal/kg/build.go")}
	anchoredA := f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "anchored a", Body: "x", Anchors: anchors}).EntityID()
	anchoredB := f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "anchored b", Body: "y", Anchors: anchors}).EntityID()

	t.Setenv("CC_NOTES_SESSION_ID", session)
	sessionedA := f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "sessioned a", Body: "p"}).EntityID()
	sessionedB := f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "sessioned b", Body: "q"}).EntityID()

	g := f.build(t)
	anchored := requireCooccur(t, g, anchoredA, anchoredB)
	sessioned := requireCooccur(t, g, sessionedA, sessionedB)
	if sessioned.Weight <= anchored.Weight {
		t.Fatalf("session pair weight %v does not outrank anchor pair weight %v", sessioned.Weight, anchored.Weight)
	}
	if want := sessionPrior / math.Log2(3); math.Abs(sessioned.Weight-want) > 1e-9 {
		t.Fatalf("session cooccur weight = %v, want %v", sessioned.Weight, want)
	}
}

func TestBuildSessionNodes(t *testing.T) {
	const session = "0b5c9b3a-7e2f-4c1d-9a8b-2f3e4d5c6b7a"
	f := newFixture(t)
	t.Setenv("CC_NOTES_SESSION_ID", session)
	first := f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "first", Body: "a"}).EntityID()
	second := f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "second", Body: "b"}).EntityID()

	g := f.build(t)
	if node := requireNode(t, g, kg.SessionNode(session)); node.Kind != kg.NodeSession || node.Value != session {
		t.Fatalf("session node = %+v", node)
	}
	firstNode, secondNode := kg.EntityNode(model.KindNote, first), kg.EntityNode(model.KindNote, second)
	requireEdge(t, g, firstNode, kg.SessionNode(session), kg.EdgeSession)
	requireEdge(t, g, secondNode, kg.SessionNode(session), kg.EdgeSession)

	nodes := []kg.NodeID{firstNode, secondNode}
	slices.Sort(nodes)
	requireEdge(t, g, nodes[0], nodes[1], kg.EdgeCooccur)
}

func TestBuildSupersedeAndTombstone(t *testing.T) {
	f := newFixture(t)
	replacement := f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "current", Body: "new"}).EntityID()
	superseded := f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "old", Body: "old"}).EntityID()
	f.append(t, model.KindNote, superseded, model.AddSupersededBy{ID: replacement})
	tombstoned := f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "gone", Body: "gone"}).EntityID()
	f.append(t, model.KindNote, tombstoned, model.DeleteNote{})

	g := f.build(t)
	requireEdge(t, g, kg.EntityNode(model.KindNote, superseded), kg.EntityNode(model.KindNote, replacement), kg.EdgeSupersede)
	if slices.ContainsFunc(g.Nodes, func(n kg.Node) bool { return n.ID == kg.EntityNode(model.KindNote, tombstoned) }) {
		t.Fatal("a tombstoned note is in the graph")
	}
}

func TestSourceDigestTracksRefTips(t *testing.T) {
	f := newFixture(t)
	empty, err := kg.SourceDigest(t.Context(), f.store)
	if err != nil {
		t.Fatalf("SourceDigest: %v", err)
	}
	note := f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "one", Body: "a"}).EntityID()
	created, err := kg.SourceDigest(t.Context(), f.store)
	if err != nil {
		t.Fatalf("SourceDigest: %v", err)
	}
	if created == empty {
		t.Fatal("creating a note left the source digest unchanged")
	}
	f.append(t, model.KindNote, note, model.SetBody{Body: "b"})
	edited, err := kg.SourceDigest(t.Context(), f.store)
	if err != nil {
		t.Fatalf("SourceDigest: %v", err)
	}
	if edited == created {
		t.Fatal("editing a note left the source digest unchanged")
	}
	if g := f.build(t); g.Source != edited {
		t.Fatalf("Build source = %s, want %s", g.Source, edited)
	}
}

// TestSourceDigestTracksLinkedCommitsTheODBGains pins the input the ref tips
// cannot express: a task links a commit this clone has not fetched, so Build
// derives no path anchors from it. Fetching the commit moves no entity tip, so
// a tips-only digest would keep reporting a hit on a graph that is permanently
// short those anchors.
func TestSourceDigestTracksLinkedCommitsTheODBGains(t *testing.T) {
	f := newFixture(t)
	f.commit(t, "local", "internal/kg/build.go")

	upstream := gittest.InitRepo(t)
	if err := os.MkdirAll(filepath.Join(upstream, "internal"), 0o750); err != nil {
		t.Fatalf("mkdir upstream internal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(upstream, "internal/parser.go"), []byte("package parser\n"), 0o600); err != nil {
		t.Fatalf("write upstream file: %v", err)
	}
	gittest.Git(t, upstream, "add", "-A")
	gittest.Git(t, upstream, "commit", "-q", "-m", "unfetched")
	unfetched := model.SHA(gittest.Git(t, upstream, "rev-parse", "HEAD"))

	task := f.create(t, model.CreateTask{Nonce: model.NewNonce(), Title: "task", Type: model.TypeTask, Branch: "main"}).EntityID()
	f.append(t, model.KindTask, task, model.LinkCommit{SHA: unfetched})

	absent, err := kg.SourceDigest(t.Context(), f.store)
	if err != nil {
		t.Fatalf("SourceDigest: %v", err)
	}
	repo, err := ccnhome.ForRepo(f.store.CommonDir())
	if err != nil {
		t.Fatalf("ForRepo: %v", err)
	}
	built := f.build(t)
	if built.Source != absent {
		t.Fatalf("Build source = %s, want %s", built.Source, absent)
	}
	if _, found := findEdge(built, kg.EntityNode(model.KindTask, task), kg.PathNode("internal/parser.go"), kg.EdgeAnchor); found {
		t.Fatal("a commit outside the object database contributed a derived anchor")
	}
	kg.Save(repo.Graph(), built)

	gittest.Git(t, f.dir, "fetch", "-q", upstream, "main")
	fetched, err := kg.SourceDigest(t.Context(), f.store)
	if err != nil {
		t.Fatalf("SourceDigest: %v", err)
	}
	if fetched == absent {
		t.Fatalf("fetching the linked commit left the source digest at %s", absent)
	}
	if index, ok := kg.Load(repo.Graph(), fetched); ok {
		_ = index.Close()
		t.Fatal("Load hit a graph built before the linked commit reached the object database")
	}
	if _, found := findEdge(f.build(t), kg.EntityNode(model.KindTask, task), kg.PathNode("internal/parser.go"), kg.EdgeAnchor); !found {
		t.Fatal("the rebuilt graph carries no derived anchor from the now-local commit")
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	f := newFixture(t)
	sha := f.commit(t, "base", "internal/kg/build.go")
	note := f.create(t, model.CreateNote{
		Nonce: model.NewNonce(), Title: "anchored", Body: "body",
		Anchors: []model.Anchor{pathAnchor("internal/kg/build.go")},
	}).EntityID()
	task := f.create(t, model.CreateTask{Nonce: model.NewNonce(), Title: "task", Type: model.TypeTask, Branch: "main"}).EntityID()
	f.append(t, model.KindTask, task, model.LinkCommit{SHA: sha})

	g := f.build(t)
	repo, err := ccnhome.ForRepo(f.store.CommonDir())
	if err != nil {
		t.Fatalf("ForRepo: %v", err)
	}
	kg.Save(repo.Graph(), g)

	index, ok := kg.Load(repo.Graph(), g.Source)
	if !ok {
		t.Fatal("Load missed a graph just saved")
	}
	t.Cleanup(func() { _ = index.Close() })

	if index.BuiltAt() != g.BuiltAt {
		t.Errorf("BuiltAt = %d, want %d", index.BuiltAt(), g.BuiltAt)
	}
	nodes, edges, events := index.Counts()
	if nodes != len(g.Nodes) || edges != len(g.Edges) || events != len(g.Events) {
		t.Errorf("Counts = (%d, %d, %d), want (%d, %d, %d)",
			nodes, edges, events, len(g.Nodes), len(g.Edges), len(g.Events))
	}
	if replayed := index.Events(0, 0); len(replayed) != len(g.Events) {
		t.Errorf("Events(0, 0) returned %d events, want %d", len(replayed), len(g.Events))
	}

	noteNode, pathNode := kg.EntityNode(model.KindNote, note), kg.PathNode("internal/kg/build.go")
	stored, found := index.Node(noteNode)
	if !found || stored.Title != "anchored" {
		t.Fatalf("Node(%s) = %+v, %v", noteNode, stored, found)
	}
	if _, found := index.Node(kg.PathNode("internal/absent.go")); found {
		t.Fatal("Node returned a node the graph never held")
	}

	out := index.Out(noteNode)
	if !slices.ContainsFunc(out, func(e kg.Edge) bool { return e.To == pathNode && e.Kind == kg.EdgeAnchor }) {
		t.Fatalf("Out(%s) = %+v, want the path anchor", noteNode, out)
	}
	in := index.In(pathNode)
	if !slices.ContainsFunc(in, func(e kg.Edge) bool { return e.From == noteNode && e.Kind == kg.EdgeAnchor }) {
		t.Fatalf("In(%s) = %+v, want the note", pathNode, in)
	}
	if !slices.ContainsFunc(in, func(e kg.Edge) bool { return e.From == kg.EntityNode(model.KindTask, task) && e.Derived }) {
		t.Fatalf("In(%s) = %+v, want the derived task anchor", pathNode, in)
	}
	for _, e := range index.Out(pathNode) {
		if e.Kind == kg.EdgeWithin && e.To != kg.DirNode("internal/kg") {
			t.Fatalf("path within %s, want internal/kg", e.To)
		}
	}
}

func TestLoadMissesOnAbsentGraph(t *testing.T) {
	f := newFixture(t)
	repo, err := ccnhome.ForRepo(f.store.CommonDir())
	if err != nil {
		t.Fatalf("ForRepo: %v", err)
	}
	if _, ok := kg.Load(repo.Graph(), "any"); ok {
		t.Fatal("Load hit on a directory with no graph")
	}
	if err := os.MkdirAll(repo.Graph(), 0o750); err != nil {
		t.Fatalf("mkdir graph dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repo.Graph(), "graph.db"), []byte("not a database"), 0o600); err != nil {
		t.Fatalf("write corrupt graph: %v", err)
	}
	if _, ok := kg.Load(repo.Graph(), "any"); ok {
		t.Fatal("Load hit on a corrupt graph")
	}
}

func TestLoadMissesOnAnotherSource(t *testing.T) {
	f := newFixture(t)
	note := f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "one", Body: "a"}).EntityID()
	repo, err := ccnhome.ForRepo(f.store.CommonDir())
	if err != nil {
		t.Fatalf("ForRepo: %v", err)
	}
	kg.Save(repo.Graph(), f.build(t))

	f.append(t, model.KindNote, note, model.SetBody{Body: "b"})
	source, err := kg.SourceDigest(t.Context(), f.store)
	if err != nil {
		t.Fatalf("SourceDigest: %v", err)
	}
	if index, ok := kg.Load(repo.Graph(), source); ok {
		_ = index.Close()
		t.Fatal("Load hit a graph built from older ref tips")
	}

	rebuilt := f.build(t)
	kg.Save(repo.Graph(), rebuilt)
	index, ok := kg.Load(repo.Graph(), source)
	if !ok {
		t.Fatal("Load missed the rebuilt graph")
	}
	t.Cleanup(func() { _ = index.Close() })
	if stored, found := index.Node(kg.EntityNode(model.KindNote, note)); !found || stored.Title != "one" {
		t.Fatalf("Node = %+v, %v", stored, found)
	}
}

func TestIndexGraphRoundTripsTheBuiltGraph(t *testing.T) {
	f := newFixture(t)
	sha := f.commit(t, "base", "internal/kg/build.go", "internal/kg/index.go")
	anchors := []model.Anchor{pathAnchor("internal/kg/build.go"), {Kind: model.AnchorDir, Value: "internal"}}
	f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "first", Body: "a", Anchors: anchors})
	f.create(t, model.CreateDoc{Nonce: model.NewNonce(), Title: "second", Body: "b", When: "always", Anchors: anchors})
	task := f.create(t, model.CreateTask{Nonce: model.NewNonce(), Title: "task", Type: model.TypeTask, Branch: "main"}).EntityID()
	f.append(t, model.KindTask, task, model.LinkCommit{SHA: sha})

	built := f.build(t)
	repo, err := ccnhome.ForRepo(f.store.CommonDir())
	if err != nil {
		t.Fatalf("ForRepo: %v", err)
	}
	kg.Save(repo.Graph(), built)
	index, ok := kg.Load(repo.Graph(), built.Source)
	if !ok {
		t.Fatal("Load missed the graph it just saved")
	}
	t.Cleanup(func() { _ = index.Close() })

	read, ok := index.Graph()
	if !ok {
		t.Fatal("Graph did not decode the stored graph")
	}
	if read.Source != built.Source || read.BuiltAt != built.BuiltAt {
		t.Fatalf("Graph provenance = %s/%d, want %s/%d", read.Source, read.BuiltAt, built.Source, built.BuiltAt)
	}
	if len(built.Nodes) == 0 || len(built.Edges) == 0 || len(built.Events) == 0 {
		t.Fatalf("fixture built %d nodes / %d edges / %d events; the round trip proves nothing",
			len(built.Nodes), len(built.Edges), len(built.Events))
	}
	if !slices.Equal(read.Nodes, built.Nodes) {
		t.Fatalf("read back %d nodes, want the %d built", len(read.Nodes), len(built.Nodes))
	}
	if !slices.Equal(read.Edges, built.Edges) {
		t.Fatalf("read back %d edges in a different order or shape than the %d built", len(read.Edges), len(built.Edges))
	}
	if !slices.Equal(read.Events, built.Events) {
		t.Fatalf("read back %d events, want the %d built", len(read.Events), len(built.Events))
	}
	nodes, edges, events := index.Counts()
	if nodes != len(read.Nodes) || edges != len(read.Edges) || events != len(read.Events) {
		t.Fatalf("Counts = %d/%d/%d, Graph read %d/%d/%d", nodes, edges, events, len(read.Nodes), len(read.Edges), len(read.Events))
	}
}

func TestGraphIsDeterministic(t *testing.T) {
	f := newFixture(t)
	sha := f.commit(t, "base", "internal/kg/build.go", "internal/kg/index.go")
	anchors := []model.Anchor{pathAnchor("internal/kg/build.go"), {Kind: model.AnchorDir, Value: "internal"}}
	f.create(t, model.CreateNote{Nonce: model.NewNonce(), Title: "first", Body: "a", Anchors: anchors})
	f.create(t, model.CreateDoc{Nonce: model.NewNonce(), Title: "second", Body: "b", When: "always", Anchors: anchors})
	task := f.create(t, model.CreateTask{Nonce: model.NewNonce(), Title: "task", Type: model.TypeTask, Branch: "main"}).EntityID()
	f.append(t, model.KindTask, task, model.LinkCommit{SHA: sha})

	first, second := f.build(t), f.build(t)
	if !slices.Equal(first.Edges, second.Edges) {
		t.Fatal("two builds of one repository produced different edges")
	}
	if !slices.Equal(first.Nodes, second.Nodes) {
		t.Fatal("two builds of one repository produced different nodes")
	}
	if !slices.IsSortedFunc(first.Nodes, func(a, b kg.Node) int { return strings.Compare(string(a.ID), string(b.ID)) }) {
		t.Fatal("nodes are not sorted by id")
	}
}
