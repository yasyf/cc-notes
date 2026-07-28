package rank

import (
	"path"
	"strings"

	"github.com/yasyf/cc-notes/internal/kg"
	"github.com/yasyf/cc-notes/internal/kg/eval"
)

// enrichDirs is how many trailing directory components of an anchored path
// join the record's searchable text.
const enrichDirs = 3

// Enrich appends every record's anchored addresses to its body, so a query
// naming a path, branch, or commit sha matches the record anchored there even
// when the prose never spells the address out. Path components follow
// cc-context's BM25 enrichment (semsearch/rank.EnrichForBM25): the file stem
// twice, then the last three directory components.
func Enrich(corpus []eval.Entity, g *kg.Graph) []eval.Entity {
	addresses := anchorText(g)
	out := make([]eval.Entity, len(corpus))
	for i, e := range corpus {
		out[i] = e
		if text := addresses[kg.EntityNode(e.Kind, e.ID)]; text != "" {
			out[i].Body = e.Body + "\n" + text
		}
	}
	return out
}

// anchorText renders each entity node's anchor, branch, and commit targets as
// searchable text.
func anchorText(g *kg.Graph) map[kg.NodeID]string {
	values := make(map[kg.NodeID]string, len(g.Nodes))
	kinds := make(map[kg.NodeID]kg.NodeKind, len(g.Nodes))
	for _, n := range g.Nodes {
		values[n.ID], kinds[n.ID] = n.Value, n.Kind
	}
	parts := map[kg.NodeID][]string{}
	for _, e := range g.Edges {
		switch e.Kind {
		case kg.EdgeAnchor, kg.EdgeBranch, kg.EdgeCommit, kg.EdgeFixCommit:
			parts[e.From] = append(parts[e.From], addressText(kinds[e.To], values[e.To]))
		}
	}
	out := make(map[kg.NodeID]string, len(parts))
	for id, ps := range parts {
		out[id] = strings.Join(ps, " ")
	}
	return out
}

// addressText spells one anchor target for the lexical index.
func addressText(kind kg.NodeKind, value string) string {
	switch kind {
	case kg.NodePath:
		stem := strings.TrimSuffix(path.Base(value), path.Ext(value))
		return strings.Join([]string{value, stem, stem, dirText(path.Dir(value))}, " ")
	case kg.NodeDir:
		return value + " " + dirText(value)
	}
	return value
}

// dirText is the last enrichDirs components of a directory.
func dirText(dir string) string {
	if dir == "." {
		return ""
	}
	parts := strings.Split(dir, "/")
	if len(parts) > enrichDirs {
		parts = parts[len(parts)-enrichDirs:]
	}
	return strings.Join(parts, " ")
}
