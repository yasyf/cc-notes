package store

import (
	"cmp"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/yasyf/cc-notes/model"
)

const (
	// foldCacheVersion is the single hard-cut cache format.
	foldCacheVersion = 1
	// foldCacheCap bounds the number of on-disk entries; the least-recently
	// used are evicted past it.
	foldCacheCap = 1024
	// foldCacheSubdir is the path under the git common dir where entries live;
	// it is never a ref, so it is never pushed or synced.
	foldCacheSubdir = "cc-notes/folds-v1"
)

// foldCache is a persistent, local, tip-keyed snapshot cache: a pure
// accelerator derived from the object database that lets short-lived CLI
// processes skip re-folding cold chains. The file name is the chain tip sha,
// which is immutable, so a present entry is always valid — there is no
// staleness logic. The cache is best-effort by design: every I/O error
// degrades to a miss (get) or a no-op (put) and is never propagated. This is
// the one intentional error-swallow in the package — the cache is a derived
// artifact, rebuildable by deleting the directory, not state whose loss is a
// failure.
type foldCache struct {
	capacity int

	// commonDir resolves the git common dir for lazy directory resolution. It
	// is nil when the directory was supplied explicitly (tests).
	commonDir func() (string, error)

	once   sync.Once
	dir    string
	dirErr error

	mu     sync.Mutex
	seeded bool
	// order is the in-process LRU index, oldest first, seeded from the
	// directory's modification times on first use.
	order []model.SHA
}

// newFoldCache returns a cache bounded at capacity entries. A non-empty dir is
// used verbatim (tests inject a temp dir and a small capacity); an empty dir
// is resolved lazily on first access via the commonDir hook the Store wires.
func newFoldCache(dir string, capacity int) *foldCache {
	c := &foldCache{capacity: capacity}
	if dir != "" {
		c.dir = dir
	}
	return c
}

// resolveDir resolves the cache directory once. An explicit dir set at
// construction is used as-is; otherwise it joins the git common dir with
// foldCacheSubdir, so linked worktrees share one cache.
func (c *foldCache) resolveDir() (string, error) {
	c.once.Do(func() {
		if c.dir != "" {
			return
		}
		if c.commonDir == nil {
			return
		}
		common, err := c.commonDir()
		if err != nil {
			c.dirErr = err
			return
		}
		c.dir = filepath.Join(common, foldCacheSubdir)
	})
	if c.dirErr != nil {
		return "", c.dirErr
	}
	return c.dir, nil
}

// get returns the cached snapshot for tip, or ok=false on any miss: an absent
// entry, an unresolvable directory, an unreadable or corrupt file, or a
// version mismatch.
func (c *foldCache) get(tip model.SHA) (model.Snapshot, bool) {
	dir, err := c.resolveDir()
	if err != nil || dir == "" {
		return nil, false
	}
	//nolint:gosec // G304: dir is this store's own fold-cache directory and tip is a validated SHA key, not external input.
	data, err := os.ReadFile(filepath.Join(dir, string(tip)))
	if err != nil {
		return nil, false
	}
	snap, ok := decodeFoldEntry(data)
	if !ok {
		return nil, false
	}
	c.touch(tip)
	return snap, true
}

// put writes the snapshot for tip, keyed by the resulting chain tip, then
// enforces the LRU bound. Every error is swallowed: the cache is a pure
// accelerator.
func (c *foldCache) put(tip model.SHA, snap model.Snapshot) {
	dir, err := c.resolveDir()
	if err != nil || dir == "" {
		return
	}
	data, ok := encodeFoldEntry(snap)
	if !ok {
		return
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return
	}
	if !writeFileAtomic(dir, string(tip), data) {
		return
	}
	c.record(dir, tip)
}

// touch moves an already-present tip to the most-recently-used end.
func (c *foldCache) touch(tip model.SHA) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.seeded {
		return
	}
	if i := slices.Index(c.order, tip); i >= 0 {
		c.order = append(slices.Delete(c.order, i, i+1), tip)
	}
}

// record marks tip as most-recently-used and evicts the oldest entries past the
// capacity bound. Disk I/O stays outside the lock: the first-use directory scan
// that seeds the LRU index and the per-eviction os.Remove both run unlocked, so
// the listConcurrency fan-out never serializes a cache put behind a filesystem
// call. The lock covers only the slice bookkeeping and the eviction-set
// computation. A racing first put may scan the directory redundantly; promote
// keeps the first seed and discards the rest, so the index stays consistent.
func (c *foldCache) record(dir string, tip model.SHA) {
	var seed []model.SHA
	if !c.isSeeded() {
		seed = seedOrder(dir)
	}
	for _, oldest := range c.promote(seed, tip) {
		_ = os.Remove(filepath.Join(dir, string(oldest)))
	}
}

// isSeeded reports whether the LRU index has been seeded from disk.
func (c *foldCache) isSeeded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seeded
}

// promote applies the first-use seed when the index is unseeded, moves tip to
// the most-recently-used end, and returns the entries evicted past the capacity
// bound for the caller to delete outside the lock.
func (c *foldCache) promote(seed []model.SHA, tip model.SHA) []model.SHA {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.seeded {
		c.order = seed
		c.seeded = true
	}
	if i := slices.Index(c.order, tip); i >= 0 {
		c.order = slices.Delete(c.order, i, i+1)
	}
	c.order = append(c.order, tip)
	var evict []model.SHA
	for len(c.order) > c.capacity {
		evict = append(evict, c.order[0])
		c.order = c.order[1:]
	}
	return evict
}

// seedOrder lists the cache directory's entries oldest-first by modification
// time, seeding the in-process LRU index from disk.
func seedOrder(dir string) []model.SHA {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type entry struct {
		sha   model.SHA
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
		entries = append(entries, entry{sha: model.SHA(e.Name()), mtime: info.ModTime().UnixNano()})
	}
	slices.SortFunc(entries, func(a, b entry) int { return cmp.Compare(a.mtime, b.mtime) })
	order := make([]model.SHA, len(entries))
	for i, e := range entries {
		order[i] = e.sha
	}
	return order
}

// tips lists the chain tips currently cached on disk, best-effort: an
// unresolvable or unreadable directory yields an empty slice. GCLocal walks it
// to find entries orphaned by appends, compaction, and merges.
func (c *foldCache) tips() []model.SHA {
	dir, err := c.resolveDir()
	if err != nil || dir == "" {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	tips := make([]model.SHA, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		tips = append(tips, model.SHA(e.Name()))
	}
	return tips
}

// delete removes the cache entry for tip and drops it from the LRU index. It is
// best-effort: a missing file or unresolvable directory is a no-op. GCLocal and
// physical prune call it to evict entries orphaned by appends, compaction,
// merges, and tombstone removal.
func (c *foldCache) delete(tip model.SHA) {
	dir, err := c.resolveDir()
	if err != nil || dir == "" {
		return
	}
	_ = os.Remove(filepath.Join(dir, string(tip)))
	c.mu.Lock()
	defer c.mu.Unlock()
	if i := slices.Index(c.order, tip); i >= 0 {
		c.order = slices.Delete(c.order, i, i+1)
	}
}

// encodeFoldEntry serializes a snapshot as a version-and-kind header line
// followed by the snapshot's own JSON. The model types carry Head, so the
// serialized form self-identifies its tip. The header kind is the snapshot's
// Meta().Kind, whose wire values are the same tokens the decoder parses.
func encodeFoldEntry(snap model.Snapshot) ([]byte, bool) {
	body, err := json.Marshal(snap)
	if err != nil {
		return nil, false
	}
	header := []byte{byte('0' + foldCacheVersion), ' '}
	header = append(header, string(snap.Meta().Kind)...)
	header = append(header, '\n')
	return append(header, body...), true
}

// decodeFoldEntry parses a cache entry, returning ok=false on a missing
// header, a version mismatch, an unknown kind, or invalid JSON.
func decodeFoldEntry(data []byte) (model.Snapshot, bool) {
	nl := slices.Index(data, '\n')
	if nl < 0 {
		return nil, false
	}
	header := data[:nl]
	body := data[nl+1:]
	if len(header) < 2 || header[0] != byte('0'+foldCacheVersion) || header[1] != ' ' {
		return nil, false
	}
	kind, err := model.ParseKind(string(header[2:]))
	if err != nil {
		return nil, false
	}
	snap, err := kind.DecodeSnapshot(body)
	if err != nil {
		return nil, false
	}
	return snap, true
}

// writeFileAtomic writes data to name within dir via a temp file and rename,
// so a reader never observes a half-written entry. It returns false on any
// error.
func writeFileAtomic(dir, name string, data []byte) bool {
	tmp, err := os.CreateTemp(dir, name+".*")
	if err != nil {
		return false
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return false
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return false
	}
	if err := os.Rename(tmpName, filepath.Join(dir, name)); err != nil {
		_ = os.Remove(tmpName)
		return false
	}
	return true
}
