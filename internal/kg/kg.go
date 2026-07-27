// Package kg builds and stores the cc-notes knowledge graph: one node per
// entity, per anchor target, and per writing session, and one edge per
// declared relation, anchor, containment step, and co-occurrence.
//
// The graph is derived state. It is folded out of refs/cc-notes/* rather than
// synced, it carries the digest of the ref tips it was built from so a stored
// graph is either exactly current or a miss, and it lives outside the
// repository under the per-user state root (internal/ccnhome). Nothing here
// reaches a language model: every edge is read from an op, a git object, or
// the shape of the graph itself.
package kg

import (
	"path"

	"github.com/yasyf/cc-notes/model"
)

// NodeKind discriminates what a node stands for. The eight entity kinds and
// the four anchor kinds reuse their model wire values verbatim, so a Kind or
// an AnchorKind converts straight across; session is the one node kind with no
// model counterpart.
type NodeKind string

// The node kinds.
const (
	NodeNote          = NodeKind(model.KindNote)
	NodeDoc           = NodeKind(model.KindDoc)
	NodeLog           = NodeKind(model.KindLog)
	NodeTask          = NodeKind(model.KindTask)
	NodeSprint        = NodeKind(model.KindSprint)
	NodeProject       = NodeKind(model.KindProject)
	NodeRunbook       = NodeKind(model.KindRunbook)
	NodeInvestigation = NodeKind(model.KindInvestigation)

	NodePath   = NodeKind(model.AnchorPath)
	NodeDir    = NodeKind(model.AnchorDir)
	NodeCommit = NodeKind(model.AnchorCommit)
	NodeBranch = NodeKind(model.AnchorBranch)

	NodeSession NodeKind = "session"
	NodeTag     NodeKind = "tag"
	NodeConcept NodeKind = "concept"
)

// NodeID is a node's stable identity: its kind and value joined by a colon.
// Entity ids are the root commit oid — content-addressed and globally unique —
// so an entity node needs no resolution step, and every other value is unique
// within its own kind.
type NodeID string

func nodeID(kind NodeKind, value string) NodeID { return NodeID(string(kind) + ":" + value) }

// EntityNode returns the node id of the entity of kind with the given id.
func EntityNode(kind model.Kind, id model.EntityID) NodeID {
	return nodeID(NodeKind(kind), string(id))
}

// PathNode returns the node id of a repository file path.
func PathNode(p string) NodeID { return nodeID(NodePath, path.Clean(p)) }

// DirNode returns the node id of a repository directory.
func DirNode(d string) NodeID { return nodeID(NodeDir, path.Clean(d)) }

// CommitNode returns the node id of a code commit.
func CommitNode(sha model.SHA) NodeID { return nodeID(NodeCommit, string(sha)) }

// BranchNode returns the node id of a branch.
func BranchNode(b model.Branch) NodeID { return nodeID(NodeBranch, string(b)) }

// SessionNode returns the node id of a Claude session.
func SessionNode(id string) NodeID { return nodeID(NodeSession, id) }

// TagNode returns the node id of a tag or label an agent wrote.
func TagNode(tag string) NodeID { return nodeID(NodeTag, tag) }

// ConceptNode returns the node id of an extracted identifier concept.
func ConceptNode(term string) NodeID { return nodeID(NodeConcept, term) }

// AnchorNode returns the node id an anchor points at. Path and dir values are
// cleaned, so "a/b/" and "a/b" name one node.
func AnchorNode(a model.Anchor) NodeID {
	switch a.Kind {
	case model.AnchorPath:
		return PathNode(a.Value)
	case model.AnchorDir:
		return DirNode(a.Value)
	case model.AnchorCommit:
		return CommitNode(model.SHA(a.Value))
	case model.AnchorBranch:
		return BranchNode(model.Branch(a.Value))
	}
	panic("kg: unregistered anchor kind " + string(a.Kind))
}

// Node is one vertex. Value is the node's identity within its kind. Title is
// entity metadata; UpdatedAt is an entity's last edit or a session's last
// event. Revisions and Churn are the co-change scan's per-path history volume
// — commits touching the path and lines it gained or lost — and stay zero on
// every other kind. Vector is the dense embedding, present only when the build
// ran with an Embedder.
type Node struct {
	ID        NodeID    `json:"id"`
	Kind      NodeKind  `json:"kind"`
	Value     string    `json:"value"`
	Title     string    `json:"title,omitempty"`
	UpdatedAt int64     `json:"updated_at,omitempty"`
	Revisions int       `json:"revisions,omitempty"`
	Churn     int64     `json:"churn,omitempty"`
	Vector    []float32 `json:"vector,omitempty"`
}

// EdgeKind names the relation an edge asserts.
type EdgeKind string

// The edge kinds. dep through supersede are declared: an agent wrote the op
// that created each one. anchor, branch, and tag tie an entity to the
// repository and its vocabulary, within is the containment chain a path
// climbs, session ties an entity to the Claude session that wrote it, concept
// to an identifier its text mentions, cooccur is the entity-to-entity relation
// summed over the nodes two entities share, and cochange couples two paths
// that keep changing in the same commit.
const (
	EdgeDep       EdgeKind = "dep"
	EdgeParent    EdgeKind = "parent"
	EdgeSprint    EdgeKind = "sprint"
	EdgeProject   EdgeKind = "project"
	EdgeFollowUp  EdgeKind = "follow_up"
	EdgeCommit    EdgeKind = "commit"
	EdgeFixCommit EdgeKind = "fix_commit"
	EdgeSupersede EdgeKind = "supersede"

	EdgeAnchor   EdgeKind = "anchor"
	EdgeBranch   EdgeKind = "branch"
	EdgeWithin   EdgeKind = "within"
	EdgeSession  EdgeKind = "session"
	EdgeTag      EdgeKind = "tag"
	EdgeConcept  EdgeKind = "concept"
	EdgeCooccur  EdgeKind = "cooccur"
	EdgeCochange EdgeKind = "cochange"
)

// Edge is one directed relation. Weight is 1 on a declared or structural edge,
// the event count on a session edge, and the summed score on a cooccur or
// cochange one. Derived marks a relation cc-notes synthesized rather than read
// from an op; Advisory marks one a ranker may only adjust a score by, never
// read as evidence two nodes are related. OID is the anchor's content witness.
type Edge struct {
	From     NodeID    `json:"from"`
	To       NodeID    `json:"to"`
	Kind     EdgeKind  `json:"kind"`
	Weight   float64   `json:"weight"`
	Derived  bool      `json:"derived,omitempty"`
	Advisory bool      `json:"advisory,omitempty"`
	OID      model.SHA `json:"oid,omitempty"`
}

// Event is one lifecycle event in an entity's history: the verb
// internal/lifecycle classified, when it happened, and the session, actor,
// branch, and pack commit that carried it. Lamport is the entity's logical
// clock, which orders two events the same author-second cannot. Events are the
// graph's time axis and the sole source of its session edges.
type Event struct {
	Entity  NodeID        `json:"entity"`
	Type    string        `json:"type"`
	At      int64         `json:"at"`
	Lamport model.Lamport `json:"lamport"`
	Session string        `json:"session,omitempty"`
	Actor   string        `json:"actor,omitempty"`
	Branch  string        `json:"branch,omitempty"`
	SHA     model.SHA     `json:"sha"`
}

// Graph is one built knowledge graph. Source is the digest of the entity ref
// tips it was folded from, so a stored graph is either exactly current or a
// miss — there is no staleness heuristic. BuiltAt is unix seconds. Nodes are
// sorted by id, Edges by (from, kind, to), and Events by (at, entity, type).
type Graph struct {
	Source  string
	BuiltAt int64
	Nodes   []Node
	Edges   []Edge
	Events  []Event
}
