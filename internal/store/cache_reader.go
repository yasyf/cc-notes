package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"os"
)

const (
	cacheChunkSize   = 4096
	cacheChunkDomain = "cc-notes-cache-block-v2"
)

var errCacheIntegrity = errors.New("cache payload integrity mismatch")

// CacheReader verifies publication-bound blocks before exposing their bytes.
// Its verified bytes and streamed file remain valid independently of the store.
type CacheReader struct {
	file      *os.File
	name      string
	token     [cacheTokenSize]byte
	size      int64
	remaining int64
	index     uint64
	buffer    []byte
	chunk     []byte
	offset    int
}

func newCacheReader(file *os.File, entry cacheEntry) (*CacheReader, error) {
	reader, err := readCacheFirstBlock(file, entry)
	if err != nil {
		return nil, err
	}
	reader.file, reader.name = file, file.Name()
	return reader, nil
}

func readCacheFirstBlock(source io.Reader, entry cacheEntry) (*CacheReader, error) {
	if !entry.valid(entry.name) {
		return nil, errCacheIntegrity
	}
	first := int(min(entry.size, int64(cacheChunkSize)))
	prefix := cacheTokenSize + 1 + len(entry.name)
	buffer := make([]byte, prefix+sha256.Size+first)
	if _, err := io.ReadFull(source, buffer); err != nil {
		return nil, err
	}
	if !bytes.Equal(buffer[:cacheTokenSize], entry.token) || int(buffer[cacheTokenSize]) != len(entry.name) || !bytes.Equal(buffer[cacheTokenSize+1:prefix], []byte(entry.name)) {
		return nil, errCacheIntegrity
	}
	chunk := buffer[prefix+sha256.Size:]
	digest := cacheBlockDigest(entry.token, entry.size, 0, chunk)
	if !bytes.Equal(buffer[prefix:prefix+sha256.Size], digest[:]) {
		return nil, errCacheIntegrity
	}
	reader := &CacheReader{size: entry.size, remaining: entry.size - int64(first), index: 1, buffer: buffer, chunk: chunk}
	copy(reader.token[:], entry.token)
	return reader, nil
}

// Read copies only bytes whose block checksum matches this publication.
func (r *CacheReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.offset == len(r.chunk) {
		if r.remaining == 0 {
			return 0, io.EOF
		}
		n := int(min(r.remaining, int64(cacheChunkSize)))
		block := r.buffer[:sha256.Size+n]
		if _, err := io.ReadFull(r.file, block); err != nil {
			if errors.Is(err, io.EOF) {
				return 0, io.ErrUnexpectedEOF
			}
			return 0, err
		}
		digest := cacheBlockDigest(r.token[:], r.size, r.index, block[sha256.Size:])
		if !bytes.Equal(block[:sha256.Size], digest[:]) {
			return 0, errCacheIntegrity
		}
		r.chunk, r.offset = block[sha256.Size:], 0
		r.remaining -= int64(n)
		r.index++
	}
	n := copy(p, r.chunk[r.offset:])
	r.offset += n
	return n, nil
}

// Size reports verified logical bytes, excluding publication and block framing.
func (r *CacheReader) Size() int64 {
	return r.size
}

// Name identifies the backing payload file, including after replacement.
func (r *CacheReader) Name() string {
	return r.name
}

// Close releases this reader's file without changing the store's lifetime.
func (r *CacheReader) Close() error {
	if r.file == nil {
		return nil
	}
	return r.file.Close()
}

func cacheBlockDigest(token []byte, size int64, index uint64, data []byte) [sha256.Size]byte {
	const prefix = len(cacheChunkDomain) + cacheTokenSize + 16
	var input [prefix + cacheChunkSize]byte
	position := copy(input[:], cacheChunkDomain)
	position += copy(input[position:], token)
	binary.BigEndian.PutUint64(input[position:position+8], uint64(size)) //nolint:gosec // Metadata sizes are nonnegative before opening; writes use slice lengths.
	binary.BigEndian.PutUint64(input[position+8:position+16], index)
	copy(input[prefix:], data)
	return sha256.Sum256(input[:prefix+len(data)])
}

func writeCachePayload(file io.Writer, name string, token, data []byte) error {
	var buffer [cacheTokenSize + 1 + cacheKeyMax + sha256.Size + cacheChunkSize]byte
	copy(buffer[:], token)
	buffer[cacheTokenSize] = byte(len(name)) //nolint:gosec // G115: Publication rejects keys longer than 255 bytes before framing.
	copy(buffer[cacheTokenSize+1:], name)
	size, index, offset := int64(len(data)), uint64(0), cacheTokenSize+1+len(name)
	for {
		n := min(len(data), cacheChunkSize)
		digest := cacheBlockDigest(token, size, index, data[:n])
		copy(buffer[offset:], digest[:])
		copy(buffer[offset+sha256.Size:], data[:n])
		if _, err := file.Write(buffer[:offset+sha256.Size+n]); err != nil {
			return err
		}
		data = data[n:]
		if len(data) == 0 {
			return nil
		}
		index++
		offset = 0
	}
}
