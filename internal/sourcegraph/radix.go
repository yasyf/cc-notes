// Package sourcegraph stores ordered immutable source indexes as Git objects.
package sourcegraph

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"

	"github.com/yasyf/cc-notes/internal/gitobj"
	"github.com/yasyf/cc-notes/model"
)

const (
	mapFormat       = "cc-notes-radix-v1"
	retentionFormat = "cc-notes-retention-index-v1"
	nibbles         = "0123456789abcdef"
	// MaxKeyBytes accommodates two 255-byte namespace components and their separator.
	MaxKeyBytes = 511
)

// ErrCorrupt reports an invalid immutable node or traversed edge.
var ErrCorrupt = errors.New("corrupt source graph")

// Entry is one original-byte-ordered key and detached value.
type Entry struct {
	Key   string
	Value []byte
}

// Change binds an exact key to its before and after membership and bytes.
type Change struct {
	Key          string
	Before       []byte
	After        []byte
	BeforeExists bool
	AfterExists  bool
}

// Mutation replaces or removes one key in an immutable map.
type Mutation struct {
	Key    string
	Value  []byte
	Delete bool
}

// Work counts graph objects and keys visited within an operation scope.
type Work struct {
	NodeReads   uint64
	NodeWrites  uint64
	ValueReads  uint64
	ValueWrites uint64
	KeysVisited uint64
	DiffPairs   uint64
}

type child struct {
	Hash      model.SHA `json:"hash"`
	Count     uint64    `json:"count"`
	Retention model.SHA `json:"retention,omitempty"`
}

type header struct {
	Format         string    `json:"format"`
	Prefix         string    `json:"prefix"`
	Count          uint64    `json:"count"`
	Children       [16]child `json:"children"`
	ValueRetention model.SHA `json:"value_retention,omitempty"`
	Aggregate      model.SHA `json:"aggregate,omitempty"`
}

type node struct {
	header
	value model.SHA
}

// Map operates only inside its owning SourceObjects callback.
type Map struct {
	objects   *gitobj.SourceObjects
	retention bool
	nodes     map[model.SHA]*node
	values    map[model.SHA][]byte
	work      Work
	shared    *WorkCounter
}

// New shares an operation's native object scope without retaining idle handles.
func New(objects *gitobj.SourceObjects) *Map {
	shared, _ := objects.Context().Value(workKey{}).(*WorkCounter)
	return &Map{objects: objects, nodes: make(map[model.SHA]*node), values: make(map[model.SHA][]byte), shared: shared}
}

// Stats returns the actual object and key work performed by this map handle.
func (m *Map) Stats() Work { return m.work }

// Count reads the root's recorded subtree cardinality without a key walk.
func (m *Map) Count(root model.SHA) (uint64, error) {
	if err := m.objects.Err(); err != nil {
		return 0, err
	}
	if root == "" {
		return 0, nil
	}
	n, err := m.load(root)
	if err != nil {
		return 0, err
	}
	return n.Count, nil
}

// Get resolves one key and returns detached bytes, including an empty value.
func (m *Map) Get(root model.SHA, key string) ([]byte, bool, error) {
	if err := m.objects.Err(); err != nil {
		return nil, false, err
	}
	if err := validKey(key); err != nil {
		return nil, false, err
	}
	encoded := hex.EncodeToString([]byte(key))
	for root != "" {
		n, err := m.load(root)
		if err != nil {
			return nil, false, err
		}
		if !strings.HasPrefix(encoded, n.Prefix) {
			return nil, false, nil
		}
		if len(encoded) == len(n.Prefix) {
			if n.value == "" {
				return nil, false, nil
			}
			m.work.KeysVisited++
			if m.shared != nil {
				m.shared.keysVisited.Add(1)
			}
			value, err := m.value(n.value)
			return value, err == nil, err
		}
		index := strings.IndexByte(nibbles, encoded[len(n.Prefix)])
		root, _, err = m.child(n, index)
		if err != nil {
			return nil, false, err
		}
	}
	return nil, false, nil
}

// Put returns a canonical root containing the key's exact new value.
func (m *Map) Put(root model.SHA, key string, value []byte) (model.SHA, error) {
	if err := m.objects.Err(); err != nil {
		return "", err
	}
	if m.retention {
		return "", errors.New("retention maps require entity leaves")
	}
	if err := validKey(key); err != nil {
		return "", err
	}
	sha, err := m.objects.WriteBlob(value)
	if err != nil {
		return "", err
	}
	m.work.ValueWrites++
	if m.shared != nil {
		m.shared.valueWrites.Add(1)
	}
	m.values[sha] = bytes.Clone(value)
	return m.mutate(root, hex.EncodeToString([]byte(key)), sha, "", false)
}

// Delete preserves the root for an absent key and removes a present one.
func (m *Map) Delete(root model.SHA, key string) (model.SHA, error) {
	if err := m.objects.Err(); err != nil {
		return "", err
	}
	if err := validKey(key); err != nil {
		return "", err
	}
	return m.mutate(root, hex.EncodeToString([]byte(key)), "", "", true)
}

// Apply rejects duplicate keys and publishes no refs while deriving a new root.
func (m *Map) Apply(root model.SHA, mutations []Mutation) (model.SHA, error) {
	if err := m.objects.Err(); err != nil {
		return "", err
	}
	mutations = slices.Clone(mutations)
	slices.SortFunc(mutations, func(a, b Mutation) int { return strings.Compare(a.Key, b.Key) })
	for i, mutation := range mutations {
		if i > 0 && mutations[i-1].Key == mutation.Key {
			return "", fmt.Errorf("duplicate source graph mutation %q", mutation.Key)
		}
		var err error
		if mutation.Delete {
			if len(mutation.Value) != 0 {
				return "", fmt.Errorf("delete source graph key %q carries a value", mutation.Key)
			}
			root, err = m.Delete(root, mutation.Key)
		} else {
			root, err = m.Put(root, mutation.Key, mutation.Value)
		}
		if err != nil {
			return "", err
		}
	}
	return root, nil
}

// Rank returns the number of keys strictly before key and its exact membership.
func (m *Map) Rank(root model.SHA, key string) (uint64, bool, error) {
	if err := m.objects.Err(); err != nil {
		return 0, false, err
	}
	if err := validKey(key); err != nil {
		return 0, false, err
	}
	encoded := hex.EncodeToString([]byte(key))
	var rank uint64
	for root != "" {
		n, err := m.load(root)
		if err != nil {
			return 0, false, err
		}
		if !strings.HasPrefix(encoded, n.Prefix) {
			if n.Prefix < encoded {
				rank += n.Count
			}
			return rank, false, nil
		}
		if len(encoded) == len(n.Prefix) {
			return rank, n.value != "", nil
		}
		if n.value != "" {
			rank++
		}
		index := strings.IndexByte(nibbles, encoded[len(n.Prefix)])
		for i := range index {
			rank += n.Children[i].Count
		}
		root, _, err = m.child(n, index)
		if err != nil {
			return 0, false, err
		}
	}
	return rank, false, nil
}

// Range reads at most limit entries beginning at an original-byte-order rank.
func (m *Map) Range(root model.SHA, start uint64, limit int) ([]Entry, error) {
	if err := m.objects.Err(); err != nil {
		return nil, err
	}
	if limit < 0 {
		return nil, errors.New("negative source graph range limit")
	}
	out := make([]Entry, 0)
	if limit == 0 || root == "" {
		return out, nil
	}
	err := m.rangeAt(root, &start, limit, &out)
	return out, err
}

// Prefix visits only keys whose original bytes start with prefix.
func (m *Map) Prefix(root model.SHA, prefix string, visit func(Entry) error) error {
	if err := m.objects.Err(); err != nil {
		return err
	}
	if err := validKey(prefix); err != nil {
		return err
	}
	return m.prefix(root, hex.EncodeToString([]byte(prefix)), visit)
}

// Diff skips identical subtree hashes and visits changed keys in byte order.
func (m *Map) Diff(before, after model.SHA, visit func(Change) error) error {
	if err := m.objects.Err(); err != nil {
		return err
	}
	return m.diff(before, after, visit)
}

// Validate explicitly checks every edge and count; ordinary lookups never call it.
func (m *Map) Validate(root model.SHA) error {
	if err := m.objects.Err(); err != nil {
		return err
	}
	if root == "" {
		return nil
	}
	n, err := m.load(root)
	if err != nil {
		return err
	}
	if n.value != "" {
		if _, err := m.value(n.value); err != nil {
			return err
		}
	}
	for i := range n.Children {
		hash, _, err := m.child(n, i)
		if err != nil {
			return err
		}
		if err := m.Validate(hash); err != nil {
			return err
		}
	}
	return nil
}

func (m *Map) load(hash model.SHA) (*node, error) {
	if err := m.objects.Err(); err != nil {
		return nil, err
	}
	if cached := m.nodes[hash]; cached != nil {
		return cached, nil
	}
	entries, err := m.objects.ReadTree(hash)
	if err != nil {
		return nil, err
	}
	m.work.NodeReads++
	if m.shared != nil {
		m.shared.nodeReads.Add(1)
	}
	byName := make(map[string]object.TreeEntry, len(entries))
	for _, entry := range entries {
		if _, found := byName[entry.Name]; found {
			return nil, fmt.Errorf("%w: duplicate node entry", ErrCorrupt)
		}
		byName[entry.Name] = entry
	}
	meta, ok := byName["node"]
	if !ok || meta.Mode != filemode.Regular {
		return nil, fmt.Errorf("%w: missing node header", ErrCorrupt)
	}
	data, err := m.objects.ReadBlob(model.SHA(meta.Hash.String()))
	if err != nil {
		return nil, err
	}
	n := &node{}
	if err := json.Unmarshal(data, &n.header); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	canonical, err := json.Marshal(n.header)
	if err != nil || !bytes.Equal(canonical, data) {
		return nil, fmt.Errorf("%w: noncanonical node header", ErrCorrupt)
	}
	format := mapFormat
	if m.retention {
		format = retentionFormat
	}
	if n.Format != format || len(n.Prefix) > MaxKeyBytes*2 || strings.Trim(n.Prefix, nibbles) != "" {
		return nil, fmt.Errorf("%w: node format or prefix", ErrCorrupt)
	}
	count, expectedEntries, children := uint64(0), 1, 0
	if entry, found := byName["v"]; found {
		if entry.Mode != filemode.Regular || len(n.Prefix)%2 != 0 || entry.Hash == plumbing.ZeroHash {
			return nil, fmt.Errorf("%w: invalid terminal value", ErrCorrupt)
		}
		n.value = model.SHA(entry.Hash.String())
		count, expectedEntries = 1, 2
	}
	if m.retention {
		if !plumbing.IsHash(string(n.Aggregate)) || (n.value != "" && !plumbing.IsHash(string(n.ValueRetention))) || (n.value == "" && n.ValueRetention != "") {
			return nil, fmt.Errorf("%w: invalid retention roots", ErrCorrupt)
		}
	} else if n.Aggregate != "" || n.ValueRetention != "" {
		return nil, fmt.Errorf("%w: retention root in plain map", ErrCorrupt)
	}
	for i, next := range n.Children {
		entry, found := byName[string(nibbles[i])]
		if next.Hash == "" {
			if found || next.Count != 0 || next.Retention != "" {
				return nil, fmt.Errorf("%w: empty child metadata", ErrCorrupt)
			}
			continue
		}
		if !found || entry.Mode != filemode.Dir || model.SHA(entry.Hash.String()) != next.Hash || next.Count == 0 || !plumbing.IsHash(string(next.Hash)) {
			return nil, fmt.Errorf("%w: child metadata differs", ErrCorrupt)
		}
		if m.retention != plumbing.IsHash(string(next.Retention)) || (!m.retention && next.Retention != "") {
			return nil, fmt.Errorf("%w: child retention differs", ErrCorrupt)
		}
		if math.MaxUint64-count < next.Count {
			return nil, fmt.Errorf("%w: count overflow", ErrCorrupt)
		}
		count += next.Count
		children++
		expectedEntries++
	}
	if count != n.Count || (n.value == "" && children < 2) || len(entries) != expectedEntries {
		return nil, fmt.Errorf("%w: noncanonical count or shape", ErrCorrupt)
	}
	m.nodes[hash] = n
	return n, nil
}

func (m *Map) child(parent *node, index int) (model.SHA, *node, error) {
	next := parent.Children[index]
	if next.Hash == "" {
		return "", nil, nil
	}
	n, err := m.load(next.Hash)
	if err != nil {
		return "", nil, err
	}
	if !strings.HasPrefix(n.Prefix, parent.Prefix+string(nibbles[index])) || n.Count != next.Count || n.Aggregate != next.Retention {
		return "", nil, fmt.Errorf("%w: child prefix, count or retention differs", ErrCorrupt)
	}
	return next.Hash, n, nil
}

func (m *Map) value(hash model.SHA) ([]byte, error) {
	if err := m.objects.Err(); err != nil {
		return nil, err
	}
	if cached, found := m.values[hash]; found {
		return bytes.Clone(cached), nil
	}
	data, err := m.objects.ReadBlob(hash)
	if err != nil {
		return nil, err
	}
	m.work.ValueReads++
	if m.shared != nil {
		m.shared.valueReads.Add(1)
	}
	m.values[hash] = bytes.Clone(data)
	return data, nil
}

func (m *Map) mutate(hash model.SHA, key string, value, retained model.SHA, remove bool) (model.SHA, error) {
	if hash == "" {
		if remove {
			return "", nil
		}
		return m.write(&node{header: header{Prefix: key, ValueRetention: retained}, value: value})
	}
	previous, err := m.load(hash)
	if err != nil {
		return "", err
	}
	n := *previous
	common := 0
	for common < len(key) && common < len(n.Prefix) && key[common] == n.Prefix[common] {
		common++
	}
	if common < len(n.Prefix) {
		if remove {
			return hash, nil
		}
		parent := &node{header: header{Prefix: key[:common]}}
		parent.Children[strings.IndexByte(nibbles, n.Prefix[common])] = child{Hash: hash, Count: n.Count, Retention: n.Aggregate}
		if len(key) == common {
			parent.value, parent.ValueRetention = value, retained
		} else {
			leaf, err := m.write(&node{header: header{Prefix: key, ValueRetention: retained}, value: value})
			if err != nil {
				return "", err
			}
			leafNode := m.nodes[leaf]
			parent.Children[strings.IndexByte(nibbles, key[common])] = child{Hash: leaf, Count: leafNode.Count, Retention: leafNode.Aggregate}
		}
		return m.write(parent)
	}
	if len(key) == common {
		if (remove && n.value == "") || (!remove && n.value == value && n.ValueRetention == retained) {
			return hash, nil
		}
		n.value, n.ValueRetention = value, retained
	} else {
		index := strings.IndexByte(nibbles, key[common])
		old, _, err := m.child(&n, index)
		if err != nil {
			return "", err
		}
		next, err := m.mutate(old, key, value, retained, remove)
		if err != nil {
			return "", err
		}
		if next == old {
			return hash, nil
		}
		n.Children[index] = child{}
		if next != "" {
			childNode, err := m.load(next)
			if err != nil {
				return "", err
			}
			n.Children[index] = child{Hash: next, Count: childNode.Count, Retention: childNode.Aggregate}
		}
	}
	if n.value == "" {
		var only model.SHA
		children := 0
		for _, next := range n.Children {
			if next.Hash != "" {
				children++
				only = next.Hash
			}
		}
		if children == 0 {
			return "", nil
		}
		if children == 1 {
			return only, nil
		}
	}
	return m.write(&n)
}

func (m *Map) write(n *node) (model.SHA, error) {
	n.Format, n.Count = mapFormat, 0
	if n.value != "" {
		n.Count = 1
	}
	for _, next := range n.Children {
		if math.MaxUint64-n.Count < next.Count {
			return "", fmt.Errorf("%w: count overflow", ErrCorrupt)
		}
		n.Count += next.Count
	}
	if m.retention {
		n.Format = retentionFormat
		aggregate, err := m.writeAggregate(n)
		if err != nil {
			return "", err
		}
		n.Aggregate = aggregate
	}
	data, err := json.Marshal(n.header)
	if err != nil {
		return "", err
	}
	meta, err := m.objects.WriteBlob(data)
	if err != nil {
		return "", err
	}
	entries := []object.TreeEntry{{Name: "node", Mode: filemode.Regular, Hash: plumbing.NewHash(string(meta))}}
	if n.value != "" {
		entries = append(entries, object.TreeEntry{Name: "v", Mode: filemode.Regular, Hash: plumbing.NewHash(string(n.value))})
	}
	for i, next := range n.Children {
		if next.Hash != "" {
			entries = append(entries, object.TreeEntry{Name: string(nibbles[i]), Mode: filemode.Dir, Hash: plumbing.NewHash(string(next.Hash))})
		}
	}
	hash, err := m.objects.WriteTree(entries)
	if err != nil {
		return "", err
	}
	m.work.NodeWrites++
	if m.shared != nil {
		m.shared.nodeWrites.Add(1)
	}
	copy := *n
	m.nodes[hash] = &copy
	return hash, nil
}

func (m *Map) rangeAt(hash model.SHA, skip *uint64, limit int, out *[]Entry) error {
	if hash == "" || len(*out) >= limit {
		return nil
	}
	n, err := m.load(hash)
	if err != nil {
		return err
	}
	if *skip >= n.Count {
		*skip -= n.Count
		return nil
	}
	if n.value != "" {
		if *skip > 0 {
			*skip--
		} else {
			entry, err := m.entry(n)
			if err != nil {
				return err
			}
			*out = append(*out, entry)
		}
	}
	for i, next := range n.Children {
		if next.Hash == "" || len(*out) >= limit {
			continue
		}
		if *skip >= next.Count {
			*skip -= next.Count
			continue
		}
		hash, _, err := m.child(n, i)
		if err != nil {
			return err
		}
		if err := m.rangeAt(hash, skip, limit, out); err != nil {
			return err
		}
	}
	return nil
}

func (m *Map) prefix(hash model.SHA, prefix string, visit func(Entry) error) error {
	if hash == "" {
		return nil
	}
	n, err := m.load(hash)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(n.Prefix, prefix) && !strings.HasPrefix(prefix, n.Prefix) {
		return nil
	}
	if n.value != "" && strings.HasPrefix(n.Prefix, prefix) {
		entry, err := m.entry(n)
		if err != nil {
			return err
		}
		if err := visit(entry); err != nil {
			return err
		}
	}
	for i, next := range n.Children {
		if next.Hash == "" {
			continue
		}
		if len(prefix) > len(n.Prefix) && prefix[len(n.Prefix)] != nibbles[i] {
			continue
		}
		hash, _, err := m.child(n, i)
		if err != nil {
			return err
		}
		if err := m.prefix(hash, prefix, visit); err != nil {
			return err
		}
	}
	return nil
}

func (m *Map) entry(n *node) (Entry, error) {
	key, err := hex.DecodeString(n.Prefix)
	if err != nil {
		return Entry{}, fmt.Errorf("%w: terminal prefix: %w", ErrCorrupt, err)
	}
	value, err := m.value(n.value)
	if err != nil {
		return Entry{}, err
	}
	m.work.KeysVisited++
	if m.shared != nil {
		m.shared.keysVisited.Add(1)
	}
	return Entry{Key: string(key), Value: value}, nil
}

func validKey(key string) error {
	if len(key) > MaxKeyBytes {
		return fmt.Errorf("source graph key exceeds %d bytes", MaxKeyBytes)
	}
	return nil
}

func hexKey(key string) string { return hex.EncodeToString([]byte(key)) }

func decodeKey(prefix string) (string, error) {
	key, err := hex.DecodeString(prefix)
	if err != nil {
		return "", fmt.Errorf("%w: terminal prefix: %w", ErrCorrupt, err)
	}
	return string(key), nil
}
