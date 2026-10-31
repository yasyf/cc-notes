package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/yasyf/cc-notes/internal/fold"
	"github.com/yasyf/cc-notes/model"
)

const (
	// foldCacheVersion is the single hard-cut cache format.
	foldCacheVersion = 2
	// foldCacheCap bounds the number of on-disk entries; the least-recently
	// used are evicted past it. Below a repository's live entity count every
	// full listing evicts what it just wrote and re-folds it on the next call.
	foldCacheCap = 1 << 15
	// foldCacheSubdir is the path under the git common dir where entries live;
	// it is never a ref, so it is never pushed or synced.
	foldCacheSubdir = "cc-notes/folds-v6"
)

// foldCacheGeneration tags every entry with the op vocabulary of the binary that
// wrote it, so an entry a binary with a different vocabulary wrote is a miss.
// The directory is shared — a brew-installed cc-notes and a dev build read the
// same one — and without the tag the cache defeats SkippedOps in one direction:
// the newer binary folds a chain completely and caches it, and the older one,
// which cannot apply every op in that chain, reads the entry back reporting zero
// skips, because SkippedOps does not marshal. The tag is derived from
// model.OpKinds, never declared, so a release that adds an op moves it without
// anyone bumping a constant.
var foldCacheGeneration = foldGeneration(model.OpKinds())

// foldGeneration digests an op vocabulary into an entry-header tag.
func foldGeneration(opKinds []string) string {
	sum := sha256.Sum256([]byte(strings.Join(opKinds, "\n")))
	return hex.EncodeToString(sum[:8])
}

type foldCache struct {
	lruDir
}

// newFoldCache returns a cache rooted at non-empty dir and bounded at capacity
// entries.
func newFoldCache(dir string, capacity int) *foldCache {
	return &foldCache{lruDir{capacity: capacity, dir: dir}}
}

// get returns the cached snapshot for tip, or ok=false on any miss: an absent
// entry, an unreadable or corrupt file, a version mismatch, or an entry a
// binary with a different op vocabulary wrote.
func (c *foldCache) get(tip model.SHA) (model.Snapshot, bool) {
	snap, _, _, ok := c.getTop(tip)
	return snap, ok
}

func (c *foldCache) getTop(tip model.SHA) (model.Snapshot, model.Lamport, model.Lamport, bool) {
	entry, ok := c.entry(string(tip))
	if !ok {
		return nil, 0, 0, false
	}
	data, ok := c.readEntry(entry)
	if !ok {
		return nil, 0, 0, false
	}
	snap, top, floor, ok := decodeFoldEntry(data)
	if !ok {
		return nil, 0, 0, false
	}
	c.rememberHit(entry)
	return snap, top, floor, true
}

func (c *foldCache) getEntry(tip model.SHA, entry cacheEntry) (model.Snapshot, bool) {
	if !entry.valid(string(tip)) {
		return nil, false
	}
	data, ok := c.readEntry(entry)
	if !ok {
		return nil, false
	}
	snap, _, _, ok := decodeFoldEntry(data)
	if !ok {
		return nil, false
	}
	c.rememberHit(entry)
	return snap, true
}

func (c *foldCache) put(tip model.SHA, snap model.Snapshot, top, floor model.Lamport) {
	if value := foldPublication(tip, snap, top, floor); value != nil {
		c.write(value.name, value.data)
	}
}

func (c *foldCache) putChain(tip model.SHA, snap model.Snapshot, chain []model.PackCommit) {
	c.put(tip, snap, topLamport(chain), fold.ExtendFloor(chain))
}

// tips lists the chain tips currently cached on disk, best-effort: an
// unreadable directory yields an empty slice. GCLocal walks it to find entries
// orphaned by appends, compaction, and merges.
func (c *foldCache) tips() []model.SHA {
	names := c.names()
	tips := make([]model.SHA, len(names))
	for i, name := range names {
		tips[i] = model.SHA(name)
	}
	return tips
}

// delete removes the cache entry for tip and drops it from the LRU index. It is
// best-effort: a missing file is a no-op. GCLocal and physical prune call it to
// evict entries orphaned by appends, compaction, merges, and tombstone removal.
func (c *foldCache) delete(tip model.SHA) {
	c.remove(string(tip))
}

func encodeFoldEntry(snap model.Snapshot, top, floor model.Lamport) ([]byte, bool) {
	body, err := json.Marshal(snap)
	if err != nil {
		return nil, false
	}
	header := []byte{byte('0' + foldCacheVersion), ' '}
	header = append(header, foldCacheGeneration...)
	header = append(header, ' ')
	header = append(header, string(snap.Meta().Kind)...)
	header = append(header, ' ')
	header = strconv.AppendUint(header, uint64(top), 10)
	header = append(header, ' ')
	header = strconv.AppendUint(header, uint64(floor), 10)
	header = append(header, '\n')
	return append(header, body...), true
}

func decodeFoldEntry(data []byte) (model.Snapshot, model.Lamport, model.Lamport, bool) {
	nl := slices.Index(data, '\n')
	if nl < 0 {
		return nil, 0, 0, false
	}
	header := data[:nl]
	body := data[nl+1:]
	if len(header) < 2 || header[0] != byte('0'+foldCacheVersion) || header[1] != ' ' {
		return nil, 0, 0, false
	}
	tail, ours := bytes.CutPrefix(header[2:], []byte(foldCacheGeneration+" "))
	if !ours {
		return nil, 0, 0, false
	}
	fields := strings.Split(string(tail), " ")
	if len(fields) != 3 {
		return nil, 0, 0, false
	}
	kind, err := model.ParseKind(fields[0])
	if err != nil {
		return nil, 0, 0, false
	}
	top, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return nil, 0, 0, false
	}
	floor, err := strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return nil, 0, 0, false
	}
	snap, err := kind.DecodeSnapshot(body)
	if err != nil {
		return nil, 0, 0, false
	}
	return snap, model.Lamport(top), model.Lamport(floor), true
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
