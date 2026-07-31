package kg

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"math"
	"path"
	"slices"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/yasyf/cc-notes/internal/refs"
	"github.com/yasyf/cc-notes/internal/store"
	"github.com/yasyf/cc-notes/model"
)

const (
	// loadConcurrency bounds the entity chains a build reads at once; it
	// matches internal/store's own list fan-out.
	loadConcurrency = 8
	// cooccurMaxDegree bounds how many entities one shared node may relate. A
	// path, branch, or session touched by more than this discriminates
	// nothing and would cost a quadratic number of edges.
	cooccurMaxDegree = 64
	// derivedPathLimit bounds how many paths one linked commit may contribute
	// as derived anchors. A commit that rewrites more files than this anchors
	// a task to nothing in particular, so it contributes none.
	derivedPathLimit = 64
	// sourceDomain separates the source digest's preimage and versions the
	// whole derived artifact: bumping it invalidates every stored graph, which
	// is what a change to the build's shape or the on-disk format needs.
	sourceDomain = "cc-notes.kg.v1\x00"
	// commitDomain separates the source digest's second section from its first.
	commitDomain = "\x00commits\x00"
)

// SourceDigest returns the digest of everything a build reads: the
// repository's current entity ref tips, and which of the commits those tips
// link the local object database actually holds. A stored graph whose Source
// matches it was folded from exactly these inputs, so it is current by
// construction rather than by heuristic.
//
// The second section is what the tips alone cannot say. A commit is immutable,
// so its content needs no digest — but whether this clone holds it is not, and
// Build silently derives no path anchors from one it does not. A fetch that
// lands a linked commit moves no entity tip, so without this the graph would
// stay short those anchors and every read would report a hit.
func SourceDigest(ctx context.Context, s *store.Store) (string, error) {
	tips, err := s.Repo.ListPrefix(ctx, refs.Namespace)
	if err != nil {
		return "", fmt.Errorf("list entity refs: %w", err)
	}
	tasks, err := loadRecords(ctx, s, taskTips(tips))
	if err != nil {
		return "", err
	}
	held, err := s.Git.HeldObjects(ctx, taskCommits(tasks))
	if err != nil {
		return "", err
	}
	return sourceDigest(tips, held), nil
}

// taskTips narrows a tip listing to the task refs — the only kind whose linked
// commits Build reads the object database for, which taskCommits is the other
// half of.
func taskTips(tips map[string]model.SHA) map[string]model.SHA {
	root := refs.Root(model.KindTask)
	out := make(map[string]model.SHA, len(tips))
	for name, sha := range tips {
		if strings.HasPrefix(name, root) {
			out[name] = sha
		}
	}
	return out
}

// Build folds every live entity under refs/cc-notes/ and assembles the graph
// over them: the entity, anchor, commit, branch, session, tag, and concept
// nodes, the declared relations, the lifecycle events each chain replays into,
// the derived path anchors backfilled from a task's linked commits, the
// path-to-directory containment chain, the co-occurrence edges the shared
// nodes imply, and the advisory co-change coupling between anchored paths.
// Tombstoned entities are dropped; superseded ones are kept, since the
// supersede edge is the whole point of keeping them.
func Build(ctx context.Context, s *store.Store) (*Graph, error) {
	tips, err := s.Repo.ListPrefix(ctx, refs.Namespace)
	if err != nil {
		return nil, fmt.Errorf("list entity refs: %w", err)
	}
	records, err := loadRecords(ctx, s, tips)
	if err != nil {
		return nil, err
	}
	commits := taskCommits(records)
	touched, err := s.Git.CommitPaths(ctx, commits)
	if err != nil {
		return nil, err
	}
	held, err := s.Git.HeldObjects(ctx, commits)
	if err != nil {
		return nil, err
	}
	terms := entityConcepts(records)

	b := newBuilder(records)
	for _, r := range records {
		b.addRecord(r, touched, terms)
	}
	b.addContainment()
	if err := b.addCochange(ctx, s.Git); err != nil {
		return nil, err
	}
	b.addCooccurrence()

	return b.graph(sourceDigest(tips, held), time.Now().Unix(), records), nil
}

// record is one folded entity flattened into the fields the graph reads.
type record struct {
	id         model.EntityID
	kind       model.Kind
	title      string
	text       string
	tags       []string
	updatedAt  int64
	anchors    []model.Anchor
	witness    []model.AnchorWitness
	commits    []model.SHA
	fixCommits []model.SHA
	branch     model.Branch
	parent     model.EntityID
	blockedBy  []model.EntityID
	sprint     model.EntityID
	project    model.EntityID
	plan       model.EntityID
	followUps  []model.EntityID
	superseded []model.EntityID
	events     []Event
}

func loadRecords(ctx context.Context, s *store.Store, tips map[string]model.SHA) ([]record, error) {
	names := slices.Sorted(maps.Keys(tips))
	loaded := make([]record, len(names))
	live := make([]bool, len(names))
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(loadConcurrency)
	for i, name := range names {
		g.Go(func() error {
			chain, err := s.Repo.ReadChain(gctx, tips[name])
			if err != nil {
				return fmt.Errorf("read chain %s: %w", name, err)
			}
			events, snap, err := entityEvents(name, chain)
			if err != nil {
				return err
			}
			if snap.Meta().Deleted {
				return nil
			}
			loaded[i], live[i] = newRecord(snap, events), true
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		return nil, err
	}
	records := make([]record, 0, len(loaded))
	for i, r := range loaded {
		if live[i] {
			records = append(records, r)
		}
	}
	return records, nil
}

func newRecord(snap model.Snapshot, events []Event) record {
	meta := snap.Meta()
	r := record{
		id:        snap.EntityID(),
		kind:      meta.Kind,
		title:     meta.Title,
		updatedAt: meta.UpdatedAt.Unix(),
		events:    events,
	}
	switch s := snap.(type) {
	case model.Note:
		r.text, r.tags = s.Body, s.Tags
		r.anchors, r.witness, r.superseded = s.Anchors, s.Witness, s.SupersededBy
	case model.Doc:
		r.text, r.tags = join(s.When, s.Body), s.Tags
		r.anchors, r.witness, r.superseded = s.Anchors, s.Witness, s.SupersededBy
	case model.Log:
		r.text, r.tags = entryText(s.Entries), s.Tags
		r.anchors = s.Anchors
	case model.Task:
		// TODO: read s.Anchors once tasks carry them. addEdge already prefers a
		// written anchor over the same path derived from s.Commits, so the
		// backfill below needs no further change.
		r.text, r.tags = join(s.Description, commentText(s.Comments), criterionText(s.Criteria)), s.Labels
		r.commits, r.branch, r.parent = s.Commits, s.Branch, s.Parent
		r.blockedBy, r.sprint, r.project, r.plan = s.BlockedBy, s.Sprint, s.Project, s.Plan
	case model.Sprint:
		r.text, r.tags = join(s.Description, commentText(s.Comments)), s.Labels
		r.commits, r.project = s.Commits, s.Project
	case model.Project:
		r.text, r.tags = join(s.Description, commentText(s.Comments)), s.Labels
		r.commits = s.Commits
	case model.Runbook:
		r.text, r.tags = join(s.Description, stepText(s.Steps), commentText(s.Comments)), s.Labels
		r.anchors = s.Anchors
	case model.Investigation:
		r.text, r.tags = join(s.Premise, s.Body, s.RootCause, findingText(s.Findings), entryText(s.Entries)), s.Tags
		r.anchors, r.commits, r.fixCommits = s.Anchors, s.Commits, s.FixCommits
		r.followUps, r.superseded = s.FollowUps, s.SupersededBy
	case model.Plan:
		r.text, r.tags = join(s.Body, s.Outcome, commentText(s.Comments)), s.Labels
		r.anchors, r.superseded = s.Anchors, s.SupersededBy
	default:
		panic(fmt.Sprintf("kg: unregistered snapshot type %T", snap))
	}
	return r
}

// entityConcepts returns each entity's discriminating identifier concepts:
// the terms its title and text mention, minus those too rare to relate
// anything to it and those too common to tell it apart.
func entityConcepts(records []record) map[NodeID][]string {
	extracted := make(map[NodeID][]string, len(records))
	for _, r := range records {
		extracted[EntityNode(r.kind, r.id)] = concepts(join(r.title, r.text))
	}
	keep := discriminating(extracted, len(records))
	for id, terms := range extracted {
		extracted[id] = slices.DeleteFunc(terms, func(t string) bool {
			_, ok := keep[t]
			return !ok
		})
	}
	return extracted
}

// taskCommits returns the distinct commits every task links, sorted. They are
// the only commits whose diffs the build reads, because tasks are the kind
// with no anchors of their own to backfill from.
func taskCommits(records []record) []model.SHA {
	seen := map[model.SHA]struct{}{}
	for _, r := range records {
		if r.kind != model.KindTask {
			continue
		}
		for _, sha := range r.commits {
			seen[sha] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

type edgeKey struct {
	from NodeID
	to   NodeID
	kind EdgeKind
}

// builder accumulates deduplicated nodes and edges. kinds resolves an entity
// reference to its node id: an id it does not hold names a tombstoned or
// not-yet-synced entity, and the edge to it is dropped rather than pointing at
// a node the graph has no record of.
type builder struct {
	nodes map[NodeID]Node
	edges map[edgeKey]Edge
	kinds map[model.EntityID]model.Kind
}

func newBuilder(records []record) *builder {
	kinds := make(map[model.EntityID]model.Kind, len(records))
	for _, r := range records {
		kinds[r.id] = r.kind
	}
	return &builder{
		nodes: make(map[NodeID]Node, 4*len(records)),
		edges: make(map[edgeKey]Edge, 8*len(records)),
		kinds: kinds,
	}
}

func (b *builder) addNode(n Node) {
	if _, ok := b.nodes[n.ID]; !ok {
		b.nodes[n.ID] = n
	}
}

// addEdge records e, keeping a written edge over a derived one in the same
// slot: a path anchor an agent wrote always outranks the same path backfilled
// from a commit, whichever order they arrive in.
func (b *builder) addEdge(e Edge) {
	key := edgeKey{from: e.From, to: e.To, kind: e.Kind}
	if existing, ok := b.edges[key]; ok && (!existing.Derived || e.Derived) {
		return
	}
	b.edges[key] = e
}

// entityEdge links from to the entity with the given id, if the graph holds it.
func (b *builder) entityEdge(from NodeID, id model.EntityID, kind EdgeKind) {
	target, ok := b.kinds[id]
	if !ok {
		return
	}
	b.addEdge(Edge{From: from, To: EntityNode(target, id), Kind: kind, Weight: 1})
}

func (b *builder) addRecord(r record, touched map[model.SHA][]string, terms map[NodeID][]string) {
	self := EntityNode(r.kind, r.id)
	b.addNode(Node{ID: self, Kind: NodeKind(r.kind), Value: string(r.id), Title: r.title, UpdatedAt: r.updatedAt})

	witnessed := witnessOIDs(r.witness)
	for _, a := range r.anchors {
		to := AnchorNode(a)
		b.addNode(Node{ID: to, Kind: NodeKind(a.Kind), Value: anchorValue(a)})
		b.addEdge(Edge{From: self, To: to, Kind: EdgeAnchor, Weight: 1, OID: witnessed[to]})
	}
	if r.kind == model.KindTask {
		for _, p := range derivedPaths(r.commits, touched) {
			to := PathNode(p)
			b.addNode(Node{ID: to, Kind: NodePath, Value: path.Clean(p)})
			b.addEdge(Edge{From: self, To: to, Kind: EdgeAnchor, Weight: 1, Derived: true})
		}
	}
	if r.branch != "" {
		to := BranchNode(r.branch)
		b.addNode(Node{ID: to, Kind: NodeBranch, Value: string(r.branch)})
		b.addEdge(Edge{From: self, To: to, Kind: EdgeBranch, Weight: 1})
	}
	b.addCommitEdges(self, r.commits, EdgeCommit)
	b.addCommitEdges(self, r.fixCommits, EdgeFixCommit)
	b.addSessionEdges(self, r.events)
	for _, tag := range r.tags {
		to := TagNode(tag)
		b.addNode(Node{ID: to, Kind: NodeTag, Value: tag})
		b.addEdge(Edge{From: self, To: to, Kind: EdgeTag, Weight: 1})
	}
	for _, term := range terms[self] {
		to := ConceptNode(term)
		b.addNode(Node{ID: to, Kind: NodeConcept, Value: term})
		b.addEdge(Edge{From: self, To: to, Kind: EdgeConcept, Weight: 1, Derived: true})
	}

	for _, id := range r.blockedBy {
		b.entityEdge(self, id, EdgeDep)
	}
	for _, id := range r.followUps {
		b.entityEdge(self, id, EdgeFollowUp)
	}
	for _, id := range r.superseded {
		b.entityEdge(self, id, EdgeSupersede)
	}
	if r.parent != "" {
		b.entityEdge(self, r.parent, EdgeParent)
	}
	if r.sprint != "" {
		b.entityEdge(self, r.sprint, EdgeSprint)
	}
	if r.project != "" {
		b.entityEdge(self, r.project, EdgeProject)
	}
	if r.plan != "" {
		b.entityEdge(self, r.plan, EdgePlan)
	}
}

func (b *builder) addCommitEdges(from NodeID, shas []model.SHA, kind EdgeKind) {
	for _, sha := range shas {
		to := CommitNode(sha)
		b.addNode(Node{ID: to, Kind: NodeCommit, Value: string(sha)})
		b.addEdge(Edge{From: from, To: to, Kind: kind, Weight: 1})
	}
}

// derivedPaths returns the paths a task's linked commits touched, sorted and
// distinct. A commit that rewrote more than derivedPathLimit files contributes
// none: it anchors the task to nothing in particular.
func derivedPaths(commits []model.SHA, touched map[model.SHA][]string) []string {
	seen := map[string]struct{}{}
	for _, sha := range commits {
		paths := touched[sha]
		if len(paths) > derivedPathLimit {
			continue
		}
		for _, p := range paths {
			seen[p] = struct{}{}
		}
	}
	return slices.Sorted(maps.Keys(seen))
}

// witnessOIDs indexes an entity's anchor witnesses by the node each covers.
func witnessOIDs(witness []model.AnchorWitness) map[NodeID]model.SHA {
	oids := make(map[NodeID]model.SHA, len(witness))
	for _, w := range witness {
		oids[AnchorNode(w.Anchor)] = w.OID
	}
	return oids
}

func anchorValue(a model.Anchor) string {
	if a.Kind == model.AnchorPath || a.Kind == model.AnchorDir {
		return path.Clean(a.Value)
	}
	return a.Value
}

// addContainment links every path and directory node to the directory holding
// it, and each of those up to the repository root, so a query on a directory
// reaches the entities anchored anywhere beneath it.
func (b *builder) addContainment() {
	for _, id := range slices.Sorted(maps.Keys(b.nodes)) {
		n := b.nodes[id]
		if n.Kind != NodePath && n.Kind != NodeDir {
			continue
		}
		child, from := n.Value, n.ID
		for {
			parent := path.Dir(child)
			if parent == child {
				break
			}
			to := DirNode(parent)
			b.addNode(Node{ID: to, Kind: NodeDir, Value: parent})
			b.addEdge(Edge{From: from, To: to, Kind: EdgeWithin, Weight: 1})
			child, from = parent, to
		}
	}
}

// hubPrior scales what one shared node of this kind says about two entities,
// and reports whether it says anything at all. Each scale is that family's
// measured standalone same-topic pair F1 (cc-notes note 3628756, 2026-07-26).
// Containment is excluded: propagating a path up to its directory relates
// everything under it, which is what a dir anchor already says.
func hubPrior(kind EdgeKind) (float64, bool) {
	switch kind {
	case EdgeSession:
		return 0.48, true
	case EdgeConcept:
		return 0.45, true
	case EdgeTag:
		return 0.38, true
	case EdgeCommit, EdgeFixCommit, EdgeBranch:
		return 0.20, true
	case EdgeAnchor:
		return 0.02, true
	}
	return 0, false
}

// addCooccurrence relates every pair of entities that share a hub node. Each
// shared node contributes its kind's prior over log2(1+n) for its own n
// members, so a rare node counts for more than a popular one and a session for
// more than an anchor. Edges are emitted once per pair, lower node id first.
func (b *builder) addCooccurrence() {
	members := map[NodeID][]NodeID{}
	priors := map[NodeID]float64{}
	for _, e := range b.edges {
		prior, ok := hubPrior(e.Kind)
		if !ok {
			continue
		}
		members[e.To] = append(members[e.To], e.From)
		priors[e.To] = prior
	}
	weights := map[edgeKey]float64{}
	for _, hub := range slices.Sorted(maps.Keys(members)) {
		entities := slices.Compact(slices.Sorted(slices.Values(members[hub])))
		if len(entities) < 2 || len(entities) > cooccurMaxDegree {
			continue
		}
		share := priors[hub] / math.Log2(1+float64(len(entities)))
		for i, a := range entities {
			for _, z := range entities[i+1:] {
				weights[edgeKey{from: a, to: z, kind: EdgeCooccur}] += share
			}
		}
	}
	for _, key := range sortedEdgeKeys(weights) {
		b.addEdge(Edge{From: key.from, To: key.to, Kind: EdgeCooccur, Weight: weights[key]})
	}
}

func sortedEdgeKeys[T any](m map[edgeKey]T) []edgeKey {
	keys := make([]edgeKey, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, compareEdgeKeys)
	return keys
}

func compareEdgeKeys(a, z edgeKey) int {
	if c := cmp.Compare(a.from, z.from); c != 0 {
		return c
	}
	if c := cmp.Compare(a.kind, z.kind); c != 0 {
		return c
	}
	return cmp.Compare(a.to, z.to)
}

func (b *builder) graph(source string, builtAt int64, records []record) *Graph {
	nodes := make([]Node, 0, len(b.nodes))
	for _, id := range slices.Sorted(maps.Keys(b.nodes)) {
		nodes = append(nodes, b.nodes[id])
	}
	edges := make([]Edge, 0, len(b.edges))
	for _, key := range sortedEdgeKeys(b.edges) {
		edges = append(edges, b.edges[key])
	}
	var events []Event
	for _, r := range records {
		events = append(events, r.events...)
	}
	sortEvents(events)
	return &Graph{Source: source, BuiltAt: builtAt, Nodes: nodes, Edges: edges, Events: events}
}

func sourceDigest(tips map[string]model.SHA, held []model.SHA) string {
	h := sha256.New()
	h.Write([]byte(sourceDomain))
	for _, name := range slices.Sorted(maps.Keys(tips)) {
		h.Write([]byte(name + "\x00" + string(tips[name]) + "\n"))
	}
	h.Write([]byte(commitDomain))
	for _, sha := range held {
		h.Write([]byte(string(sha) + "\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}
