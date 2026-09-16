package store

import (
	"cmp"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// lruDir is a directory of named files bounded at capacity entries, evicting
// the least-recently used past it. Every I/O error degrades to a miss or a
// no-op: its entries are derived artifacts, rebuildable by deleting the
// directory.
type lruDir struct {
	capacity int
	dir      string

	mu     sync.Mutex
	seeded bool
	order  []string
}

func (l *lruDir) read(name string) ([]byte, bool) {
	//nolint:gosec // G304: dir is this store's own cache directory and name is a derived key, not external input.
	data, err := os.ReadFile(filepath.Join(l.dir, name))
	if err != nil {
		return nil, false
	}
	return data, true
}

func (l *lruDir) write(name string, data []byte) {
	if err := os.MkdirAll(l.dir, 0o750); err != nil {
		return
	}
	if !writeFileAtomic(l.dir, name, data) {
		return
	}
	l.record(name)
}

func (l *lruDir) touch(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.seeded {
		return
	}
	if i := slices.Index(l.order, name); i >= 0 {
		l.order = append(slices.Delete(l.order, i, i+1), name)
	}
}

// record marks name as most-recently-used and evicts the oldest entries past
// the capacity bound. Disk I/O stays outside the lock: the first-use directory
// scan that seeds the LRU index and the per-eviction os.Remove both run
// unlocked, so a concurrent fan-out never serializes a write behind a
// filesystem call. A racing first write may scan the directory redundantly;
// promote keeps the first seed and discards the rest.
func (l *lruDir) record(name string) {
	var seed []string
	if !l.isSeeded() {
		seed = seedOrder(l.dir)
	}
	for _, oldest := range l.promote(seed, name) {
		_ = os.Remove(filepath.Join(l.dir, oldest))
	}
}

func (l *lruDir) isSeeded() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.seeded
}

func (l *lruDir) promote(seed []string, name string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.seeded {
		l.order = seed
		l.seeded = true
	}
	if i := slices.Index(l.order, name); i >= 0 {
		l.order = slices.Delete(l.order, i, i+1)
	}
	l.order = append(l.order, name)
	var evict []string
	for len(l.order) > l.capacity {
		evict = append(evict, l.order[0])
		l.order = l.order[1:]
	}
	return evict
}

// seedOrder lists dir's entries oldest-first by modification time, seeding the
// in-process LRU index from disk.
func seedOrder(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type entry struct {
		name  string
		mtime int64
	}
	entries := make([]entry, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		entries = append(entries, entry{name: e.Name(), mtime: info.ModTime().UnixNano()})
	}
	slices.SortFunc(entries, func(a, b entry) int { return cmp.Compare(a.mtime, b.mtime) })
	order := make([]string, len(entries))
	for i, e := range entries {
		order[i] = e.name
	}
	return order
}

func (l *lruDir) names() []string {
	ents, err := os.ReadDir(l.dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func (l *lruDir) remove(name string) {
	_ = os.Remove(filepath.Join(l.dir, name))
	l.mu.Lock()
	defer l.mu.Unlock()
	if i := slices.Index(l.order, name); i >= 0 {
		l.order = slices.Delete(l.order, i, i+1)
	}
}
