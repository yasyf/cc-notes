package store

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const (
	cacheInlineStride = cacheChunkSize / 4
	cacheInlineFile   = "values.bin"
)

func cacheInlineFits(name string, size int64) bool {
	return len(name) <= cacheKeyMax && size >= 0 && size <= int64(cacheInlineStride-cacheTokenSize-1-len(name)-sha256.Size)
}

func (l *lruDir) inlinePath() string {
	return filepath.Join(l.dir, cacheInlineFile)
}

func (l *lruDir) payloadFile(access cacheAccess) (*os.File, error) {
	l.indexMu.Lock()
	defer l.indexMu.Unlock()
	if l.closed {
		return nil, os.ErrClosed
	}
	if access == cacheLookup && l.dataMissing {
		return nil, os.ErrNotExist
	}
	if access != cacheLookup {
		l.dataMissing = false
	}
	writable := access == cacheWrite || access == cacheErase
	if l.data != nil {
		if access == cacheLookup {
			return l.data, nil
		}
		info, err := os.Stat(l.inlinePath())
		if err == nil && os.SameFile(info, l.dataInfo) && (!writable || l.dataWritable) {
			return l.data, nil
		}
		if err := l.closePayloadLocked(); err != nil {
			return nil, err
		}
	}
	flags := os.O_RDONLY
	if writable {
		flags = os.O_RDWR
	}
	if access == cacheWrite {
		flags |= os.O_CREATE
	}
	file, err := os.OpenFile(l.inlinePath(), flags, 0o600)
	if err != nil {
		l.dataMissing = errors.Is(err, os.ErrNotExist)
		return nil, err
	}
	info, err := file.Stat()
	if err != nil {
		return nil, errors.Join(err, file.Close())
	}
	l.data, l.dataInfo, l.dataWritable = file, info, writable
	return file, nil
}

func (l *lruDir) closePayloadLocked() error {
	file := l.data
	l.data, l.dataInfo, l.dataWritable, l.dataMissing = nil, nil, false, false
	if file == nil {
		return nil
	}
	return file.Close()
}

func writeInlineCachePayload(file *os.File, previous cacheHintSlot, name string, token, data []byte) error {
	var record [cacheInlineStride]byte
	frame := bytes.NewBuffer(record[:0])
	if err := writeCachePayload(frame, name, token, data); err != nil {
		return err
	}
	n := frame.Len()
	if !previous.empty && previous.inline {
		prior := min(int64(cacheInlineStride), int64(cacheTokenSize+1+cacheKeyMax+sha256.Size)+previous.size)
		n = max(n, int(prior))
	}
	_, err := file.WriteAt(record[:n], int64(previous.slot)*cacheInlineStride)
	return err
}

func eraseInlineCachePayload(file *os.File, slot int) error {
	var record [cacheInlineStride]byte
	_, err := file.WriteAt(record[:], int64(slot)*cacheInlineStride)
	return err
}

type cachePayloadWriter struct {
	cache   *lruDir
	file    *os.File
	checked bool
}

func (w *cachePayloadWriter) publish(previous cacheHintSlot, name string, token, data []byte) bool {
	if cacheInlineFits(name, int64(len(data))) {
		if w.file == nil {
			file, err := w.cache.payloadFile(cacheWrite)
			if err != nil {
				return false
			}
			w.file, w.checked = file, true
		}
		if err := writeInlineCachePayload(w.file, previous, name, token, data); err != nil {
			return false
		}
		if err := os.Remove(w.cache.slotPath(previous.slot)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false
		}
		return true
	}
	if !w.cache.publish(previous.slot, name, token, data) {
		return false
	}
	if !w.checked {
		file, err := w.cache.payloadFile(cacheErase)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false
		}
		w.file, w.checked = file, true
	}
	return w.file == nil || eraseInlineCachePayload(w.file, previous.slot) == nil
}

func cachePayloadName(reader io.ReadSeeker, slot cacheHintSlot) (string, bool) {
	var prefix [cacheTokenSize + 1]byte
	if _, err := io.ReadFull(reader, prefix[:]); err != nil {
		return "", false
	}
	name := make([]byte, int(prefix[cacheTokenSize]))
	if _, err := io.ReadFull(reader, name); err != nil {
		return "", false
	}
	if cacheNameTag(string(name)) != slot.tag {
		return "", false
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return "", false
	}
	if _, err := readCacheFirstBlock(reader, slot.entry(string(name))); err != nil {
		return "", false
	}
	return string(name), true
}
