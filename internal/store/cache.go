package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yasyf/cc-notes/model"
)

const (
	// foldCacheVersion is the single hard-cut cache format.
	foldCacheVersion = 1
	// foldCacheCap bounds the number of on-disk entries; the least-recently
	// used are evicted past it. Below a repository's live entity count every
	// full listing evicts what it just wrote and re-folds it on the next call.
	foldCacheCap = 1 << 14
	// foldCacheSubdir is the path under the git common dir where entries live;
	// it is never a ref, so it is never pushed or synced.
	foldCacheSubdir = "cc-notes/folds-v1"
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

// foldCache is a persistent, local, tip-keyed snapshot cache: a pure
// accelerator derived from the object database that lets short-lived CLI
// processes skip re-folding cold chains. The file name is the chain tip sha,
// which is immutable, so a present entry of this binary's fold generation is
// always valid — there is no staleness logic. The cache is best-effort by
// design: every I/O error degrades to a miss (get) or a no-op (put) and is
// never propagated. This is the one intentional error-swallow in the package —
// the cache is a derived artifact, rebuildable by deleting the directory, not
// state whose loss is a failure.
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
	data, ok := c.read(string(tip))
	if !ok {
		return nil, false
	}
	snap, ok := decodeFoldEntry(data)
	if !ok {
		return nil, false
	}
	c.touch(string(tip))
	return snap, true
}

// put writes the snapshot for tip, keyed by the resulting chain tip, then
// enforces the LRU bound. Every error is swallowed: the cache is a pure
// accelerator.
//
// A snapshot whose fold skipped ops is never stored: SkippedOps does not
// marshal, so a cache hit would report zero where a cold fold reports N. That
// covers only what this binary writes; foldCacheGeneration covers what another
// binary wrote.
func (c *foldCache) put(tip model.SHA, snap model.Snapshot) {
	if snap.Meta().SkippedOps > 0 {
		return
	}
	data, ok := encodeFoldEntry(snap)
	if !ok {
		return
	}
	c.write(string(tip), data)
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

// encodeFoldEntry serializes a snapshot as a version, fold-generation, and kind
// header line followed by the snapshot's own JSON. The model types carry Head,
// so the serialized form self-identifies its tip. The header kind is the
// snapshot's Meta().Kind, whose wire values are the same tokens the decoder
// parses.
func encodeFoldEntry(snap model.Snapshot) ([]byte, bool) {
	body, err := json.Marshal(snap)
	if err != nil {
		return nil, false
	}
	header := []byte{byte('0' + foldCacheVersion), ' '}
	header = append(header, foldCacheGeneration...)
	header = append(header, ' ')
	header = append(header, string(snap.Meta().Kind)...)
	header = append(header, '\n')
	return append(header, body...), true
}

// decodeFoldEntry parses a cache entry, returning ok=false on a missing header,
// a version mismatch, a fold generation other than this binary's, an unknown
// kind, or invalid JSON.
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
	tail, ours := bytes.CutPrefix(header[2:], []byte(foldCacheGeneration+" "))
	if !ours {
		return nil, false
	}
	kind, err := model.ParseKind(string(tail))
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
