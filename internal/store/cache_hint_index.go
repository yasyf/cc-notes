package store

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
)

const (
	cacheHintHeaderSize = 24
	cacheHintRecordSize = 56
	cacheProbeLimit     = 32
	cacheHintPageSize   = 4096
	cacheKeyMax         = 255
	cacheHintFile       = "index.bin"
)

var cacheHintMagic = [8]byte{'C', 'C', 'N', 'H', 'I', 'N', 'T', 1}

type cacheHint struct {
	tag   uint64
	size  int64
	rank  uint64
	nonce [32]byte
	empty bool
}

type cacheHintSlot struct {
	slot int
	cacheHint
}

type cacheHintWindow struct {
	slots [cacheProbeLimit]cacheHintSlot
	count int
}

func decodeCacheHint(data []byte) (cacheHint, error) {
	size := binary.BigEndian.Uint64(data[8:16])
	if size == 0 {
		return cacheHint{empty: true}, nil
	}
	if size-1 > math.MaxInt64 {
		return cacheHint{}, errCacheIntegrity
	}
	hint := cacheHint{
		tag:  binary.BigEndian.Uint64(data[:8]),
		size: int64(size - 1),
		rank: binary.BigEndian.Uint64(data[16:24]),
	}
	copy(hint.nonce[:], data[24:])
	return hint, nil
}

func encodeCacheHint(hint cacheHint) [cacheHintRecordSize]byte {
	var data [cacheHintRecordSize]byte
	if !hint.empty {
		binary.BigEndian.PutUint64(data[:8], hint.tag)
		binary.BigEndian.PutUint64(data[8:16], uint64(hint.size)+1)
		binary.BigEndian.PutUint64(data[16:24], hint.rank)
		copy(data[24:], hint.nonce[:])
	}
	return data
}

func readCacheHintHeader(file *os.File, capacity int) (uint64, error) {
	var data [cacheHintHeaderSize]byte
	if _, err := file.ReadAt(data[:], 0); err != nil {
		return 0, err
	}
	if !bytes.Equal(data[:8], cacheHintMagic[:]) || binary.BigEndian.Uint64(data[8:16]) != uint64(capacity) {
		return 0, errCacheIntegrity
	}
	return binary.BigEndian.Uint64(data[16:]), nil
}

func initializeCacheHints(file *os.File, capacity int) error {
	if err := file.Truncate(cacheHintHeaderSize + int64(capacity)*cacheHintRecordSize); err != nil {
		return err
	}
	var data [cacheHintHeaderSize]byte
	copy(data[:8], cacheHintMagic[:])
	binary.BigEndian.PutUint64(data[8:16], uint64(capacity))
	_, err := file.WriteAt(data[:], 0)
	return err
}

func writeCacheHintRank(file *os.File, rank uint64) error {
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], rank)
	_, err := file.WriteAt(data[:], 16)
	return err
}

type cacheHintPages struct {
	file     *os.File
	capacity int
	pages    map[int64][]byte
}

func (p *cacheHintPages) read(data []byte, offset int64) error {
	if p.pages == nil {
		_, err := p.file.ReadAt(data, offset)
		return err
	}
	for len(data) > 0 {
		pageAt := offset / cacheHintPageSize * cacheHintPageSize
		page, ok := p.pages[pageAt]
		if !ok {
			size := min(int64(cacheHintPageSize), cacheHintHeaderSize+int64(p.capacity)*cacheHintRecordSize-pageAt)
			page = make([]byte, int(size))
			if _, err := p.file.ReadAt(page, pageAt); err != nil {
				return err
			}
			p.pages[pageAt] = page
		}
		n := copy(data, page[offset-pageAt:])
		data, offset = data[n:], offset+int64(n)
	}
	return nil
}

func (p *cacheHintPages) window(tag uint64) (cacheHintWindow, error) {
	var window cacheHintWindow
	window.count = min(p.capacity, cacheProbeLimit)
	start := int(tag % uint64(p.capacity))
	beforeWrap := min(window.count, p.capacity-start)
	var data [cacheProbeLimit * cacheHintRecordSize]byte
	if err := p.read(data[:beforeWrap*cacheHintRecordSize], cacheHintHeaderSize+int64(start)*cacheHintRecordSize); err != nil {
		return cacheHintWindow{}, err
	}
	if beforeWrap < window.count {
		if err := p.read(data[beforeWrap*cacheHintRecordSize:window.count*cacheHintRecordSize], cacheHintHeaderSize); err != nil {
			return cacheHintWindow{}, err
		}
	}
	for i := range window.count {
		hint, err := decodeCacheHint(data[i*cacheHintRecordSize : (i+1)*cacheHintRecordSize])
		if err != nil {
			return cacheHintWindow{}, err
		}
		window.slots[i] = cacheHintSlot{slot: (start + i) % p.capacity, cacheHint: hint}
	}
	return window, nil
}

func writeCacheHint(file *os.File, slot int, hint cacheHint) error {
	data := encodeCacheHint(hint)
	_, err := file.WriteAt(data[:], cacheHintHeaderSize+int64(slot)*cacheHintRecordSize)
	return err
}
