package sourcegraph

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/model"
)

type bulkEntry struct {
	key      string
	value    model.SHA
	retained model.SHA
}

// Build writes the canonical final nodes for a complete map in byte order.
func (m *Map) Build(entries []Entry) (model.SHA, error) {
	if err := m.objects.Err(); err != nil {
		return "", err
	}
	if m.retention {
		return "", fmt.Errorf("retention maps require entity leaves")
	}
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Key, b.Key) })
	values := make([]bulkEntry, 0, len(entries))
	for i, entry := range entries {
		if err := validKey(entry.Key); err != nil {
			return "", err
		}
		if i > 0 && entries[i-1].Key == entry.Key {
			return "", fmt.Errorf("duplicate source graph key %q", entry.Key)
		}
		value, err := m.objects.WriteBlob(entry.Value)
		if err != nil {
			return "", err
		}
		m.work.ValueWrites++
		if m.shared != nil {
			m.shared.valueWrites.Add(1)
		}
		m.values[value] = bytes.Clone(entry.Value)
		values = append(values, bulkEntry{key: hexKey(entry.Key), value: value})
	}
	return m.build(values)
}

func (m *Map) build(entries []bulkEntry) (model.SHA, error) {
	if err := m.objects.Err(); err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", nil
	}
	first, last := entries[0].key, entries[len(entries)-1].key
	common := 0
	for common < len(first) && common < len(last) && first[common] == last[common] {
		common++
	}
	n := &node{header: header{Prefix: first[:common]}}
	if len(first) == common {
		n.value, n.ValueRetention = entries[0].value, entries[0].retained
		entries = entries[1:]
	}
	for len(entries) > 0 {
		index := strings.IndexByte(nibbles, entries[0].key[common])
		end := 1
		for end < len(entries) && entries[end].key[common] == entries[0].key[common] {
			end++
		}
		hash, err := m.build(entries[:end])
		if err != nil {
			return "", err
		}
		next := m.nodes[hash]
		n.Children[index] = child{Hash: hash, Count: next.Count, Retention: next.Aggregate}
		entries = entries[end:]
	}
	return m.write(n)
}

// RetentionEntry binds one key to its final retained entity declaration.
type RetentionEntry struct {
	Key    string
	Entity RetainedEntity
}

// Build writes each final retention leaf and aggregate without intermediate roots.
func (r *Retention) Build(entries []RetentionEntry) (RetentionRoot, error) {
	if err := r.index.objects.Err(); err != nil {
		return RetentionRoot{}, err
	}
	entries = slices.Clone(entries)
	slices.SortFunc(entries, func(a, b RetentionEntry) int { return strings.Compare(a.Key, b.Key) })
	values := make([]bulkEntry, 0, len(entries))
	for i, entry := range entries {
		if err := validKey(entry.Key); err != nil {
			return RetentionRoot{}, err
		}
		if i > 0 && entries[i-1].Key == entry.Key {
			return RetentionRoot{}, fmt.Errorf("duplicate retention key %q", entry.Key)
		}
		leaf, err := r.writeLeaf(entry.Key, entry.Entity)
		if err != nil {
			return RetentionRoot{}, err
		}
		value, err := r.index.objects.WriteBlob([]byte(leaf))
		if err != nil {
			return RetentionRoot{}, err
		}
		r.index.work.ValueWrites++
		if r.index.shared != nil {
			r.index.shared.valueWrites.Add(1)
		}
		values = append(values, bulkEntry{key: hexKey(entry.Key), value: value, retained: leaf})
	}
	index, err := r.index.build(values)
	if err != nil {
		return RetentionRoot{}, err
	}
	return r.root(index)
}
