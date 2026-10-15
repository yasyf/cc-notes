package store

import (
	"cmp"
	"container/list"
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

	seedOnce sync.Once
	mu       sync.Mutex
	seeded   bool
	order    *list.List
	index    map[string]*list.Element
}

func (l *lruDir) read(name string) ([]byte, bool) {
	//nolint:gosec // G304: dir is this store's own cache directory and name is a derived key, not external input.
	data, err := os.ReadFile(filepath.Join(l.dir, name))
	if err != nil {
		return nil, false
	}
	return data, true
}

func (l *lruDir) open(name string) (*os.File, bool) {
	//nolint:gosec // G304: dir is this store's own cache directory and name is a derived key, not external input.
	f, err := os.Open(filepath.Join(l.dir, name))
	if err != nil {
		return nil, false
	}
	return f, true
}

func (l *lruDir) ensure() bool {
	return os.MkdirAll(l.dir, 0o750) == nil
}

func (l *lruDir) write(name string, data []byte) {
	if !l.ensure() {
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
	if e, ok := l.index[name]; ok {
		l.order.MoveToBack(e)
	}
}

func (l *lruDir) record(name string) {
	l.seedOnce.Do(l.seed)
	for _, oldest := range l.promote(name) {
		_ = os.Remove(filepath.Join(l.dir, oldest))
	}
}

func (l *lruDir) seed() {
	order := seedOrder(l.dir)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.order = list.New()
	l.index = make(map[string]*list.Element, len(order))
	for _, n := range order {
		l.index[n] = l.order.PushBack(n)
	}
	l.seeded = true
}

func (l *lruDir) promote(name string) []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.index[name]; ok {
		l.order.MoveToBack(e)
	} else {
		l.index[name] = l.order.PushBack(name)
	}
	var evict []string
	for l.order.Len() > l.capacity {
		oldest := l.order.Remove(l.order.Front()).(string)
		delete(l.index, oldest)
		evict = append(evict, oldest)
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
	if e, ok := l.index[name]; ok {
		l.order.Remove(e)
		delete(l.index, name)
	}
}
