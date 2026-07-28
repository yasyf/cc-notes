package rank

import (
	"cmp"
	"maps"
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/internal/kg"
	"github.com/yasyf/cc-notes/model"
)

// importanceIterations is how far the unpersonalized walk that supplies the
// importance prior iterates. At the default damping the residual is under 4 %.
const importanceIterations = 20

// minShaLen is the shortest hex prefix that may address a commit node.
const minShaLen = 7

// separators are the characters that make a node value identifier-shaped. A
// value without one is an English word — "build", "current", "review" — and
// seeding it walks out from a coincidence.
const separators = "/._-"

// neighbour is one transition out of a node: the target and its share of the
// node's outgoing mass.
type neighbour struct {
	to    int
	share float64
}

// graphLane is the personalized-PageRank retrieval lane. It holds the graph as
// a symmetric, row-stochastic transition matrix over node indexes, the entity
// node each index belongs to, the HippoRAG node-specificity term for every
// node, and the value index a query resolves seeds through.
type graphLane struct {
	index   map[kg.NodeID]int
	entity  []model.EntityID
	adj     [][]neighbour
	spec    []float64
	values  []hub
	commits map[string]int
}

// hub is one addressable node value and the nodes that carry it, held in a
// sorted slice so a query resolves seeds in a fixed order — floating-point
// addition is not associative, and map order would make the ranking wobble.
type hub struct {
	value string
	nodes []int
}

// newGraphLane indexes the graph for walking. Advisory edges are excluded: the
// graph declares them as rank adjustments, not evidence that two nodes are
// related, and a walk cannot tell the difference.
func newGraphLane(g *kg.Graph) *graphLane {
	l := &graphLane{
		index:   make(map[kg.NodeID]int, len(g.Nodes)),
		entity:  make([]model.EntityID, len(g.Nodes)),
		adj:     make([][]neighbour, len(g.Nodes)),
		spec:    make([]float64, len(g.Nodes)),
		commits: map[string]int{},
	}
	byValue := map[string][]int{}
	for i, n := range g.Nodes {
		l.index[n.ID] = i
		if id, ok := entityID(n); ok {
			l.entity[i] = id
			continue
		}
		if n.Kind == kg.NodeCommit {
			l.commits[strings.ToLower(n.Value)] = i
		}
		if strings.ContainsAny(n.Value, separators) {
			v := strings.ToLower(n.Value)
			byValue[v] = append(byValue[v], i)
		}
	}
	for _, v := range slices.Sorted(maps.Keys(byValue)) {
		l.values = append(l.values, hub{value: v, nodes: byValue[v]})
	}
	weights := make([][]neighbour, len(g.Nodes))
	members := make([]int, len(g.Nodes))
	for _, e := range g.Edges {
		if e.Advisory {
			continue
		}
		from, to := l.index[e.From], l.index[e.To]
		weights[from] = append(weights[from], neighbour{to: to, share: e.Weight})
		weights[to] = append(weights[to], neighbour{to: from, share: e.Weight})
		if l.entity[from] != "" && l.entity[to] == "" {
			members[to]++
		}
	}
	for i, out := range weights {
		l.adj[i] = normalizeShares(out)
		l.spec[i] = 1 / float64(max(1, members[i]))
	}
	return l
}

// entityID reports the entity a node stands for, and whether it stands for one
// at all: the hub kinds address the repository, not a record.
func entityID(n kg.Node) (model.EntityID, bool) {
	switch n.Kind {
	case kg.NodePath, kg.NodeDir, kg.NodeCommit, kg.NodeBranch, kg.NodeSession, kg.NodeTag, kg.NodeConcept:
		return "", false
	}
	return model.EntityID(n.Value), true
}

// normalizeShares turns a node's edge weights into a probability distribution
// over its neighbours, folding parallel edges together.
func normalizeShares(out []neighbour) []neighbour {
	merged := map[int]float64{}
	total := 0.0
	for _, n := range out {
		merged[n.to] += n.share
		total += n.share
	}
	if total == 0 {
		return nil
	}
	kept := make([]neighbour, 0, len(merged))
	for to, w := range merged {
		kept = append(kept, neighbour{to: to, share: w / total})
	}
	slices.SortFunc(kept, func(a, b neighbour) int { return cmp.Compare(a.to, b.to) })
	return kept
}

// personalize builds the walk's restart distribution from two independent
// groups at equal mass: the addresses the query names, and the session's own
// position. Seeding it on the lexical head as well was measured a wash
// (5 wins / 5 losses over the evaluation corpus, 2026-07-27) and is not done —
// the graph lane earns its place by answering what the lexical lane cannot
// address, not by re-ranking what it already found.
func (l *graphLane) personalize(query string, sess Session) map[int]float64 {
	seeds := map[int]float64{}
	addGroup(seeds, l.querySeeds(query))
	addGroup(seeds, l.sessionSeeds(sess))
	return seeds
}

// querySeeds resolves the identifier-shaped node values the query names at a
// token boundary, plus any commit a hex prefix addresses. Each is weighted by
// its node specificity, so a branch three records share outweighs a directory
// forty of them sit under.
func (l *graphLane) querySeeds(query string) map[int]float64 {
	low := strings.ToLower(query)
	seeds := map[int]float64{}
	for _, h := range l.values {
		if !containsToken(low, h.value) {
			continue
		}
		for _, i := range h.nodes {
			seeds[i] += l.spec[i]
		}
	}
	for _, token := range strings.FieldsFunc(low, func(r rune) bool { return !isHexDigit(r) }) {
		if len(token) < minShaLen {
			continue
		}
		for _, sha := range slices.Sorted(maps.Keys(l.commits)) {
			if strings.HasPrefix(sha, token) {
				seeds[l.commits[sha]] += l.spec[l.commits[sha]]
			}
		}
	}
	return seeds
}

// sessionSeeds resolves the branch, paths, and records the session is holding.
func (l *graphLane) sessionSeeds(sess Session) map[int]float64 {
	seeds := map[int]float64{}
	if sess.Branch != "" {
		l.seedNode(seeds, kg.BranchNode(sess.Branch))
	}
	for _, p := range sess.Paths {
		l.seedNode(seeds, kg.PathNode(p))
	}
	for _, id := range sess.Entities {
		for _, i := range l.entityNodes(id) {
			seeds[i]++
		}
	}
	return seeds
}

// walk runs the truncated personalized PageRank and returns the mass that
// settled on each entity node. An empty personalization retrieves nothing.
func (l *graphLane) walk(seeds map[int]float64, hops int, damping float64) map[model.EntityID]float64 {
	if len(seeds) == 0 {
		return nil
	}
	p := make([]float64, len(l.adj))
	total := 0.0
	for _, i := range slices.Sorted(maps.Keys(seeds)) {
		total += seeds[i]
	}
	for i, w := range seeds {
		p[i] = w / total
	}
	return l.mass(l.iterate(p, hops, damping))
}

// lift divides the personalized mass by the unpersonalized mass: how far the
// query moved a record, not where it already stood. Two hops over a graph this
// dense reach nearly every record, so raw mass ranks hubs.
func lift(walk, background map[model.EntityID]float64) map[model.EntityID]float64 {
	out := make(map[model.EntityID]float64, len(walk))
	for id, mass := range walk {
		out[id] = mass / background[id]
	}
	return out
}

// importance is the unpersonalized walk: each record's standing in the graph
// regardless of the query.
func (l *graphLane) importance() map[model.EntityID]float64 {
	p := make([]float64, len(l.adj))
	for i := range p {
		p[i] = 1 / float64(len(p))
	}
	return l.mass(l.iterate(p, importanceIterations, DefaultDamping))
}

// iterate sums the damped walk over every path length up to hops:
// (1-d) * sum over h of d^h * W^h * p. Accumulating is what makes it a
// personalized PageRank; the distribution after exactly hops steps instead
// leaves most of the mass at the far end, ranking a record two hops out above
// the one the seed points straight at.
func (l *graphLane) iterate(p []float64, hops int, damping float64) []float64 {
	acc := make([]float64, len(p))
	for i, mass := range p {
		acc[i] = (1 - damping) * mass
	}
	cur, next := slices.Clone(p), make([]float64, len(p))
	weight := 1 - damping
	for range hops {
		clear(next)
		for i, mass := range cur {
			if mass == 0 {
				continue
			}
			for _, n := range l.adj[i] {
				next[n.to] += mass * n.share
			}
		}
		weight *= damping
		for i, mass := range next {
			acc[i] += weight * mass
		}
		cur, next = next, cur
	}
	return acc
}

// mass projects a node distribution onto the entities it landed on.
func (l *graphLane) mass(x []float64) map[model.EntityID]float64 {
	out := map[model.EntityID]float64{}
	for i, id := range l.entity {
		if id != "" && x[i] > 0 {
			out[id] = x[i]
		}
	}
	return out
}

// entityNodes resolves the node indexes an entity id may hold. The id is
// content-addressed and unique, so at most one kind claims it.
func (l *graphLane) entityNodes(id model.EntityID) []int {
	var out []int
	for _, kind := range model.Kinds() {
		if i, ok := l.index[kg.EntityNode(kind, id)]; ok {
			out = append(out, i)
		}
	}
	return out
}

func (l *graphLane) seedNode(seeds map[int]float64, id kg.NodeID) {
	if i, ok := l.index[id]; ok {
		seeds[i] += l.spec[i]
	}
}

// addGroup folds one seed group into the personalization at unit mass, in
// index order so the sum is reproducible.
func addGroup(seeds, group map[int]float64) {
	indexes := slices.Sorted(maps.Keys(group))
	total := 0.0
	for _, i := range indexes {
		total += group[i]
	}
	if total == 0 {
		return
	}
	for _, i := range indexes {
		seeds[i] += group[i] / total
	}
}

// containsToken reports whether needle appears in haystack delimited by
// non-alphanumeric characters on both sides.
func containsToken(haystack, needle string) bool {
	for at := 0; at < len(haystack); {
		i := strings.Index(haystack[at:], needle)
		if i < 0 {
			return false
		}
		start := at + i
		end := start + len(needle)
		if !alnumAt(haystack, start-1) && !alnumAt(haystack, end) {
			return true
		}
		at = start + 1
	}
	return false
}

func alnumAt(s string, i int) bool {
	if i < 0 || i >= len(s) {
		return false
	}
	c := s[i]
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isHexDigit(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'f'
}
