package kg

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"

	"go.etcd.io/bbolt"
)

const (
	dbName      = "graph.db"
	dirPerm     = 0o750
	filePerm    = 0o600
	openTimeout = 5 * time.Second
)

var (
	metaBucket  = []byte("meta")
	nodeBucket  = []byte("node")
	edgeBucket  = []byte("edge")
	backBucket  = []byte("back")
	eventBucket = []byte("event")

	metaSource  = []byte("source")
	metaBuiltAt = []byte("built_at")
)

// Index is a read handle on a stored graph.
//
// Every failure on the read path is a miss and never propagates, the same
// discipline the fold cache applies to derived state: the graph is
// rebuildable, so an absent, unreadable, or stale one is a signal to call
// Build, not an error to report. The stored source digest makes that decision
// exact — a graph either matches the repository's current ref tips or it does
// not.
type Index struct {
	db      *bbolt.DB
	source  string
	builtAt int64
}

// Load opens the graph stored in dir if it was built from source, reporting a
// miss on an absent file, an unreadable one, or any other digest.
func Load(dir, source string) (*Index, bool) {
	db, err := bbolt.Open(filepath.Join(dir, dbName), filePerm, &bbolt.Options{ReadOnly: true, Timeout: openTimeout})
	if err != nil {
		return nil, false
	}
	var builtAt int64
	matched := false
	_ = db.View(func(tx *bbolt.Tx) error {
		meta := tx.Bucket(metaBucket)
		if meta == nil || tx.Bucket(nodeBucket) == nil || tx.Bucket(edgeBucket) == nil ||
			tx.Bucket(backBucket) == nil || tx.Bucket(eventBucket) == nil {
			return nil
		}
		if string(meta.Get(metaSource)) != source {
			return nil
		}
		stamp, err := strconv.ParseInt(string(meta.Get(metaBuiltAt)), 10, 64)
		if err != nil {
			return nil
		}
		builtAt, matched = stamp, true
		return nil
	})
	if !matched {
		_ = db.Close()
		return nil, false
	}
	return &Index{db: db, source: source, builtAt: builtAt}, true
}

// Save writes g into dir through a temporary file and a rename, so a reader
// never observes a partial graph and a failed write leaves the previous one in
// place. It is best-effort and reports nothing: a graph that failed to persist
// is rebuilt on the next miss.
func Save(dir string, g *Graph) {
	if err := os.MkdirAll(dir, dirPerm); err != nil {
		return
	}
	tmp, err := os.CreateTemp(dir, dbName+".*")
	if err != nil {
		return
	}
	name := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return
	}
	if !writeGraph(name, g) || os.Rename(name, filepath.Join(dir, dbName)) != nil {
		_ = os.Remove(name)
	}
}

func writeGraph(path string, g *Graph) bool {
	db, err := bbolt.Open(path, filePerm, &bbolt.Options{Timeout: openTimeout})
	if err != nil {
		return false
	}
	if err := db.Update(func(tx *bbolt.Tx) error { return putGraph(tx, g) }); err != nil {
		_ = db.Close()
		return false
	}
	return db.Close() == nil
}

func putGraph(tx *bbolt.Tx, g *Graph) error {
	meta, err := tx.CreateBucketIfNotExists(metaBucket)
	if err != nil {
		return err
	}
	if err := meta.Put(metaSource, []byte(g.Source)); err != nil {
		return err
	}
	if err := meta.Put(metaBuiltAt, []byte(strconv.FormatInt(g.BuiltAt, 10))); err != nil {
		return err
	}

	nodes, err := tx.CreateBucketIfNotExists(nodeBucket)
	if err != nil {
		return err
	}
	nodes.FillPercent = 1
	for _, n := range g.Nodes {
		encoded, err := json.Marshal(n)
		if err != nil {
			return err
		}
		if err := nodes.Put([]byte(n.ID), encoded); err != nil {
			return err
		}
	}

	forward, err := tx.CreateBucketIfNotExists(edgeBucket)
	if err != nil {
		return err
	}
	back, err := tx.CreateBucketIfNotExists(backBucket)
	if err != nil {
		return err
	}
	forward.FillPercent, back.FillPercent = 1, 1
	for _, e := range g.Edges {
		encoded, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if err := forward.Put(adjacencyKey(e.From, e.To, e.Kind), encoded); err != nil {
			return err
		}
		if err := back.Put(adjacencyKey(e.To, e.From, e.Kind), encoded); err != nil {
			return err
		}
	}

	events, err := tx.CreateBucketIfNotExists(eventBucket)
	if err != nil {
		return err
	}
	events.FillPercent = 1
	for _, ev := range g.Events {
		encoded, err := json.Marshal(ev)
		if err != nil {
			return err
		}
		if err := events.Put(eventKey(ev), encoded); err != nil {
			return err
		}
	}
	return nil
}

// eventKey orders events exactly as sortEvents does, so a time range is one
// cursor scan and a read-back is the built order. The timestamp and lamport
// clock are big-endian, so byte order is their numeric order.
func eventKey(ev Event) []byte {
	key := appendTime(nil, ev.At)
	key = append(key, ev.Entity...)
	key = append(key, 0)
	key = binary.BigEndian.AppendUint64(key, uint64(ev.Lamport))
	key = append(key, ev.Type...)
	key = append(key, 0)
	return append(key, ev.SHA...)
}

// appendTime and readTime are the event key's time codec: a git author time is
// a non-negative unix second, so the unsigned encoding is total and byte order
// is time order.
//
//nolint:gosec // G115: an author time is non-negative; readTime inverts this exactly.
func appendTime(key []byte, at int64) []byte {
	return binary.BigEndian.AppendUint64(key, uint64(at))
}

//nolint:gosec // G115: the key was written by appendTime from a non-negative int64.
func readTime(key []byte) int64 { return int64(binary.BigEndian.Uint64(key[:8])) }

// adjacencyKey orders an edge under the node it is indexed by, so every edge
// of one node is a single prefix scan. No component may contain NUL: node ids
// are built from entity ids, repository paths, shas, branch names, and session
// ids, none of which can.
func adjacencyKey(anchor, other NodeID, kind EdgeKind) []byte {
	key := make([]byte, 0, len(anchor)+len(other)+len(kind)+2)
	key = append(key, anchor...)
	key = append(key, 0)
	key = append(key, other...)
	key = append(key, 0)
	return append(key, kind...)
}

// Close releases the handle.
func (i *Index) Close() error { return i.db.Close() }

// BuiltAt returns when the stored graph was built, in unix seconds.
func (i *Index) BuiltAt() int64 { return i.builtAt }

// Node returns the node with the given id, or ok=false when the graph holds
// none.
func (i *Index) Node(id NodeID) (Node, bool) {
	var node Node
	found := false
	_ = i.db.View(func(tx *bbolt.Tx) error {
		encoded := tx.Bucket(nodeBucket).Get([]byte(id))
		if encoded == nil {
			return nil
		}
		found = json.Unmarshal(encoded, &node) == nil
		return nil
	})
	return node, found
}

// Out returns every edge leaving from, in stored order: by target, then kind.
func (i *Index) Out(from NodeID) []Edge { return i.adjacent(edgeBucket, from) }

// In returns every edge arriving at to, in stored order: by source, then kind.
func (i *Index) In(to NodeID) []Edge { return i.adjacent(backBucket, to) }

func (i *Index) adjacent(bucket []byte, node NodeID) []Edge {
	var edges []Edge
	prefix := append([]byte(node), 0)
	_ = i.db.View(func(tx *bbolt.Tx) error {
		cursor := tx.Bucket(bucket).Cursor()
		for key, value := cursor.Seek(prefix); key != nil && bytes.HasPrefix(key, prefix); key, value = cursor.Next() {
			var edge Edge
			if err := json.Unmarshal(value, &edge); err != nil {
				return err
			}
			edges = append(edges, edge)
		}
		return nil
	})
	return edges
}

// Events returns every lifecycle event in [since, until] unix seconds, in time
// order. An until of 0 reads to the end of history.
func (i *Index) Events(since, until int64) []Event {
	if until == 0 {
		until = math.MaxInt64
	}
	var events []Event
	_ = i.db.View(func(tx *bbolt.Tx) error {
		cursor := tx.Bucket(eventBucket).Cursor()
		for key, value := cursor.Seek(appendTime(nil, since)); key != nil; key, value = cursor.Next() {
			if readTime(key) > until {
				return nil
			}
			var event Event
			if err := json.Unmarshal(value, &event); err != nil {
				return err
			}
			events = append(events, event)
		}
		return nil
	})
	return events
}

// Graph reads the whole stored graph back into memory in built order, or
// ok=false when a bucket does not decode — a miss like every other read-path
// failure. The ranker indexes a *Graph rather than walking an Index, so a
// caller that needs every node and edge reads it here instead of rebuilding.
func (i *Index) Graph() (*Graph, bool) {
	g := Graph{Source: i.source, BuiltAt: i.builtAt}
	err := i.db.View(func(tx *bbolt.Tx) error {
		nodes, err := decodeBucket[Node](tx.Bucket(nodeBucket))
		if err != nil {
			return err
		}
		edges, err := decodeBucket[Edge](tx.Bucket(edgeBucket))
		if err != nil {
			return err
		}
		events, err := decodeBucket[Event](tx.Bucket(eventBucket))
		if err != nil {
			return err
		}
		g.Nodes, g.Edges, g.Events = nodes, edges, events
		return nil
	})
	if err != nil {
		return nil, false
	}
	// The forward adjacency key orders edges by (from, to, kind) so one node's
	// edges are a prefix scan; Graph.Edges is ordered by (from, kind, to).
	slices.SortFunc(g.Edges, func(a, z Edge) int {
		return compareEdgeKeys(
			edgeKey{from: a.From, to: a.To, kind: a.Kind},
			edgeKey{from: z.From, to: z.To, kind: z.Kind},
		)
	})
	return &g, true
}

func decodeBucket[T any](b *bbolt.Bucket) ([]T, error) {
	out := make([]T, 0, b.Stats().KeyN)
	err := b.ForEach(func(_, value []byte) error {
		var decoded T
		if err := json.Unmarshal(value, &decoded); err != nil {
			return err
		}
		out = append(out, decoded)
		return nil
	})
	return out, err
}

// Counts returns how many nodes, edges, and events the stored graph holds.
func (i *Index) Counts() (nodes, edges, events int) {
	_ = i.db.View(func(tx *bbolt.Tx) error {
		nodes = tx.Bucket(nodeBucket).Stats().KeyN
		edges = tx.Bucket(edgeBucket).Stats().KeyN
		events = tx.Bucket(eventBucket).Stats().KeyN
		return nil
	})
	return nodes, edges, events
}
