package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
)

const cacheTokenSize = 64

type cacheAccess uint8

const (
	cacheLookup cacheAccess = iota
	cacheEnumerate
	cacheWrite
	cacheErase
)

type cacheEntry struct {
	name   string
	slot   int
	token  []byte
	size   int64
	rank   uint64
	inline bool
}

type cacheHit struct {
	nonce    [32]byte
	diskRank uint64
	rank     uint64
}

type lruDir struct {
	capacity int
	dir      string

	writer sync.Mutex
	mu     sync.Mutex
	active bool
	rank   uint64
	hits   map[int]cacheHit

	indexMu       sync.Mutex
	index         *os.File
	indexInfo     os.FileInfo
	indexWritable bool
	lookupMissing bool
	data          *os.File
	dataInfo      os.FileInfo
	dataWritable  bool
	dataMissing   bool
	closed        bool
}

func cacheNameTag(name string) uint64 {
	key := sha256.Sum256([]byte(name))
	return binary.BigEndian.Uint64(key[:8])
}

func (slot cacheHintSlot) entry(name string) cacheEntry {
	key := sha256.Sum256([]byte(name))
	token := make([]byte, cacheTokenSize)
	copy(token, key[:])
	copy(token[32:], slot.nonce[:])
	return cacheEntry{name: name, slot: slot.slot, token: token, size: slot.size, rank: slot.rank, inline: slot.inline}
}

func (entry cacheEntry) valid(name string) bool {
	key := sha256.Sum256([]byte(name))
	return len(name) <= cacheKeyMax && entry.name == name && entry.size >= 0 && (!entry.inline || cacheInlineFits(name, entry.size)) && len(entry.token) == cacheTokenSize && bytes.Equal(entry.token[:32], key[:])
}

func (l *lruDir) read(name string) ([]byte, bool) {
	entry, ok := l.entry(name)
	if !ok {
		return nil, false
	}
	return l.readEntry(entry)
}

func (l *lruDir) readEntry(entry cacheEntry) ([]byte, bool) {
	reader, ok := l.openEntry(entry)
	if !ok {
		return nil, false
	}
	defer func() { _ = reader.Close() }()
	if reader.remaining == 0 {
		return reader.chunk, true
	}
	data, err := io.ReadAll(reader)
	return data, err == nil
}

func (l *lruDir) entry(name string) (cacheEntry, bool) {
	if l.capacity <= 0 || len(name) > cacheKeyMax {
		return cacheEntry{}, false
	}
	file, err := l.hintFile(cacheLookup)
	if err != nil {
		return cacheEntry{}, false
	}
	pages := cacheHintPages{file: file, capacity: l.capacity}
	return lookupCacheHint(&pages, name)
}

func lookupCacheHint(pages *cacheHintPages, name string) (cacheEntry, bool) {
	tag := cacheNameTag(name)
	window, err := pages.window(tag)
	if err != nil {
		return cacheEntry{}, false
	}
	for _, slot := range window.slots[:window.count] {
		if !slot.empty && slot.tag == tag {
			return slot.entry(name), true
		}
	}
	return cacheEntry{}, false
}

func (l *lruDir) entries(ctx context.Context, names []string) map[string]cacheEntry {
	if len(names) == 0 || l.capacity <= 0 || ctx.Err() != nil {
		return nil
	}
	file, err := l.hintFile(cacheLookup)
	if err != nil {
		return nil
	}
	pages := cacheHintPages{file: file, capacity: l.capacity, pages: make(map[int64][]byte)}
	entries := make(map[string]cacheEntry, min(len(names), l.capacity))
	for _, name := range names {
		if ctx.Err() != nil {
			return nil
		}
		if len(entries) == l.capacity {
			break
		}
		if len(name) <= cacheKeyMax {
			if entry, ok := lookupCacheHint(&pages, name); ok {
				entries[name] = entry
			}
		}
	}
	return entries
}

func (l *lruDir) open(name string) (*CacheReader, bool) {
	entry, ok := l.entry(name)
	if !ok {
		return nil, false
	}
	return l.openEntry(entry)
}

func (l *lruDir) openEntry(entry cacheEntry) (*CacheReader, bool) {
	if !entry.valid(entry.name) {
		return nil, false
	}
	if entry.inline {
		file, err := l.payloadFile(cacheLookup)
		if err != nil {
			return nil, false
		}
		reader, err := readCacheFirstBlock(io.NewSectionReader(file, int64(entry.slot)*cacheInlineStride, cacheInlineStride), entry)
		if err != nil {
			return nil, false
		}
		reader.name = file.Name()
		return reader, true
	}
	file, err := os.Open(l.slotPath(entry.slot))
	if err != nil {
		return nil, false
	}
	reader, err := newCacheReader(file, entry)
	if err != nil {
		_ = file.Close()
		return nil, false
	}
	return reader, true
}

func (l *lruDir) ensure() bool {
	file, err := l.writerFile()
	if err != nil {
		return false
	}
	return file.Close() == nil
}

func (l *lruDir) writerFile() (*os.File, error) {
	if err := os.MkdirAll(l.dir, 0o750); err != nil { //nolint:gosec // G703: The directory belongs to this store's derived cache, never a cache key.
		return nil, err
	}
	return os.OpenFile(l.dir+".lock", os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // G703: The sibling lock derives only from the store's cache directory.
}

func (l *lruDir) lockWriter() (*os.File, bool) {
	file, err := l.writerFile()
	if err != nil {
		return nil, false
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, false
	}
	return file, true
}

func (l *lruDir) hintFile(access cacheAccess) (*os.File, error) {
	l.indexMu.Lock()
	defer l.indexMu.Unlock()
	if l.closed {
		return nil, os.ErrClosed
	}
	if access == cacheLookup && l.lookupMissing {
		return nil, os.ErrNotExist
	}
	if access != cacheLookup {
		l.lookupMissing = false
		l.dataMissing = false
	}
	path := filepath.Join(l.dir, cacheHintFile)
	if l.index != nil {
		if access == cacheLookup {
			return l.index, nil
		}
		info, err := os.Stat(path) //nolint:gosec // G703: path is the fixed routing file inside the store's cache directory.
		same := err == nil && os.SameFile(info, l.indexInfo)
		if same && (access != cacheWrite || l.indexWritable) {
			return l.index, nil
		}
		if err := l.index.Close(); err != nil {
			return nil, err
		}
		l.index = nil
		if !same {
			if err := l.closePayloadLocked(); err != nil {
				return nil, err
			}
			l.mu.Lock()
			clear(l.hits)
			l.mu.Unlock()
		}
	}
	if access == cacheLookup {
		if _, err := os.Stat(path); err != nil { //nolint:gosec // G703: path is the fixed routing file inside the store's cache directory.
			l.lookupMissing = errors.Is(err, os.ErrNotExist)
			return nil, err
		}
	}
	flags := os.O_RDONLY
	if access == cacheWrite {
		flags = os.O_RDWR | os.O_CREATE
	}
	file, err := os.OpenFile(path, flags, 0o600) //nolint:gosec // G703: path is the fixed routing file inside the store's cache directory.
	if err != nil {
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	if access == cacheWrite && info.Size() == 0 {
		if err := initializeCacheHints(file, l.capacity); err != nil {
			return nil, errors.Join(err, file.Close())
		}
	}
	if _, err := readCacheHintHeader(file, l.capacity); err != nil {
		return nil, errors.Join(err, file.Close())
	}
	l.index, l.indexInfo, l.indexWritable = file, info, access == cacheWrite
	return file, nil
}

func (l *lruDir) close() error {
	l.writer.Lock()
	defer l.writer.Unlock()
	l.indexMu.Lock()
	defer l.indexMu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	l.lookupMissing = false
	l.mu.Lock()
	l.active = false
	l.hits = nil
	l.mu.Unlock()
	var indexErr error
	if l.index != nil {
		indexErr = l.index.Close()
	}
	return errors.Join(indexErr, l.closePayloadLocked())
}

func (l *lruDir) write(name string, data []byte) {
	l.writeBatch([]cachePublication{{name: name, data: data}})
}

func (l *lruDir) writeBatch(values []cachePublication) {
	if l.capacity <= 0 || len(values) == 0 {
		return
	}
	l.writer.Lock()
	defer l.writer.Unlock()
	lock, ok := l.lockWriter()
	if !ok {
		return
	}
	defer func() { _ = lock.Close() }()
	file, err := l.hintFile(cacheWrite)
	if err != nil {
		return
	}
	rank, err := readCacheHintHeader(file, l.capacity)
	if err != nil {
		return
	}
	initial := rank
	defer func() {
		if rank != initial {
			_ = writeCacheHintRank(file, rank)
		}
	}()
	pages := cacheHintPages{file: file, capacity: l.capacity}
	payload := cachePayloadWriter{cache: l}
	for _, value := range values {
		if len(value.name) > cacheKeyMax {
			return
		}
		tag := cacheNameTag(value.name)
		window, err := pages.window(tag)
		if err != nil {
			return
		}
		previous := l.chooseSlot(window, tag)
		slot := previous.slot
		for _, candidate := range window.slots[:window.count] {
			if !candidate.empty {
				rank = max(rank, candidate.rank)
			}
		}
		key := sha256.Sum256([]byte(value.name))
		var token [cacheTokenSize]byte
		copy(token[:32], key[:])
		if _, err := rand.Read(token[32:]); err != nil {
			return
		}
		inline := cacheInlineFits(value.name, int64(len(value.data)))
		if !payload.publish(previous, value.name, token[:], value.data) {
			return
		}
		l.mu.Lock()
		rank = max(rank, l.rank) + 1
		l.mu.Unlock()
		hint := cacheHint{tag: tag, size: int64(len(value.data)), rank: rank, inline: inline}
		copy(hint.nonce[:], token[32:])
		if err := writeCacheHint(file, slot, hint); err != nil {
			return
		}
		l.mu.Lock()
		l.active = true
		l.rank = max(l.rank, rank)
		if previous, ok := l.hits[slot]; ok && previous.nonce != hint.nonce {
			delete(l.hits, slot)
		}
		if l.hits == nil {
			l.hits = make(map[int]cacheHit)
		}
		l.mu.Unlock()
	}
}

func (l *lruDir) chooseSlot(window cacheHintWindow, tag uint64) cacheHintSlot {
	for _, candidate := range window.slots[:window.count] {
		if !candidate.empty && candidate.tag == tag {
			return candidate
		}
	}
	for _, candidate := range window.slots[:window.count] {
		if candidate.empty {
			return candidate
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	victim, oldest := window.slots[0], ^uint64(0)
	for _, candidate := range window.slots[:window.count] {
		rank := candidate.rank
		if hit, ok := l.hits[candidate.slot]; ok && hit.nonce == candidate.nonce && hit.diskRank == candidate.rank {
			rank = max(rank, hit.rank)
		}
		if rank < oldest {
			victim, oldest = candidate, rank
		}
	}
	return victim
}

func (l *lruDir) slotPath(slot int) string {
	return filepath.Join(l.dir, fmt.Sprintf("slot-%08x", slot))
}

func (l *lruDir) publish(slot int, name string, token, data []byte) bool {
	pending := filepath.Join(l.dir, ".pending")
	file, err := os.OpenFile(pending, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:gosec // G703: pending is the fixed staging file in the store's cache directory.
	if err != nil {
		return false
	}
	if err := writeCachePayload(file, name, token, data); err != nil {
		_ = file.Close()
		return false
	}
	if file.Close() != nil {
		return false
	}
	return os.Rename(pending, l.slotPath(slot)) == nil //nolint:gosec // G703: Fixed staging and integer slot paths derive only from the store's cache directory.
}

func (l *lruDir) touch(name string) {
	l.mu.Lock()
	active := l.active
	l.mu.Unlock()
	if !active {
		return
	}
	entry, ok := l.entry(name)
	if !ok {
		return
	}
	reader, ok := l.openEntry(entry)
	if !ok {
		return
	}
	_ = reader.Close()
	l.rememberHit(entry)
}

func (l *lruDir) rememberHit(entry cacheEntry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.active {
		return
	}
	var nonce [32]byte
	copy(nonce[:], entry.token[32:])
	if previous, ok := l.hits[entry.slot]; ok && previous.diskRank > entry.rank {
		return
	}
	l.rank = max(l.rank, entry.rank) + 1
	l.hits[entry.slot] = cacheHit{nonce: nonce, diskRank: entry.rank, rank: l.rank}
}

func (l *lruDir) hintName(slot cacheHintSlot) (string, bool) {
	if slot.inline {
		file, err := l.payloadFile(cacheEnumerate)
		if err != nil {
			return "", false
		}
		return cachePayloadName(io.NewSectionReader(file, int64(slot.slot)*cacheInlineStride, cacheInlineStride), slot)
	}
	file, err := os.Open(l.slotPath(slot.slot))
	if err != nil {
		return "", false
	}
	defer func() { _ = file.Close() }()
	return cachePayloadName(file, slot)
}

func (l *lruDir) names() []string {
	if l.capacity <= 0 {
		return nil
	}
	file, err := l.hintFile(cacheEnumerate)
	if err != nil {
		return nil
	}
	pages := cacheHintPages{file: file, capacity: l.capacity, pages: make(map[int64][]byte)}
	var names []string
	for slot := range l.capacity {
		var data [cacheHintRecordSize]byte
		if err := pages.read(data[:], cacheHintHeaderSize+int64(slot)*cacheHintRecordSize); err != nil {
			return nil
		}
		hint, err := decodeCacheHint(data[:])
		if err != nil || hint.empty {
			continue
		}
		if name, ok := l.hintName(cacheHintSlot{slot: slot, cacheHint: hint}); ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func (l *lruDir) remove(name string) {
	if l.capacity <= 0 {
		return
	}
	l.writer.Lock()
	defer l.writer.Unlock()
	lock, ok := l.lockWriter()
	if !ok {
		return
	}
	defer func() { _ = lock.Close() }()
	file, err := l.hintFile(cacheWrite)
	if err != nil {
		return
	}
	pages := cacheHintPages{file: file, capacity: l.capacity}
	window, err := pages.window(cacheNameTag(name))
	if err != nil {
		return
	}
	for _, candidate := range window.slots[:window.count] {
		if candidate.empty || candidate.tag != cacheNameTag(name) {
			continue
		}
		if found, ok := l.hintName(candidate); ok && found != name {
			continue
		}
		if payload, err := l.payloadFile(cacheErase); err == nil {
			_ = eraseInlineCachePayload(payload, candidate.slot)
		}
		_ = os.Remove(l.slotPath(candidate.slot))
		_ = writeCacheHint(file, candidate.slot, cacheHint{empty: true})
		return
	}
}
